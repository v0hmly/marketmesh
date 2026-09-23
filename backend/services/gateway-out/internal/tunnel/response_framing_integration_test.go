//go:build integration

package tunnel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e2ev1 "github.com/v0hmly/marketmesh/api/gen/go/e2e/v1"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	gatewayintest "github.com/v0hmly/marketmesh/services/gateway-in/tunneltest"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/goleak"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	responseFrameBytes = 32
	framingWait        = 2 * time.Second
)

func TestResponseFraming_RealGatewayPair(t *testing.T) {
	goleakBaseline := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, goleakBaseline) })

	fixture := newResponseFramingFixture(t)
	before := collectInboundFrameCounts(t, fixture.reader)

	t.Run("zero body", func(t *testing.T) {
		response, err := fixture.invoke(t, 0x00)
		if err != nil {
			t.Fatalf("Invoke() error = %v", err)
		}
		if len(response.Payload) != 0 {
			t.Fatalf("response payload bytes = %d, want 0", len(response.Payload))
		}
		before = assertFrameDelta(t, fixture.reader, before, frameCounts{
			"half_close": 1,
			"result":     1,
		})
	})

	t.Run("multi Data payload", func(t *testing.T) {
		response, err := fixture.invoke(t, 0x01)
		if err != nil {
			t.Fatalf("Invoke() error = %v", err)
		}
		wantMessage := multiDataResponse()
		wantPayload, err := proto.MarshalOptions{Deterministic: true}.Marshal(wantMessage)
		if err != nil {
			t.Fatalf("marshal expected response: %v", err)
		}
		if !proto.Equal(decodeReadResponse(t, response.Payload), wantMessage) {
			t.Fatal("response payload changed across reverse tunnel")
		}
		dataFrames := (len(wantPayload) + responseFrameBytes - 1) / responseFrameBytes
		if dataFrames < 2 {
			t.Fatalf("response Data frames = %d, want at least 2", dataFrames)
		}
		before = assertFrameDelta(t, fixture.reader, before, frameCounts{
			"data":       int64(dataFrames),
			"half_close": 1,
			"result":     1,
		})
	})

	t.Run("internal error", func(t *testing.T) {
		_, err := fixture.invoke(t, 0x02)
		resultErr := new(gatewayintest.ResultError)
		if !errors.As(err, &resultErr) || resultErr.Code() != contractv1.ResultCode_RESULT_CODE_INTERNAL {
			t.Fatalf("Invoke() error = %v, want finite INTERNAL Result", err)
		}
		before = assertFrameDelta(t, fixture.reader, before, frameCounts{
			"half_close": 1,
			"result":     1,
		})
	})

	t.Run("caller cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := fixture.invokeContext(ctx, 0x03)
			result <- err
		}()
		select {
		case <-fixture.backend.started:
		case <-time.After(framingWait):
			t.Fatal("internal call did not start")
		}
		cancel()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Invoke() error = %v, want context.Canceled", err)
			}
		case <-time.After(framingWait):
			t.Fatal("canceled call did not complete boundedly")
		}
		before = assertFrameDelta(t, fixture.reader, before, frameCounts{
			"half_close": 1,
			"result":     1,
		})
	})

	t.Run("deadline races backend completion", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		started := time.Now()
		_, err := fixture.invokeContext(ctx, 0x04)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Invoke() error = %v, want context.DeadlineExceeded", err)
		}
		if elapsed := time.Since(started); elapsed >= framingWait {
			t.Fatalf("deadline race completed in %s, want bounded completion", elapsed)
		}
		before = assertFrameDelta(t, fixture.reader, before, frameCounts{
			"half_close": 1,
			"result":     1,
		})
	})

	response, err := fixture.invoke(t, 0x00)
	if err != nil || len(response.Payload) != 0 {
		t.Fatalf(
			"post-cancellation Invoke() = (%d bytes, %v), want healthy empty response",
			len(response.Payload),
			err,
		)
	}
	assertFrameDelta(t, fixture.reader, before, frameCounts{
		"half_close": 1,
		"result":     1,
	})
	if !fixture.client.IsReady() {
		t.Fatal("gateway-out tunnel was torn down after valid response framing")
	}
	instanceID, found := fixture.client.ServerInstanceID()
	if !found || instanceID != fixture.serverInstanceID {
		t.Fatalf("ServerInstanceID() = (%x, %t), want unchanged %x", instanceID, found, fixture.serverInstanceID)
	}
	if connects := fixture.connects.Load(); connects != 1 {
		t.Fatalf("gateway-in Connect calls = %d, want exactly one connection", connects)
	}
	if failures := collectTunnelFailures(t, fixture.reader); failures != 0 {
		t.Fatalf("gateway-in tunnel failures = %d, want 0", failures)
	}
}

type responseFramingFixture struct {
	server           *gatewayintest.Server
	client           *Client
	reader           *sdkmetric.ManualReader
	backend          *framingBackend
	connects         *atomic.Int64
	serverInstanceID [protocolv1.InstanceIDBytes]byte
}

