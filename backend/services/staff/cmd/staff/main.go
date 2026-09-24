package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/v0hmly/marketmesh/services/staff/internal/app"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "health" {
		if health() != nil {
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, os.Getenv("STAFF_CONFIG_FILE")); err != nil {
		slog.Error("staff stopped", "error", err)
		os.Exit(1)
	}
}
