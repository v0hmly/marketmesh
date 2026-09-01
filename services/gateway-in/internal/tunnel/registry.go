package tunnel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Registry owns authenticated active tunnels and dispatches bounded logical
// calls without exposing a network destination selector.
type Registry struct {
	settings *settings

	selectionMu     sync.Mutex
	mu              sync.Mutex
	sessions        map[[16]byte]*session
	instanceTunnels map[[16]byte]int
	instanceActive  map[[16]byte]int
	selection       map[selectionKey]*selectionState
	handshakes      int
	isDraining      bool
}

type selectionKey struct {
	route      contractv1.RouteId
	dataCenter string
}

type selectionState struct {
	readySince    time.Time
	currentWeight int64
	nextSession   int
	isReady       bool
}

type requestSelection struct {
	route        contractv1.RouteId
	policy       RoutePolicy
	deadline     time.Time
	requestBytes int
}

type reservationResult uint8

const (
	reservationAccepted reservationResult = iota
	reservationDraining
	reservationSaturated
)

func newRegistry(settings *settings) *Registry {
	return &Registry{
		settings:        settings,
		sessions:        map[[16]byte]*session{},
		instanceTunnels: map[[16]byte]int{},
		instanceActive:  map[[16]byte]int{},
		selection:       map[selectionKey]*selectionState{},
	}
}

// RoutePolicy returns a defensive copy of the local static route policy.
func (r *Registry) RoutePolicy(route contractv1.RouteId) (RoutePolicy, bool) {
	if r == nil || r.settings == nil {
		return RoutePolicy{}, false
	}

	policy, found := r.settings.routes[route]
	return policy, found
}

// IsRouteReady reports whether at least one authenticated, non-draining
// tunnel currently accepts the statically configured route.
func (r *Registry) IsRouteReady(route contractv1.RouteId) bool {
	if r == nil || r.settings == nil {
		return false
	}
	if _, allowed := r.settings.routes[route]; !allowed {
		return false
	}

	r.mu.Lock()
	sessions := make([]*session, 0, len(r.sessions))
	for _, activeSession := range r.sessions {
		sessions = append(sessions, activeSession)
	}
	isDraining := r.isDraining
	r.mu.Unlock()
	if isDraining {
		return false
	}
	for _, activeSession := range sessions {
		if activeSession.accepts(route) {
			return true
		}
	}

	return false
}

// Invoke sends one unary logical request through a ready tunnel. The effective
// deadline is the shorter of the caller deadline and the static route limit.
func (r *Registry) Invoke(ctx context.Context, call Call) (response Response, resultErr error) {
	if r == nil || r.settings == nil {
		return Response{}, ErrNoTunnel
	}
	if ctx == nil {
		return Response{}, errors.New("tunnel: context must not be nil")
	}

	policy, allowed := r.settings.routes[call.Route]
	if !allowed {
		r.settings.instrumentation.refusal(ctx, "route_not_allowed")
		return Response{}, ErrRouteNotAllowed
	}
	if len(call.Payload) > policy.MaxRequestBytes {
		r.settings.instrumentation.refusal(ctx, "request_too_large")
		return Response{}, newResultError(
			contractv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED,
		)
	}

	started := r.settings.now()
	selectedDataCenter := "none"
	ctx, span := r.settings.instrumentation.tracer.Start(
		ctx,
		"gateway_in.tunnel.invoke",
		trace.WithAttributes(
			attribute.String("tunnel.route", routeLabel(call.Route)),
			attribute.String("tunnel.traffic_class", classLabel(policy.TrafficClass)),
		),
	)
	defer span.End()
	defer func() {
		telemetryCtx := context.WithoutCancel(ctx)
		r.settings.instrumentation.finishRequest(
			telemetryCtx,
			requestResultMetric{
				requestMetric: requestMetric{
					dataCenter: selectedDataCenter,
					route:      call.Route,
					class:      policy.TrafficClass,
				},
				started: started,
				err:     resultErr,
			},
		)
		result := "ok"
		if resultErr != nil {
			result = errorResultLabel(resultErr)
			span.SetStatus(codes.Error, result)
		}
		span.SetAttributes(attribute.String("tunnel.result", result))
		r.settings.log.DebugContext(
			telemetryCtx,
			"логический запрос туннеля завершён",
			slog.String("data_center", selectedDataCenter),
			slog.String("route", routeLabel(call.Route)),
			slog.String("traffic_class", classLabel(policy.TrafficClass)),
			slog.String("result", result),
			slog.Duration("duration", time.Since(started)),
		)
	}()

	callCtx, cancel, deadline, err := contextWithRouteDeadline(ctx, policy.MaxDeadline)
	if err != nil {
		return Response{}, err
	}
	defer cancel()
	call.Metadata = metadataWithTrace(callCtx, call.Metadata)
	if err := validateCall(call, policy, deadline); err != nil {
		r.settings.instrumentation.refusal(callCtx, "invalid_call")
		return Response{}, err
	}

	request, err := r.selectAndOpenRequest(callCtx, call, requestSelection{
		route:        call.Route,
		policy:       policy,
		deadline:     deadline,
		requestBytes: len(call.Payload),
	})
	if err != nil {
		return Response{}, err
	}
	selectedDataCenter = request.session.dataCenter
	span.SetAttributes(attribute.String("tunnel.data_center", selectedDataCenter))
	defer request.session.finishRequest(request)

	// Open is already queued. Any failure from this point is returned without
	// replay so an uncertain mutation cannot run on a second tunnel.
	if err := request.sendInitialResponseCredit(callCtx); err != nil {
		request.complete(Response{}, err)
		return Response{}, err
	}
	if err := request.sendBody(callCtx, slices.Clone(call.Payload)); err != nil {
		if contextErr := callCtx.Err(); contextErr != nil {
			request.cancel(contextErr)
		} else {
			request.complete(Response{}, err)
		}
		return Response{}, err
	}

	return request.wait(callCtx)
}

