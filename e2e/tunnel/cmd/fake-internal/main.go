package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/fakeapp"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "prestop" {
		preStop()
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := fakeapp.Run(ctx)
	stop()
	if err != nil {
		os.Exit(1)
	}
}

func preStop() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"http://127.0.0.1:8080/drainz",
		http.NoBody,
	)
	if err == nil {
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr == nil {
			_ = response.Body.Close()
		}
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	<-timer.C
}
