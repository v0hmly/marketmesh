// staff-local is a disposable test identity provider, never a production service.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/v0hmly/marketmesh/tools/staff-local/internal/provider"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func run(ctx context.Context) error {
	raw, err := os.ReadFile(os.Getenv("OIDC_CONFIG_FILE"))
	if err != nil {
		return errors.New("test IdP configuration missing")
	}
	var cfg provider.Config
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return errors.New("invalid test IdP configuration")
	}
	p, err := provider.New(cfg)
	if err != nil {
		return err
	}
	server := provider.Server(cfg, p.Handler())
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		case <-done:
		}
	}()
	err = server.ListenAndServeTLS(cfg.Cert, cfg.Key)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func main() {
	if len(os.Args) == 2 && os.Args[1] == "health" {
		if health() != nil {
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("test IdP stopped", "error", err)
		os.Exit(1)
	}
}
