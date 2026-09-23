package tunnel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	internalSessionAssertionMetadata = "marketmesh-session-assertion-bin"
)

var (
	errSessionClosed    = errors.New("gateway-out tunnel session closed")
	errProtocol         = errors.New("gateway-out tunnel protocol violation")
	errKeepaliveTimeout = errors.New("gateway-out tunnel application keepalive timeout")
)

type drainSource uint8

const (
	drainLocalShutdown drainSource = iota + 1
	drainPeer
)

type requestKey [protocolv1.RequestIDBytes]byte

type queuedFrame struct {
	frame     *contractv1.ConnectRequest
	following []*contractv1.ConnectRequest
	sent      chan error
}

type terminalRequest struct {
	class            contractv1.TrafficClass
	incomingSequence uint64
	sendCredit       uint64
	maximumCredit    uint64
	canceled         bool
}

type requestState struct {
	key       requestKey
	requestID []byte
	route     route
	ctx       context.Context
	cancel    context.CancelFunc
	span      trace.Span
	started   time.Time
	done      chan struct{}
	terminal  atomic.Bool

	mu               sync.Mutex
	incomingSequence uint64
	outgoingSequence uint64
	receiveCredit    uint64
	sendCredit       uint64
	requestBytes     []byte
	halfClosed       bool
	peerCanceled     bool
	creditChanged    chan struct{}
}

type session struct {
	ctx              context.Context
	cancel           context.CancelFunc
	settings         settings
	registry         *Registry
	observer         observer
	stream           grpcgo.BidiStreamingClient[contractv1.ConnectRequest, contractv1.ConnectResponse]
	tunnelID         []byte
	serverInstanceID [protocolv1.InstanceIDBytes]byte
	limits           *contractv1.Limits
	routes           map[contractv1.RouteId]struct{}
	classes          map[contractv1.TrafficClass]struct{}

	controlQueue  chan queuedFrame
	regularQueue  chan queuedFrame
	realtimeQueue chan queuedFrame
	pongs         chan uint64

	draining  atomic.Bool
	drainSent atomic.Bool
	workers   sync.WaitGroup

	requestsMu    sync.Mutex
	requests      map[requestKey]*requestState
	terminal      map[requestKey]*terminalRequest
	terminalOrder []requestKey
	terminalNext  int
	activeByClass map[contractv1.TrafficClass]uint32
	activeChanged chan struct{}
}

type handshakeResult struct {
	frame *contractv1.ConnectResponse
	err   error
}

func newSession(
	ctx context.Context,
	settings settings,
	registry *Registry,
	observer observer,
	connection *grpcgo.ClientConn,
) (*session, error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	stream, err := contractv1.NewTunnelServiceClient(connection).Connect(sessionCtx)
	if err != nil {
		cancel()
		return nil, errHandshake
	}

	session := &session{
		ctx:           sessionCtx,
		cancel:        cancel,
		settings:      settings,
		registry:      registry,
		observer:      observer,
		stream:        stream,
		controlQueue:  make(chan queuedFrame, settings.classLimits[contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH].SendQueueDepth),
		regularQueue:  make(chan queuedFrame, settings.classLimits[contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR].SendQueueDepth),
		realtimeQueue: make(chan queuedFrame, settings.classLimits[contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME].SendQueueDepth),
		pongs:         make(chan uint64, 1),
		requests:      make(map[requestKey]*requestState),
		terminal:      make(map[requestKey]*terminalRequest),
		activeByClass: make(map[contractv1.TrafficClass]uint32, protocolv1.MaxTrafficClasses),
		activeChanged: make(chan struct{}, 1),
	}

	if err := session.handshake(); err != nil {
		cancel()
		_ = stream.CloseSend()
		return nil, err
	}

	return session, nil
}

func (session *session) handshake() error {
	capabilities := []contractv1.Capability{contractv1.Capability_CAPABILITY_DRAIN}
	if session.registry.hasClass(contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME) {
		capabilities = append(capabilities, contractv1.Capability_CAPABILITY_REALTIME)
	}
	hello := &contractv1.ConnectRequest{
		Header: &contractv1.FrameHeader{},
		Payload: &contractv1.ConnectRequest_Hello{
			Hello: &contractv1.GatewayOutHello{
				InstanceId:                slices.Clone(session.settings.instanceID[:]),
				SupportedProtocolVersions: []uint32{protocolVersion},
				Capabilities:              capabilities,
				TrafficClasses:            session.registry.advertisedClasses(),
				RouteIds:                  session.registry.advertisedRoutes(),
				Limits:                    session.settings.limits.proto(),
			},
		},
	}
	if err := session.stream.Send(hello); err != nil {
		return errHandshake
	}
	session.observer.frame(
		session.ctx,
		"out",
		"hello",
		contractv1.TrafficClass_TRAFFIC_CLASS_UNSPECIFIED,
	)

	result := make(chan handshakeResult, 1)
	go func() {
		frame, err := session.stream.Recv()
		result <- handshakeResult{frame: frame, err: err}
	}()
	timer := time.NewTimer(session.settings.handshakeTimeout)
	defer timer.Stop()

	select {
	case received := <-result:
		if received.err != nil {
			return errHandshake
		}
		if err := session.acceptHello(received.frame, capabilities); err != nil {
			return err
		}
		return nil
	case <-timer.C:
		session.cancel()
		<-result
		return errHandshake
	case <-session.ctx.Done():
		<-result
		return errHandshake
	}
}

