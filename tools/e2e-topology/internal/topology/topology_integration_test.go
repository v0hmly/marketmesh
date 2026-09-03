//go:build integration

package topology

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestFirewallToolchainIntegrationExecRunner проверяет через настоящий
// ExecRunner сценарий «iptables отсутствует в базовом образе → pinned install».
func TestFirewallToolchainIntegrationExecRunner(t *testing.T) {
	root := t.TempDir()
	config, err := NewConfig(root, "mm44-it")
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	cluster, err := config.Cluster("dc-a", "dmz")
	if err != nil {
		t.Fatalf("Cluster() error = %v", err)
	}
	marker := filepath.Join(root, "iptables-installed")

	orbctlScript := fmt.Sprintf(`#!/bin/sh
set -eu
# argv: run -m <vm> sudo -n <guest argv...>
if [ "$1" != "run" ] || [ "$2" != "-m" ] || [ "$3" != "%[1]s" ] || [ "$4" != "sudo" ] || [ "$5" != "-n" ]; then
  exit 42
fi
shift 5
if [ "$1" = "iptables" ] && [ "$2" = "--version" ]; then
  if [ -f "%[2]s" ]; then
    printf 'iptables v1.8.10 (nf_tables)\n'
    exit 0
  fi
  printf 'sudo: iptables: command not found\n' >&2
  exit 1
fi
if [ "$1" = "env" ] && [ "$2" = "DEBIAN_FRONTEND=noninteractive" ] && [ "$3" = "apt-get" ] && [ "$4" = "update" ]; then
  exit 0
fi
if [ "$1" = "env" ] && [ "$2" = "DEBIAN_FRONTEND=noninteractive" ] && [ "$3" = "apt-get" ] && [ "$4" = "install" ]; then
  if [ "$7" != "iptables=%[3]s" ]; then
    exit 43
  fi
  : > "%[2]s"
  exit 0
fi
exit 44
`, cluster.Name, marker, IPTablesVersion)
	writeExecutable(t, filepath.Join(root, "orbctl"), orbctlScript)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := New(config, NewExecRunner(logger), logger)
	if err := manager.ensureFirewallToolchain(t.Context(), cluster); err != nil {
		t.Fatalf("ensureFirewallToolchain() error = %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("pinned install marker missing: %v", err)
	}

	if err := os.Remove(marker); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if err := manager.ensureFirewallToolchain(t.Context(), cluster); err != nil {
		t.Fatalf("second ensureFirewallToolchain() error = %v", err)
	}
}
