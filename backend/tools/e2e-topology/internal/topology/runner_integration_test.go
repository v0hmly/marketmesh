//go:build integration

package topology

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecRunnerIntegration(t *testing.T) {
	directory := t.TempDir()
	script := filepath.Join(directory, "runner-fixture.sh")
	contents := []byte("#!/bin/sh\nprintf '%s' \"${KUBECONFIG:-missing}\"\n")
	if err := os.WriteFile(script, contents, 0o750); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("KUBECONFIG", "/unexpected/user-config")

	runner := NewExecRunner(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := runner.Run(t.Context(), Command{
		Program: script,
		Env:     []string{"KUBECONFIG=/owned/config"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.TrimSpace(result.Stdout) != "/owned/config" {
		t.Errorf("stdout = %q, want /owned/config", result.Stdout)
	}
}

func TestExecRunnerIntegrationTimeout(t *testing.T) {
	directory := t.TempDir()
	script := filepath.Join(directory, "runner-timeout-fixture.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 10\n"), 0o750); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	runner := NewExecRunner(slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := runner.Run(ctx, Command{Program: script})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want context deadline exceeded", err)
	}
}
