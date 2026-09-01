package tunnel

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
)

func TestRegistry_SelectionBalancesDataCentersAndTunnels(t *testing.T) {
	t.Parallel()

	server, err := New(testConfig())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	registry := server.Registry()
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	registry.settings.now = func() time.Time { return now }
	registry.settings.failbackWarmup = time.Minute
	registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 1, instanceID: 1, dataCenter: "dc-a", lastActivity: now,
	})
	registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 2, instanceID: 2, dataCenter: "dc-a", lastActivity: now,
	})
	registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 3, instanceID: 3, dataCenter: "dc-b", lastActivity: now,
	})

	wantDataCenters := []string{"dc-a", "dc-b", "dc-a", "dc-b"}
	wantTunnelIDs := []byte{1, 3, 2, 3}
	for index := range wantDataCenters {
		ordered, isDraining := registry.selectionOrder(
			contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
		)
		if isDraining || len(ordered) != 3 {
			t.Fatalf("selectionOrder() = (%d, %t), want (3, false)", len(ordered), isDraining)
		}
		if ordered[0].dataCenter != wantDataCenters[index] {
			t.Fatalf(
				"selection %d data center = %q, want %q",
				index,
				ordered[0].dataCenter,
				wantDataCenters[index],
			)
		}
		if ordered[0].id[0] != wantTunnelIDs[index] {
			t.Fatalf(
				"selection %d tunnel = %d, want %d",
				index,
				ordered[0].id[0],
				wantTunnelIDs[index],
			)
		}
		registry.commitSessionSelection(
			contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
			ordered[0],
		)
	}
}

func TestRegistry_SelectionWarmsRecoveredDataCenter(t *testing.T) {
	t.Parallel()

	server, err := New(testConfig())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	registry := server.Registry()
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	registry.settings.now = func() time.Time { return now }
	registry.settings.failbackWarmup = time.Minute
	dcA := registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 1, instanceID: 1, dataCenter: "dc-a", lastActivity: now,
	})

	assertSelectedDataCenter(t, registry, "dc-a")
	now = now.Add(registry.settings.failbackWarmup)
	assertSelectedDataCenter(t, registry, "dc-a")
	registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 2, instanceID: 2, dataCenter: "dc-b", lastActivity: now,
	})

	for range 5 {
		assertSelectedDataCenter(t, registry, "dc-a")
	}
	assertSelectedDataCenter(t, registry, "dc-b")

	registry.unregister(dcA)
	assertSelectedDataCenter(t, registry, "dc-b")

	now = now.Add(registry.settings.failbackWarmup)
	assertSelectedDataCenter(t, registry, "dc-b")
	registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 4, instanceID: 4, dataCenter: "dc-a", lastActivity: now,
	})
	for range 5 {
		assertSelectedDataCenter(t, registry, "dc-b")
	}
	assertSelectedDataCenter(t, registry, "dc-a")
}

func TestRegistry_SelectionUsesReadyAgeBeforeFirstCall(t *testing.T) {
	t.Parallel()

	server, err := New(testConfig())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	registry := server.Registry()
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	registry.settings.now = func() time.Time { return now }
	registry.settings.failbackWarmup = time.Minute
	registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 1, instanceID: 1, dataCenter: "dc-a", lastActivity: now,
	})

	now = now.Add(registry.settings.failbackWarmup)
	registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 2, instanceID: 2, dataCenter: "dc-b", lastActivity: now,
	})

	for range 5 {
		assertSelectedDataCenter(t, registry, "dc-a")
	}
	assertSelectedDataCenter(t, registry, "dc-b")
}

func TestSession_AcceptsRejectsStaleTunnel(t *testing.T) {
	t.Parallel()

	server, err := New(testConfig())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	registry := server.Registry()
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	registry.settings.now = func() time.Time { return now }
	activeSession := registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 1, instanceID: 1, dataCenter: "dc-a", lastActivity: now,
	})
	route := contractv1.RouteId_ROUTE_ID_AUTH_LOGIN
	if !activeSession.accepts(route) {
		t.Fatal("accepts() = false before stale timeout")
	}

	now = now.Add(registry.settings.staleAfter)
	if activeSession.accepts(route) {
		t.Fatal("accepts() = true at stale timeout")
	}
	select {
	case failure := <-activeSession.terminal:
		if failure.reason != "stale" {
			t.Fatalf("stale failure reason = %q, want stale", failure.reason)
		}
	default:
		t.Fatal("stale tunnel was not evicted")
	}
}

