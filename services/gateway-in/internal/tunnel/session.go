package tunnel

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type tunnelStream interface {
	Context() context.Context
	Recv() (*contractv1.ConnectRequest, error)
	Send(*contractv1.ConnectResponse) error
}

type sessionFailure struct {
	requestErr error
	rpcErr     error
	reason     string
}

type sessionParams struct {
	settings   *settings
	registry   *Registry
	stream     tunnelStream
	id         [16]byte
	instanceID [16]byte
	dataCenter string
	negotiated negotiation
}

type session struct {
	settings     *settings
	registry     *Registry
	stream       tunnelStream
	id           [16]byte
	instanceID   [16]byte
	dataCenter   string
	limits       *contractv1.Limits
	routes       map[contractv1.RouteId]struct{}
	classes      map[contractv1.TrafficClass]struct{}
	capabilities map[contractv1.Capability]struct{}

	ctx       context.Context
	cancel    context.CancelFunc
	outbound  *outboundQueue
	done      chan struct{}
	terminal  chan sessionFailure
	failOnce  sync.Once
	doneOnce  sync.Once
	drainOnce sync.Once

	mu                sync.Mutex
	isReady           bool
	isDraining        bool
	isClosed          bool
	failureReason     string
	lastActivity      time.Time
	requests          map[[16]byte]*logicalRequest
	routeActive       map[contractv1.RouteId]int
	tombstones        map[[16]byte]struct{}
	tombstoneOrder    [][16]byte
	tombstonePosition int
	pendingPing       uint64
	drainTimer        *time.Timer
	localDrain        bool
	drainSent         chan struct{}
	pongReceived      chan struct{}
}

func newSession(params sessionParams) *session {
	ctx, cancel := context.WithCancel(params.stream.Context())
	return &session{
		settings:       params.settings,
		registry:       params.registry,
		stream:         params.stream,
		id:             params.id,
		instanceID:     params.instanceID,
		dataCenter:     params.dataCenter,
		limits:         params.negotiated.limits,
		routes:         params.negotiated.routes,
		classes:        params.negotiated.classes,
		capabilities:   params.negotiated.capabilities,
		ctx:            ctx,
		cancel:         cancel,
		outbound:       newOutboundQueue(params.settings.queues, params.settings.instrumentation),
		done:           make(chan struct{}),
		terminal:       make(chan sessionFailure, 1),
		requests:       map[[16]byte]*logicalRequest{},
		routeActive:    map[contractv1.RouteId]int{},
		tombstones:     map[[16]byte]struct{}{},
		tombstoneOrder: make([][16]byte, 0, params.negotiated.limits.GetMaxInFlightRequests()),
		drainSent:      make(chan struct{}, 1),
		pongReceived:   make(chan struct{}, 1),
	}
}

func (s *session) run() error {
	s.mu.Lock()
	s.isReady = true
	s.lastActivity = s.settings.now()
	s.mu.Unlock()
	s.registry.markSessionReady(s)
	s.settings.instrumentation.tunnelDelta(s.ctx, s.dataCenter, 1)
	s.settings.log.InfoContext(
		s.ctx,
		"обратный туннель принят",
		slog.String("data_center", s.dataCenter),
		slog.Int("routes", len(s.routes)),
	)

	go s.readLoop()
	go s.writeLoop()
	go s.keepaliveLoop()

	failure := <-s.terminal
	s.cancel()
	s.registry.unregister(s)
	s.outbound.discard(context.WithoutCancel(s.ctx))
	s.settings.instrumentation.tunnelDelta(context.WithoutCancel(s.ctx), s.dataCenter, -1)
	s.settings.instrumentation.failure(
		context.WithoutCancel(s.ctx),
		s.dataCenter,
		failure.reason,
	)
	s.doneOnce.Do(func() { close(s.done) })
	s.settings.log.InfoContext(
		context.WithoutCancel(s.ctx),
		"обратный туннель завершён",
		slog.String("data_center", s.dataCenter),
		slog.String("reason", failure.reason),
	)

	return failure.rpcErr
}

func (s *session) abortBeforeRun() {
	s.cancel()
	s.registry.unregister(s)
	s.doneOnce.Do(func() { close(s.done) })
}

