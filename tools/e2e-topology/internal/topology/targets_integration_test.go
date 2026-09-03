//go:build integration

package topology

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTargetContractIntegrationExecRunner(t *testing.T) {
	root := t.TempDir()
	config, err := NewConfig(root, "mm44-it")
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	cluster, err := config.Cluster("dc-a", "dmz")
	if err != nil {
		t.Fatalf("Cluster() error = %v", err)
	}
	if err := os.MkdirAll(config.BinDir, 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	orbctlPath := filepath.Join(root, "orbctl")
	orbctlScript := fmt.Sprintf(`#!/bin/sh
set -eu
if [ "$1" = "info" ] && [ "$2" = "%[1]s" ]; then
  printf '%%s' '{"record":{"id":"%[2]s","name":"%[1]s","image":{"distro":"ubuntu","version":"noble","arch":"%[4]s","variant":"default"},"config":{"isolated":false,"default_username":"%[5]s"},"builtin":false,"state":"running"},"disk_size":683962368,"ip4":"%[3]s","ip6":"fd07:b51a:cc66:0:ac8c:31ff:fe6b:b491"}'
  exit 0
fi
if [ "$1" = "run" ] && [ "$2" = "-m" ] && [ "$3" = "%[1]s" ]; then
  shift 3
  if [ "$1" = "ip" ]; then
    printf '%%s' '[{"ifindex":1,"ifname":"lo","address":"00:00:00:00:00:00","addr_info":[{"family":"inet","local":"127.0.0.1","prefixlen":8}]},{"ifindex":2,"ifname":"%[6]s","address":"%[7]s","addr_info":[{"family":"inet","local":"%[3]s","prefixlen":24}]}]'
    exit 0
  fi
  if [ "$1" = "cat" ]; then
    printf '%%s\n' '%[8]s'
    exit 0
  fi
fi
exit 42
`,
		cluster.Name,
		testMachineID,
		testIPv4,
		runtime.GOARCH,
		testVMUser,
		testIface,
		testMAC,
		testBootID,
	)
	writeExecutable(t, orbctlPath, orbctlScript)

	kubectlScript := fmt.Sprintf(`#!/bin/sh
set -eu
printf '%%s' '{"metadata":{"name":"%s","uid":"%s","labels":{"marketmesh.dev/cluster":"%s","marketmesh.dev/dc":"%s","marketmesh.dev/owner-task":"%s","marketmesh.dev/topology-instance":"%s","marketmesh.dev/zone":"%s"}}}'
`, cluster.NodeName, testNodeUID, cluster.LogicalName, cluster.DC, TaskKey, config.Instance, cluster.Zone)
	writeExecutable(t, config.KubectlPath, kubectlScript)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := New(config, NewExecRunner(logger), logger)
	for run := 1; run <= 2; run++ {
		snapshot, resolveErr := manager.ResolveTargets(t.Context(), TargetResolveRequest{
			ConsumerTask:  "MM-38",
			ConsumerRunID: fmt.Sprintf("mm38-integration-%d", run),
			Selector:      TargetSelector{DC: "dc-a", Zone: "dmz"},
		})
		if resolveErr != nil {
			t.Fatalf("run %d ResolveTargets() error = %v", run, resolveErr)
		}
		if _, validateErr := manager.ValidateTargets(t.Context(), snapshot, TargetValidateRequest{
			ExpectedState: ExpectedStateRunning,
		}); validateErr != nil {
			t.Fatalf("run %d ValidateTargets() error = %v", run, validateErr)
		}
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o750); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}
