package networkchaos

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testReadyURLA = "http://127.0.0.1:30080/readyz"
	testReadyURLB = "http://10.36.2.10:30080/readyz"
)

type roundTripFunc func(request *http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestNewHTTPCapacitySourceRejectsUnsafeOrAmbiguousConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config HTTPCapacityConfig
	}{
		{name: "missing URL", config: HTTPCapacityConfig{ReadinessURLs: []string{testReadyURLA}}},
		{name: "DNS host", config: httpCapacityConfig("http://backend-a:30080/readyz", testReadyURLB)},
		{name: "public IP", config: httpCapacityConfig("http://203.0.113.1:30080/readyz", testReadyURLB)},
		{name: "TLS", config: httpCapacityConfig("https://127.0.0.1:30080/readyz", testReadyURLB)},
		{name: "privileged port", config: httpCapacityConfig("http://127.0.0.1:80/readyz", testReadyURLB)},
		{name: "missing port", config: httpCapacityConfig("http://127.0.0.1/readyz", testReadyURLB)},
		{name: "wrong path", config: httpCapacityConfig("http://127.0.0.1:30080/livez", testReadyURLB)},
		{name: "encoded path", config: httpCapacityConfig("http://127.0.0.1:30080/%72eadyz", testReadyURLB)},
		{name: "empty query", config: httpCapacityConfig(testReadyURLA+"?", testReadyURLB)},
		{name: "userinfo", config: httpCapacityConfig("http://user@127.0.0.1:30080/readyz", testReadyURLB)},
		{name: "same URL", config: httpCapacityConfig(testReadyURLA, testReadyURLA)},
		{name: "negative timeout", config: HTTPCapacityConfig{
			ReadinessURLs: []string{testReadyURLA, testReadyURLB},
			Timeout:       -time.Second,
		}},
		{name: "long timeout", config: HTTPCapacityConfig{
			ReadinessURLs: []string{testReadyURLA, testReadyURLB},
			Timeout:       maxReadyTimeout + time.Nanosecond,
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewHTTPCapacitySource(test.config); err == nil {
				t.Fatal("NewHTTPCapacitySource() error = nil, want validation error")
			}
		})
	}
}

func TestHTTPCapacitySourceDefaultTransportIgnoresAmbientProxy(t *testing.T) {
	t.Parallel()

	source := newHTTPCapacitySource(t, httpCapacityConfig(testReadyURLA, testReadyURLB))
	transport, ok := source.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", source.client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("default transport unexpectedly uses ambient proxy configuration")
	}
	if !transport.DisableCompression {
		t.Fatal("default transport unexpectedly accepts compressed response bodies")
	}
	if transport.MaxResponseHeaderBytes != maxReadyHeaderBytes {
		t.Fatalf(
			"maximum response header = %d, want %d",
			transport.MaxResponseHeaderBytes,
			maxReadyHeaderBytes,
		)
	}
	if err := source.client.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect policy error = %v, want http.ErrUseLastResponse", err)
	}
}

func TestHTTPCapacitySourceProbesBothDataCentersConcurrently(t *testing.T) {
	t.Parallel()

	allStarted := make(chan struct{})
	var started atomic.Int32
	var release sync.Once
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if started.Add(1) == readyEndpointCount {
			release.Do(func() { close(allStarted) })
		}

		select {
		case <-request.Context().Done():
			return nil, request.Context().Err()
		case <-allStarted:
			return readyResponse(http.StatusOK, "ready"), nil
		}
	})
	config := httpCapacityConfig(testReadyURLA, testReadyURLB)
	config.Timeout = 100 * time.Millisecond
	config.Transport = transport
	source := newHTTPCapacitySource(t, config)

	ready, err := source.Ready(t.Context())
	if err != nil {
		t.Fatalf("Ready() error = %v", err)
	}
	if ready != readyEndpointCount || started.Load() != readyEndpointCount {
		t.Fatalf("Ready() = %d, probes = %d, want 2 concurrent probes", ready, started.Load())
	}
}

func TestHTTPCapacitySourceCountsOnlyHTTP200(t *testing.T) {
	t.Parallel()

	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		status := http.StatusServiceUnavailable
		if request.URL.Host == "127.0.0.1:30080" {
			status = http.StatusOK
		}
		return readyResponse(status, "bounded"), nil
	})
	config := httpCapacityConfig(testReadyURLA, testReadyURLB)
	config.Transport = transport
	source := newHTTPCapacitySource(t, config)

	ready, err := source.Ready(t.Context())
	if err != nil {
		t.Fatalf("Ready() error = %v", err)
	}
	if ready != 1 {
		t.Fatalf("Ready() = %d, want 1", ready)
	}
}

func TestHTTPCapacitySourceFailsClosedOnProbeErrors(t *testing.T) {
	t.Parallel()

	transportErr := errors.New("transport unavailable")
	tests := []struct {
		name      string
		transport http.RoundTripper
		wantErr   string
	}{
		{
			name: "transport error",
			transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, transportErr
			}),
			wantErr: transportErr.Error(),
		},
		{
			name: "oversized response",
			transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return readyResponse(
					http.StatusOK,
					strings.Repeat("x", int(maxReadyResponseBody)+1),
				), nil
			}),
			wantErr: "response body exceeds",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := httpCapacityConfig(testReadyURLA, testReadyURLB)
			config.Transport = test.transport
			source := newHTTPCapacitySource(t, config)

			ready, err := source.Ready(t.Context())
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Ready() error = %v, want %q", err, test.wantErr)
			}
			if ready != 0 {
				t.Fatalf("Ready() = %d after probe errors, want 0", ready)
			}
		})
	}
}

func TestHTTPCapacitySourcePropagatesCancellation(t *testing.T) {
	t.Parallel()

	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	config := httpCapacityConfig(testReadyURLA, testReadyURLB)
	config.Transport = transport
	source := newHTTPCapacitySource(t, config)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	ready, err := source.Ready(ctx)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Ready() error = %v, want context cancellation", err)
	}
	if ready != 0 {
		t.Fatalf("Ready() = %d after cancellation, want 0", ready)
	}
}

func TestHTTPCapacitySourceZeroValueFailsClosed(t *testing.T) {
	t.Parallel()

	ready, err := (&HTTPCapacitySource{}).Ready(t.Context())
	if err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("Ready() error = %v, want uninitialized source error", err)
	}
	if ready != 0 {
		t.Fatalf("Ready() = %d for zero value, want 0", ready)
	}
}

func httpCapacityConfig(firstURL string, secondURL string) HTTPCapacityConfig {
	return HTTPCapacityConfig{ReadinessURLs: []string{firstURL, secondURL}}
}

func newHTTPCapacitySource(t *testing.T, config HTTPCapacityConfig) *HTTPCapacitySource {
	t.Helper()
	source, err := NewHTTPCapacitySource(config)
	if err != nil {
		t.Fatalf("NewHTTPCapacitySource() error = %v", err)
	}
	return source
}

func readyResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
