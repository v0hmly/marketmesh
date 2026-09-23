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

// failingDiagnosticsRunner отклоняет каждый subprocess, имитируя частично
// созданную VM (нет k3s, нет iptables-ответов, нет kubeconfig).
type failingDiagnosticsRunner struct{}

func (failingDiagnosticsRunner) Run(_ context.Context, command Command) (Result, error) {
	return Result{}, errors.New("machine is not fully provisioned: " + command.Program)
}

func TestInspectSurvivesPartiallyCreatedMachines(t *testing.T) {
	t.Parallel()

	config, err := NewConfig(t.TempDir(), "mm44-test")
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := New(config, failingDiagnosticsRunner{}, logger)
	manager.now = func() time.Time {
		return time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	}

	if err := manager.Inspect(t.Context()); err != nil {
		t.Fatalf("Inspect() error = %v, want best-effort success", err)
	}

	runs, err := os.ReadDir(config.DiagnosticsDir)
	if err != nil || len(runs) != 1 {
		t.Fatalf("diagnostics runs = %v, %v; want exactly one", runs, err)
	}
	runDir := filepath.Join(config.DiagnosticsDir, runs[0].Name())

	if _, err := os.Stat(filepath.Join(runDir, "summary.json")); err != nil {
		t.Errorf("summary.json missing: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(runDir, "orbctl-list.err")) // #nosec G304 -- test path
	if err != nil || !strings.Contains(string(contents), "not fully provisioned") {
		t.Errorf("orbctl-list.err = %q, %v; want recorded capture error", contents, err)
	}
	for _, cluster := range config.Clusters() {
		if _, err := os.Stat(filepath.Join(runDir, cluster.LogicalName)); err != nil {
			t.Errorf("cluster diagnostics dir %s missing: %v", cluster.LogicalName, err)
		}
	}
}
