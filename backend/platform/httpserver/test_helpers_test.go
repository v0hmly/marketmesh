package httpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestRequest(
	t *testing.T,
	method string,
	target string,
	body io.Reader,
) *http.Request {
	t.Helper()

	return httptest.NewRequest(method, target, body)
}

func newTestRecorder() *httptest.ResponseRecorder {
	return httptest.NewRecorder()
}