func (session *session) acceptHello(
	frame *contractv1.ConnectResponse,
	offeredCapabilities []contractv1.Capability,
) error {
	if err := protocolv1.ValidateGatewayInFrame(frame); err != nil {
		return errProtocol
	}
	hello := frame.GetHello()
	if hello == nil || !slices.Contains(hello.GetCapabilities(), contractv1.Capability_CAPABILITY_DRAIN) {
		return errProtocol
	}
	for _, capability := range hello.GetCapabilities() {
		if !slices.Contains(offeredCapabilities, capability) {
			return errProtocol
		}
	}
	if len(hello.GetTrafficClasses()) == 0 || len(hello.GetRouteIds()) == 0 {
		return errProtocol
	}
	copy(session.serverInstanceID[:], hello.GetInstanceId())

	classes := make(map[contractv1.TrafficClass]struct{}, len(hello.GetTrafficClasses()))
	for _, class := range hello.GetTrafficClasses() {
		if !session.registry.hasClass(class) {
			return errProtocol
		}
		classes[class] = struct{}{}
	}
	routes := make(map[contractv1.RouteId]struct{}, len(hello.GetRouteIds()))
	for _, id := range hello.GetRouteIds() {
		route, found := session.registry.lookup(id)
		if !found {
			return errProtocol
		}
		if _, found := classes[route.TrafficClass]; !found {
			return errProtocol
		}
		routes[id] = struct{}{}
	}
	if !limitsDoNotExceed(hello.GetLimits(), session.settings.limits) {
		return errProtocol
	}

	limits := cloneLimits(hello.GetLimits())
	if limits == nil {
		return errProtocol
	}
	session.tunnelID = slices.Clone(frame.GetHeader().GetTunnelId())
	session.limits = limits
	session.routes = routes
	session.classes = classes
	session.observer.frame(
		session.ctx,
		"in",
		"hello",
		contractv1.TrafficClass_TRAFFIC_CLASS_UNSPECIFIED,
	)

	return nil
}

func (session *session) run() error {
	errorsChannel := make(chan error, 3)
	loops := []func() error{session.sendLoop, session.receiveLoop, session.keepaliveLoop}
	var loopsWait sync.WaitGroup
	for _, loop := range loops {
		loopsWait.Add(1)
		go func() {
			defer loopsWait.Done()
			errorsChannel <- loop()
		}()
	}

	err := <-errorsChannel
	session.cancel()
	session.abortAll(contractv1.ResultCode_RESULT_CODE_UNAVAILABLE)
	loopsWait.Wait()
	session.workers.Wait()
	_ = session.stream.CloseSend()

	return err
}

func (session *session) sendLoop() error {
	for {
		queued, err := session.nextOutbound()
		if err != nil {
			return err
		}
		frames := append([]*contractv1.ConnectRequest{queued.frame}, queued.following...)
		for _, frame := range frames {
			err = session.stream.Send(frame)
			if err != nil {
				break
			}
			session.observer.frame(
				session.ctx,
				"out",
				gatewayOutFrameType(frame),
				frame.GetHeader().GetTrafficClass(),
			)
		}
		if queued.sent != nil {
			queued.sent <- err
			close(queued.sent)
		}
		if err != nil {
			return errSessionClosed
		}
	}
}

func (session *session) nextOutbound() (queuedFrame, error) {
	select {
	case queued := <-session.controlQueue:
		return queued, nil
	default:
	}

	select {
	case queued := <-session.controlQueue:
		return queued, nil
	case queued := <-session.regularQueue:
		return queued, nil
	case queued := <-session.realtimeQueue:
		return queued, nil
	case <-session.ctx.Done():
		return queuedFrame{}, errSessionClosed
	}
}

func (session *session) receiveLoop() error {
	for {
		frame, err := session.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) || session.ctx.Err() != nil {
				return errSessionClosed
			}
			return errSessionClosed
		}
		if err := session.handleFrame(frame); err != nil {
			session.observer.protocolFailure(session.ctx, protocolFailureCategory(err))
			return err
		}
		session.observer.frame(
			session.ctx,
			"in",
			gatewayInFrameType(frame),
			frame.GetHeader().GetTrafficClass(),
		)
	}
}

