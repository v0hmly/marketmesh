package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/rolling"
)

func TestParseOptionsRequiresExplicitSafeScenarioInputs(t *testing.T) {
	t.Parallel()

	digest := "registry.test/marketmesh/component@sha256:" + strings.Repeat("a", 64)
	configuration, err := parseOptions([]string{
		"--run-id=mm34-run",
		"--mode=a",
		"--inventory=/tmp/mm34-inventory.json",
		"--frontdoor=http://127.0.0.1:18080",
		"--artifacts=/tmp/mm34-artifacts",
		"--gateway-in-image=" + digest,
		"--gateway-out-image=" + digest,
		"--fake-internal-image=" + digest,
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if configuration.mode != "a" || configuration.runID != "mm34-run" {
		t.Fatalf("configuration = %#v", configuration)
	}

	tests := [][]string{
		{"--run-id=mm34-run", "--mode=unknown"},
		{
			"--run-id=mm34-run", "--mode=rollback",
			"--inventory=relative", "--frontdoor=http://127.0.0.1:18080",
			"--artifacts=/tmp/mm34-artifacts",
		},
		{
			"--run-id=mm34-run", "--mode=rollback",
			"--inventory=/tmp/mm34-inventory.json", "--frontdoor=http://127.0.0.1:18080",
			"--artifacts=/tmp/mm34-artifacts", "--ledger-limit=100001",
		},
	}
	for _, arguments := range tests {
		if _, err := parseOptions(arguments, &bytes.Buffer{}); err == nil {
			t.Fatalf("parseOptions(%q) error = nil", arguments)
		}
	}
}

func TestProbeConfigBoundsCompleteArtifactCapacity(t *testing.T) {
	t.Parallel()

	configuration := options{
		totalTimeout: 30 * time.Minute, stopTimeout: 20 * time.Second,
		requestTimeout: 5 * time.Second,
		readRPS:        5, mutatingRPS: 5,
		readConcurrency: 2, mutatingConcurrency: 2,
		steadyRead: 10, steadyMutating: 10,
	}
	result, err := probeConfig(configuration)
	if err != nil {
		t.Fatalf("probeConfig() error = %v", err)
	}
	if result.RecordCapacity != 18_006 || result.EventCapacity != 54_146 {
		t.Fatalf("probe capacities = %d/%d", result.RecordCapacity, result.EventCapacity)
	}

	configuration.totalTimeout = time.Hour
	if _, err := probeConfig(configuration); err == nil {
		t.Fatal("probeConfig() error = nil for oversized artifact plan")
	}
	configuration.totalTimeout = time.Minute
	configuration.steadyMutating = 0
	if _, err := probeConfig(configuration); err == nil {
		t.Fatal("probeConfig() error = nil for missing steady streak")
	}
}

func TestScenarioActionSelectsExactEmbeddedContract(t *testing.T) {
	t.Parallel()

	digest := "registry.test/marketmesh/component@sha256:" + strings.Repeat("a", 64)
	configuration := options{
		mode: "b", gatewayInImage: digest, gatewayOutImage: digest,
		fakeInternalImage:          digest,
		gatewayInImageRevision:     "gateway-in-v2",
		gatewayOutImageRevision:    "gateway-out-v2",
		fakeInternalImageRevision:  "fake-internal-v2",
		gatewayInConfigRevision:    "gateway-in-config-v2",
		gatewayOutConfigRevision:   "gateway-out-config-v2",
		fakeInternalConfigRevision: "fake-internal-config-v2",
	}
	scenario, action, err := scenarioAction(configuration, nil)
	if err != nil {
		t.Fatalf("scenarioAction() error = %v", err)
	}
	if scenario.ID != "rolling-update-mm34-b" || action == nil || len(scenario.Faults) != 12 {
		t.Fatalf("scenario = %#v, action nil = %v", scenario, action == nil)
	}

	configuration.mode = "rollback"
	scenario, action, err = scenarioAction(configuration, nil)
	if err != nil {
		t.Fatalf("rollback scenarioAction() error = %v", err)
	}
	if scenario.ID != "rolling-rollback-mm34" || action == nil ||
		len(scenario.Faults) != len(rolling.RollbackTargets()) {
		t.Fatalf("rollback scenario = %#v", scenario)
	}
}
