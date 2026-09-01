package networkchaos

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const (
	readyEndpointCount   = 2
	defaultReadyTimeout  = 250 * time.Millisecond
	maxReadyTimeout      = 5 * time.Second
	maxReadyResponseBody = int64(1024)
	maxReadyHeaderBytes  = int64(32 * 1024)
)

// HTTPCapacityConfig задаёт два фиксированных readiness URL независимых DC.
// Transport предназначен для тестов; nil использует transport без ambient
// proxy и compression.
type HTTPCapacityConfig struct {
	ReadinessURLs []string
	Timeout       time.Duration
	Transport     http.RoundTripper
}

// HTTPCapacitySource считает готовые DC по публичному GET /readyz контракту.
type HTTPCapacitySource struct {
	readinessURLs []*url.URL
	client        *http.Client
}

type readinessProbeResult struct {
	ready bool
	err   error
}

// NewHTTPCapacitySource проверяет все URL и bounded policy до первого запроса.
func NewHTTPCapacitySource(config HTTPCapacityConfig) (*HTTPCapacitySource, error) {
	if len(config.ReadinessURLs) != readyEndpointCount {
		return nil, fmt.Errorf(
			"networkchaos: HTTP capacity requires exactly %d readiness URLs",
			readyEndpointCount,
		)
	}

	timeout := config.Timeout
	if timeout == 0 {
		timeout = defaultReadyTimeout
	}
	if timeout <= 0 || timeout > maxReadyTimeout {
		return nil, fmt.Errorf(
			"networkchaos: HTTP readiness timeout must be between 1ns and %s",
			maxReadyTimeout,
		)
	}

	readinessURLs := make([]*url.URL, 0, readyEndpointCount)
	for endpointIndex, rawURL := range config.ReadinessURLs {
		readinessURL, err := parseReadinessURL(rawURL)
		if err != nil {
			return nil, fmt.Errorf(
				"networkchaos: validating readiness URL %d: %w",
				endpointIndex,
				err,
			)
		}
		readinessURLs = append(readinessURLs, readinessURL)
	}
	if readinessURLs[0].String() == readinessURLs[1].String() {
		return nil, errors.New("networkchaos: readiness URLs must be distinct")
	}

	transport := config.Transport
	if transport == nil {
		defaultTransport := http.DefaultTransport.(*http.Transport).Clone()
		defaultTransport.Proxy = nil
		defaultTransport.DisableCompression = true
		defaultTransport.MaxResponseHeaderBytes = maxReadyHeaderBytes
		transport = defaultTransport
	}

	return &HTTPCapacitySource{
		readinessURLs: readinessURLs,
		client: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Ready проверяет оба DC одновременно. Неуспешный HTTP status означает
// неготовую capacity, а transport или bounded-body ошибка прерывает gate.
func (source *HTTPCapacitySource) Ready(ctx context.Context) (uint, error) {
	if ctx == nil {
		return 0, errors.New("networkchaos: readiness context must not be nil")
	}
	if source == nil || source.client == nil ||
		len(source.readinessURLs) != readyEndpointCount {
		return 0, errors.New("networkchaos: HTTP capacity source is not initialized")
	}
	for _, readinessURL := range source.readinessURLs {
		if readinessURL == nil {
			return 0, errors.New("networkchaos: HTTP capacity source has an empty URL")
		}
	}

	results := make(chan readinessProbeResult, len(source.readinessURLs))
	var probes sync.WaitGroup
	for endpointIndex, readinessURL := range source.readinessURLs {
		probes.Go(func() {
			ready, err := source.probe(ctx, readinessURL)
			results <- readinessProbeResult{
				ready: ready,
				err:   wrapReadinessProbeError(endpointIndex, err),
			}
		})
	}
	probes.Wait()
	close(results)

	var ready uint
	probeErrors := make([]error, 0, len(source.readinessURLs))
	for result := range results {
		if result.ready {
			ready++
		}
		if result.err != nil {
			probeErrors = append(probeErrors, result.err)
		}
	}

	return ready, errors.Join(probeErrors...)
}

func (source *HTTPCapacitySource) probe(
	ctx context.Context,
	readinessURL *url.URL,
) (bool, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		readinessURL.String(),
		http.NoBody,
	)
	if err != nil {
		return false, fmt.Errorf("creating request: %w", err)
	}

	response, err := source.client.Do(request)
	if err != nil {
		return false, fmt.Errorf("sending request: %w", err)
	}

	readBytes, readErr := io.Copy(
		io.Discard,
		io.LimitReader(response.Body, maxReadyResponseBody+1),
	)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return false, fmt.Errorf(
			"discarding response body: %w",
			errors.Join(readErr, closeErr),
		)
	}
	if readBytes > maxReadyResponseBody {
		return false, fmt.Errorf(
			"response body exceeds %d bytes",
			maxReadyResponseBody,
		)
	}

	return response.StatusCode == http.StatusOK, nil
}

func parseReadinessURL(rawURL string) (*url.URL, error) {
	readinessURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("invalid URL")
	}
	if readinessURL.Scheme != "http" || readinessURL.User != nil ||
		readinessURL.RawQuery != "" || readinessURL.ForceQuery ||
		readinessURL.Fragment != "" || readinessURL.RawFragment != "" ||
		readinessURL.RawPath != "" || readinessURL.Opaque != "" {
		return nil, errors.New("readiness endpoint must be a plain HTTP URL")
	}
	if readinessURL.Path != "/readyz" {
		return nil, errors.New("readiness endpoint must use exact /readyz path")
	}

	host, portText, err := net.SplitHostPort(readinessURL.Host)
	if err != nil {
		return nil, errors.New("readiness endpoint must contain a literal IP and port")
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return nil, errors.New("readiness endpoint host must be a literal IP")
	}
	address = address.Unmap()
	if !address.IsLoopback() && !address.IsPrivate() {
		return nil, errors.New("readiness endpoint IP must be loopback or private")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1024 || port > 65535 {
		return nil, errors.New("readiness endpoint port is outside bounds")
	}

	readinessURL.Host = net.JoinHostPort(address.String(), strconv.Itoa(port))
	return readinessURL, nil
}

func wrapReadinessProbeError(endpointIndex int, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("probing readiness URL %d: %w", endpointIndex, err)
}
