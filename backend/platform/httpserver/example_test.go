package httpserver_test

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/v0hmly/marketmesh/platform/httpserver"
	"github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/platform/telemetry"
)

func ExampleNew() {
	log, _ := logger.New(logger.Config{
		Service:     "example",
		Version:     "dev",
		Environment: "test",
		Output:      io.Discard,
	})
	server, _ := httpserver.New(httpserver.Config{
		Handler:           http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		RequestTimeout:    10 * time.Second,
		MaxHeaderBytes:    64 * 1024,
		MaxBodyBytes:      1024 * 1024,
		Logger:            log,
		Telemetry:         telemetry.NewNoop(),
	})

	fmt.Println(server.ReadTimeout)
	// Output: 15s
}