func newResponseFramingFixture(t *testing.T) *responseFramingFixture {
	t.Helper()
	pki := newTestPKI(t)
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), framingWait)
		defer cancel()
		if err := meterProvider.Shutdown(shutdownCtx); err != nil {
			t.Errorf("meter provider shutdown: %v", err)
		}
	})

	limits := &contractv1.Limits{
		MaxFrameBytes:         1024,
		MaxDataBytes:          responseFrameBytes,
		MaxMessageBytes:       4096,
		MaxInFlightRequests:   8,
		MaxMetadataEntries:    8,
		MaxMetadataValueBytes: 1024,
		MaxCreditBytes:        4096,
	}
	tunnelServer, err := gatewayintest.New(gatewayintest.Config{
		InstanceID: [protocolv1.InstanceIDBytes]byte{0x42},
		Peer: gatewayintest.PeerPolicy{
			AllowedURIs: []string{testClientIdentity},
			DataCenterByURI: map[string]string{
				testClientIdentity: "dc-a",
			},
		},
		Limits: limits,
		Routes: map[contractv1.RouteId]gatewayintest.RoutePolicy{
			contractv1.RouteId_ROUTE_ID_AUTH_LOGIN: {
				TrafficClass:     contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
				MaxRequestBytes:  4096,
				MaxResponseBytes: 4096,
				MaxDeadline:      framingWait,
				MaxInFlight:      4,
			},
		},
		Capabilities: []contractv1.Capability{contractv1.Capability_CAPABILITY_DRAIN},
		Queues: gatewayintest.QueueLimits{
			TunnelControl: 4,
			ControlAuth:   8,
			Regular:       2,
			Realtime:      2,
		},
		MaxTunnels:             2,
		MaxTunnelsPerInstance:  1,
		MaxInFlightPerInstance: 4,
		InitialResponseCredit:  4096,
		HandshakeTimeout:       500 * time.Millisecond,
		PingInterval:           time.Second,
		PongTimeout:            250 * time.Millisecond,
		FailbackWarmup:         time.Second,
		Logger:                 slog.New(slog.NewJSONHandler(io.Discard, nil)),
		MeterProvider:          meterProvider,
	})
	if err != nil {
		t.Fatalf("gateway-in tunnel New() error = %v", err)
	}
	countingServer := &countingTunnelServer{target: tunnelServer}
	dialer := startGatewayIn(t, pki, countingServer)

	backend := &framingBackend{started: make(chan struct{}, 1)}
	registry, err := NewRegistry(
		ClassClients{ControlAuth: backend},
		RouteSpec{
			ID:               contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
			TrafficClass:     contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
			Method:           e2ev1.FakeInternalService_Read_FullMethodName,
			NewRequest:       func() proto.Message { return &e2ev1.ReadRequest{} },
			NewResponse:      func() proto.Message { return &e2ev1.ReadResponse{} },
			MaxRequestBytes:  4096,
			MaxResponseBytes: 4096,
			MaxDeadline:      framingWait,
		},
	)
	if err != nil {
		t.Fatalf("gateway-out NewRegistry() error = %v", err)
	}
	config := newTestConfig(t, pki, dialer, &bytes.Buffer{})
	config.Limits = ReceiveLimits{
		MaxFrameBytes:         limits.GetMaxFrameBytes(),
		MaxDataBytes:          limits.GetMaxDataBytes(),
		MaxMessageBytes:       limits.GetMaxMessageBytes(),
		MaxInFlightRequests:   limits.GetMaxInFlightRequests(),
		MaxMetadataEntries:    limits.GetMaxMetadataEntries(),
		MaxMetadataValueBytes: limits.GetMaxMetadataValueBytes(),
		MaxCreditBytes:        limits.GetMaxCreditBytes(),
	}
	config.ClassLimits[contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH] = ClassLimits{
		MaxInFlight:        4,
		SendQueueDepth:     8,
		ReceiveWindowBytes: 4096,
	}
	client, err := NewClient(config, registry)
	if err != nil {
		t.Fatalf("gateway-out NewClient() error = %v", err)
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() { runResult <- client.Run(runCtx) }()
	t.Cleanup(func() {
		cancelRun()
		select {
		case err := <-runResult:
			if err != nil {
				t.Errorf("gateway-out Run() error = %v", err)
			}
		case <-time.After(framingWait):
			t.Error("gateway-out Run() did not stop")
		}
	})
	waitRealPairReady(t, client, tunnelServer)
	serverInstanceID, found := client.ServerInstanceID()
	if !found {
		t.Fatal("gateway-out did not expose negotiated server instance")
	}

	return &responseFramingFixture{
		server:           tunnelServer,
		client:           client,
		reader:           reader,
		backend:          backend,
		connects:         &countingServer.connects,
		serverInstanceID: serverInstanceID,
	}
}

type countingTunnelServer struct {
	contractv1.UnimplementedTunnelServiceServer
	target   contractv1.TunnelServiceServer
	connects atomic.Int64
}