func (s *session) accepts(route contractv1.RouteId) bool {
	s.mu.Lock()
	if !s.isReady || s.isDraining || s.isClosed {
		s.mu.Unlock()
		return false
	}
	_, supported := s.routes[route]
	isStale := s.settings.now().Sub(s.lastActivity) >= s.settings.staleAfter
	if isStale {
		s.isReady = false
	}
	s.mu.Unlock()
	if isStale {
		s.fail(sessionFailure{
			requestErr: ErrTunnelClosed,
			rpcErr:     status.Error(codes.Unavailable, "tunnel unavailable"),
			reason:     "stale",
		})
		return false
	}

	return supported
}

func (s *session) isReadyForRoute(route contractv1.RouteId) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isReady || s.isDraining || s.isClosed {
		return false
	}
	_, supported := s.routes[route]

	return supported && s.settings.now().Sub(s.lastActivity) < s.settings.staleAfter
}

func (s *session) startRequest(
	route contractv1.RouteId,
	policy RoutePolicy,
	deadline time.Time,
	requestBytes int,
) (*logicalRequest, error) {
	effectivePolicy := policy
	effectivePolicy.MaxRequestBytes = min(
		effectivePolicy.MaxRequestBytes,
		int(s.limits.GetMaxMessageBytes()),
	)
	effectivePolicy.MaxResponseBytes = min(
		effectivePolicy.MaxResponseBytes,
		int(s.limits.GetMaxMessageBytes()),
	)
	effectivePolicy.MaxInFlight = min(
		effectivePolicy.MaxInFlight,
		int(s.limits.GetMaxInFlightRequests()),
	)
	if requestBytes > effectivePolicy.MaxRequestBytes {
		return nil, newResultError(contractv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED)
	}

	for range 4 {
		requestID, err := randomOpaqueID()
		if err != nil {
			return nil, err
		}

		s.mu.Lock()
		if !s.isReady || s.isDraining || s.isClosed {
			s.mu.Unlock()
			return nil, ErrDraining
		}
		if _, supported := s.routes[route]; !supported {
			s.mu.Unlock()
			return nil, ErrRouteNotAllowed
		}
		if len(s.requests) >= int(s.limits.GetMaxInFlightRequests()) ||
			s.routeActive[route] >= effectivePolicy.MaxInFlight {
			s.mu.Unlock()
			return nil, ErrQueueFull
		}
		if _, exists := s.requests[requestID]; exists {
			s.mu.Unlock()
			continue
		}
		if _, exists := s.tombstones[requestID]; exists {
			s.mu.Unlock()
			continue
		}

		request := newLogicalRequest(s, requestID, route, effectivePolicy, deadline)
		s.requests[requestID] = request
		s.routeActive[route]++
		s.mu.Unlock()
		s.settings.instrumentation.requestDelta(
			s.ctx,
			requestMetric{
				dataCenter: s.dataCenter,
				route:      route,
				class:      policy.TrafficClass,
			},
			1,
		)
		return request, nil
	}

	return nil, errors.New("tunnel: request id collision")
}

func (s *session) finishRequest(request *logicalRequest) {
	s.removeRequest(request, true)
}

func (s *session) rollbackRequestBeforeOpen(request *logicalRequest) {
	s.removeRequest(request, false)
}

func (s *session) removeRequest(request *logicalRequest, addTombstone bool) {
	s.mu.Lock()
	current, exists := s.requests[request.id]
	if !exists || current != request {
		s.mu.Unlock()
		return
	}
	delete(s.requests, request.id)
	s.routeActive[request.route]--
	if s.routeActive[request.route] == 0 {
		delete(s.routeActive, request.route)
	}
	if addTombstone {
		s.addTombstoneLocked(request.id)
	}
	shouldFinishDrain := s.isDraining && len(s.requests) == 0
	isLocalDrain := s.localDrain
	s.mu.Unlock()

	s.registry.releaseInstance(s.instanceID)
	s.settings.instrumentation.requestDelta(
		context.WithoutCancel(s.ctx),
		requestMetric{
			dataCenter: s.dataCenter,
			route:      request.route,
			class:      request.policy.TrafficClass,
		},
		-1,
	)
	if shouldFinishDrain {
		if isLocalDrain {
			s.finishLocalDrainAfterSend()
		} else {
			s.fail(sessionFailure{
				requestErr: ErrDraining,
				reason:     "drained",
			})
		}
	}
}

