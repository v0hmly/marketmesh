package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/v0hmly/marketmesh/services/auth/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := app.Run(ctx)
	stop()
	if err != nil {
		os.Exit(1)
	}
}