// Drain stops all new calls, asks negotiated peers to drain, and waits only
// until the supplied absolute deadline or caller cancellation.
func (r *Registry) Drain(
	ctx context.Context,
	deadline time.Time,
	reason contractv1.DrainReason,
) error {
	if r == nil || r.settings == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("tunnel: context must not be nil")
	}
	if !deadline.After(r.settings.now()) {
		return context.DeadlineExceeded
	}
	if reason == contractv1.DrainReason_DRAIN_REASON_UNSPECIFIED {
		return errors.New("tunnel: drain reason must be specified")
	}

	r.mu.Lock()
	r.isDraining = true
	sessions := make([]*session, 0, len(r.sessions))
	for _, activeSession := range r.sessions {
		sessions = append(sessions, activeSession)
	}
	r.mu.Unlock()

	var resultErr error
	for _, activeSession := range sessions {
		if err := activeSession.initiateDrain(deadline, reason); err != nil &&
			!errors.Is(err, ErrTunnelClosed) {
			resultErr = errors.Join(resultErr, err)
		}
	}

	waitCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	for _, activeSession := range sessions {
		select {
		case <-activeSession.done:
		case <-waitCtx.Done():
			return errors.Join(resultErr, waitCtx.Err())
		}
	}

	return resultErr
}

func (r *Registry) beginHandshake() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.isDraining || len(r.sessions)+r.handshakes >= r.settings.maxTunnels {
		return false
	}
	r.handshakes++

	return true
}

func (r *Registry) releaseHandshake() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.handshakes > 0 {
		r.handshakes--
	}
}

func (r *Registry) registerFromHandshake(activeSession *session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.handshakes == 0 {
		return ErrProtocolViolation
	}
	r.handshakes--
	if r.isDraining {
		return ErrDraining
	}
	if len(r.sessions)+r.handshakes >= r.settings.maxTunnels {
		return ErrQueueFull
	}
	if r.instanceTunnels[activeSession.instanceID] >= r.settings.maxTunnelsPerInstance {
		return ErrQueueFull
	}
	if _, exists := r.sessions[activeSession.id]; exists {
		return ErrProtocolViolation
	}

	r.sessions[activeSession.id] = activeSession
	r.instanceTunnels[activeSession.instanceID]++

	return nil
}

func (r *Registry) unregister(activeSession *session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, exists := r.sessions[activeSession.id]; !exists || current != activeSession {
		return
	}

	delete(r.sessions, activeSession.id)
	r.instanceTunnels[activeSession.instanceID]--
	if r.instanceTunnels[activeSession.instanceID] == 0 {
		delete(r.instanceTunnels, activeSession.instanceID)
	}
	if r.instanceActive[activeSession.instanceID] == 0 {
		delete(r.instanceActive, activeSession.instanceID)
	}
	r.resetDataCenterIfUnavailableLocked(activeSession.dataCenter)
}