func (s *session) addTombstoneLocked(requestID [16]byte) {
	capacity := int(s.limits.GetMaxInFlightRequests())
	if len(s.tombstoneOrder) < capacity {
		s.tombstoneOrder = append(s.tombstoneOrder, requestID)
		s.tombstones[requestID] = struct{}{}
		return
	}

	oldest := s.tombstoneOrder[s.tombstonePosition]
	delete(s.tombstones, oldest)
	s.tombstoneOrder[s.tombstonePosition] = requestID
	s.tombstones[requestID] = struct{}{}
	s.tombstonePosition = (s.tombstonePosition + 1) % capacity
}

func (s *session) enqueueFrame(ctx context.Context, frame *contractv1.ConnectResponse) error {
	s.mu.Lock()
	isClosed := s.isClosed
	s.mu.Unlock()
	if isClosed {
		return ErrTunnelClosed
	}
	if err := protocolv1.ValidateGatewayInFrame(frame); err != nil {
		return errors.Join(ErrProtocolViolation, err)
	}
	if !bytes.Equal(frame.GetHeader().GetTunnelId(), s.id[:]) {
		return ErrProtocolViolation
	}
	if proto.Size(frame) > int(s.limits.GetMaxFrameBytes()) {
		return ErrQueueFull
	}
	if data := frame.GetData(); data != nil &&
		len(data.GetPayload()) > int(s.limits.GetMaxDataBytes()) {
		return ErrQueueFull
	}
	if credit := frame.GetCredit(); credit != nil &&
		credit.GetBytes() > s.limits.GetMaxCreditBytes() {
		return ErrProtocolViolation
	}

	return s.outbound.enqueue(ctx, frame)
}

func (s *session) enqueueControl(ctx context.Context, payload any) error {
	frame := &contractv1.ConnectResponse{
		Header: &contractv1.FrameHeader{
			ProtocolVersion: protocolVersion,
			TunnelId:        slices.Clone(s.id[:]),
		},
	}
	switch typedPayload := payload.(type) {
	case *contractv1.ConnectResponse_Ping:
		frame.Payload = typedPayload
	case *contractv1.ConnectResponse_Pong:
		frame.Payload = typedPayload
	case *contractv1.ConnectResponse_Drain:
		frame.Payload = typedPayload
	default:
		return ErrProtocolViolation
	}

	return s.enqueueFrame(ctx, frame)
}

func (s *session) readLoop() {
	for {
		frame, err := s.stream.Recv()
		if err != nil {
			s.handleReadError(err)
			return
		}
		if err := s.validateInbound(frame); err != nil {
			s.fail(sessionFailure{
				requestErr: ErrTunnelClosed,
				rpcErr:     status.Error(codes.InvalidArgument, "tunnel protocol violation"),
				reason:     "protocol_violation",
			})
			return
		}
		s.mu.Lock()
		s.lastActivity = s.settings.now()
		s.mu.Unlock()
		s.settings.instrumentation.recordFrame(
			s.ctx,
			"gateway_out_to_gateway_in",
			frame.GetHeader().GetTrafficClass(),
			inboundFrameType(frame),
		)
		if err := s.handleInbound(frame); err != nil {
			if errors.Is(err, ErrQueueFull) {
				continue
			}
			s.fail(sessionFailure{
				requestErr: ErrTunnelClosed,
				rpcErr:     status.Error(codes.InvalidArgument, "tunnel protocol violation"),
				reason:     "protocol_violation",
			})
			return
		}
	}
}

func (s *session) handleReadError(err error) {
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) ||
		errors.Is(err, s.stream.Context().Err()) {
		s.fail(sessionFailure{
			requestErr: ErrTunnelClosed,
			reason:     "peer_closed",
		})
		return
	}

	s.fail(sessionFailure{
		requestErr: ErrTunnelClosed,
		rpcErr:     status.Error(codes.Unavailable, "tunnel unavailable"),
		reason:     "receive_failed",
	})
}