func (session *session) handleFrame(frame *contractv1.ConnectResponse) error {
	if err := protocolv1.ValidateGatewayInFrame(frame); err != nil {
		return errProtocol
	}
	if !withinNegotiatedLimits(frame, session.limits) {
		return errProtocol
	}
	if !bytes.Equal(frame.GetHeader().GetTunnelId(), session.tunnelID) {
		return errProtocol
	}

	switch payload := frame.GetPayload().(type) {
	case *contractv1.ConnectResponse_Open:
		return session.handleOpen(frame.GetHeader(), payload.Open)
	case *contractv1.ConnectResponse_Data:
		return session.handleData(frame.GetHeader(), payload.Data)
	case *contractv1.ConnectResponse_HalfClose:
		return session.handleHalfClose(frame.GetHeader())
	case *contractv1.ConnectResponse_Cancel:
		return session.handleCancel(frame.GetHeader(), payload.Cancel)
	case *contractv1.ConnectResponse_Credit:
		return session.handleCredit(frame.GetHeader(), payload.Credit)
	case *contractv1.ConnectResponse_Ping:
		return session.enqueueTunnelFrame(&contractv1.ConnectRequest_Pong{
			Pong: &contractv1.Pong{Nonce: payload.Ping.GetNonce()},
		})
	case *contractv1.ConnectResponse_Pong:
		session.publishPong(payload.Pong.GetNonce())
		return nil
	case *contractv1.ConnectResponse_Drain:
		return session.handlePeerDrain(payload.Drain)
	default:
		return errProtocol
	}
}

func (session *session) handleOpen(
	header *contractv1.FrameHeader,
	open *contractv1.Open,
) error {
	if header.GetSequence() != 1 {
		return errProtocol
	}
	if session.draining.Load() {
		return session.rejectOpen(header, contractv1.ResultCode_RESULT_CODE_UNAVAILABLE)
	}
	route, found := session.registry.lookup(open.GetRouteId())
	if !found {
		return session.rejectOpen(header, contractv1.ResultCode_RESULT_CODE_PERMISSION_DENIED)
	}
	if _, enabled := session.routes[route.ID]; !enabled {
		return session.rejectOpen(header, contractv1.ResultCode_RESULT_CODE_PERMISSION_DENIED)
	}
	if _, enabled := session.classes[route.TrafficClass]; !enabled || header.GetTrafficClass() != route.TrafficClass {
		return errProtocol
	}

	now := session.settings.now()
	deadline := open.GetDeadline().AsTime()
	if !deadline.After(now) {
		return session.rejectOpen(header, contractv1.ResultCode_RESULT_CODE_DEADLINE_EXCEEDED)
	}
	if deadline.After(now.Add(route.MaxDeadline)) {
		return session.rejectOpen(header, contractv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT)
	}
	if route.RequireIdempotencyKey && len(open.GetIdempotencyKey()) == 0 {
		return session.rejectOpen(header, contractv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT)
	}

	requestContext := session.metadataContext(
		session.ctx,
		open.GetMetadata(),
		open.GetIdempotencyKey(),
		route.Mutating,
	)
	requestContext, cancel := context.WithDeadline(requestContext, deadline)
	request := &requestState{
		requestID:        slices.Clone(header.GetRequestId()),
		route:            route,
		ctx:              requestContext,
		cancel:           cancel,
		done:             make(chan struct{}),
		incomingSequence: header.GetSequence(),
		creditChanged:    make(chan struct{}, 1),
	}
	copy(request.key[:], header.GetRequestId())

	if !session.reserve(request) {
		cancel()
		return session.rejectOpen(header, contractv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED)
	}
	requestContext, span, started := session.observer.requestStarted(requestContext, route)
	request.ctx = requestContext
	request.span = span
	request.started = started

	grant := min(
		uint64(route.MaxRequestBytes),
		uint64(session.limits.GetMaxMessageBytes()),
		uint64(session.limits.GetMaxCreditBytes()),
		uint64(session.settings.classLimits[route.TrafficClass].ReceiveWindowBytes),
	)
	request.receiveCredit = grant
	if err := session.enqueueRequestFrame(request, &contractv1.ConnectRequest_Credit{
		Credit: &contractv1.Credit{Bytes: uint32(grant)},
	}); err != nil {
		session.abortRequest(request, contractv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED)
		return nil
	}

	session.workers.Add(1)
	go session.watchRequest(request)

	return nil
}

func (session *session) handleData(
	header *contractv1.FrameHeader,
	data *contractv1.Data,
) error {
	request, err := session.requestForFrame(header)
	if err != nil {
		return err
	}

	request.mu.Lock()
	defer request.mu.Unlock()
	if request.halfClosed || request.terminal.Load() {
		return errProtocol
	}
	size := uint64(len(data.GetPayload()))
	if size > request.receiveCredit {
		return errProtocol
	}
	maximum := min(uint64(request.route.MaxRequestBytes), uint64(session.limits.GetMaxMessageBytes()))
	if uint64(len(request.requestBytes))+size > maximum {
		return errProtocol
	}
	request.requestBytes = append(request.requestBytes, data.GetPayload()...)
	request.receiveCredit -= size

	remaining := maximum - uint64(len(request.requestBytes)) - request.receiveCredit
	window := uint64(session.settings.classLimits[request.route.TrafficClass].ReceiveWindowBytes)
	grant := min(remaining, window-request.receiveCredit, uint64(session.limits.GetMaxCreditBytes()))
	if grant == 0 {
		return nil
	}
	request.receiveCredit += grant
	request.outgoingSequence++
	frame := session.requestFrame(
		request,
		request.outgoingSequence,
		&contractv1.ConnectRequest_Credit{Credit: &contractv1.Credit{Bytes: uint32(grant)}},
	)

	return session.enqueue(request.ctx, request.route.TrafficClass, queuedFrame{frame: frame})
}