func TestRegistry_SelectionSkipsDrainingTunnel(t *testing.T) {
	t.Parallel()

	server, err := New(testConfig())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	registry := server.Registry()
	now := time.Now()
	registry.settings.now = func() time.Time { return now }
	draining := registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 1, instanceID: 1, dataCenter: "dc-a", lastActivity: now,
	})
	registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 2, instanceID: 2, dataCenter: "dc-b", lastActivity: now,
	})
	draining.mu.Lock()
	draining.isDraining = true
	draining.mu.Unlock()

	assertSelectedDataCenter(t, registry, "dc-b")
}

func TestRegistry_SelectionReportsSaturation(t *testing.T) {
	t.Parallel()

	server, err := New(testConfig())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	registry := server.Registry()
	now := time.Now()
	registry.settings.now = func() time.Time { return now }
	activeSession := registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 1, instanceID: 1, dataCenter: "dc-a", lastActivity: now,
	})
	registry.instanceActive[activeSession.instanceID] = registry.settings.maxInFlightPerInstance
	policy := registry.settings.routes[contractv1.RouteId_ROUTE_ID_AUTH_LOGIN]

	_, err = registry.selectAndOpenRequest(context.Background(), Call{
		Route: contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
	}, requestSelection{
		route:        contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
		policy:       policy,
		deadline:     now.Add(time.Second),
		requestBytes: 1,
	})
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("selectAndOpenRequest() error = %v, want ErrQueueFull", err)
	}
}

func TestRegistry_SelectionReportsDrainStartedAfterSnapshot(t *testing.T) {
	t.Parallel()

	server, err := New(testConfig())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	registry := server.Registry()
	now := time.Now()
	registry.settings.now = func() time.Time { return now }
	activeSession := registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 1, instanceID: 1, dataCenter: "dc-a", lastActivity: now,
	})
	ordered, isDraining := registry.selectionOrder(contractv1.RouteId_ROUTE_ID_AUTH_LOGIN)
	if isDraining || len(ordered) != 1 {
		t.Fatalf("selectionOrder() = (%d, %t), want (1, false)", len(ordered), isDraining)
	}

	registry.mu.Lock()
	registry.isDraining = true
	registry.mu.Unlock()
	if result := registry.reserveInstance(activeSession.instanceID); result != reservationDraining {
		t.Fatalf("reserveInstance() = %d, want reservationDraining", result)
	}
}

func TestRegistry_SelectionAdvancesTunnelInFallbackDataCenter(t *testing.T) {
	t.Parallel()

	server, err := New(testConfig())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	registry := server.Registry()
	now := time.Now()
	registry.settings.now = func() time.Time { return now }
	saturated := registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 1, instanceID: 1, dataCenter: "dc-a", lastActivity: now,
	})
	registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 2, instanceID: 2, dataCenter: "dc-b", lastActivity: now,
	})
	registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 3, instanceID: 3, dataCenter: "dc-b", lastActivity: now,
	})
	registry.instanceActive[saturated.instanceID] = registry.settings.maxInFlightPerInstance
	call := Call{Route: contractv1.RouteId_ROUTE_ID_AUTH_LOGIN}
	selection := requestSelection{
		route:        call.Route,
		policy:       registry.settings.routes[call.Route],
		deadline:     now.Add(time.Second),
		requestBytes: 0,
	}

	first, err := registry.selectAndOpenRequest(context.Background(), call, selection)
	if err != nil {
		t.Fatalf("first selectAndOpenRequest() error = %v", err)
	}
	if first.session.id[0] != 2 {
		t.Fatalf("first selected tunnel = %d, want 2", first.session.id[0])
	}
	first.session.finishRequest(first)

	second, err := registry.selectAndOpenRequest(context.Background(), call, selection)
	if err != nil {
		t.Fatalf("second selectAndOpenRequest() error = %v", err)
	}
	if second.session.id[0] != 3 {
		t.Fatalf("second selected tunnel = %d, want 3", second.session.id[0])
	}
	second.session.finishRequest(second)
}

