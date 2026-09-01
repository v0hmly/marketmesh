package httpserver

import (
	"errors"
	"net/http"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

// NewHealthHandler создаёт HTTP liveness/readiness endpoints поверх
// transport-agnostic runtime.Health. Readiness возвращает только общий статус и
// никогда не раскрывает ошибку зависимости.
func NewHealthHandler(health *serviceruntime.Health) (http.Handler, error) {
	if health == nil {
		return nil, errors.New("httpserver: health must not be nil")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /readyz", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		if err := health.Ready(request.Context()); err != nil {
			http.Error(response, "not ready", http.StatusServiceUnavailable)
			return
		}

		response.WriteHeader(http.StatusNoContent)
	})

	return mux, nil
}