func (r *Registry) markSessionReady(activeSession *session) {
	now := r.settings.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for route := range activeSession.routes {
		key := selectionKey{route: route, dataCenter: activeSession.dataCenter}
		state := r.selection[key]
		if state == nil {
			state = &selectionState{}
			r.selection[key] = state
		}
		hadReadyPeer := false
		for _, peerSession := range r.sessions {
			if peerSession != activeSession &&
				peerSession.dataCenter == activeSession.dataCenter &&
				peerSession.isReadyForRoute(route) {
				hadReadyPeer = true
				break
			}
		}
		if !state.isReady || !hadReadyPeer {
			state.readySince = now
			state.currentWeight = 0
		}
		state.isReady = true
	}
}

func (r *Registry) resetDataCenterIfUnavailableLocked(dataCenter string) {
	for key, state := range r.selection {
		if key.dataCenter != dataCenter {
			continue
		}
		isReady := false
		for _, activeSession := range r.sessions {
			if activeSession.dataCenter == dataCenter && activeSession.isReadyForRoute(key.route) {
				isReady = true
				break
			}
		}
		if !isReady {
			state.isReady = false
			state.currentWeight = 0
		}
	}
}

func (r *Registry) selectAndOpenRequest(
	ctx context.Context,
	call Call,
	selection requestSelection,
) (*logicalRequest, error) {
	r.selectionMu.Lock()
	defer r.selectionMu.Unlock()

	ordered, isDraining := r.selectionOrder(selection.route)
	if isDraining {
		r.settings.instrumentation.selection(ctx, "none", selection.route, "draining")
		return nil, ErrDraining
	}

	var selectionErr error
	for _, activeSession := range ordered {
		switch r.reserveInstance(activeSession.instanceID) {
		case reservationDraining:
			r.settings.instrumentation.selection(ctx, "none", selection.route, "draining")
			return nil, ErrDraining
		case reservationSaturated:
			selectionErr = errors.Join(selectionErr, ErrQueueFull)
			continue
		}
		request, err := activeSession.startRequest(
			selection.route,
			selection.policy,
			selection.deadline,
			selection.requestBytes,
		)
		if err != nil {
			selectionErr = errors.Join(selectionErr, err)
			r.releaseInstance(activeSession.instanceID)
			continue
		}
		if err := request.enqueueOpen(ctx, call); err != nil {
			// enqueueOpen returning an error proves that Open was not accepted by
			// the bounded queue, so trying another ready tunnel cannot replay it.
			request.complete(Response{}, err)
			activeSession.rollbackRequestBeforeOpen(request)
			selectionErr = errors.Join(selectionErr, err)
			if ctx.Err() != nil {
				return nil, selectionErr
			}
			continue
		}

		r.commitSessionSelection(selection.route, activeSession)
		r.settings.instrumentation.selection(
			ctx,
			activeSession.dataCenter,
			selection.route,
			"selected",
		)
		return request, nil
	}

	if selectionErr != nil {
		status := "unavailable"
		if errors.Is(selectionErr, ErrQueueFull) {
			status = "saturated"
		}
		r.settings.instrumentation.selection(ctx, "none", selection.route, status)
		return nil, selectionErr
	}
	r.settings.instrumentation.refusal(ctx, "no_ready_tunnel")
	r.settings.instrumentation.selection(ctx, "none", selection.route, "no_ready")
	return nil, ErrNoTunnel
}

func (r *Registry) selectionOrder(route contractv1.RouteId) ([]*session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.isDraining {
		return []*session{}, true
	}

	byDataCenter := r.eligibleSessionsByDataCenterLocked(route)
	r.resetUnavailableDataCentersLocked(route, byDataCenter)
	if len(byDataCenter) == 0 {
		return []*session{}, false
	}

	dataCenters := sortedDataCenters(byDataCenter)
	selectedDataCenter := r.selectDataCenterLocked(route, dataCenters, r.settings.now())
	r.orderDataCentersLocked(route, dataCenters, selectedDataCenter)

	return r.orderSessionsLocked(route, byDataCenter, dataCenters), false
}

func (r *Registry) eligibleSessionsByDataCenterLocked(
	route contractv1.RouteId,
) map[string][]*session {
	byDataCenter := map[string][]*session{}
	for _, activeSession := range r.sessions {
		if activeSession.accepts(route) {
			byDataCenter[activeSession.dataCenter] = append(
				byDataCenter[activeSession.dataCenter],
				activeSession,
			)
		}
	}

	return byDataCenter
}