func TestRegistry_ConcurrentSelectionBalancesTunnels(t *testing.T) {
	t.Parallel()

	config := testConfig()
	config.Limits.MaxInFlightRequests = 32
	config.MaxInFlightPerInstance = 32
	config.Queues.ControlAuth = 64
	policy := config.Routes[contractv1.RouteId_ROUTE_ID_AUTH_LOGIN]
	policy.MaxInFlight = 32
	config.Routes[contractv1.RouteId_ROUTE_ID_AUTH_LOGIN] = policy
	server, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	registry := server.Registry()
	now := time.Now()
	registry.settings.now = func() time.Time { return now }
	registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 1, instanceID: 1, dataCenter: "dc-a", lastActivity: now,
	})
	registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 2, instanceID: 2, dataCenter: "dc-a", lastActivity: now,
	})
	call := Call{Route: contractv1.RouteId_ROUTE_ID_AUTH_LOGIN}
	selection := requestSelection{
		route:        call.Route,
		policy:       registry.settings.routes[call.Route],
		deadline:     now.Add(time.Second),
		requestBytes: 0,
	}

	const calls = 32
	start := make(chan struct{})
	results := make(chan *logicalRequest, calls)
	errorsChannel := make(chan error, calls)
	var ready sync.WaitGroup
	ready.Add(calls)
	for range calls {
		go func() {
			ready.Done()
			<-start
			request, selectionErr := registry.selectAndOpenRequest(
				context.Background(),
				call,
				selection,
			)
			if selectionErr != nil {
				errorsChannel <- selectionErr
				return
			}
			results <- request
		}()
	}
	ready.Wait()
	close(start)

	counts := map[byte]int{}
	requests := make([]*logicalRequest, 0, calls)
	for range calls {
		select {
		case selectionErr := <-errorsChannel:
			t.Fatalf("selectAndOpenRequest() error = %v", selectionErr)
		case request := <-results:
			counts[request.session.id[0]]++
			requests = append(requests, request)
		}
	}
	if counts[1] != calls/2 || counts[2] != calls/2 {
		t.Fatalf("concurrent selections = %v, want 16 per tunnel", counts)
	}
	for _, request := range requests {
		request.session.finishRequest(request)
	}
}

func TestRegistry_SelectionFallsBackWhenOpenWasNotQueued(t *testing.T) {
	t.Parallel()

	server, err := New(testConfig())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	registry := server.Registry()
	now := time.Now()
	registry.settings.now = func() time.Time { return now }
	full := registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 1, instanceID: 1, dataCenter: "dc-a", lastActivity: now,
	})
	registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 2, instanceID: 2, dataCenter: "dc-a", lastActivity: now,
	})
	lane, valid := queueLane(contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH)
	if !valid {
		t.Fatal("CONTROL_AUTH does not have a scheduler lane")
	}
	for range cap(full.outbound.lanes[lane]) {
		full.outbound.lanes[lane] <- &contractv1.ConnectResponse{}
	}
	call := Call{Route: contractv1.RouteId_ROUTE_ID_AUTH_LOGIN}

	request, err := registry.selectAndOpenRequest(context.Background(), call, requestSelection{
		route:        call.Route,
		policy:       registry.settings.routes[call.Route],
		deadline:     now.Add(time.Second),
		requestBytes: 0,
	})
	if err != nil {
		t.Fatalf("selectAndOpenRequest() error = %v", err)
	}
	if request.session.id[0] != 2 {
		t.Fatalf("selected tunnel = %d, want 2", request.session.id[0])
	}
	full.mu.Lock()
	fullRequests := len(full.requests)
	fullTombstones := len(full.tombstones)
	full.mu.Unlock()
	if fullRequests != 0 || fullTombstones != 0 ||
		registry.instanceActive[full.instanceID] != 0 {
		t.Fatalf(
			"failed tunnel retained requests=%d tombstones=%d instance_active=%d",
			fullRequests,
			fullTombstones,
			registry.instanceActive[full.instanceID],
		)
	}
	request.session.finishRequest(request)
}

func TestRegistry_InvokeDoesNotFallbackAfterOpenWasQueued(t *testing.T) {
	t.Parallel()

	server, err := New(testConfig())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	registry := server.Registry()
	now := time.Now()
	registry.settings.now = func() time.Time { return now }
	selected := registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 1, instanceID: 1, dataCenter: "dc-a", lastActivity: now,
	})
	fallback := registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 2, instanceID: 2, dataCenter: "dc-a", lastActivity: now,
	})
	lane, valid := queueLane(contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH)
	if !valid {
		t.Fatal("CONTROL_AUTH does not have a scheduler lane")
	}
	selected.outbound.lanes[lane] <- &contractv1.ConnectResponse{}

	_, err = registry.Invoke(context.Background(), Call{
		Route: contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
	})
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("Invoke() error = %v, want ErrQueueFull", err)
	}
	if queued := len(selected.outbound.lanes[lane]); queued != cap(selected.outbound.lanes[lane]) {
		t.Fatalf("selected queue depth = %d, want %d", queued, cap(selected.outbound.lanes[lane]))
	}
	if queued := len(fallback.outbound.lanes[lane]); queued != 0 {
		t.Fatalf("fallback queue depth = %d, want 0 after Open", queued)
	}
}

