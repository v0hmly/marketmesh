// Package frontdoor provides a local, health-aware entry point for the two-DC
// tunnel E2E environment. It deliberately supports only the finite E2E API.
package frontdoor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strconv"
	"sync"
	"time"

	e2ev1connect "github.com/v0hmly/marketmesh/api/gen/go/e2e/v1/e2ev1connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

const (
	dataCenterA = "dc-a"
	dataCenterB = "dc-b"

	defaultHealthInterval = time.Second
	defaultHealthTimeout  = 250 * time.Millisecond
	defaultFailbackWarmup = 30 * time.Second
	maxHealthInterval     = 10 * time.Second
	maxHealthTimeout      = 5 * time.Second
	maxFailbackWarmup     = 10 * time.Minute
	minimumBackendWeight  = int64(10)
	maximumBackendWeight  = int64(100)
	maxHealthBodyBytes    = int64(1024)
)

// Config defines the two fixed data-center targets and bounded health policy.
type Config struct {
	DCATarget           string
	DCBTarget           string
	HealthCheckInterval time.Duration
	HealthCheckTimeout  time.Duration
	FailbackWarmup      time.Duration
	Logger              *slog.Logger
	MeterProvider       metric.MeterProvider
	Transport           http.RoundTripper
	Now                 func() time.Time
}

// FrontDoor selects one healthy data center for each request. It never retries
// a request against a different target, including after an ambiguous failure.
type FrontDoor struct {
	mu                  sync.Mutex
	backends            []*backend
	healthCheckInterval time.Duration
	healthCheckTimeout  time.Duration
	failbackWarmup      time.Duration
	healthClient        *http.Client
	now                 func() time.Time
	log                 *slog.Logger
	instrumentation     *instrumentation
}

type backend struct {
	dataCenter    string
	target        *url.URL
	proxy         *httputil.ReverseProxy
	healthy       bool
	recoveredAt   time.Time
	currentWeight int64
}

type instrumentation struct {
	selections   metric.Int64Counter
	healthChecks metric.Int64Counter
}

