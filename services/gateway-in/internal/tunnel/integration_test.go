//go:build integration

package tunnel_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	authv1connect "github.com/v0hmly/marketmesh/api/gen/go/auth/v1/authv1connect"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/connectbridge"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

const (
	testPeerURI       = "spiffe://marketmesh.test/gateway-out/instance-1"
	wrongPeerURI      = "spiffe://marketmesh.test/untrusted/instance-1"
	serverDNSName     = "gateway-in.marketmesh.test"
	integrationWait   = 3 * time.Second
	integrationSecret = "secret-password-MM-11"
)

func TestReverseTunnel_ConnectUnaryRoundTripAndSafeTelemetry(t *testing.T) {
	logOutput := newLockedBuffer()
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = meterProvider.Shutdown(context.Background()) })
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	t.Cleanup(func() { _ = tracerProvider.Shutdown(context.Background()) })

	fixture := newTunnelFixture(t, fixtureOptions{
		clientURI:      testPeerURI,
		clientPurposes: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		logger: slog.New(slog.NewJSONHandler(logOutput, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})),
		meterProvider:  meterProvider,
		tracerProvider: tracerProvider,
	})
	wantResponse := &authv1.LoginResponse{SubjectId: []byte("opaque-subject-MM-11")}
	peer := newScriptedPeer(fixture.client, peerOptions{response: wantResponse})
	peer.start(t)
	waitRouteReady(t, fixture.server.Registry(), contractv1.RouteId_ROUTE_ID_AUTH_LOGIN)
	waitSignal(t, peer.pongSeen, "Pong")

	handler, err := connectbridge.NewUnaryHandler[authv1.LoginRequest, authv1.LoginResponse](
		connectbridge.Config{
			Procedure: authv1connect.AuthServiceLoginProcedure,
			Route:     contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
			Invoker:   fixture.server.Registry(),
		},
	)
	if err != nil {
		t.Fatalf("NewUnaryHandler() error = %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle(authv1connect.AuthServiceLoginProcedure, handler)
	publicServer := httptest.NewServer(mux)
	t.Cleanup(publicServer.Close)
	client := connect.NewClient[authv1.LoginRequest, authv1.LoginResponse](
		publicServer.Client(),
		publicServer.URL+authv1connect.AuthServiceLoginProcedure,
	)
	request := connect.NewRequest(&authv1.LoginRequest{
		Identifier: "alice-MM-11@example.test",
		Password:   []byte(integrationSecret),
	})
	request.Header().Set("Authorization", "Bearer external-token-MM-11")
	response, err := client.CallUnary(context.Background(), request)
	if err != nil {
		t.Fatalf("CallUnary() error = %v", err)
	}
	if !proto.Equal(response.Msg, wantResponse) {
		t.Fatalf("response = %v, want %v", response.Msg, wantResponse)
	}
	peer.assertHealthy(t)

	for _, forbidden := range []string{
		"alice-MM-11",
		integrationSecret,
		"external-token-MM-11",
		"opaque-subject-MM-11",
		testPeerURI,
	} {
		if strings.Contains(logOutput.String(), forbidden) {
			t.Fatalf("structured logs contain forbidden value %q: %s", forbidden, logOutput.String())
		}
	}
	assertSafeSpans(t, spanRecorder.Ended())
	assertSafeMetrics(t, reader)
}

func TestReverseTunnel_MapsFiniteResultWithoutLeakingPeerDetails(t *testing.T) {
	fixture := newTunnelFixture(t, fixtureOptions{
		clientURI:      testPeerURI,
		clientPurposes: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	peer := newScriptedPeer(fixture.client, peerOptions{
		resultCode: contractv1.ResultCode_RESULT_CODE_PERMISSION_DENIED,
	})
	peer.start(t)
	waitRouteReady(t, fixture.server.Registry(), contractv1.RouteId_ROUTE_ID_AUTH_LOGIN)

	handler, err := connectbridge.NewUnaryHandler[authv1.LoginRequest, authv1.LoginResponse](
		connectbridge.Config{
			Procedure: authv1connect.AuthServiceLoginProcedure,
			Route:     contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
			Invoker:   fixture.server.Registry(),
		},
	)
	if err != nil {
		t.Fatalf("NewUnaryHandler() error = %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle(authv1connect.AuthServiceLoginProcedure, handler)
	publicServer := httptest.NewServer(mux)
	t.Cleanup(publicServer.Close)
	client := connect.NewClient[authv1.LoginRequest, authv1.LoginResponse](
		publicServer.Client(),
		publicServer.URL+authv1connect.AuthServiceLoginProcedure,
	)
	_, err = client.CallUnary(context.Background(), connect.NewRequest(&authv1.LoginRequest{
		Identifier: "alice-MM-11@example.test",
		Password:   []byte(integrationSecret),
	}))
	connectErr := new(connect.Error)
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodePermissionDenied {
		t.Fatalf("CallUnary() error = %v, want PermissionDenied", err)
	}
	for _, forbidden := range []string{integrationSecret, "gateway-out", "internal:"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("public error leaked peer detail %q: %v", forbidden, err)
		}
	}
	peer.assertHealthy(t)
}

func TestReverseTunnel_RejectsPeerAndRoutePolicyViolations(t *testing.T) {
	tests := []struct {
		name           string
		clientURI      string
		clientPurposes []x509.ExtKeyUsage
		routes         []contractv1.RouteId
		wantCode       codes.Code
	}{
		{
			name:           "valid chain with wrong workload identity",
			clientURI:      wrongPeerURI,
			clientPurposes: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
			routes:         []contractv1.RouteId{contractv1.RouteId_ROUTE_ID_AUTH_LOGIN},
			wantCode:       codes.PermissionDenied,
		},
		{
			name:           "certificate with wrong purpose",
			clientURI:      testPeerURI,
			clientPurposes: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			routes:         []contractv1.RouteId{contractv1.RouteId_ROUTE_ID_AUTH_LOGIN},
			wantCode:       codes.Unavailable,
		},
		{
			name:           "route absent from static allowlist",
			clientURI:      testPeerURI,
			clientPurposes: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
			routes:         []contractv1.RouteId{contractv1.RouteId_ROUTE_ID_USER_GET_ME},
			wantCode:       codes.PermissionDenied,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTunnelFixture(t, fixtureOptions{
				clientURI:      test.clientURI,
				clientPurposes: test.clientPurposes,
			})
			ctx, cancel := context.WithTimeout(context.Background(), integrationWait)
			defer cancel()
			stream, err := fixture.client.Connect(ctx)
			if err == nil {
				err = stream.Send(gatewayOutHello(test.routes))
			}
			if err == nil {
				_, err = stream.Recv()
			}
			if err == nil {
				t.Fatal("Connect() completed handshake, want rejection")
			}
			if code := status.Code(err); code != test.wantCode {
				t.Fatalf("Connect() code = %s, want %s (error %v)", code, test.wantCode, err)
			}
		})
	}
}

func TestReverseTunnel_HandshakeDeadlineIsBounded(t *testing.T) {
	fixture := newTunnelFixture(t, fixtureOptions{
		clientURI:      testPeerURI,
		clientPurposes: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	ctx, cancel := context.WithTimeout(context.Background(), integrationWait)
	defer cancel()
	stream, err := fixture.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	_, err = stream.Recv()
	if code := status.Code(err); code != codes.DeadlineExceeded {
		t.Fatalf("handshake code = %s, want DeadlineExceeded (error %v)", code, err)
	}
}

func TestReverseTunnel_DisconnectFailsActiveCall(t *testing.T) {
	fixture := newTunnelFixture(t, fixtureOptions{
		clientURI:      testPeerURI,
		clientPurposes: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	peer := newScriptedPeer(fixture.client, peerOptions{})
	peer.start(t)
	waitRouteReady(t, fixture.server.Registry(), contractv1.RouteId_ROUTE_ID_AUTH_LOGIN)

	result := make(chan error, 1)
	go func() {
		_, err := fixture.server.Registry().Invoke(context.Background(), tunnel.Call{
			Route:   contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
			Payload: []byte("bounded request"),
		})
		result <- err
	}()
	waitSignal(t, peer.requestHalfClosed, "request HalfClose")
	peer.stop()

	select {
	case err := <-result:
		if !errors.Is(err, tunnel.ErrTunnelClosed) {
			t.Fatalf("Invoke() error = %v, want ErrTunnelClosed", err)
		}
	case <-time.After(integrationWait):
		t.Fatal("Invoke() remained blocked after tunnel disconnect")
	}
}

func TestReverseTunnel_CallerCancellationPropagatesCancel(t *testing.T) {
	fixture := newTunnelFixture(t, fixtureOptions{
		clientURI:      testPeerURI,
		clientPurposes: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	peer := newScriptedPeer(fixture.client, peerOptions{})
	peer.start(t)
	waitRouteReady(t, fixture.server.Registry(), contractv1.RouteId_ROUTE_ID_AUTH_LOGIN)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := fixture.server.Registry().Invoke(ctx, tunnel.Call{
			Route:   contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
			Payload: []byte("bounded request"),
		})
		result <- err
	}()
	waitSignal(t, peer.requestHalfClosed, "request HalfClose")
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Invoke() error = %v, want context.Canceled", err)
		}
	case <-time.After(integrationWait):
		t.Fatal("Invoke() remained blocked after caller cancellation")
	}
	waitSignal(t, peer.cancelSeen, "Cancel")
	peer.assertHealthy(t)
}

func TestReverseTunnel_CancellationDuringBackpressurePropagatesCancel(t *testing.T) {
	fixture := newTunnelFixture(t, fixtureOptions{
		clientURI:      testPeerURI,
		clientPurposes: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	peer := newScriptedPeer(fixture.client, peerOptions{requestCredit: 1})
	peer.start(t)
	waitRouteReady(t, fixture.server.Registry(), contractv1.RouteId_ROUTE_ID_AUTH_LOGIN)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := fixture.server.Registry().Invoke(ctx, tunnel.Call{
			Route:   contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
			Payload: []byte("body blocked by bounded request credit"),
		})
		result <- err
	}()
	waitSignal(t, peer.requestDataSeen, "backpressured request Data")
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Invoke() error = %v, want context.Canceled", err)
		}
	case <-time.After(integrationWait):
		t.Fatal("Invoke() remained blocked after cancellation under backpressure")
	}
	waitSignal(t, peer.cancelSeen, "Cancel under backpressure")
	peer.assertHealthy(t)
}

func TestReverseTunnel_DrainStopsNewCallsAndClosesBoundedly(t *testing.T) {
	fixture := newTunnelFixture(t, fixtureOptions{
		clientURI:      testPeerURI,
		clientPurposes: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	peer := newScriptedPeer(fixture.client, peerOptions{})
	peer.start(t)
	waitRouteReady(t, fixture.server.Registry(), contractv1.RouteId_ROUTE_ID_AUTH_LOGIN)

	deadline := time.Now().Add(integrationWait)
	if err := fixture.server.Registry().Drain(
		context.Background(),
		deadline,
		contractv1.DrainReason_DRAIN_REASON_MAINTENANCE,
	); err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	waitSignal(t, peer.drainSeen, "Drain")
	_, err := fixture.server.Registry().Invoke(context.Background(), tunnel.Call{
		Route: contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
	})
	if !errors.Is(err, tunnel.ErrDraining) {
		t.Fatalf("Invoke() after Drain error = %v, want ErrDraining", err)
	}
}

func TestReverseTunnel_ExpiredContextDoesNotOpenRequest(t *testing.T) {
	fixture := newTunnelFixture(t, fixtureOptions{
		clientURI:      testPeerURI,
		clientPurposes: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	peer := newScriptedPeer(fixture.client, peerOptions{})
	peer.start(t)
	waitRouteReady(t, fixture.server.Registry(), contractv1.RouteId_ROUTE_ID_AUTH_LOGIN)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := fixture.server.Registry().Invoke(ctx, tunnel.Call{
		Route:   contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
		Payload: []byte("must not be queued"),
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Invoke() error = %v, want context.DeadlineExceeded", err)
	}
	time.Sleep(100 * time.Millisecond)
	if opens := peer.openCount.Load(); opens != 0 {
		t.Fatalf("peer observed %d Open frames for expired call", opens)
	}
}

type fixtureOptions struct {
	clientURI      string
	clientPurposes []x509.ExtKeyUsage
	logger         *slog.Logger
	meterProvider  *sdkmetric.MeterProvider
	tracerProvider *sdktrace.TracerProvider
}

type tunnelFixture struct {
	server *tunnel.Server
	client contractv1.TunnelServiceClient
}

func newTunnelFixture(t *testing.T, options fixtureOptions) *tunnelFixture {
	t.Helper()
	ca := newCertificateAuthority(t)
	serverCertificate := ca.issue(t, certificateOptions{
		commonName: serverDNSName,
		dnsNames:   []string{serverDNSName},
		purposes:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	clientCertificate := ca.issue(t, certificateOptions{
		commonName: "ignored-common-name",
		uri:        options.clientURI,
		purposes:   options.clientPurposes,
	})
	logger := options.logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	config := tunnel.Config{
		Peer: tunnel.PeerPolicy{AllowedURIs: []string{testPeerURI}},
		Limits: &contractv1.Limits{
			MaxFrameBytes:         4096,
			MaxDataBytes:          512,
			MaxMessageBytes:       4096,
			MaxInFlightRequests:   8,
			MaxMetadataEntries:    8,
			MaxMetadataValueBytes: 1024,
			MaxCreditBytes:        1024,
		},
		Routes: map[contractv1.RouteId]tunnel.RoutePolicy{
			contractv1.RouteId_ROUTE_ID_AUTH_LOGIN: {
				TrafficClass:     contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
				MaxRequestBytes:  2048,
				MaxResponseBytes: 2048,
				MaxDeadline:      time.Second,
				MaxInFlight:      4,
			},
		},
		Capabilities: []contractv1.Capability{contractv1.Capability_CAPABILITY_DRAIN},
		Queues: tunnel.QueueLimits{
			TunnelControl: 4,
			ControlAuth:   4,
			Regular:       2,
			Realtime:      2,
		},
		MaxTunnels:             4,
		MaxTunnelsPerInstance:  2,
		MaxInFlightPerInstance: 8,
		InitialResponseCredit:  1024,
		HandshakeTimeout:       500 * time.Millisecond,
		PingInterval:           time.Second,
		PongTimeout:            250 * time.Millisecond,
		Logger:                 logger,
		MeterProvider:          options.meterProvider,
		TracerProvider:         options.tracerProvider,
	}
	tunnelServer, err := tunnel.New(config)
	if err != nil {
		t.Fatalf("tunnel.New() error = %v", err)
	}

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{serverCertificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    ca.pool,
		MinVersion:   tls.VersionTLS13,
	})))
	contractv1.RegisterTunnelServiceServer(grpcServer, tunnelServer)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)
	t.Cleanup(func() { _ = listener.Close() })

	connection, err := grpc.NewClient(
		"passthrough:///marketmesh-bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			Certificates: []tls.Certificate{clientCertificate},
			RootCAs:      ca.pool,
			ServerName:   serverDNSName,
			MinVersion:   tls.VersionTLS13,
		})),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })

	return &tunnelFixture{
		server: tunnelServer,
		client: contractv1.NewTunnelServiceClient(connection),
	}
}

type peerOptions struct {
	response      *authv1.LoginResponse
	requestCredit uint32
	resultCode    contractv1.ResultCode
}

type scriptedPeer struct {
	client contractv1.TunnelServiceClient
	option peerOptions

	ctx               context.Context
	cancel            context.CancelFunc
	pongSeen          chan struct{}
	requestHalfClosed chan struct{}
	requestDataSeen   chan struct{}
	cancelSeen        chan struct{}
	drainSeen         chan struct{}
	errors            chan error
	openCount         atomic.Int64
}

func newScriptedPeer(client contractv1.TunnelServiceClient, option peerOptions) *scriptedPeer {
	ctx, cancel := context.WithCancel(context.Background())
	return &scriptedPeer{
		client:            client,
		option:            option,
		ctx:               ctx,
		cancel:            cancel,
		pongSeen:          make(chan struct{}),
		requestHalfClosed: make(chan struct{}),
		requestDataSeen:   make(chan struct{}),
		cancelSeen:        make(chan struct{}),
		drainSeen:         make(chan struct{}),
		errors:            make(chan error, 1),
	}
}

func (p *scriptedPeer) start(t *testing.T) {
	t.Helper()
	t.Cleanup(p.stop)
	go p.run()
}

func (p *scriptedPeer) stop() {
	p.cancel()
}

func (p *scriptedPeer) assertHealthy(t *testing.T) {
	t.Helper()
	select {
	case err := <-p.errors:
		t.Fatalf("scripted gateway-out error = %v", err)
	default:
	}
}

func (p *scriptedPeer) run() {
	stream, err := p.client.Connect(p.ctx)
	if err != nil {
		p.report(err)
		return
	}
	if err := stream.Send(gatewayOutHello(
		[]contractv1.RouteId{contractv1.RouteId_ROUTE_ID_AUTH_LOGIN},
	)); err != nil {
		p.report(err)
		return
	}
	hello, err := stream.Recv()
	if err != nil {
		p.report(err)
		return
	}
	if hello.GetHello() == nil || len(hello.GetHeader().GetTunnelId()) != 16 ||
		!bytes.Equal(hello.GetHello().GetInstanceId(), []byte{
			0x55, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		}) {
		p.report(errors.New("gateway-in returned invalid Hello"))
		return
	}
	tunnelID := slices.Clone(hello.GetHeader().GetTunnelId())
	if err := stream.Send(&contractv1.ConnectRequest{
		Header: tunnelHeader(tunnelID),
		Payload: &contractv1.ConnectRequest_Ping{
			Ping: &contractv1.Ping{Nonce: 42},
		},
	}); err != nil {
		p.report(err)
		return
	}

	requests := map[string]*peerRequest{}
	for {
		frame, err := stream.Recv()
		if err != nil {
			if p.ctx.Err() == nil && status.Code(err) != codes.Canceled && !errors.Is(err, io.EOF) {
				p.report(err)
			}
			return
		}
		switch payload := frame.GetPayload().(type) {
		case *contractv1.ConnectResponse_Pong:
			if payload.Pong.GetNonce() != 42 {
				p.report(fmt.Errorf("Pong nonce = %d, want 42", payload.Pong.GetNonce()))
				return
			}
			closeOnce(p.pongSeen)
		case *contractv1.ConnectResponse_Ping:
			if err := stream.Send(&contractv1.ConnectRequest{
				Header:  tunnelHeader(tunnelID),
				Payload: &contractv1.ConnectRequest_Pong{Pong: &contractv1.Pong{Nonce: payload.Ping.GetNonce()}},
			}); err != nil {
				p.report(err)
				return
			}
		case *contractv1.ConnectResponse_Open:
			p.openCount.Add(1)
			if payload.Open.GetRouteId() != contractv1.RouteId_ROUTE_ID_AUTH_LOGIN ||
				!payload.Open.GetDeadline().IsValid() {
				p.report(errors.New("Open violated fixed route or deadline policy"))
				return
			}
			key := string(frame.GetHeader().GetRequestId())
			requests[key] = &peerRequest{
				id:    slices.Clone(frame.GetHeader().GetRequestId()),
				class: frame.GetHeader().GetTrafficClass(),
				open:  payload.Open,
			}
			credit := p.option.requestCredit
			if credit == 0 {
				credit = 1024
			}
			if err := stream.Send(requestFrame(
				tunnelID,
				requests[key],
				&contractv1.ConnectRequest_Credit{Credit: &contractv1.Credit{Bytes: credit}},
			)); err != nil {
				p.report(err)
				return
			}
		case *contractv1.ConnectResponse_Credit:
			request := requests[string(frame.GetHeader().GetRequestId())]
			if request != nil {
				request.responseCredit += uint64(payload.Credit.GetBytes())
			}
		case *contractv1.ConnectResponse_Data:
			request := requests[string(frame.GetHeader().GetRequestId())]
			if request == nil {
				p.report(errors.New("Data for unknown request"))
				return
			}
			request.payload = append(request.payload, payload.Data.GetPayload()...)
			closeOnce(p.requestDataSeen)
		case *contractv1.ConnectResponse_HalfClose:
			request := requests[string(frame.GetHeader().GetRequestId())]
			if request == nil {
				p.report(errors.New("HalfClose for unknown request"))
				return
			}
			closeOnce(p.requestHalfClosed)
			if p.option.response != nil ||
				p.option.resultCode != contractv1.ResultCode_RESULT_CODE_UNSPECIFIED {
				if err := p.respond(stream, tunnelID, request); err != nil {
					p.report(err)
					return
				}
			}
		case *contractv1.ConnectResponse_Cancel:
			closeOnce(p.cancelSeen)
		case *contractv1.ConnectResponse_Drain:
			closeOnce(p.drainSeen)
		}
	}
}

func (p *scriptedPeer) respond(
	stream contractv1.TunnelService_ConnectClient,
	tunnelID []byte,
	request *peerRequest,
) error {
	loginRequest := new(authv1.LoginRequest)
	if err := proto.Unmarshal(request.payload, loginRequest); err != nil {
		return fmt.Errorf("decode LoginRequest: %w", err)
	}
	if loginRequest.GetIdentifier() != "alice-MM-11@example.test" ||
		string(loginRequest.GetPassword()) != integrationSecret {
		return errors.New("logical request payload changed")
	}
	frames := make([]any, 0, 3)
	if p.option.response != nil {
		responsePayload, err := proto.MarshalOptions{Deterministic: true}.Marshal(p.option.response)
		if err != nil {
			return err
		}
		if uint64(len(responsePayload)) > request.responseCredit {
			return errors.New("gateway-in did not grant enough response credit")
		}
		frames = append(frames, &contractv1.ConnectRequest_Data{
			Data: &contractv1.Data{Payload: responsePayload},
		})
	}
	code := p.option.resultCode
	if code == contractv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		code = contractv1.ResultCode_RESULT_CODE_OK
	}
	frames = append(
		frames,
		&contractv1.ConnectRequest_HalfClose{HalfClose: &contractv1.HalfClose{}},
		&contractv1.ConnectRequest_Result{Result: &contractv1.Result{Code: code}},
	)
	for _, payload := range frames {
		if err := stream.Send(requestFrame(tunnelID, request, payload)); err != nil {
			return err
		}
	}

	return nil
}

type peerRequest struct {
	id             []byte
	class          contractv1.TrafficClass
	open           *contractv1.Open
	sequence       uint64
	responseCredit uint64
	payload        []byte
}

func requestFrame(tunnelID []byte, request *peerRequest, payload any) *contractv1.ConnectRequest {
	request.sequence++
	frame := &contractv1.ConnectRequest{
		Header: &contractv1.FrameHeader{
			ProtocolVersion: 1,
			TunnelId:        slices.Clone(tunnelID),
			RequestId:       slices.Clone(request.id),
			Sequence:        request.sequence,
			TrafficClass:    request.class,
		},
	}
	switch typedPayload := payload.(type) {
	case *contractv1.ConnectRequest_Credit:
		frame.Payload = typedPayload
	case *contractv1.ConnectRequest_Data:
		frame.Payload = typedPayload
	case *contractv1.ConnectRequest_HalfClose:
		frame.Payload = typedPayload
	case *contractv1.ConnectRequest_Result:
		frame.Payload = typedPayload
	}

	return frame
}

func gatewayOutHello(routes []contractv1.RouteId) *contractv1.ConnectRequest {
	return &contractv1.ConnectRequest{
		Header: &contractv1.FrameHeader{},
		Payload: &contractv1.ConnectRequest_Hello{Hello: &contractv1.GatewayOutHello{
			InstanceId:                []byte("instance-id-0001"),
			SupportedProtocolVersions: []uint32{1},
			Capabilities:              []contractv1.Capability{contractv1.Capability_CAPABILITY_DRAIN},
			TrafficClasses:            []contractv1.TrafficClass{contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH},
			RouteIds:                  slices.Clone(routes),
			Limits: &contractv1.Limits{
				MaxFrameBytes:         4096,
				MaxDataBytes:          512,
				MaxMessageBytes:       4096,
				MaxInFlightRequests:   8,
				MaxMetadataEntries:    8,
				MaxMetadataValueBytes: 1024,
				MaxCreditBytes:        1024,
			},
		}},
	}
}

func tunnelHeader(tunnelID []byte) *contractv1.FrameHeader {
	return &contractv1.FrameHeader{
		ProtocolVersion: 1,
		TunnelId:        slices.Clone(tunnelID),
	}
}

func waitRouteReady(t *testing.T, registry *tunnel.Registry, route contractv1.RouteId) {
	t.Helper()
	deadline := time.Now().Add(integrationWait)
	for time.Now().Before(deadline) {
		if registry.IsRouteReady(route) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("route did not become ready")
}

func waitSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(integrationWait):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func closeOnce(signal chan struct{}) {
	select {
	case <-signal:
	default:
		close(signal)
	}
}

func (p *scriptedPeer) report(err error) {
	select {
	case p.errors <- err:
	default:
	}
}

type certificateAuthority struct {
	certificate *x509.Certificate
	key         *ecdsa.PrivateKey
	pool        *x509.CertPool
	serial      atomic.Int64
}

type certificateOptions struct {
	commonName string
	dnsNames   []string
	uri        string
	purposes   []x509.ExtKeyUsage
}

func newCertificateAuthority(t *testing.T) *certificateAuthority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey() error = %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "MarketMesh test CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("x509.CreateCertificate(CA) error = %v", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("x509.ParseCertificate(CA) error = %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(certificate)
	return &certificateAuthority{certificate: certificate, key: key, pool: pool}
}

func (c *certificateAuthority) issue(t *testing.T, options certificateOptions) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey() error = %v", err)
	}
	serial := c.serial.Add(1) + 1
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: options.commonName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  slices.Clone(options.purposes),
		DNSNames:     slices.Clone(options.dnsNames),
	}
	if options.uri != "" {
		identity, err := url.Parse(options.uri)
		if err != nil {
			t.Fatalf("url.Parse(%q) error = %v", options.uri, err)
		}
		template.URIs = []*url.URL{identity}
	}
	der, err := x509.CreateCertificate(
		rand.Reader,
		template,
		c.certificate,
		&key.PublicKey,
		c.key,
	)
	if err != nil {
		t.Fatalf("x509.CreateCertificate(leaf) error = %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("x509.MarshalPKCS8PrivateKey() error = %v", err)
	}
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatalf("tls.X509KeyPair() error = %v", err)
	}

	return certificate
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer strings.Builder
}