func (session *session) handleHalfClose(header *contractv1.FrameHeader) error {
	request, err := session.requestForFrame(header)
	if err != nil {
		return err
	}

	request.mu.Lock()
	if request.halfClosed || request.terminal.Load() {
		request.mu.Unlock()
		return errProtocol
	}
	request.halfClosed = true
	payload := slices.Clone(request.requestBytes)
	request.requestBytes = nil
	request.mu.Unlock()

	session.workers.Add(1)
	go session.execute(request, payload)

	return nil
}

func (session *session) handleCancel(
	header *contractv1.FrameHeader,
	cancel *contractv1.Cancel,
) error {
	request, err := session.updateRequestForFrame(header, func(request *requestState) error {
		if request.peerCanceled {
			return errProtocol
		}
		request.peerCanceled = true

		return nil
	})
	if err != nil {
		if handled, terminalErr := session.handleTerminalFrame(header, cancel); handled {
			return terminalErr
		}
		return err
	}
	request.cancel()
	session.completeRequest(request, nil, contractv1.ResultCode_RESULT_CODE_CANCELED)

	return nil
}

func (session *session) handleCredit(
	header *contractv1.FrameHeader,
	credit *contractv1.Credit,
) error {
	_, err := session.updateRequestForFrame(header, func(request *requestState) error {
		maximum := min(uint64(request.route.MaxResponseBytes), uint64(session.limits.GetMaxMessageBytes()))
		increment := uint64(credit.GetBytes())
		if increment > maximum || request.sendCredit > maximum-increment {
			return errProtocol
		}
		request.sendCredit += increment
		notify(request.creditChanged)

		return nil
	})
	if err != nil {
		if handled, terminalErr := session.handleTerminalFrame(header, credit); handled {
			return terminalErr
		}
		return err
	}

	return nil
}

func (session *session) handlePeerDrain(drain *contractv1.Drain) error {
	if session.draining.Swap(true) {
		return nil
	}

	deadline := drain.GetDeadline().AsTime()
	session.workers.Add(1)
	go func() {
		defer session.workers.Done()
		ctx, cancel := context.WithDeadline(context.WithoutCancel(session.ctx), deadline)
		defer cancel()
		_ = session.drain(ctx, drainPeer)
		session.cancel()
	}()

	return nil
}

func (session *session) requestForFrame(
	header *contractv1.FrameHeader,
) (*requestState, error) {
	var key requestKey
	copy(key[:], header.GetRequestId())
	session.requestsMu.Lock()
	request := session.requests[key]
	if request == nil || header.GetTrafficClass() != request.route.TrafficClass {
		session.requestsMu.Unlock()
		return nil, errProtocol
	}

	request.mu.Lock()
	expected := request.incomingSequence + 1
	if header.GetSequence() != expected {
		request.mu.Unlock()
		session.requestsMu.Unlock()
		return nil, errProtocol
	}
	request.incomingSequence = header.GetSequence()
	request.mu.Unlock()
	session.requestsMu.Unlock()

	return request, nil
}

func (session *session) updateRequestForFrame(
	header *contractv1.FrameHeader,
	update func(*requestState) error,
) (*requestState, error) {
	var key requestKey
	copy(key[:], header.GetRequestId())
	session.requestsMu.Lock()
	defer session.requestsMu.Unlock()
	request := session.requests[key]
	if request == nil || header.GetTrafficClass() != request.route.TrafficClass {
		return nil, errProtocol
	}

	request.mu.Lock()
	defer request.mu.Unlock()
	if header.GetSequence() != request.incomingSequence+1 {
		return nil, errProtocol
	}
	if err := update(request); err != nil {
		return nil, err
	}
	request.incomingSequence = header.GetSequence()

	return request, nil
}

func (session *session) handleTerminalFrame(
	header *contractv1.FrameHeader,
	payload any,
) (bool, error) {
	var key requestKey
	copy(key[:], header.GetRequestId())
	session.requestsMu.Lock()
	defer session.requestsMu.Unlock()
	terminal := session.terminal[key]
	if terminal == nil {
		return false, nil
	}
	if header.GetTrafficClass() != terminal.class ||
		header.GetSequence() != terminal.incomingSequence+1 ||
		terminal.canceled {
		return true, errProtocol
	}

	switch typed := payload.(type) {
	case *contractv1.Credit:
		increment := uint64(typed.GetBytes())
		if increment > terminal.maximumCredit ||
			terminal.sendCredit > terminal.maximumCredit-increment {
			return true, errProtocol
		}
		terminal.sendCredit += increment
	case *contractv1.Cancel:
		terminal.canceled = true
	default:
		return true, errProtocol
	}
	terminal.incomingSequence = header.GetSequence()

	return true, nil
}

