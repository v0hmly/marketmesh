package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestObservedResponseWriterPreservesStatusAndUnwrap(t *testing.T) {
	t.Parallel()

	response := httptest.NewRecorder()
	observed := newObservedResponseWriter(response)
	observed.WriteHeader(http.StatusEarlyHints)
	observed.WriteHeader(http.StatusAccepted)
	observed.WriteHeader(http.StatusInternalServerError)

	if observed.Status() != http.StatusAccepted {
		t.Fatalf("Status() = %d, want 202", observed.Status())
	}
	if observed.Unwrap() != response {
		t.Fatal("Unwrap() did not return original ResponseWriter")
	}
}

func TestObservedResponseWriterFlushesDefaultStatus(t *testing.T) {
	t.Parallel()

	response := httptest.NewRecorder()
	observed := newObservedResponseWriter(response)
	observed.Flush()

	if observed.Status() != http.StatusOK || response.Code != http.StatusOK {
		t.Fatalf("flushed statuses = %d/%d, want 200/200", observed.Status(), response.Code)
	}
	if !response.Flushed {
		t.Fatal("underlying ResponseWriter was not flushed")
	}
}
