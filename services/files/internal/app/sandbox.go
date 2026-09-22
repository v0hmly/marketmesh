package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/v0hmly/marketmesh/services/files/internal/adapter/in/sandbox"
)

// RunSandbox owns only a private Unix socket and disposable conversion files.
func RunSandbox() error {
	started := time.Now()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	handler, err := sandbox.New("/work")
	if err != nil {
		return err
	}
	const socket = "/run/files-sandbox/clean.sock"
	if previous, statErr := os.Lstat(socket); statErr == nil {
		if previous.Mode()&os.ModeSocket == 0 {
			return errors.New("sandbox: unexpected socket path")
		}
		if err = os.Remove(socket); err != nil {
			return errors.New("sandbox: stale socket cleanup")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("sandbox: socket unavailable")
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return errors.New("sandbox: listener unavailable")
	}
	defer listener.Close()
	if err = os.Chmod("/run/files-sandbox/clean.sock", 0600); err != nil {
		return errors.New("sandbox: socket permissions")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 7 * time.Minute, WriteTimeout: 7 * time.Minute, IdleTimeout: time.Second, MaxHeaderBytes: 4096, BaseContext: func(net.Listener) context.Context { return ctx }}
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	select {
	case err = <-stopped:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("sandbox: server unavailable")
	case <-ctx.Done():
	case <-handler.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = server.Shutdown(shutdown); err != nil {
		server.Close()
	}
	// Compose resets restart backoff after ten seconds. This single-use process
	// accepts no second job while waiting; Kubernetes should use one-shot pods.
	if remaining := time.Until(started.Add(11 * time.Second)); remaining > 0 {
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return nil
}
