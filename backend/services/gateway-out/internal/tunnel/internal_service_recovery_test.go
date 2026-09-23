package tunnel

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

const (
	internalReconnectTimeout = 3 * time.Second
	internalRequestTimeout   = 250 * time.Millisecond
)

func TestInternalServiceConnectionRecoversWithoutGatewayOutRestart(t *testing.T) {
	t.Parallel()

	backend := newReplaceableHealthBackend(t)
	initial := newFakeHealth()
	backend.start(t, initial)
	registry := newRecoveryRegistry(t, backend.clients(t))
	readRoute := recoveryRoute(t, registry, contractv1.RouteId_ROUTE_ID_USER_GET_ME)
	mutatingRoute := recoveryRoute(t, registry, contractv1.RouteId_ROUTE_ID_AUTH_LOGIN)

	if code := invokeHealthRoute(t, readRoute, "initial"); code != codes.OK {
		t.Fatalf("initial read code = %s, want OK", code)
	}
	if calls := initial.callCount.Load(); calls != 1 {
		t.Fatalf("initial backend calls = %d, want 1", calls)
	}

	backend.stop(t)
	if code := invokeHealthRoute(t, readRoute, "unavailable"); code != codes.Unavailable {
		t.Fatalf("read during outage code = %s, want Unavailable", code)
	}

	replacement := newFakeHealth()
	backend.start(t, replacement)
	waitForRouteRecovery(t, readRoute)

	if code := invokeHealthRoute(t, mutatingRoute, "mutation-after-recovery"); code != codes.OK {
		t.Fatalf("mutating request after recovery code = %s, want OK", code)
	}
	if calls := replacement.callCount.Load(); calls != 2 {
		t.Fatalf("replacement backend calls = %d, want one read and one mutation", calls)
	}
}

type replaceableHealthBackend struct {
	mu          sync.RWMutex
	listener    *bufconn.Listener
	server      *grpcgo.Server
	serveResult chan error
	connections []*grpcgo.ClientConn
}

func newReplaceableHealthBackend(t *testing.T) *replaceableHealthBackend {
	t.Helper()

	backend := &replaceableHealthBackend{
		connections: make([]*grpcgo.ClientConn, 0, 3),
	}
	t.Cleanup(func() {
		for _, connection := range backend.connections {
			if err := connection.Close(); err != nil {
				t.Errorf("close internal client: %v", err)
			}
		}
		backend.stop(t)
	})

	return backend
}

func (backend *replaceableHealthBackend) clients(t *testing.T) ClassClients {
	t.Helper()

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.connections) != 0 {
		t.Fatal("internal clients already created")
	}

	for range 3 {
		connection, err := grpcgo.NewClient(
			"passthrough:///internal.test",
			grpcgo.WithTransportCredentials(insecure.NewCredentials()),
			grpcgo.WithDisableRetry(),
			grpcgo.WithConnectParams(grpcgo.ConnectParams{
				Backoff: backoff.Config{
					BaseDelay:  5 * time.Millisecond,
					Multiplier: 1.2,
					Jitter:     0,
					MaxDelay:   25 * time.Millisecond,
				},
				MinConnectTimeout: 50 * time.Millisecond,
			}),
			grpcgo.WithContextDialer(backend.dialContext),
		)
		if err != nil {
			t.Fatalf("create internal client: %v", err)
		}
		backend.connections = append(backend.connections, connection)
	}

	return ClassClients{
		ControlAuth: backend.connections[0],
		Regular:     backend.connections[1],
		Realtime:    backend.connections[2],
	}
}

func (backend *replaceableHealthBackend) start(t *testing.T, implementation *fakeHealth) {
	t.Helper()

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.listener != nil || backend.server != nil || backend.serveResult != nil {
		t.Fatal("internal backend already running")
	}

	listener := bufconn.Listen(1 << 20)
	server := grpcgo.NewServer()
	grpc_health_v1.RegisterHealthServer(server, implementation)
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()

	backend.listener = listener
	backend.server = server
	backend.serveResult = serveResult
}

func (backend *replaceableHealthBackend) stop(t *testing.T) {
	t.Helper()

	backend.mu.Lock()
	listener := backend.listener
	server := backend.server
	serveResult := backend.serveResult
	backend.listener = nil
	backend.server = nil
	backend.serveResult = nil
	backend.mu.Unlock()

	if listener == nil && server == nil && serveResult == nil {
		return
	}
	if listener == nil || server == nil || serveResult == nil {
		t.Fatal("internal backend lifecycle is inconsistent")
	}

	server.Stop()
	if err := listener.Close(); err != nil {
		t.Errorf("close internal listener: %v", err)
	}

	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case err := <-serveResult:
		if err != nil && !errors.Is(err, grpcgo.ErrServerStopped) {
			t.Errorf("internal Serve() error = %v", err)
		}
	case <-timer.C:
		t.Fatal("internal server did not stop")
	}
}

func (backend *replaceableHealthBackend) dialContext(
	ctx context.Context,
	_ string,
) (net.Conn, error) {
	backend.mu.RLock()
	listener := backend.listener
	backend.mu.RUnlock()
	if listener == nil {
		return nil, errors.New("internal backend unavailable")
	}

	return listener.DialContext(ctx)
}

func newRecoveryRegistry(t *testing.T, clients ClassClients) *Registry {
	t.Helper()

	registry, err := NewRegistry(
		clients,
		recoveryRouteSpec(contractv1.RouteId_ROUTE_ID_USER_GET_ME, false),
		recoveryRouteSpec(contractv1.RouteId_ROUTE_ID_AUTH_LOGIN, true),
	)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	return registry
}

func recoveryRouteSpec(routeID contractv1.RouteId, isMutating bool) RouteSpec {
	return RouteSpec{
		ID:                    routeID,
		TrafficClass:          routeTrafficClass(routeID),
		Method:                grpc_health_v1.Health_Check_FullMethodName,
		NewRequest:            func() proto.Message { return &grpc_health_v1.HealthCheckRequest{} },
		NewResponse:           func() proto.Message { return &grpc_health_v1.HealthCheckResponse{} },
		MaxRequestBytes:       4 << 10,
		MaxResponseBytes:      4 << 10,
		MaxDeadline:           time.Second,
		Mutating:              isMutating,
		RequireIdempotencyKey: isMutating,
	}
}

func recoveryRoute(t *testing.T, registry *Registry, routeID contractv1.RouteId) route {
	t.Helper()

	selected, found := registry.lookup(routeID)
	if !found {
		t.Fatalf("route %s not found", routeID)
	}

	return selected
}

func invokeHealthRoute(t *testing.T, selected route, service string) codes.Code {
	t.Helper()

	payload, err := proto.Marshal(&grpc_health_v1.HealthCheckRequest{Service: service})
	if err != nil {
		t.Fatalf("marshal health request: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), internalRequestTimeout)
	defer cancel()

	_, code := selected.invoke(ctx, payload)
	return code
}

func waitForRouteRecovery(t *testing.T, selected route) {
	t.Helper()

	deadline := time.NewTimer(internalReconnectTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for attempt := 1; ; attempt++ {
		code := invokeHealthRoute(t, selected, "recovery-probe")
		if code == codes.OK {
			return
		}
		if code != codes.Unavailable && code != codes.DeadlineExceeded {
			t.Fatalf("recovery probe %d code = %s", attempt, code)
		}

		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("internal route did not recover within %s", internalReconnectTimeout)
		}
	}
}