func (s *countingTunnelServer) Connect(stream contractv1.TunnelService_ConnectServer) error {
	s.connects.Add(1)

	return s.target.Connect(stream)
}

func (f *responseFramingFixture) invoke(t *testing.T, marker byte) (gatewayintest.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), framingWait)
	defer cancel()

	return f.invokeContext(ctx, marker)
}

func (f *responseFramingFixture) invokeContext(
	ctx context.Context,
	marker byte,
) (gatewayintest.Response, error) {
	payload, err := proto.Marshal(&e2ev1.ReadRequest{
		RequestId: append([]byte{marker}, make([]byte, 15)...),
	})
	if err != nil {
		return gatewayintest.Response{}, err
	}

	return f.server.Registry().Invoke(ctx, gatewayintest.Call{
		Route:   contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
		Payload: payload,
	})
}

type framingBackend struct {
	started chan struct{}
}

func (b *framingBackend) Invoke(
	ctx context.Context,
	method string,
	args any,
	reply any,
	_ ...grpcgo.CallOption,
) error {
	if method != e2ev1.FakeInternalService_Read_FullMethodName {
		return status.Error(codes.Unimplemented, "unexpected method")
	}
	request, requestOK := args.(*e2ev1.ReadRequest)
	response, responseOK := reply.(*e2ev1.ReadResponse)
	if !requestOK || !responseOK || len(request.GetRequestId()) != 16 {
		return status.Error(codes.InvalidArgument, "unexpected message")
	}
	switch request.GetRequestId()[0] {
	case 0x00:
		return nil
	case 0x01:
		proto.Merge(response, multiDataResponse())
		return nil
	case 0x02:
		return status.Error(codes.Internal, "private internal error")
	case 0x03:
		select {
		case b.started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return status.FromContextError(ctx.Err()).Err()
	case 0x04:
		<-ctx.Done()
		return nil
	default:
		return status.Error(codes.InvalidArgument, "unexpected marker")
	}
}

func (*framingBackend) NewStream(
	context.Context,
	*grpcgo.StreamDesc,
	string,
	...grpcgo.CallOption,
) (grpcgo.ClientStream, error) {
	return nil, status.Error(codes.Unimplemented, "streaming is not used")
}

func multiDataResponse() *e2ev1.ReadResponse {
	return &e2ev1.ReadResponse{
		InstanceId: strings.Repeat("gateway-out-response-", 5),
		Sequence:   42,
	}
}

func decodeReadResponse(t *testing.T, payload []byte) *e2ev1.ReadResponse {
	t.Helper()
	response := new(e2ev1.ReadResponse)
	if err := proto.Unmarshal(payload, response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	return response
}

type frameCounts map[string]int64

func assertFrameDelta(
	t *testing.T,
	reader *sdkmetric.ManualReader,
	before frameCounts,
	want frameCounts,
) frameCounts {
	t.Helper()
	deadline := time.Now().Add(framingWait)
	for {
		after := collectInboundFrameCounts(t, reader)
		matches := true
		for _, frameType := range []string{"data", "half_close", "result"} {
			if after[frameType]-before[frameType] != want[frameType] {
				matches = false
				break
			}
		}
		if matches {
			return after
		}
		if time.Now().After(deadline) {
			t.Fatalf("inbound frame delta = %v, want %v", subtractCounts(after, before), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func collectInboundFrameCounts(t *testing.T, reader *sdkmetric.ManualReader) frameCounts {
	t.Helper()
	metrics := metricdata.ResourceMetrics{}
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	result := frameCounts{}
	for _, scope := range metrics.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != "marketmesh.gateway_in.tunnel.frames" {
				continue
			}
			sum, ok := metric.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, point := range sum.DataPoints {
				direction, hasDirection := point.Attributes.Value("tunnel.direction")
				frameType, hasFrameType := point.Attributes.Value("tunnel.frame_type")
				if hasDirection && hasFrameType && direction.AsString() == "gateway_out_to_gateway_in" {
					result[frameType.AsString()] += point.Value
				}
			}
		}
	}

	return result
}

func collectTunnelFailures(t *testing.T, reader *sdkmetric.ManualReader) int64 {
	t.Helper()
	metrics := metricdata.ResourceMetrics{}
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	var result int64
	for _, scope := range metrics.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != "marketmesh.gateway_in.tunnel.failures" {
				continue
			}
			sum, ok := metric.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, point := range sum.DataPoints {
				result += point.Value
			}
		}
	}

	return result
}

func subtractCounts(after frameCounts, before frameCounts) frameCounts {
	result := frameCounts{}
	for _, frameType := range []string{"data", "half_close", "result"} {
		result[frameType] = after[frameType] - before[frameType]
	}

	return result
}

func waitRealPairReady(t *testing.T, client *Client, server *gatewayintest.Server) {
	t.Helper()
	deadline := time.Now().Add(framingWait)
	for time.Now().Before(deadline) {
		if client.IsReady() && server.Registry().IsRouteReady(contractv1.RouteId_ROUTE_ID_AUTH_LOGIN) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("real gateway pair did not become ready")
}
