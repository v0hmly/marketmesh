package frontdoor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	e2ev1connect "github.com/v0hmly/marketmesh/api/gen/go/e2e/v1/e2ev1connect"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

const (
	testTargetA = "http://127.0.0.1:18081"
	testTargetB = "http://127.0.0.1:18082"
)

func TestNewRejectsUnsafeOrAmbiguousConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config Config
	}{
		{name: "DNS target", config: Config{DCATarget: "http://backend-a:8080", DCBTarget: testTargetB}},
		{name: "public target", config: Config{DCATarget: "http://203.0.113.1:8080", DCBTarget: testTargetB}},
		{name: "TLS target", config: Config{DCATarget: "https://127.0.0.1:18081", DCBTarget: testTargetB}},
		{name: "privileged port", config: Config{DCATarget: "http://127.0.0.1:80", DCBTarget: testTargetB}},
		{name: "path", config: Config{DCATarget: testTargetA + "/api", DCBTarget: testTargetB}},
		{name: "encoded path", config: Config{DCATarget: testTargetA + "/%2f", DCBTarget: testTargetB}},
		{name: "empty query", config: Config{DCATarget: testTargetA + "?", DCBTarget: testTargetB}},
		{name: "same target", config: Config{DCATarget: testTargetA, DCBTarget: testTargetA}},
		{name: "long health interval", config: Config{
			DCATarget: testTargetA, DCBTarget: testTargetB, HealthCheckInterval: 11 * time.Second,
		}},
		{name: "timeout exceeds interval", config: Config{
			DCATarget: testTargetA, DCBTarget: testTargetB,
			HealthCheckInterval: time.Second, HealthCheckTimeout: 2 * time.Second,
		}},
		{name: "long warmup", config: Config{
			DCATarget: testTargetA, DCBTarget: testTargetB, FailbackWarmup: 11 * time.Minute,
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(test.config); err == nil {
				t.Fatal("New() error = nil, want validation error")
			}
		})
	}
}

func TestDefaultTransportIgnoresAmbientProxyConfiguration(t *testing.T) {
	t.Parallel()

	frontDoor := newTestFrontDoor(t, time.Now)
	transport, ok := frontDoor.healthClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("health transport = %T, want *http.Transport", frontDoor.healthClient.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("default transport unexpectedly uses ambient proxy configuration")
	}
}

func TestSelectBackendBalancesHealthyDataCenters(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	frontDoor := newTestFrontDoor(t, func() time.Time { return now })
	frontDoor.setHealth(context.Background(), frontDoor.backends[0], true)
	frontDoor.setHealth(context.Background(), frontDoor.backends[1], true)
	now = now.Add(time.Minute)

	counts := selectCounts(frontDoor, 32)
	if counts[dataCenterA] != 16 || counts[dataCenterB] != 16 {
		t.Fatalf("selection counts = %v, want 16/16", counts)
	}
}

func TestSelectBackendExcludesUnhealthyAndWarmsRecoveredDataCenter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	frontDoor := newTestFrontDoor(t, func() time.Time { return now })
	frontDoor.setHealth(context.Background(), frontDoor.backends[0], true)
	now = now.Add(time.Minute)
	frontDoor.setHealth(context.Background(), frontDoor.backends[1], true)

	warmCounts := selectCounts(frontDoor, 110)
	if warmCounts[dataCenterA] != 100 || warmCounts[dataCenterB] != 10 {
		t.Fatalf("warm selection counts = %v, want 100/10", warmCounts)
	}

	now = now.Add(time.Minute)
	steadyCounts := selectCounts(frontDoor, 40)
	if steadyCounts[dataCenterA] != 20 || steadyCounts[dataCenterB] != 20 {
		t.Fatalf("steady selection counts = %v, want 20/20", steadyCounts)
	}

	frontDoor.setHealth(context.Background(), frontDoor.backends[0], false)
	failoverCounts := selectCounts(frontDoor, 20)
	if failoverCounts[dataCenterA] != 0 || failoverCounts[dataCenterB] != 20 {
		t.Fatalf("failover selection counts = %v, want 0/20", failoverCounts)
	}
}