func (r *Registry) resetUnavailableDataCentersLocked(
	route contractv1.RouteId,
	byDataCenter map[string][]*session,
) {
	for key, state := range r.selection {
		if key.route != route {
			continue
		}
		if _, ready := byDataCenter[key.dataCenter]; ready {
			continue
		}
		state.isReady = false
		state.currentWeight = 0
	}
}

func sortedDataCenters(byDataCenter map[string][]*session) []string {
	dataCenters := make([]string, 0, len(byDataCenter))
	for dataCenter, sessions := range byDataCenter {
		slices.SortFunc(sessions, func(left *session, right *session) int {
			return bytes.Compare(left.id[:], right.id[:])
		})
		byDataCenter[dataCenter] = sessions
		dataCenters = append(dataCenters, dataCenter)
	}
	slices.Sort(dataCenters)

	return dataCenters
}

func (r *Registry) selectDataCenterLocked(
	route contractv1.RouteId,
	dataCenters []string,
	now time.Time,
) string {
	totalWeight := int64(0)
	selectedDataCenter := dataCenters[0]
	selectedWeight := int64(0)
	for index, dataCenter := range dataCenters {
		key := selectionKey{route: route, dataCenter: dataCenter}
		state := r.selection[key]
		if state == nil {
			state = &selectionState{}
			r.selection[key] = state
		}
		if !state.isReady {
			state.isReady = true
			state.readySince = now
			state.currentWeight = 0
		}
		weight := failbackWeight(now.Sub(state.readySince), r.settings.failbackWarmup)
		state.currentWeight += weight
		totalWeight += weight
		if index == 0 || state.currentWeight > selectedWeight {
			selectedDataCenter = dataCenter
			selectedWeight = state.currentWeight
		}
	}
	selectedState := r.selection[selectionKey{route: route, dataCenter: selectedDataCenter}]
	selectedState.currentWeight -= totalWeight
	if len(dataCenters) == 1 {
		selectedState.currentWeight = 0
	}

	return selectedDataCenter
}

func (r *Registry) orderDataCentersLocked(
	route contractv1.RouteId,
	dataCenters []string,
	selectedDataCenter string,
) {
	slices.SortStableFunc(dataCenters, func(left string, right string) int {
		if left == right {
			return 0
		}
		if left == selectedDataCenter {
			return -1
		}
		if right == selectedDataCenter {
			return 1
		}
		leftWeight := r.selection[selectionKey{route: route, dataCenter: left}].currentWeight
		rightWeight := r.selection[selectionKey{route: route, dataCenter: right}].currentWeight
		switch {
		case leftWeight > rightWeight:
			return -1
		case leftWeight < rightWeight:
			return 1
		case left < right:
			return -1
		case left > right:
			return 1
		default:
			return 0
		}
	})
}

func (r *Registry) orderSessionsLocked(
	route contractv1.RouteId,
	byDataCenter map[string][]*session,
	dataCenters []string,
) []*session {
	ordered := make([]*session, 0, len(r.sessions))
	for _, dataCenter := range dataCenters {
		key := selectionKey{route: route, dataCenter: dataCenter}
		state := r.selection[key]
		sessions := byDataCenter[dataCenter]
		start := state.nextSession % len(sessions)
		for offset := range len(sessions) {
			ordered = append(ordered, sessions[(start+offset)%len(sessions)])
		}
	}

	return ordered
}

func (r *Registry) commitSessionSelection(
	route contractv1.RouteId,
	selectedSession *session,
) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.selection[selectionKey{route: route, dataCenter: selectedSession.dataCenter}]
	if state == nil {
		return
	}
	sessions := make([]*session, 0, len(r.sessions))
	for _, activeSession := range r.sessions {
		if activeSession.dataCenter == selectedSession.dataCenter &&
			activeSession.isReadyForRoute(route) {
			sessions = append(sessions, activeSession)
		}
	}
	slices.SortFunc(sessions, func(left *session, right *session) int {
		return bytes.Compare(left.id[:], right.id[:])
	})
	for index, activeSession := range sessions {
		if activeSession == selectedSession {
			state.nextSession = index + 1
			return
		}
	}
}

