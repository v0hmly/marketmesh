package e2esnapshot

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
)

// SnapshotFunc reads one instantaneous defensive copy from the local registry.
// Implementations must not log or persist the returned opaque instance IDs.
type SnapshotFunc func(context.Context) (Snapshot, error)

// NewHandler creates the loopback-only E2E endpoint. The gateway-in app must
// register it only when its explicit E2E switch is enabled.
func NewHandler(snapshot SnapshotFunc) (http.Handler, error) {
	if snapshot == nil {
		return nil, errors.New("e2esnapshot: snapshot function is required")
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		serve(response, request, snapshot)
	}), nil
}

func serve(response http.ResponseWriter, request *http.Request, snapshot SnapshotFunc) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")

	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if request.URL.Path != Path || request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	if request.ContentLength != 0 || len(request.TransferEncoding) != 0 {
		http.Error(response, "request body is not allowed", http.StatusBadRequest)
		return
	}
	if !isLoopbackRemoteAddress(request.RemoteAddr) {
		http.Error(response, "forbidden", http.StatusForbidden)
		return
	}

	current, err := snapshot(request.Context())
	if err != nil {
		http.Error(response, "snapshot unavailable", http.StatusServiceUnavailable)
		return
	}
	var document bytes.Buffer
	if err := Encode(&document, current); err != nil {
		http.Error(response, "snapshot unavailable", http.StatusServiceUnavailable)
		return
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(document.Bytes())
}

func isLoopbackRemoteAddress(remoteAddress string) bool {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		return false
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