func newLockedBuffer() *lockedBuffer {
	return &lockedBuffer{}
}

func (b *lockedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.Write(value)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.String()
}

func assertSafeSpans(t *testing.T, spans []sdktrace.ReadOnlySpan) {
	t.Helper()
	if len(spans) == 0 {
		t.Fatal("no spans were exported")
	}
	allowedKeys := map[attribute.Key]struct{}{
		"tunnel.route":         {},
		"tunnel.traffic_class": {},
		"tunnel.result":        {},
	}
	for _, span := range spans {
		for _, item := range span.Attributes() {
			if _, allowed := allowedKeys[item.Key]; !allowed {
				t.Fatalf("span attribute key %q is not in finite allowlist", item.Key)
			}
			assertSafeAttributeValue(t, item.Value)
		}
	}
}

func assertSafeMetrics(t *testing.T, reader *sdkmetric.ManualReader) {
	t.Helper()
	metrics := metricdata.ResourceMetrics{}
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	metricCount := 0
	for _, scope := range metrics.ScopeMetrics {
		for _, metric := range scope.Metrics {
			metricCount++
			for _, set := range metricAttributeSets(metric.Data) {
				for _, item := range set.ToSlice() {
					if !strings.HasPrefix(string(item.Key), "tunnel.") {
						t.Fatalf("metric attribute key %q is outside tunnel namespace", item.Key)
					}
					assertSafeAttributeValue(t, item.Value)
				}
			}
		}
	}
	if metricCount == 0 {
		t.Fatal("no metrics were exported")
	}
}

