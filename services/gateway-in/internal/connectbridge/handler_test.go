package connectbridge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	authv1connect "github.com/v0hmly/marketmesh/api/gen/go/auth/v1/authv1connect"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
	"google.golang.org/protobuf/proto"
)

func TestUnaryHandler_UsesFixedRouteAndTypedProtobuf(t *testing.T) {
	t.Parallel()

	wantResponse := &authv1.LoginResponse{SubjectId: []byte("opaque-subject")}
	responsePayload, err := proto.Marshal(wantResponse)
	if err != nil {
		t.Fatalf("proto.Marshal() error = %v", err)
	}
	invoker := &fakeInvoker{response: tunnel.Response{Payload: responsePayload}}
	handler, err := NewUnaryHandler[authv1.LoginRequest, authv1.LoginResponse](Config{
		Procedure: authv1connect.AuthServiceLoginProcedure,
		Route:     contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
		Invoker:   invoker,
	})
	if err != nil {
		t.Fatalf("NewUnaryHandler() error = %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle(authv1connect.AuthServiceLoginProcedure, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := connect.NewClient[authv1.LoginRequest, authv1.LoginResponse](
		http.DefaultClient,
		server.URL+authv1connect.AuthServiceLoginProcedure,
	)
	request := connect.NewRequest(&authv1.LoginRequest{
		Identifier: "alice@example.test",
		Password:   []byte("secret-password"),
	})
	request.Header().Set("Authorization", "Bearer must-not-cross")
	response, err := client.CallUnary(context.Background(), request)
	if err != nil {
		t.Fatalf("CallUnary() error = %v", err)
	}
	if !proto.Equal(response.Msg, wantResponse) {
		t.Fatalf("response = %v, want %v", response.Msg, wantResponse)
	}

	call := invoker.lastCall()
	if call.Route != contractv1.RouteId_ROUTE_ID_AUTH_LOGIN {
		t.Fatalf("route = %s, want AUTH_LOGIN", call.Route)
	}
	if len(call.Metadata) != 0 {
		t.Fatalf("metadata = %v, want no forwarded HTTP headers", call.Metadata)
	}
	decoded := new(authv1.LoginRequest)
	if err := proto.Unmarshal(call.Payload, decoded); err != nil {
		t.Fatalf("proto.Unmarshal() error = %v", err)
	}
	if !proto.Equal(decoded, request.Msg) {
		t.Fatalf("tunnel request = %v, want %v", decoded, request.Msg)
	}
}

func TestUnaryHandler_ReturnsFinitePublicError(t *testing.T) {
	t.Parallel()

	invoker := &fakeInvoker{err: errors.Join(
		tunnel.ErrNoTunnel,
		errors.New("private backend auth.internal:7443"),
	)}
	handler, err := NewUnaryHandler[authv1.LoginRequest, authv1.LoginResponse](Config{
		Procedure: authv1connect.AuthServiceLoginProcedure,
		Route:     contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
		Invoker:   invoker,
	})
	if err != nil {
		t.Fatalf("NewUnaryHandler() error = %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle(authv1connect.AuthServiceLoginProcedure, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := connect.NewClient[authv1.LoginRequest, authv1.LoginResponse](
		http.DefaultClient,
		server.URL+authv1connect.AuthServiceLoginProcedure,
	)
	_, err = client.CallUnary(
		context.Background(),
		connect.NewRequest(&authv1.LoginRequest{}),
	)
	if err == nil {
		t.Fatal("CallUnary() error = nil, want unavailable")
	}
	connectErr := new(connect.Error)
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnavailable {
		t.Fatalf("CallUnary() error = %v, want unavailable", err)
	}
	if strings.Contains(err.Error(), "auth.internal") {
		t.Fatalf("public error leaked internal destination: %v", err)
	}
}

func TestUnaryHandler_RequiresFiniteIdempotencyKey(t *testing.T) {
	t.Parallel()

	responsePayload, err := proto.Marshal(&authv1.LoginResponse{})
	if err != nil {
		t.Fatalf("proto.Marshal() error = %v", err)
	}
	tests := []struct {
		name       string
		values     []string
		wantKey    string
		wantInvoke bool
	}{
		{name: "missing"},
		{
			name:       "valid printable key",
			values:     []string{"mm29-mutation-1"},
			wantKey:    "mm29-mutation-1",
			wantInvoke: true,
		},
		{
			name:   "duplicate header",
			values: []string{"mm29-mutation-1", "mm29-mutation-2"},
		},
		{
			name:   "control character",
			values: []string{"mm29\tmutation"},
		},
		{
			name:   "oversized",
			values: []string{strings.Repeat("k", 129)},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			invoker := &fakeInvoker{response: tunnel.Response{Payload: responsePayload}}
			handler, handlerErr := NewUnaryHandler[authv1.LoginRequest, authv1.LoginResponse](Config{
				Procedure:             authv1connect.AuthServiceLoginProcedure,
				Route:                 contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
				RequireIdempotencyKey: true,
				Invoker:               invoker,
			})
			if handlerErr != nil {
				t.Fatalf("NewUnaryHandler() error = %v", handlerErr)
			}

			server := httptest.NewServer(handler)
			t.Cleanup(server.Close)
			client := connect.NewClient[authv1.LoginRequest, authv1.LoginResponse](
				server.Client(),
				server.URL+authv1connect.AuthServiceLoginProcedure,
			)
			request := connect.NewRequest(&authv1.LoginRequest{})
			for _, value := range test.values {
				request.Header().Add(idempotencyKeyHeader, value)
			}
			_, callErr := client.CallUnary(t.Context(), request)

			if test.wantInvoke {
				if callErr != nil {
					t.Fatalf("CallUnary() error = %v", callErr)
				}
				if key := string(invoker.lastCall().IdempotencyKey); key != test.wantKey {
					t.Fatalf("idempotency key = %q, want %q", key, test.wantKey)
				}
				return
			}

			if code := connect.CodeOf(callErr); code != connect.CodeInvalidArgument {
				t.Fatalf("CallUnary() code = %s, want invalid_argument (error %v)", code, callErr)
			}
		})
	}
}

func TestNewUnaryHandler_RejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()

	var typedNil *fakeInvoker
	tests := []struct {
		name   string
		config Config
	}{
		{name: "nil invoker", config: Config{Invoker: nil}},
		{name: "typed nil invoker", config: Config{Invoker: typedNil}},
		{
			name: "caller-shaped procedure",
			config: Config{
				Procedure: "/auth.v1.AuthService/Login?target=internal",
				Route:     contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
				Invoker:   &fakeInvoker{},
			},
		},
		{
			name: "route absent from local policy",
			config: Config{
				Procedure: authv1connect.AuthServiceLoginProcedure,
				Route:     contractv1.RouteId_ROUTE_ID_USER_GET_ME,
				Invoker:   &fakeInvoker{},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewUnaryHandler[authv1.LoginRequest, authv1.LoginResponse](
				test.config,
			); err == nil {
				t.Fatal("NewUnaryHandler() error = nil")
			}
		})
	}
}

type fakeInvoker struct {
	mu       sync.Mutex
	call     tunnel.Call
	response tunnel.Response
	err      error
}

func (f *fakeInvoker) Invoke(_ context.Context, call tunnel.Call) (tunnel.Response, error) {
	f.mu.Lock()
	f.call = call
	f.mu.Unlock()

	return f.response, f.err
}

func (f *fakeInvoker) RoutePolicy(route contractv1.RouteId) (tunnel.RoutePolicy, bool) {
	if route != contractv1.RouteId_ROUTE_ID_AUTH_LOGIN {
		return tunnel.RoutePolicy{}, false
	}

	return tunnel.RoutePolicy{
		TrafficClass:     contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
		MaxRequestBytes:  4096,
		MaxResponseBytes: 4096,
		MaxDeadline:      time.Second,
		MaxInFlight:      4,
	}, true
}

func (f *fakeInvoker) lastCall() tunnel.Call {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.call
}