func (session *session) execute(request *requestState, payload []byte) {
	defer session.workers.Done()
	response, code := request.route.invoke(request.ctx, payload)
	session.completeRequest(request, response, resultCode(code))
}

func (session *session) watchRequest(request *requestState) {
	defer session.workers.Done()
	select {
	case <-request.done:
		return
	case <-request.ctx.Done():
		code := contractv1.ResultCode_RESULT_CODE_CANCELED
		if errors.Is(request.ctx.Err(), context.DeadlineExceeded) {
			code = contractv1.ResultCode_RESULT_CODE_DEADLINE_EXCEEDED
		}
		session.completeRequest(request, nil, code)
	}
}

func (session *session) completeRequest(
	request *requestState,
	response []byte,
	code contractv1.ResultCode,
) {
	if !request.terminal.CompareAndSwap(false, true) {
		return
	}

	if code == contractv1.ResultCode_RESULT_CODE_OK && len(response) > 0 {
		if err := session.sendResponse(request, response); err != nil {
			switch {
			case session.ctx.Err() != nil:
				code = contractv1.ResultCode_RESULT_CODE_UNAVAILABLE
			case errors.Is(request.ctx.Err(), context.DeadlineExceeded):
				code = contractv1.ResultCode_RESULT_CODE_DEADLINE_EXCEEDED
			default:
				code = contractv1.ResultCode_RESULT_CODE_CANCELED
			}
		}
	}
	if err := session.enqueueRequestFrames(
		request,
		&contractv1.ConnectRequest_HalfClose{HalfClose: &contractv1.HalfClose{}},
		&contractv1.ConnectRequest_Result{Result: &contractv1.Result{Code: code}},
	); err != nil {
		session.cancel()
	}

	request.mu.Lock()
	request.requestBytes = nil
	request.mu.Unlock()
	request.cancel()
	close(request.done)
	session.release(request)
	session.observer.requestFinished(
		context.WithoutCancel(session.ctx),
		request.route,
		request.span,
		request.started,
		code,
	)
}

func (session *session) sendResponse(request *requestState, response []byte) error {
	maxData := int(session.limits.GetMaxDataBytes())
	for len(response) > 0 {
		chunkSize := min(len(response), maxData)
		if err := request.consumeSendCredit(chunkSize); err != nil {
			return err
		}
		chunk := slices.Clone(response[:chunkSize])
		response = response[chunkSize:]
		if err := session.enqueueRequestFrame(request, &contractv1.ConnectRequest_Data{
			Data: &contractv1.Data{Payload: chunk},
		}); err != nil {
			return err
		}
	}

	return nil
}

func (request *requestState) consumeSendCredit(size int) error {
	for {
		request.mu.Lock()
		if request.sendCredit >= uint64(size) {
			request.sendCredit -= uint64(size)
			request.mu.Unlock()
			return nil
		}
		request.mu.Unlock()

		select {
		case <-request.creditChanged:
		case <-request.ctx.Done():
			return request.ctx.Err()
		}
	}
}

func (session *session) reserve(request *requestState) bool {
	session.requestsMu.Lock()
	defer session.requestsMu.Unlock()
	if session.draining.Load() {
		return false
	}
	if _, found := session.requests[request.key]; found {
		return false
	}
	if _, found := session.terminal[request.key]; found {
		return false
	}
	if uint32(len(session.requests)) >= session.limits.GetMaxInFlightRequests() {
		return false
	}
	classLimit := session.settings.classLimits[request.route.TrafficClass].MaxInFlight
	if session.activeByClass[request.route.TrafficClass] >= classLimit {
		return false
	}

	session.requests[request.key] = request
	session.activeByClass[request.route.TrafficClass]++
	notify(session.activeChanged)

	return true
}

func (session *session) release(request *requestState) {
	session.requestsMu.Lock()
	if current := session.requests[request.key]; current == request {
		request.mu.Lock()
		session.addTerminalLocked(request.key, &terminalRequest{
			class:            request.route.TrafficClass,
			incomingSequence: request.incomingSequence,
			sendCredit:       request.sendCredit,
			canceled:         request.peerCanceled,
			maximumCredit: min(
				uint64(request.route.MaxResponseBytes),
				uint64(session.limits.GetMaxMessageBytes()),
			),
		})
		request.mu.Unlock()
		delete(session.requests, request.key)
		session.activeByClass[request.route.TrafficClass]--
	}
	session.requestsMu.Unlock()
	notify(session.activeChanged)
}