func metricAttributeSets(data metricdata.Aggregation) []attribute.Set {
	switch typed := data.(type) {
	case metricdata.Gauge[int64]:
		return numberPointSets(typed.DataPoints)
	case metricdata.Gauge[float64]:
		return numberPointSets(typed.DataPoints)
	case metricdata.Sum[int64]:
		return numberPointSets(typed.DataPoints)
	case metricdata.Sum[float64]:
		return numberPointSets(typed.DataPoints)
	case metricdata.Histogram[int64]:
		return histogramPointSets(typed.DataPoints)
	case metricdata.Histogram[float64]:
		return histogramPointSets(typed.DataPoints)
	default:
		return nil
	}
}

func numberPointSets[N int64 | float64](points []metricdata.DataPoint[N]) []attribute.Set {
	result := make([]attribute.Set, 0, len(points))
	for _, point := range points {
		result = append(result, point.Attributes)
	}

	return result
}

func histogramPointSets[N int64 | float64](
	points []metricdata.HistogramDataPoint[N],
) []attribute.Set {
	result := make([]attribute.Set, 0, len(points))
	for _, point := range points {
		result = append(result, point.Attributes)
	}

	return result
}

func assertSafeAttributeValue(t *testing.T, value attribute.Value) {
	t.Helper()
	text := value.String()
	for _, forbidden := range []string{
		"alice-MM-11",
		integrationSecret,
		"external-token-MM-11",
		"opaque-subject-MM-11",
		testPeerURI,
		"instance-id-0001",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("telemetry attribute contains forbidden value %q", forbidden)
		}
	}
}