// New validates all external inputs before constructing the front door.
func New(config Config) (*FrontDoor, error) {
	healthCheckInterval := config.HealthCheckInterval
	if healthCheckInterval == 0 {
		healthCheckInterval = defaultHealthInterval
	}
	healthCheckTimeout := config.HealthCheckTimeout
	if healthCheckTimeout == 0 {
		healthCheckTimeout = defaultHealthTimeout
	}
	failbackWarmup := config.FailbackWarmup
	if failbackWarmup == 0 {
		failbackWarmup = defaultFailbackWarmup
	}
	if healthCheckInterval <= 0 || healthCheckInterval > maxHealthInterval {
		return nil, errors.New("frontdoor: health check interval is outside bounds")
	}
	if healthCheckTimeout <= 0 || healthCheckTimeout > maxHealthTimeout ||
		healthCheckTimeout > healthCheckInterval {
		return nil, errors.New("frontdoor: health check timeout is outside bounds")
	}
	if failbackWarmup < 0 || failbackWarmup > maxFailbackWarmup {
		return nil, errors.New("frontdoor: failback warmup is outside bounds")
	}

	targetA, err := parseTarget(config.DCATarget)
	if err != nil {
		return nil, fmt.Errorf("frontdoor: validating dc-a target: %w", err)
	}
	targetB, err := parseTarget(config.DCBTarget)
	if err != nil {
		return nil, fmt.Errorf("frontdoor: validating dc-b target: %w", err)
	}
	if targetA.String() == targetB.String() {
		return nil, errors.New("frontdoor: data-center targets must be distinct")
	}

	log := config.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	meterProvider := config.MeterProvider
	if meterProvider == nil {
		meterProvider = noop.NewMeterProvider()
	}
	instruments, err := newInstrumentation(meterProvider)
	if err != nil {
		return nil, err
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	transport := config.Transport
	if transport == nil {
		defaultTransport := http.DefaultTransport.(*http.Transport).Clone()
		defaultTransport.Proxy = nil
		defaultTransport.DisableCompression = true
		transport = defaultTransport
	}

	frontDoor := &FrontDoor{
		healthCheckInterval: healthCheckInterval,
		healthCheckTimeout:  healthCheckTimeout,
		failbackWarmup:      failbackWarmup,
		healthClient: &http.Client{
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now:             now,
		log:             log,
		instrumentation: instruments,
	}
	frontDoor.backends = []*backend{
		{dataCenter: dataCenterA, target: targetA},
		{dataCenter: dataCenterB, target: targetB},
	}
	for _, candidate := range frontDoor.backends {
		candidate.proxy = frontDoor.newProxy(candidate, transport)
	}

	return frontDoor, nil
}

// Handler returns the finite public HTTP surface of the local front door.
func (frontDoor *FrontDoor) Handler() http.Handler {
	return http.HandlerFunc(frontDoor.serveHTTP)
}

// Run performs an immediate health pass and then checks both DCs at a bounded
// interval until the context is cancelled.
func (frontDoor *FrontDoor) Run(ctx context.Context) error {
	frontDoor.Check(ctx)
	ticker := time.NewTicker(frontDoor.healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			frontDoor.Check(ctx)
		}
	}
}

// Check probes both fixed targets concurrently and atomically updates their
// individual health state.
func (frontDoor *FrontDoor) Check(ctx context.Context) {
	var waitGroup sync.WaitGroup
	for _, candidate := range frontDoor.backends {
		waitGroup.Go(func() {
			frontDoor.checkBackend(ctx, candidate)
		})
	}
	waitGroup.Wait()
}

func (frontDoor *FrontDoor) serveHTTP(response http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/livez":
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", http.MethodGet)
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		response.WriteHeader(http.StatusOK)
		return
	case "/readyz":
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", http.MethodGet)
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !frontDoor.ready() {
			http.Error(response, "not ready", http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusOK)
		return
	case e2ev1connect.FakeInternalServiceReadProcedure,
		e2ev1connect.FakeInternalServiceMutateProcedure:
		if request.Method != http.MethodPost {
			response.Header().Set("Allow", http.MethodPost)
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
	default:
		http.NotFound(response, request)
		return
	}

	selected := frontDoor.selectBackend(request.Context(), routeLabel(request.URL.Path))
	if selected == nil {
		http.Error(response, "no healthy data center", http.StatusServiceUnavailable)
		return
	}
	selected.proxy.ServeHTTP(response, request)
}

func (frontDoor *FrontDoor) ready() bool {
	frontDoor.mu.Lock()
	defer frontDoor.mu.Unlock()

	for _, candidate := range frontDoor.backends {
		if candidate.healthy {
			return true
		}
	}
	return false
}

func (frontDoor *FrontDoor) selectBackend(ctx context.Context, route string) *backend {
	frontDoor.mu.Lock()
	defer frontDoor.mu.Unlock()

	now := frontDoor.now()
	var selected *backend
	var totalWeight int64
	for _, candidate := range frontDoor.backends {
		if !candidate.healthy {
			continue
		}
		weight := frontDoor.weightAt(candidate, now)
		candidate.currentWeight += weight
		totalWeight += weight
		if selected == nil || candidate.currentWeight > selected.currentWeight {
			selected = candidate
		}
	}
	if selected == nil {
		frontDoor.instrumentation.recordSelection(ctx, "none", route, "unavailable")
		return nil
	}
	selected.currentWeight -= totalWeight
	frontDoor.instrumentation.recordSelection(ctx, selected.dataCenter, route, "selected")
	return selected
}

func (frontDoor *FrontDoor) weightAt(candidate *backend, now time.Time) int64 {
	if frontDoor.failbackWarmup == 0 || candidate.recoveredAt.IsZero() {
		return maximumBackendWeight
	}
	elapsed := now.Sub(candidate.recoveredAt)
	if elapsed <= 0 {
		return minimumBackendWeight
	}
	if elapsed >= frontDoor.failbackWarmup {
		return maximumBackendWeight
	}
	weightRange := maximumBackendWeight - minimumBackendWeight
	return minimumBackendWeight + weightRange*elapsed.Nanoseconds()/frontDoor.failbackWarmup.Nanoseconds()
}

func (frontDoor *FrontDoor) checkBackend(ctx context.Context, candidate *backend) {
	checkContext, cancel := context.WithTimeout(ctx, frontDoor.healthCheckTimeout)
	defer cancel()

	healthURL := candidate.target.JoinPath("readyz")
	request, err := http.NewRequestWithContext(checkContext, http.MethodGet, healthURL.String(), http.NoBody)
	if err != nil {
		frontDoor.setHealth(ctx, candidate, false)
		return
	}
	response, err := frontDoor.healthClient.Do(request)
	healthy := err == nil && response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices
	if response != nil {
		_, _ = io.CopyN(io.Discard, response.Body, maxHealthBodyBytes)
		_ = response.Body.Close()
	}
	frontDoor.setHealth(ctx, candidate, healthy)
}

func (frontDoor *FrontDoor) setHealth(ctx context.Context, candidate *backend, healthy bool) {
	frontDoor.mu.Lock()
	changed := candidate.healthy != healthy
	if changed {
		candidate.healthy = healthy
		candidate.currentWeight = 0
		if healthy {
			candidate.recoveredAt = frontDoor.now()
		} else {
			candidate.recoveredAt = time.Time{}
		}
		for _, other := range frontDoor.backends {
			other.currentWeight = 0
		}
	}
	frontDoor.mu.Unlock()

	status := "not_ready"
	if healthy {
		status = "ready"
	}
	frontDoor.instrumentation.recordHealthCheck(ctx, candidate.dataCenter, status)
	if changed {
		frontDoor.log.InfoContext(
			ctx,
			"изменилось состояние front door backend",
			slog.String("data_center", candidate.dataCenter),
			slog.String("status", status),
		)
	}
}

func (frontDoor *FrontDoor) newProxy(
	candidate *backend,
	transport http.RoundTripper,
) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(proxyRequest *httputil.ProxyRequest) {
			proxyRequest.SetURL(candidate.target)
			proxyRequest.Out.Host = candidate.target.Host
			proxyRequest.Out.Header.Del("Forwarded")
			proxyRequest.Out.Header.Del("X-Forwarded-For")
			proxyRequest.Out.Header.Del("X-Forwarded-Host")
			proxyRequest.Out.Header.Del("X-Forwarded-Proto")
		},
		ErrorHandler: func(response http.ResponseWriter, request *http.Request, _ error) {
			frontDoor.instrumentation.recordSelection(
				request.Context(),
				candidate.dataCenter,
				routeLabel(request.URL.Path),
				"upstream_error",
			)
			http.Error(response, "upstream unavailable", http.StatusBadGateway)
		},
	}
}