func (session *session) addTerminalLocked(key requestKey, terminal *terminalRequest) {
	capacity := int(session.limits.GetMaxInFlightRequests())
	if current := session.terminal[key]; current != nil {
		session.terminal[key] = terminal
		return
	}
	if len(session.terminalOrder) < capacity {
		session.terminalOrder = append(session.terminalOrder, key)
		session.terminal[key] = terminal
		return
	}

	oldest := session.terminalOrder[session.terminalNext]
	delete(session.terminal, oldest)
	session.terminalOrder[session.terminalNext] = key
	session.terminal[key] = terminal
	session.terminalNext = (session.terminalNext + 1) % capacity
}

func (session *session) abortRequest(
	request *requestState,
	code contractv1.ResultCode,
) {
	if !request.terminal.CompareAndSwap(false, true) {
		return
	}
	request.cancel()
	request.mu.Lock()
	request.requestBytes = nil
	request.mu.Unlock()
	close(request.done)
	session.release(request)
	session.observer.requestFinished(
		context.WithoutCancel(session.ctx),
		request.route,
		request.span,
		request.started,
		code,
	)
}

func (session *session) abortAll(code contractv1.ResultCode) {
	session.requestsMu.Lock()
	requests := make([]*requestState, 0, len(session.requests))
	for _, request := range session.requests {
		requests = append(requests, request)
	}
	session.requestsMu.Unlock()
	for _, request := range requests {
		session.abortRequest(request, code)
	}
}

func (session *session) rejectOpen(
	header *contractv1.FrameHeader,
	code contractv1.ResultCode,
) error {
	halfClose := &contractv1.ConnectRequest{
		Header: &contractv1.FrameHeader{
			ProtocolVersion: protocolVersion,
			TunnelId:        slices.Clone(session.tunnelID),
			RequestId:       slices.Clone(header.GetRequestId()),
			Sequence:        1,
			TrafficClass:    header.GetTrafficClass(),
		},
		Payload: &contractv1.ConnectRequest_HalfClose{
			HalfClose: &contractv1.HalfClose{},
		},
	}
	result := &contractv1.ConnectRequest{
		Header: &contractv1.FrameHeader{
			ProtocolVersion: protocolVersion,
			TunnelId:        slices.Clone(session.tunnelID),
			RequestId:       slices.Clone(header.GetRequestId()),
			Sequence:        2,
			TrafficClass:    header.GetTrafficClass(),
		},
		Payload: &contractv1.ConnectRequest_Result{
			Result: &contractv1.Result{Code: code},
		},
	}

	if err := session.enqueue(
		session.ctx,
		header.GetTrafficClass(),
		queuedFrame{frame: halfClose, following: []*contractv1.ConnectRequest{result}},
	); err != nil {
		return err
	}

	var key requestKey
	copy(key[:], header.GetRequestId())
	session.requestsMu.Lock()
	session.addTerminalLocked(key, &terminalRequest{
		class:            header.GetTrafficClass(),
		incomingSequence: header.GetSequence(),
		maximumCredit:    uint64(session.limits.GetMaxMessageBytes()),
	})
	session.requestsMu.Unlock()

	return nil
}

func (session *session) enqueueRequestFrame(
	request *requestState,
	payload any,
) error {
	request.mu.Lock()
	request.outgoingSequence++
	sequence := request.outgoingSequence
	request.mu.Unlock()
	frame := session.requestFrame(request, sequence, payload)

	return session.enqueue(session.ctx, request.route.TrafficClass, queuedFrame{frame: frame})
}

func (session *session) enqueueRequestFrames(
	request *requestState,
	payloads ...any,
) error {
	request.mu.Lock()
	defer request.mu.Unlock()
	frames := make([]*contractv1.ConnectRequest, 0, len(payloads))
	for _, payload := range payloads {
		request.outgoingSequence++
		frames = append(frames, session.requestFrame(request, request.outgoingSequence, payload))
	}
	if len(frames) == 0 {
		return nil
	}

	return session.enqueue(
		session.ctx,
		request.route.TrafficClass,
		queuedFrame{frame: frames[0], following: frames[1:]},
	)
}

func (session *session) requestFrame(
	request *requestState,
	sequence uint64,
	payload any,
) *contractv1.ConnectRequest {
	frame := &contractv1.ConnectRequest{
		Header: &contractv1.FrameHeader{
			ProtocolVersion: protocolVersion,
			TunnelId:        slices.Clone(session.tunnelID),
			RequestId:       slices.Clone(request.requestID),
			Sequence:        sequence,
			TrafficClass:    request.route.TrafficClass,
		},
	}
	setGatewayOutPayload(frame, payload)

	return frame
}

func (session *session) enqueueTunnelFrame(payload any) error {
	frame := &contractv1.ConnectRequest{
		Header: &contractv1.FrameHeader{
			ProtocolVersion: protocolVersion,
			TunnelId:        slices.Clone(session.tunnelID),
		},
	}
	setGatewayOutPayload(frame, payload)

	return session.enqueue(
		session.ctx,
		contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
		queuedFrame{frame: frame},
	)
}

