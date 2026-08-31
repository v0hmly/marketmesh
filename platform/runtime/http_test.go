package runtime

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewHTTPServerRequiresLimits(t *testing.T) {
	t.Parallel()

	valid := validHTTPServerConfig()
	tests := []struct {
		name   string
		mutate func(*HTTPServerConfig)
	}{
		{name: "read header timeout", mutate: func(config *HTTPServerConfig) { config.ReadHeaderTimeout = 0 }},
		{name: "read timeout", mutate: func(config *HTTPServerConfig) { config.ReadTimeout = 0 }},
		{name: "write timeout", mutate: func(config *HTTPServerConfig) { config.WriteTimeout = 0 }},
		{name: "idle timeout", mutate: func(config *HTTPServerConfig) { config.IdleTimeout = 0 }},
		{name: "header bytes", mutate: func(config *HTTPServerConfig) { config.MaxHeaderBytes = 0 }},
		{name: "body bytes", mutate: func(config *HTTPServerConfig) { config.MaxBodyBytes = 0 }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			config := valid
			test.mutate(&config)
			if _, err := NewHTTPServer(config, http.NotFoundHandler()); err == nil {
				t.Fatal("NewHTTPServer() error = nil, want validation error")
			}
		})
	}
}

func TestNewHTTPServerLimitsRequestBody(t *testing.T) {
	t.Parallel()

	config := validHTTPServerConfig()
	config.MaxBodyBytes = 4
	server, err := NewHTTPServer(config, http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		_, readErr := io.ReadAll(request.Body)
		var maxBytesErr *http.MaxBytesError
		if !errors.As(readErr, &maxBytesErr) {
			http.Error(response, "body was not limited", http.StatusInternalServerError)
			return
		}
		response.WriteHeader(http.StatusRequestEntityTooLarge)
	}))
	if err != nil {
		t.Fatalf("NewHTTPServer() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345"))
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.Code)
	}
}

func validHTTPServerConfig() HTTPServerConfig {
	return HTTPServerConfig{
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       2 * time.Second,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 * 1024,
		MaxBodyBytes:      1024,
	}
}