func TestHandlerExposesOnlyFiniteSurface(t *testing.T) {
	t.Parallel()

	frontDoor := newTestFrontDoor(t, time.Now)
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "live", method: http.MethodGet, path: "/livez", wantStatus: http.StatusOK},
		{name: "not ready", method: http.MethodGet, path: "/readyz", wantStatus: http.StatusServiceUnavailable},
		{name: "health method", method: http.MethodPost, path: "/livez", wantStatus: http.StatusMethodNotAllowed},
		{name: "route method", method: http.MethodGet, path: e2ev1connect.FakeInternalServiceReadProcedure, wantStatus: http.StatusMethodNotAllowed},
		{name: "unknown", method: http.MethodPost, path: "/admin", wantStatus: http.StatusNotFound},
		{name: "no healthy backend", method: http.MethodPost, path: e2ev1connect.FakeInternalServiceMutateProcedure, wantStatus: http.StatusServiceUnavailable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(test.method, test.path, http.NoBody)
			response := httptest.NewRecorder()
			frontDoor.Handler().ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
		})
	}
}

func TestCheckAndProxyFailOverWithoutCrossDataCenterRetry(t *testing.T) {
	t.Parallel()

	var readyA atomic.Bool
	var readyB atomic.Bool
	readyA.Store(true)
	readyB.Store(true)
	var requestsA atomic.Int64
	var requestsB atomic.Int64
	backendA := newBackendServer(t, &readyA, &requestsA)
	backendB := newBackendServer(t, &readyB, &requestsB)
	frontDoor := newNetworkFrontDoor(t, backendA.URL, backendB.URL)
	frontDoor.Check(context.Background())

	proxyRequest(t, frontDoor, e2ev1connect.FakeInternalServiceReadProcedure, "read-a")
	if requestsA.Load() != 1 || requestsB.Load() != 0 {
		t.Fatalf("initial requests = dc-a:%d dc-b:%d, want 1/0", requestsA.Load(), requestsB.Load())
	}

	readyA.Store(false)
	frontDoor.Check(context.Background())
	proxyRequest(t, frontDoor, e2ev1connect.FakeInternalServiceReadProcedure, "read-b")
	if requestsA.Load() != 1 || requestsB.Load() != 1 {
		t.Fatalf("failover requests = dc-a:%d dc-b:%d, want 1/1", requestsA.Load(), requestsB.Load())
	}
}

func TestMutatingRequestIsNotReplayedAfterAmbiguousFailure(t *testing.T) {
	t.Parallel()

	var requestsA atomic.Int64
	var requestsB atomic.Int64
	backendA := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/readyz" {
			response.WriteHeader(http.StatusOK)
			return
		}
		requestsA.Add(1)
		_, _ = io.Copy(io.Discard, request.Body)
		connection, _, err := response.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("Hijack() error = %v", err)
			return
		}
		_ = connection.Close()
	}))
	t.Cleanup(backendA.Close)
	readyB := atomic.Bool{}
	readyB.Store(true)
	backendB := newBackendServer(t, &readyB, &requestsB)
	frontDoor := newNetworkFrontDoor(t, backendA.URL, backendB.URL)
	frontDoor.Check(context.Background())

	request := httptest.NewRequest(
		http.MethodPost,
		e2ev1connect.FakeInternalServiceMutateProcedure,
		strings.NewReader("mutation"),
	)
	request.GetBody = nil
	request.Header.Set("Idempotency-Key", "ambiguous-test")
	response := httptest.NewRecorder()
	frontDoor.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadGateway)
	}
	if requestsA.Load() != 1 || requestsB.Load() != 0 {
		t.Fatalf("mutating attempts = dc-a:%d dc-b:%d, want 1/0", requestsA.Load(), requestsB.Load())
	}
}