func setGatewayOutPayload(frame *contractv1.ConnectRequest, payload any) {
	switch typed := payload.(type) {
	case *contractv1.ConnectRequest_Data:
		frame.Payload = typed
	case *contractv1.ConnectRequest_HalfClose:
		frame.Payload = typed
	case *contractv1.ConnectRequest_Cancel:
		frame.Payload = typed
	case *contractv1.ConnectRequest_Result:
		frame.Payload = typed
	case *contractv1.ConnectRequest_Credit:
		frame.Payload = typed
	case *contractv1.ConnectRequest_Ping:
		frame.Payload = typed
	case *contractv1.ConnectRequest_Pong:
		frame.Payload = typed
	case *contractv1.ConnectRequest_Drain:
		frame.Payload = typed
	case *contractv1.ConnectRequest_RevokeSession:
		frame.Payload = typed
	}
}

func (session *session) enqueue(
	ctx context.Context,
	class contractv1.TrafficClass,
	queued queuedFrame,
) error {
	for _, frame := range append([]*contractv1.ConnectRequest{queued.frame}, queued.following...) {
		if err := protocolv1.ValidateGatewayOutFrame(frame); err != nil {
			return errProtocol
		}
		if session.limits != nil && !withinNegotiatedLimits(frame, session.limits) {
			return errProtocol
		}
	}
	queue := session.queue(class)
	select {
	case queue <- queued:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-session.ctx.Done():
		return errSessionClosed
	default:
		return ErrQueueFull
	}
}

func (session *session) queue(class contractv1.TrafficClass) chan queuedFrame {
	switch class {
	case contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR:
		return session.regularQueue
	case contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME:
		return session.realtimeQueue
	default:
		return session.controlQueue
	}
}

func (session *session) keepaliveLoop() error {
	ticker := time.NewTicker(session.settings.pingInterval)
	defer ticker.Stop()
	nonce := uint64(0)

	for {
		select {
		case <-session.ctx.Done():
			return errSessionClosed
		case <-ticker.C:
			nonce++
			if err := session.enqueueTunnelFrame(&contractv1.ConnectRequest_Ping{
				Ping: &contractv1.Ping{Nonce: nonce},
			}); err != nil {
				return err
			}
			if err := session.waitPong(nonce); err != nil {
				return err
			}
		}
	}
}

func (session *session) waitPong(nonce uint64) error {
	timer := time.NewTimer(session.settings.pingTimeout)
	defer timer.Stop()
	for {
		select {
		case received := <-session.pongs:
			if received == nonce {
				return nil
			}
		case <-timer.C:
			return errKeepaliveTimeout
		case <-session.ctx.Done():
			return errSessionClosed
		}
	}
}

func (session *session) publishPong(nonce uint64) {
	select {
	case session.pongs <- nonce:
		return
	default:
	}
	select {
	case <-session.pongs:
	default:
	}
	select {
	case session.pongs <- nonce:
	default:
	}
}

