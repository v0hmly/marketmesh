package e2esnapshot

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerReturnsValidatedSnapshotOnlyToLoopback(t *testing.T) {
	t.Parallel()

	handler, err := NewHandler(func(ctx context.Context) (Snapshot, error) {
		if ctx == nil {
			t.Fatal("snapshot context is nil")
		}
		return validSnapshot(), nil
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, Path, http.NoBody)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" ||
		response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v", response.Header())
	}
	if _, err := Decode(response.Body); err != nil {
		t.Fatalf("Decode(response) error = %v", err)
	}
}

func TestHandlerFailsClosedWithoutLeakingSourceError(t *testing.T) {
	t.Parallel()

	const sensitive = "opaque-instance-do-not-leak"
	handler, err := NewHandler(func(context.Context) (Snapshot, error) {
		return Snapshot{}, errors.New(sensitive)
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, Path, http.NoBody)
	request.RemoteAddr = "[::1]:12345"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.Code)
	}
	if strings.Contains(response.Body.String(), sensitive) {
		t.Fatalf("response leaked source error: %q", response.Body.String())
	}
}

func TestHandlerRejectsUnsafeRequestsBeforeReadingSnapshot(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		method     string
		target     string
		remoteAddr string
		body       string
		wantStatus int
	}{
		{
			name: "non-loopback", method: http.MethodGet, target: Path,
			remoteAddr: "192.0.2.10:12345", wantStatus: http.StatusForbidden,
		},
		{
			name: "invalid remote address", method: http.MethodGet, target: Path,
			remoteAddr: "127.0.0.1", wantStatus: http.StatusForbidden,
		},
		{
			name: "unexpected query", method: http.MethodGet, target: Path + "?route=other",
			remoteAddr: "127.0.0.1:12345", wantStatus: http.StatusNotFound,
		},
		{
			name: "request body", method: http.MethodGet, target: Path,
			remoteAddr: "127.0.0.1:12345", body: "body", wantStatus: http.StatusBadRequest,
		},
		{
			name: "mutating method", method: http.MethodPost, target: Path,
			remoteAddr: "127.0.0.1:12345", wantStatus: http.StatusMethodNotAllowed,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			called := false
			handler, err := NewHandler(func(context.Context) (Snapshot, error) {
				called = true
				return validSnapshot(), nil
			})
			if err != nil {
				t.Fatalf("NewHandler() error = %v", err)
			}
			var body io.Reader = http.NoBody
			if testCase.body != "" {
				body = strings.NewReader(testCase.body)
			}
			request := httptest.NewRequest(testCase.method, testCase.target, body)
			request.RemoteAddr = testCase.remoteAddr
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, testCase.wantStatus)
			}
			if called {
				t.Fatal("snapshot function was called for an unsafe request")
			}
		})
	}
}

func TestNewHandlerRejectsMissingSource(t *testing.T) {
	t.Parallel()

	if _, err := NewHandler(nil); err == nil {
		t.Fatal("NewHandler(nil) error = nil")
	}
}