func newTestFrontDoor(t *testing.T, now func() time.Time) *FrontDoor {
	t.Helper()
	frontDoor, err := New(Config{
		DCATarget: testTargetA, DCBTarget: testTargetB,
		HealthCheckInterval: time.Second, HealthCheckTimeout: 100 * time.Millisecond,
		FailbackWarmup: 30 * time.Second, Now: now,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return frontDoor
}

func newNetworkFrontDoor(t *testing.T, targetA string, targetB string) *FrontDoor {
	t.Helper()
	frontDoor, err := New(Config{
		DCATarget: targetA, DCBTarget: targetB,
		HealthCheckInterval: time.Second, HealthCheckTimeout: 500 * time.Millisecond,
		FailbackWarmup: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return frontDoor
}

func newBackendServer(t *testing.T, ready *atomic.Bool, requests *atomic.Int64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/readyz" {
			if ready.Load() {
				response.WriteHeader(http.StatusOK)
			} else {
				response.WriteHeader(http.StatusServiceUnavailable)
			}
			return
		}
		requests.Add(1)
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)
	return server
}

func proxyRequest(t *testing.T, frontDoor *FrontDoor, path string, body string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.GetBody = nil
	response := httptest.NewRecorder()
	frontDoor.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
}

func selectCounts(frontDoor *FrontDoor, count int) map[string]int {
	result := map[string]int{dataCenterA: 0, dataCenterB: 0}
	for range count {
		selected := frontDoor.selectBackend(context.Background(), "read")
		if selected != nil {
			result[selected.dataCenter]++
		}
	}
	return result
}

func TestConcurrentSelectionRemainsBalanced(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	frontDoor := newTestFrontDoor(t, func() time.Time { return now })
	frontDoor.setHealth(context.Background(), frontDoor.backends[0], true)
	frontDoor.setHealth(context.Background(), frontDoor.backends[1], true)
	now = now.Add(time.Minute)

	var counts sync.Map
	var waitGroup sync.WaitGroup
	for range 32 {
		waitGroup.Go(func() {
			selected := frontDoor.selectBackend(context.Background(), "read")
			if selected != nil {
				value, _ := counts.LoadOrStore(selected.dataCenter, &atomic.Int64{})
				value.(*atomic.Int64).Add(1)
			}
		})
	}
	waitGroup.Wait()
	countA, _ := counts.Load(dataCenterA)
	countB, _ := counts.Load(dataCenterB)
	if countA.(*atomic.Int64).Load() != 16 || countB.(*atomic.Int64).Load() != 16 {
		t.Fatalf(
			"concurrent counts = dc-a:%d dc-b:%d, want 16/16",
			countA.(*atomic.Int64).Load(),
			countB.(*atomic.Int64).Load(),
		)
	}
}

func TestMetricsContainOnlyBoundedLabels(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	frontDoor, err := New(Config{
		DCATarget: testTargetA, DCBTarget: testTargetB,
		HealthCheckInterval: time.Second, HealthCheckTimeout: 100 * time.Millisecond,
		FailbackWarmup: 30 * time.Second, Now: func() time.Time { return now },
		MeterProvider: provider,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	frontDoor.setHealth(context.Background(), frontDoor.backends[0], true)
	frontDoor.setHealth(context.Background(), frontDoor.backends[1], false)
	_ = frontDoor.selectBackend(context.Background(), "read")

	metrics := metricdata.ResourceMetrics{}
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	pointCount := 0
	for _, scope := range metrics.ScopeMetrics {
		for _, collected := range scope.Metrics {
			sum, ok := collected.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("metric %q aggregation = %T, want Sum[int64]", collected.Name, collected.Data)
			}
			for _, point := range sum.DataPoints {
				pointCount++
				for _, label := range point.Attributes.ToSlice() {
					assertBoundedMetricLabel(t, string(label.Key), label.Value.AsString())
				}
			}
		}
	}
	if pointCount == 0 {
		t.Fatal("collected metric points = 0, want bounded telemetry")
	}
}

func assertBoundedMetricLabel(t *testing.T, key string, value string) {
	t.Helper()
	allowed := map[string]map[string]struct{}{
		"data_center": {"dc-a": {}, "dc-b": {}, "none": {}},
		"route":       {"health": {}, "read": {}, "mutate": {}, "unknown": {}},
		"status": {
			"not_ready": {}, "ready": {}, "selected": {}, "unavailable": {}, "upstream_error": {},
		},
	}
	values, ok := allowed[key]
	if !ok {
		t.Fatalf("unexpected metric label key %q", key)
	}
	if _, ok := values[value]; !ok {
		t.Fatalf("metric label %q has unbounded value %q", key, value)
	}
}