func parseTarget(rawTarget string) (*url.URL, error) {
	target, err := url.Parse(rawTarget)
	if err != nil {
		return nil, errors.New("invalid URL")
	}
	if target.Scheme != "http" || target.User != nil || target.RawQuery != "" ||
		target.ForceQuery || target.Fragment != "" || target.RawFragment != "" ||
		target.RawPath != "" || target.Opaque != "" {
		return nil, errors.New("target must be a plain HTTP URL")
	}
	if target.Path != "" && target.Path != "/" {
		return nil, errors.New("target path must be empty")
	}
	host, portText, err := net.SplitHostPort(target.Host)
	if err != nil {
		return nil, errors.New("target must contain a literal IP and port")
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return nil, errors.New("target host must be a literal IP")
	}
	address = address.Unmap()
	if !address.IsLoopback() && !address.IsPrivate() {
		return nil, errors.New("target IP must be loopback or private")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1024 || port > 65535 {
		return nil, errors.New("target port is outside bounds")
	}
	target.Host = net.JoinHostPort(address.String(), strconv.Itoa(port))
	target.Path = ""
	return target, nil
}

func routeLabel(path string) string {
	switch path {
	case e2ev1connect.FakeInternalServiceReadProcedure:
		return "read"
	case e2ev1connect.FakeInternalServiceMutateProcedure:
		return "mutate"
	default:
		return "unknown"
	}
}

func newInstrumentation(provider metric.MeterProvider) (*instrumentation, error) {
	meter := provider.Meter("marketmesh/e2e/tunnel/frontdoor")
	selections, selectionErr := meter.Int64Counter(
		"marketmesh.e2e.frontdoor.selections",
		metric.WithDescription("Health-aware front door selection results."),
	)
	healthChecks, healthErr := meter.Int64Counter(
		"marketmesh.e2e.frontdoor.health_checks",
		metric.WithDescription("Bounded health check results by data center."),
	)
	if err := errors.Join(selectionErr, healthErr); err != nil {
		return nil, fmt.Errorf("frontdoor: creating metrics: %w", err)
	}
	return &instrumentation{selections: selections, healthChecks: healthChecks}, nil
}

func (instruments *instrumentation) recordSelection(
	ctx context.Context,
	dataCenter string,
	route string,
	status string,
) {
	instruments.selections.Add(ctx, 1, metric.WithAttributes(
		attribute.String("data_center", dataCenter),
		attribute.String("route", route),
		attribute.String("status", status),
	))
}

func (instruments *instrumentation) recordHealthCheck(
	ctx context.Context,
	dataCenter string,
	status string,
) {
	instruments.healthChecks.Add(ctx, 1, metric.WithAttributes(
		attribute.String("data_center", dataCenter),
		attribute.String("route", "health"),
		attribute.String("status", status),
	))
}
