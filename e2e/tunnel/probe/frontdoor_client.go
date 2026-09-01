package probe

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"

	e2ev1connect "github.com/v0hmly/marketmesh/api/gen/go/e2e/v1/e2ev1connect"
)

// FrontDoorInvoker is the constrained Connect adapter for the local MM-30
// front door. Call [FrontDoorInvoker.Close] after the runner stops so idle
// connections do not outlive the E2E process.
type FrontDoorInvoker struct {
	delegate *FakeInvoker
	client   *http.Client
}

// NewFrontDoorInvoker creates the constrained Connect adapter for the local
// MM-30 front door. The endpoint must be a literal loopback HTTP address with
// an unprivileged port and no path, query, credentials, or fragment.
//
// The client deliberately disables environment proxies, compression, and
// redirects. Request deadlines continue to come exclusively from [Runner].
func NewFrontDoorInvoker(
	endpoint string,
	directory InstanceDirectory,
) (*FrontDoorInvoker, error) {
	baseURL, err := validateFrontDoorEndpoint(endpoint)
	if err != nil {
		return nil, err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	trafficClient := e2ev1connect.NewFakeInternalServiceClient(
		client,
		baseURL.String(),
	)

	delegate, err := NewFakeInvoker(trafficClient, directory)
	if err != nil {
		return nil, err
	}

	return &FrontDoorInvoker{delegate: delegate, client: client}, nil
}

// Invoke performs exactly one front-door request through the MM-29 adapter.
func (invoker *FrontDoorInvoker) Invoke(ctx context.Context, request Request) Response {
	return invoker.delegate.Invoke(ctx, request)
}

// Close releases idle front-door connections. It is safe to call repeatedly
// and concurrently after or during request cancellation.
func (invoker *FrontDoorInvoker) Close() {
	invoker.client.CloseIdleConnections()
}

func validateFrontDoorEndpoint(endpoint string) (*url.URL, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("probe: invalid front door endpoint")
	}
	if parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" ||
		parsed.RawPath != "" || parsed.Opaque != "" {
		return nil, errors.New("probe: front door endpoint must be a plain HTTP URL")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.New("probe: front door endpoint path must be empty")
	}

	host, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		return nil, errors.New("probe: front door endpoint must contain a literal loopback IP and port")
	}
	address, err := netip.ParseAddr(host)
	if err != nil || !address.Unmap().IsLoopback() {
		return nil, errors.New("probe: front door endpoint must use a literal loopback IP")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1024 || port > 65535 {
		return nil, errors.New("probe: front door endpoint port is outside bounds")
	}

	parsed.Host = net.JoinHostPort(address.Unmap().String(), strconv.Itoa(port))
	parsed.Path = ""

	return parsed, nil
}
