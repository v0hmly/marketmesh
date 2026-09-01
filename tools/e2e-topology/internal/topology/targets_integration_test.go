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

func TestTargetContractIntegrationExecRunner(t *testing.T) {
	root := t.TempDir()
	config, err := NewConfig(root, "mm38-it", "default")
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

	dockerPath := filepath.Join(root, "docker")
	dockerScript := fmt.Sprintf(`#!/bin/sh
set -eu
if [ "$1" != "--context" ] || [ "$2" != "default" ]; then exit 41; fi
shift 2
if [ "$1" = "container" ] && [ "$2" = "inspect" ]; then
  printf '%%s' '[{"Id":"%s","Name":"/%s","Image":"%s","Config":{"Image":"%s","Labels":{"%s":"%s"}},"State":{"Status":"running","Running":true,"StartedAt":"2026-09-01T10:00:00Z","FinishedAt":"0001-01-01T00:00:00Z"},"NetworkSettings":{"SandboxID":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","Networks":{"%s":{"NetworkID":"%s","EndpointID":"%s","Gateway":"172.28.10.1","IPAddress":"172.28.10.2","IPPrefixLen":24,"MacAddress":"%s"}}}}]'
  exit 0
fi
if [ "$1" = "network" ] && [ "$2" = "inspect" ]; then
  printf '%%s' '[{"Id":"%s","Name":"%s","Driver":"bridge","Scope":"local","Labels":{"%s":"%s","%s":"%s"},"IPAM":{"Config":[{"Subnet":"%s"}]},"Containers":{"%s":{"Name":"%s","EndpointID":"%s","MacAddress":"%s","IPv4Address":"172.28.10.2/24"}}}]'
  exit 0
fi
if [ "$1" = "exec" ] && [ "$3" = "readlink" ]; then
  printf 'net:[4026533001]\n'
  exit 0
fi
if [ "$1" = "exec" ] && [ "$3" = "ip" ]; then
  printf '%%s' '[{"ifindex":2,"ifname":"eth0","address":"%s","addr_info":[{"family":"inet","local":"172.28.10.2","prefixlen":24}]}]'
  exit 0
fi
exit 42
`,
		testContainerID,
		cluster.NodeName,
		testImageID,
		NodeImage,
		clusterLabelKey,
		cluster.Name,
		cluster.NetworkName,
		testNetworkID,
		testEndpointID,
		testMAC,
		testNetworkID,
		cluster.NetworkName,
		ownerLabelKey,
		TaskKey,
		instanceLabelKey,
		config.Instance,
		cluster.DockerSubnet,
		testContainerID,
		cluster.NodeName,
		testEndpointID,
		testMAC,
		testMAC,
	)
	writeExecutable(t, dockerPath, dockerScript)

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
