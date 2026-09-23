package tunnel

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	platformlogger "github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

const (
	testServerName     = "gateway-in.test"
	testServerIdentity = "spiffe://marketmesh.test/dmz/gateway-in"
	testClientIdentity = "spiffe://marketmesh.test/internal/gateway-out"
	testSensitiveBody  = "body-secret-must-not-leak"
	testSensitiveToken = "Bearer tunnel-secret-must-not-leak"
)

type testPKI struct {
	clientTLS *tls.Config
	serverTLS *tls.Config
}

type fakeGatewayIn struct {
	contractv1.UnimplementedTunnelServiceServer
	connect func(grpcgo.BidiStreamingServer[contractv1.ConnectRequest, contractv1.ConnectResponse]) error
}

func (server *fakeGatewayIn) Connect(
	stream grpcgo.BidiStreamingServer[contractv1.ConnectRequest, contractv1.ConnectResponse],
) error {
	return server.connect(stream)
}

type fakeHealth struct {
	grpc_health_v1.UnimplementedHealthServer

	started    chan struct{}
	canceled   chan struct{}
	block      atomic.Bool
	callCount  atomic.Int64
	metadataMu sync.Mutex
	metadata   metadata.MD
	deadline   time.Time
}

func newFakeHealth() *fakeHealth {
	return &fakeHealth{
		started:  make(chan struct{}, 1),
		canceled: make(chan struct{}, 1),
	}
}

func (server *fakeHealth) Check(
	ctx context.Context,
	request *grpc_health_v1.HealthCheckRequest,
) (*grpc_health_v1.HealthCheckResponse, error) {
	server.callCount.Add(1)
	incoming, _ := metadata.FromIncomingContext(ctx)
	server.metadataMu.Lock()
	server.metadata = incoming.Copy()
	server.deadline, _ = ctx.Deadline()
	server.metadataMu.Unlock()
	notify(server.started)

	if server.block.Load() {
		<-ctx.Done()
		notify(server.canceled)
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if request.GetService() == "internal-error" {
		return nil, status.Error(codes.Internal, "private-dsn-and-stack-must-not-leak")
	}
	if err := grpcgo.SetTrailer(ctx, metadata.Pairs(
		"authorization",
		testSensitiveToken,
		"x-private-detail",
		"private-dsn-and-stack-must-not-leak",
	)); err != nil {
		return nil, status.Error(codes.Internal, "setting private trailer failed")
	}

	return &grpc_health_v1.HealthCheckResponse{
		Status: grpc_health_v1.HealthCheckResponse_SERVING,
	}, nil
}

func newTestPKI(t *testing.T) testPKI {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	now := time.Now().UTC()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "MM-12 test CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	caCertificate, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}

	serverCertificate := issueTestCertificate(
		t,
		caCertificate,
		caKey,
		2,
		testServerName,
		testServerIdentity,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	)
	clientCertificate := issueTestCertificate(
		t,
		caCertificate,
		caKey,
		3,
		"gateway-out.test",
		testClientIdentity,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	)
	pool := x509.NewCertPool()
	pool.AddCert(caCertificate)

	clientTLS := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		ServerName:   testServerName,
		RootCAs:      pool,
		Certificates: []tls.Certificate{clientCertificate},
	}
	serverTLS := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverCertificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}

	return testPKI{clientTLS: clientTLS, serverTLS: serverTLS}
}

func issueTestCertificate(
	t *testing.T,
	ca *x509.Certificate,
	caKey *ecdsa.PrivateKey,
	serial int64,
	dnsName string,
	identity string,
	usage []x509.ExtKeyUsage,
) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	identityURI, err := url.Parse(identity)
	if err != nil {
		t.Fatalf("parse test identity: %v", err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: dnsName},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  usage,
		DNSNames:     []string{dnsName},
		URIs:         []*url.URL{identityURI},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal leaf key: %v", err)
	}
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatalf("load leaf key pair: %v", err)
	}

	return certificate
}