func failbackWeight(elapsed time.Duration, warmup time.Duration) int64 {
	const (
		minimumWeight int64 = 100
		fullWeight    int64 = 1000
	)
	if warmup <= 0 || elapsed >= warmup {
		return fullWeight
	}
	if elapsed <= 0 {
		return minimumWeight
	}

	return minimumWeight + int64(elapsed)*(fullWeight-minimumWeight)/int64(warmup)
}

func (r *Registry) reserveInstance(instanceID [16]byte) reservationResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.isDraining {
		return reservationDraining
	}
	if r.instanceActive[instanceID] >= r.settings.maxInFlightPerInstance {
		return reservationSaturated
	}
	r.instanceActive[instanceID]++

	return reservationAccepted
}

func (r *Registry) releaseInstance(instanceID [16]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.instanceActive[instanceID] > 0 {
		r.instanceActive[instanceID]--
	}
	if r.instanceActive[instanceID] == 0 && r.instanceTunnels[instanceID] == 0 {
		delete(r.instanceActive, instanceID)
	}
}

func contextWithRouteDeadline(
	ctx context.Context,
	maximum time.Duration,
) (context.Context, context.CancelFunc, time.Time, error) {
	now := time.Now()
	deadline := now.Add(maximum)
	if callerDeadline, found := ctx.Deadline(); found && callerDeadline.Before(deadline) {
		deadline = callerDeadline
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, time.Time{}, err
	}
	if !deadline.After(now) {
		return nil, nil, time.Time{}, context.DeadlineExceeded
	}

	callCtx, cancel := context.WithDeadline(ctx, deadline)
	return callCtx, cancel, deadline, nil
}

func validateCall(call Call, policy RoutePolicy, deadline time.Time) error {
	frame := &contractv1.ConnectResponse{
		Header: &contractv1.FrameHeader{
			ProtocolVersion: protocolVersion,
			TunnelId:        make([]byte, protocolv1.TunnelIDBytes),
			RequestId:       make([]byte, protocolv1.RequestIDBytes),
			Sequence:        1,
			TrafficClass:    policy.TrafficClass,
		},
		Payload: &contractv1.ConnectResponse_Open{
			Open: &contractv1.Open{
				RouteId:        call.Route,
				Deadline:       timestamppb.New(deadline),
				IdempotencyKey: slices.Clone(call.IdempotencyKey),
				Metadata:       cloneMetadata(call.Metadata),
			},
		},
	}
	if err := protocolv1.ValidateGatewayInFrame(frame); err != nil {
		return fmt.Errorf("tunnel: invalid logical call: %w", err)
	}

	return nil
}

func metadataWithTrace(ctx context.Context, values []*contractv1.Metadata) []*contractv1.Metadata {
	result := make([]*contractv1.Metadata, 0, len(values)+3)
	for _, value := range values {
		if value == nil {
			result = append(result, nil)
			continue
		}
		switch value.GetKey() {
		case contractv1.MetadataKey_METADATA_KEY_CONTENT_TYPE,
			contractv1.MetadataKey_METADATA_KEY_TRACEPARENT,
			contractv1.MetadataKey_METADATA_KEY_TRACESTATE:
			continue
		default:
			result = append(result, &contractv1.Metadata{
				Key:   value.GetKey(),
				Value: slices.Clone(value.GetValue()),
			})
		}
	}
	result = append(result, &contractv1.Metadata{
		Key:   contractv1.MetadataKey_METADATA_KEY_CONTENT_TYPE,
		Value: []byte("application/protobuf"),
	})

	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return result
	}
	traceparent := fmt.Sprintf(
		"00-%s-%s-%02x",
		spanContext.TraceID(),
		spanContext.SpanID(),
		byte(spanContext.TraceFlags()),
	)
	result = append(result, &contractv1.Metadata{
		Key:   contractv1.MetadataKey_METADATA_KEY_TRACEPARENT,
		Value: []byte(traceparent),
	})
	if state := spanContext.TraceState().String(); state != "" {
		result = append(result, &contractv1.Metadata{
			Key:   contractv1.MetadataKey_METADATA_KEY_TRACESTATE,
			Value: []byte(state),
		})
	}

	return result
}

func errorResultLabel(err error) string {
	var resultErr *ResultError
	switch {
	case errors.As(err, &resultErr):
		return resultLabel(resultErr.Code())
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, ErrQueueFull):
		return "resource_exhausted"
	case errors.Is(err, ErrNoTunnel), errors.Is(err, ErrTunnelClosed), errors.Is(err, ErrDraining):
		return "unavailable"
	default:
		return "internal"
	}
}