func TestRegistry_RoutingSnapshotIsBoundedDeterministicAndDefensive(t *testing.T) {
	t.Parallel()

	server, err := New(testConfig())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	registry := server.Registry()
	now := time.Now()
	registry.settings.now = func() time.Time { return now }
	draining := registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 3, instanceID: 3, dataCenter: "dc-b", lastActivity: now,
	})
	draining.mu.Lock()
	draining.isDraining = true
	draining.mu.Unlock()
	ready := registerSelectableSession(t, registry, testSessionParams{
		tunnelID: 2, instanceID: 2, dataCenter: "dc-a", lastActivity: now,
	})
	ready.mu.Lock()
	ready.routeActive[contractv1.RouteId_ROUTE_ID_AUTH_LOGIN] = 2
	ready.mu.Unlock()
	registerSelectableSession(t, registry, testSessionParams{
		tunnelID:     1,
		instanceID:   1,
		dataCenter:   "dc-a",
		lastActivity: now.Add(-registry.settings.staleAfter),
	})

	snapshot := registry.RoutingSnapshot(contractv1.RouteId_ROUTE_ID_AUTH_LOGIN)
	if !snapshot.RouteAllowed || snapshot.RegistryDraining || len(snapshot.Tunnels) != 3 {
		t.Fatalf("RoutingSnapshot() = %+v, want allowed non-draining registry with 3 tunnels", snapshot)
	}
	wantStates := []TunnelState{TunnelStateStale, TunnelStateReady, TunnelStateDraining}
	wantActive := []int{0, 2, 0}
	for index := range wantStates {
		if snapshot.Tunnels[index].State != wantStates[index] {
			t.Fatalf(
				"tunnel %d state = %q, want %q",
				index,
				snapshot.Tunnels[index].State,
				wantStates[index],
			)
		}
		if snapshot.Tunnels[index].ActiveRequests != wantActive[index] {
			t.Fatalf(
				"tunnel %d active requests = %d, want %d",
				index,
				snapshot.Tunnels[index].ActiveRequests,
				wantActive[index],
			)
		}
	}

	snapshot.Tunnels[0].TunnelID[0] = 99
	second := registry.RoutingSnapshot(contractv1.RouteId_ROUTE_ID_AUTH_LOGIN)
	if second.Tunnels[0].TunnelID[0] != 1 {
		t.Fatal("RoutingSnapshot() exposed mutable registry identity")
	}
}

type testSessionParams struct {
	tunnelID     byte
	instanceID   byte
	dataCenter   string
	lastActivity time.Time
}

func registerSelectableSession(
	t *testing.T,
	registry *Registry,
	params testSessionParams,
) *session {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	activeSession := &session{
		settings:    registry.settings,
		registry:    registry,
		id:          [16]byte{params.tunnelID},
		instanceID:  [16]byte{params.instanceID},
		dataCenter:  params.dataCenter,
		limits:      cloneLimits(registry.settings.limits),
		routes:      map[contractv1.RouteId]struct{}{contractv1.RouteId_ROUTE_ID_AUTH_LOGIN: {}},
		ctx:         ctx,
		cancel:      cancel,
		outbound:    newOutboundQueue(registry.settings.queues, registry.settings.instrumentation),
		done:        make(chan struct{}),
		terminal:    make(chan sessionFailure, 1),
		requests:    map[[16]byte]*logicalRequest{},
		routeActive: map[contractv1.RouteId]int{},
		tombstones:  map[[16]byte]struct{}{},
		tombstoneOrder: make(
			[][16]byte,
			0,
			registry.settings.limits.GetMaxInFlightRequests(),
		),
		isReady:      true,
		lastActivity: params.lastActivity,
	}
	registry.sessions[activeSession.id] = activeSession
	registry.instanceTunnels[activeSession.instanceID]++
	registry.markSessionReady(activeSession)

	return activeSession
}

func assertSelectedDataCenter(t *testing.T, registry *Registry, expected string) {
	t.Helper()
	ordered, isDraining := registry.selectionOrder(contractv1.RouteId_ROUTE_ID_AUTH_LOGIN)
	if isDraining || len(ordered) == 0 {
		t.Fatalf("selectionOrder() = (%d, %t), want ready tunnel", len(ordered), isDraining)
	}
	if ordered[0].dataCenter != expected {
		t.Fatalf("selected data center = %q, want %q", ordered[0].dataCenter, expected)
	}
	registry.commitSessionSelection(contractv1.RouteId_ROUTE_ID_AUTH_LOGIN, ordered[0])
}