func startGatewayIn(
	t *testing.T,
	pki testPKI,
	implementation contractv1.TunnelServiceServer,
) ContextDialer {
	t.Helper()

	listener := bufconn.Listen(protocolv1.MaxEncodedFrameBytes * 2)
	server := grpcgo.NewServer(
		grpcgo.Creds(credentials.NewTLS(pki.serverTLS)),
		grpcgo.MaxRecvMsgSize(protocolv1.MaxEncodedFrameBytes),
		grpcgo.MaxSendMsgSize(protocolv1.MaxEncodedFrameBytes),
	)
	contractv1.RegisterTunnelServiceServer(server, implementation)
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		select {
		case err := <-serveResult:
			if err != nil && !errors.Is(err, grpcgo.ErrServerStopped) {
				t.Errorf("gateway-in Serve() error = %v", err)
			}
		case <-time.After(time.Second):
			t.Error("gateway-in server did not stop")
		}
	})

	return func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}
}

func startInternalHealth(
	t *testing.T,
	implementation *fakeHealth,
) (ClassClients, func()) {
	t.Helper()

	listener := bufconn.Listen(1 << 20)
	server := grpcgo.NewServer()
	grpc_health_v1.RegisterHealthServer(server, implementation)
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()

	connections := make([]*grpcgo.ClientConn, 0, 3)
	for range 3 {
		connection, err := grpcgo.NewClient(
			"passthrough:///internal.test",
			grpcgo.WithTransportCredentials(insecure.NewCredentials()),
			grpcgo.WithDisableRetry(),
			grpcgo.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return listener.DialContext(ctx)
			}),
		)
		if err != nil {
			t.Fatalf("create internal client: %v", err)
		}
		connections = append(connections, connection)
	}

	cleanup := func() {
		for _, connection := range connections {
			if err := connection.Close(); err != nil {
				t.Errorf("close internal client: %v", err)
			}
		}
		server.Stop()
		_ = listener.Close()
		select {
		case err := <-serveResult:
			if err != nil && !errors.Is(err, grpcgo.ErrServerStopped) {
				t.Errorf("internal Serve() error = %v", err)
			}
		case <-time.After(time.Second):
			t.Error("internal server did not stop")
		}
	}
	t.Cleanup(cleanup)

	return ClassClients{
		ControlAuth: connections[0],
		Regular:     connections[1],
		Realtime:    connections[2],
	}, cleanup
}