func (s *session) writeLoop() {
	for {
		frame, err := s.outbound.dequeue(s.ctx)
		if err != nil {
			return
		}
		if err := s.stream.Send(frame); err != nil {
			s.fail(sessionFailure{
				requestErr: ErrTunnelClosed,
				rpcErr:     status.Error(codes.Unavailable, "tunnel unavailable"),
				reason:     "send_failed",
			})
			return
		}
		if frame.GetDrain() != nil {
			select {
			case s.drainSent <- struct{}{}:
			default:
			}
		}
		s.settings.instrumentation.recordFrame(
			s.ctx,
			"gateway_in_to_gateway_out",
			frame.GetHeader().GetTrafficClass(),
			outboundFrameType(frame),
		)
	}
}

func (s *session) keepaliveLoop() {
	timer := time.NewTimer(s.settings.pingInterval)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.pongReceived:
			resetTimer(timer, s.settings.pingInterval)
		case <-timer.C:
			s.mu.Lock()
			if s.pendingPing != 0 {
				s.mu.Unlock()
				s.fail(sessionFailure{
					requestErr: ErrTunnelClosed,
					rpcErr:     status.Error(codes.Unavailable, "tunnel unavailable"),
					reason:     "pong_timeout",
				})
				return
			}
			nonce, err := randomNonce()
			if err != nil {
				s.mu.Unlock()
				s.fail(sessionFailure{
					requestErr: ErrTunnelClosed,
					rpcErr:     status.Error(codes.Internal, "internal error"),
					reason:     "random_failed",
				})
				return
			}
			s.pendingPing = nonce
			s.mu.Unlock()
			if err := s.enqueueControl(
				s.ctx,
				&contractv1.ConnectResponse_Ping{Ping: &contractv1.Ping{Nonce: nonce}},
			); err != nil {
				s.fail(sessionFailure{
					requestErr: ErrTunnelClosed,
					rpcErr:     status.Error(codes.ResourceExhausted, "tunnel control saturated"),
					reason:     "control_queue_full",
				})
				return
			}
			resetTimer(timer, s.settings.pongTimeout)
		}
	}
}

func (s *session) validateInbound(frame *contractv1.ConnectRequest) error {
	if err := protocolv1.ValidateGatewayOutFrame(frame); err != nil {
		return err
	}
	if _, isHello := frame.GetPayload().(*contractv1.ConnectRequest_Hello); isHello {
		return ErrProtocolViolation
	}
	if proto.Size(frame) > int(s.limits.GetMaxFrameBytes()) {
		return ErrProtocolViolation
	}
	if !bytes.Equal(frame.GetHeader().GetTunnelId(), s.id[:]) {
		return ErrProtocolViolation
	}
	if data := frame.GetData(); data != nil &&
		len(data.GetPayload()) > int(s.limits.GetMaxDataBytes()) {
		return ErrProtocolViolation
	}
	if credit := frame.GetCredit(); credit != nil &&
		credit.GetBytes() > s.limits.GetMaxCreditBytes() {
		return ErrProtocolViolation
	}
	if result := frame.GetResult(); result != nil {
		if len(result.GetMetadata()) > int(s.limits.GetMaxMetadataEntries()) {
			return ErrProtocolViolation
		}
		for _, metadata := range result.GetMetadata() {
			if metadata == nil ||
				len(metadata.GetValue()) > int(s.limits.GetMaxMetadataValueBytes()) {
				return ErrProtocolViolation
			}
		}
	}

	return nil
}