func (session *session) drain(ctx context.Context, source drainSource) error {
	session.draining.Store(true)
	if session.drainSent.CompareAndSwap(false, true) {
		deadline, found := ctx.Deadline()
		if !found {
			deadline = session.settings.now().Add(session.settings.drainTimeout)
		}
		reason := contractv1.DrainReason_DRAIN_REASON_SHUTDOWN
		if source == drainPeer {
			reason = contractv1.DrainReason_DRAIN_REASON_MAINTENANCE
		}
		frame := &contractv1.ConnectRequest{
			Header: &contractv1.FrameHeader{
				ProtocolVersion: protocolVersion,
				TunnelId:        slices.Clone(session.tunnelID),
			},
			Payload: &contractv1.ConnectRequest_Drain{
				Drain: &contractv1.Drain{
					Deadline: timestamppb.New(deadline),
					Reason:   reason,
				},
			},
		}
		sent := make(chan error, 1)
		if err := session.enqueue(
			ctx,
			contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
			queuedFrame{frame: frame, sent: sent},
		); err != nil {
			return err
		}
		select {
		case err := <-sent:
			if err != nil {
				return errSessionClosed
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-session.ctx.Done():
			return errSessionClosed
		}
	}

	for {
		session.requestsMu.Lock()
		active := len(session.requests)
		session.requestsMu.Unlock()
		if active == 0 {
			return nil
		}

		select {
		case <-session.activeChanged:
		case <-ctx.Done():
			session.abortAll(contractv1.ResultCode_RESULT_CODE_CANCELED)
			return ctx.Err()
		case <-session.ctx.Done():
			return errSessionClosed
		}
	}
}

func (session *session) metadataContext(
	ctx context.Context,
	entries []*contractv1.Metadata,
	idempotencyKey []byte,
	isMutating bool,
) context.Context {
	carrier := propagation.MapCarrier{}
	outgoing := metadata.MD{}
	for _, entry := range entries {
		value := string(entry.GetValue())
		switch entry.GetKey() {
		case contractv1.MetadataKey_METADATA_KEY_TRACEPARENT:
			carrier.Set("traceparent", value)
			outgoing.Set("traceparent", value)
		case contractv1.MetadataKey_METADATA_KEY_TRACESTATE:
			carrier.Set("tracestate", value)
			outgoing.Set("tracestate", value)
		case contractv1.MetadataKey_METADATA_KEY_SESSION_ASSERTION:
			outgoing.Set(internalSessionAssertionMetadata, value)
		case contractv1.MetadataKey_METADATA_KEY_CONTENT_TYPE:
			// gRPC transport owns content-type; it is validated but not copied.
		}
	}
	if isMutating && len(idempotencyKey) > 0 {
		outgoing.Set(protocolv1.InternalIdempotencyKeyMetadata, string(idempotencyKey))
	}
	ctx = session.settings.telemetry.Propagator().Extract(ctx, carrier)

	return metadata.NewOutgoingContext(ctx, outgoing)
}

func limitsDoNotExceed(selected *contractv1.Limits, offered ReceiveLimits) bool {
	if selected == nil {
		return false
	}

	return selected.GetMaxFrameBytes() <= offered.MaxFrameBytes &&
		selected.GetMaxDataBytes() <= offered.MaxDataBytes &&
		selected.GetMaxMessageBytes() <= offered.MaxMessageBytes &&
		selected.GetMaxInFlightRequests() <= offered.MaxInFlightRequests &&
		selected.GetMaxMetadataEntries() <= offered.MaxMetadataEntries &&
		selected.GetMaxMetadataValueBytes() <= offered.MaxMetadataValueBytes &&
		selected.GetMaxCreditBytes() <= offered.MaxCreditBytes
}

func cloneLimits(source *contractv1.Limits) *contractv1.Limits {
	if source == nil {
		return nil
	}
	cloned, ok := proto.Clone(source).(*contractv1.Limits)
	if !ok {
		return nil
	}

	return cloned
}

func withinNegotiatedLimits(message proto.Message, limits *contractv1.Limits) bool {
	if message == nil || limits == nil || proto.Size(message) > int(limits.GetMaxFrameBytes()) {
		return false
	}

	var data *contractv1.Data
	var credit *contractv1.Credit
	var entries []*contractv1.Metadata
	switch frame := message.(type) {
	case *contractv1.ConnectRequest:
		data = frame.GetData()
		credit = frame.GetCredit()
		if result := frame.GetResult(); result != nil {
			entries = result.GetMetadata()
		}
	case *contractv1.ConnectResponse:
		data = frame.GetData()
		credit = frame.GetCredit()
		if open := frame.GetOpen(); open != nil {
			entries = open.GetMetadata()
		}
	default:
		return false
	}

	if data != nil && uint64(len(data.GetPayload())) > uint64(limits.GetMaxDataBytes()) {
		return false
	}
	if credit != nil && credit.GetBytes() > limits.GetMaxCreditBytes() {
		return false
	}
	if uint64(len(entries)) > uint64(limits.GetMaxMetadataEntries()) {
		return false
	}
	for _, entry := range entries {
		if uint64(len(entry.GetValue())) > uint64(limits.GetMaxMetadataValueBytes()) {
			return false
		}
	}

	return true
}

func notify(channel chan<- struct{}) {
	select {
	case channel <- struct{}{}:
	default:
	}
}

func protocolFailureCategory(err error) string {
	switch {
	case errors.Is(err, ErrQueueFull):
		return "queue_full"
	case errors.Is(err, errKeepaliveTimeout):
		return "keepalive"
	default:
		return "invalid_frame"
	}
}

func gatewayInFrameType(frame *contractv1.ConnectResponse) string {
	switch frame.GetPayload().(type) {
	case *contractv1.ConnectResponse_Hello:
		return "hello"
	case *contractv1.ConnectResponse_Open:
		return "open"
	case *contractv1.ConnectResponse_Data:
		return "data"
	case *contractv1.ConnectResponse_HalfClose:
		return "half_close"
	case *contractv1.ConnectResponse_Cancel:
		return "cancel"
	case *contractv1.ConnectResponse_Credit:
		return "credit"
	case *contractv1.ConnectResponse_Ping:
		return "ping"
	case *contractv1.ConnectResponse_Pong:
		return "pong"
	case *contractv1.ConnectResponse_Drain:
		return "drain"
	default:
		return "unknown"
	}
}

func gatewayOutFrameType(frame *contractv1.ConnectRequest) string {
	switch frame.GetPayload().(type) {
	case *contractv1.ConnectRequest_Hello:
		return "hello"
	case *contractv1.ConnectRequest_Data:
		return "data"
	case *contractv1.ConnectRequest_HalfClose:
		return "half_close"
	case *contractv1.ConnectRequest_Cancel:
		return "cancel"
	case *contractv1.ConnectRequest_Result:
		return "result"
	case *contractv1.ConnectRequest_Credit:
		return "credit"
	case *contractv1.ConnectRequest_Ping:
		return "ping"
	case *contractv1.ConnectRequest_Pong:
		return "pong"
	case *contractv1.ConnectRequest_Drain:
		return "drain"
	case *contractv1.ConnectRequest_RevokeSession:
		return "revoke_session"
	default:
		return "unknown"
	}
}