func newTestRegistry(t *testing.T, clients ClassClients, routeID contractv1.RouteId) *Registry {
	t.Helper()

	registry, err := NewRegistry(clients, RouteSpec{
		ID:                    routeID,
		TrafficClass:          routeTrafficClass(routeID),
		Method:                grpc_health_v1.Health_Check_FullMethodName,
		NewRequest:            func() proto.Message { return &grpc_health_v1.HealthCheckRequest{} },
		NewResponse:           func() proto.Message { return &grpc_health_v1.HealthCheckResponse{} },
		MaxRequestBytes:       4 << 10,
		MaxResponseBytes:      4 << 10,
		MaxDeadline:           time.Second,
		Mutating:              routeID == contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
		RequireIdempotencyKey: routeID == contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	return registry
}

func newTestConfig(
	t *testing.T,
	pki testPKI,
	dialer ContextDialer,
	logs *bytes.Buffer,
) Config {
	t.Helper()

	log, err := platformlogger.New(platformlogger.Config{
		Service:     "gateway-out",
		Version:     "test",
		Environment: "test",
		Level:       "debug",
		Output:      logs,
	})
	if err != nil {
		t.Fatalf("logger.New() error = %v", err)
	}
	instanceID := [protocolv1.InstanceIDBytes]byte{}
	copy(instanceID[:], []byte("gateway-out-test"))

	return Config{
		Target:                 "passthrough:///gateway-in.test",
		TLSConfig:              pki.clientTLS,
		ExpectedServerIdentity: testServerIdentity,
		InstanceID:             instanceID,
		ConnectTimeout:         500 * time.Millisecond,
		HandshakeTimeout:       500 * time.Millisecond,
		KeepaliveTime:          time.Second,
		KeepaliveTimeout:       100 * time.Millisecond,
		PingInterval:           time.Second,
		PingTimeout:            100 * time.Millisecond,
		DrainTimeout:           100 * time.Millisecond,
		Limits: ReceiveLimits{
			MaxFrameBytes:         protocolv1.MaxEncodedFrameBytes,
			MaxDataBytes:          4 << 10,
			MaxMessageBytes:       16 << 10,
			MaxInFlightRequests:   8,
			MaxMetadataEntries:    protocolv1.MaxMetadataEntries,
			MaxMetadataValueBytes: protocolv1.MaxMetadataValueBytes,
			MaxCreditBytes:        16 << 10,
		},
		ClassLimits: map[contractv1.TrafficClass]ClassLimits{
			contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH: {
				MaxInFlight:        2,
				SendQueueDepth:     8,
				ReceiveWindowBytes: 4 << 10,
			},
			contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR: {
				MaxInFlight:        2,
				SendQueueDepth:     8,
				ReceiveWindowBytes: 4 << 10,
			},
			contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME: {
				MaxInFlight:        2,
				SendQueueDepth:     8,
				ReceiveWindowBytes: 4 << 10,
			},
		},
		Reconnect: ReconnectPolicy{
			MaxAttempts:      3,
			InitialBackoff:   time.Millisecond,
			MaxBackoff:       5 * time.Millisecond,
			Multiplier:       2,
			JitterRatio:      0,
			StableResetAfter: time.Second,
		},
		Logger:    log,
		Telemetry: telemetry.NewNoop(),
		Dialer:    dialer,
	}
}

func acceptTestHello(
	stream grpcgo.BidiStreamingServer[contractv1.ConnectRequest, contractv1.ConnectResponse],
) (*contractv1.GatewayOutHello, error) {
	request, err := stream.Recv()
	if err != nil {
		return nil, err
	}
	hello := request.GetHello()
	if hello == nil {
		return nil, errors.New("expected gateway-out Hello")
	}
	limits, ok := proto.Clone(hello.GetLimits()).(*contractv1.Limits)
	if !ok {
		return nil, errors.New("clone Hello limits")
	}
	tunnelID := bytes.Repeat([]byte{0x42}, protocolv1.TunnelIDBytes)
	if err := stream.Send(&contractv1.ConnectResponse{
		Header: &contractv1.FrameHeader{TunnelId: tunnelID},
		Payload: &contractv1.ConnectResponse_Hello{
			Hello: &contractv1.GatewayInHello{
				InstanceId:              bytes.Repeat([]byte{0x24}, protocolv1.InstanceIDBytes),
				SelectedProtocolVersion: protocolVersion,
				Capabilities:            slicesClone(hello.GetCapabilities()),
				TrafficClasses:          slicesClone(hello.GetTrafficClasses()),
				RouteIds:                slicesClone(hello.GetRouteIds()),
				Limits:                  limits,
			},
		},
	}); err != nil {
		return nil, err
	}

	return hello, nil
}

func testGatewayInFrame(
	requestID []byte,
	class contractv1.TrafficClass,
	sequence uint64,
	payload any,
) *contractv1.ConnectResponse {
	frame := &contractv1.ConnectResponse{
		Header: &contractv1.FrameHeader{
			ProtocolVersion: protocolVersion,
			TunnelId:        bytes.Repeat([]byte{0x42}, protocolv1.TunnelIDBytes),
			RequestId:       bytes.Clone(requestID),
			Sequence:        sequence,
			TrafficClass:    class,
		},
	}
	switch typed := payload.(type) {
	case *contractv1.ConnectResponse_Open:
		frame.Payload = typed
	case *contractv1.ConnectResponse_Data:
		frame.Payload = typed
	case *contractv1.ConnectResponse_HalfClose:
		frame.Payload = typed
	case *contractv1.ConnectResponse_Cancel:
		frame.Payload = typed
	case *contractv1.ConnectResponse_Credit:
		frame.Payload = typed
	case *contractv1.ConnectResponse_Ping:
		frame.Payload = typed
	case *contractv1.ConnectResponse_Pong:
		frame.Payload = typed
	case *contractv1.ConnectResponse_Drain:
		frame.Payload = typed
	}

	return frame
}

func recvApplicationFrame(
	stream grpcgo.BidiStreamingServer[contractv1.ConnectRequest, contractv1.ConnectResponse],
) (*contractv1.ConnectRequest, error) {
	for {
		frame, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		if ping := frame.GetPing(); ping != nil {
			if err := stream.Send(testGatewayInFrame(
				nil,
				contractv1.TrafficClass_TRAFFIC_CLASS_UNSPECIFIED,
				0,
				&contractv1.ConnectResponse_Pong{Pong: &contractv1.Pong{Nonce: ping.GetNonce()}},
			)); err != nil {
				return nil, err
			}
			continue
		}
		return frame, nil
	}
}

func slicesClone[T ~int32](values []T) []T {
	result := make([]T, len(values))
	copy(result, values)
	return result
}