func (s *session) handleInbound(frame *contractv1.ConnectRequest) error {
	switch payload := frame.GetPayload().(type) {
	case *contractv1.ConnectRequest_Ping:
		return s.enqueueControl(
			s.ctx,
			&contractv1.ConnectResponse_Pong{Pong: &contractv1.Pong{Nonce: payload.Ping.GetNonce()}},
		)
	case *contractv1.ConnectRequest_Pong:
		s.mu.Lock()
		if s.pendingPing == 0 || s.pendingPing != payload.Pong.GetNonce() {
			s.mu.Unlock()
			return ErrProtocolViolation
		}
		s.pendingPing = 0
		s.mu.Unlock()
		select {
		case s.pongReceived <- struct{}{}:
		default:
		}
		return nil
	case *contractv1.ConnectRequest_Drain:
		if _, enabled := s.capabilities[contractv1.Capability_CAPABILITY_DRAIN]; !enabled {
			return ErrProtocolViolation
		}
		shouldFinish, err := s.markDraining(payload.Drain.GetDeadline().AsTime(), false)
		if err != nil {
			return err
		}
		if shouldFinish {
			s.fail(sessionFailure{
				requestErr: ErrDraining,
				reason:     "drained",
			})
		}
		return nil
	case *contractv1.ConnectRequest_RevokeSession:
		return ErrProtocolViolation
	}

	requestID := [16]byte{}
	copy(requestID[:], frame.GetHeader().GetRequestId())
	s.mu.Lock()
	request, exists := s.requests[requestID]
	_, isTombstone := s.tombstones[requestID]
	s.mu.Unlock()
	if !exists {
		if isTombstone {
			return nil
		}
		return ErrProtocolViolation
	}
	if frame.GetHeader().GetTrafficClass() != request.policy.TrafficClass {
		return ErrProtocolViolation
	}

	err := request.handleInbound(frame)
	if errors.Is(err, ErrQueueFull) {
		request.complete(Response{}, ErrQueueFull)
	}

	return err
}

func (s *session) initiateDrain(
	deadline time.Time,
	reason contractv1.DrainReason,
) error {
	if _, enabled := s.capabilities[contractv1.Capability_CAPABILITY_DRAIN]; !enabled {
		s.fail(sessionFailure{
			requestErr: ErrDraining,
			reason:     "drain_unsupported",
		})
		return nil
	}
	shouldFinish, err := s.markDraining(deadline, true)
	if err != nil {
		return err
	}

	err = s.enqueueControl(
		s.ctx,
		&contractv1.ConnectResponse_Drain{
			Drain: &contractv1.Drain{
				Deadline: timestamppb.New(deadline),
				Reason:   reason,
			},
		},
	)
	if err != nil {
		s.fail(sessionFailure{
			requestErr: ErrDraining,
			reason:     "drain_enqueue_failed",
		})
		return err
	}
	if shouldFinish {
		s.finishLocalDrainAfterSend()
	}

	return nil
}

func (s *session) markDraining(deadline time.Time, local bool) (bool, error) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false, context.DeadlineExceeded
	}

	s.mu.Lock()
	if s.isClosed {
		s.mu.Unlock()
		return false, ErrTunnelClosed
	}
	s.isDraining = true
	s.localDrain = s.localDrain || local
	if s.drainTimer != nil {
		s.drainTimer.Stop()
	}
	s.drainTimer = time.AfterFunc(remaining, func() {
		s.fail(sessionFailure{
			requestErr: ErrDraining,
			reason:     "drain_deadline",
		})
	})
	shouldFinish := len(s.requests) == 0
	s.mu.Unlock()

	return shouldFinish, nil
}

func (s *session) finishLocalDrainAfterSend() {
	s.drainOnce.Do(func() {
		go func() {
			select {
			case <-s.drainSent:
				s.fail(sessionFailure{
					requestErr: ErrDraining,
					reason:     "drained",
				})
			case <-s.ctx.Done():
			}
		}()
	})
}

func (s *session) fail(failure sessionFailure) {
	s.failOnce.Do(func() {
		s.mu.Lock()
		s.isClosed = true
		s.isReady = false
		s.failureReason = failure.reason
		if s.drainTimer != nil {
			s.drainTimer.Stop()
		}
		requests := make([]*logicalRequest, 0, len(s.requests))
		for _, request := range s.requests {
			requests = append(requests, request)
		}
		s.mu.Unlock()

		for _, request := range requests {
			request.complete(Response{}, failure.requestErr)
		}
		s.cancel()
		s.terminal <- failure
	})
}

func randomOpaqueID() ([16]byte, error) {
	value := [16]byte{}
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		return [16]byte{}, errors.New("tunnel: generate opaque id")
	}

	return value, nil
}

func randomNonce() (uint64, error) {
	value, err := randomOpaqueID()
	if err != nil {
		return 0, err
	}
	nonce := uint64(0)
	for _, part := range value[:8] {
		nonce = nonce<<8 | uint64(part)
	}
	if nonce == 0 {
		return 1, nil
	}

	return nonce, nil
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func negotiatedCapability(
	capabilities map[contractv1.Capability]struct{},
	capability contractv1.Capability,
) bool {
	_, enabled := capabilities[capability]
	return enabled
}
