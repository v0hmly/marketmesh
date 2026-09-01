package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/probe"
	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

func TestParseOptionsAcceptsExactBoundedInputs(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	inventory := writeTestFile(t, directory, "inventory.json")
	scenario := writeTestFile(t, directory, "scenario.json")
	kubectl := writeExecutableTestFile(t, directory, "kubectl")
	artifacts := filepath.Join(directory, "artifacts")
	result, err := parseOptions([]string{
		"--run-id=mm32-command",
		"--inventory=" + inventory,
		"--scenario=" + scenario,
		"--artifacts=" + artifacts,
		"--kubectl=" + kubectl,
		"--revision=abc123",
		"--timeout=15m",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if result.runID != "mm32-command" || result.timeout != maximumOverallTimeout ||
		result.artifactsRoot != artifacts {
		t.Fatalf("parseOptions() = %+v", result)
	}
}

func TestParseOptionsRejectsUnsafeInputs(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	inventory := writeTestFile(t, directory, "inventory.json")
	scenario := writeTestFile(t, directory, "scenario.json")
	kubectl := writeExecutableTestFile(t, directory, "kubectl")
	symlink := filepath.Join(directory, "inventory-link.json")
	if err := os.Symlink(inventory, symlink); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	base := []string{
		"--run-id=mm32-command",
		"--inventory=" + inventory,
		"--scenario=" + scenario,
		"--artifacts=" + filepath.Join(directory, "artifacts"),
		"--kubectl=" + kubectl,
		"--revision=abc123",
	}
	tests := map[string][]string{
		"timeout":  append(append([]string{}, base...), "--timeout=15m1ns"),
		"symlink":  append(append([]string{}, base[0]), append([]string{"--inventory=" + symlink}, base[2:]...)...),
		"revision": append(append([]string{}, base[:5]...), "--revision=bad/revision"),
	}
	for name, arguments := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseOptions(arguments, &bytes.Buffer{}); err == nil {
				t.Fatal("parseOptions() error = nil")
			}
		})
	}
}

func TestTrafficConfigCoversMaximumRun(t *testing.T) {
	t.Parallel()

	records, events, err := trafficUpperBounds(maximumOverallTimeout)
	if err != nil {
		t.Fatalf("trafficUpperBounds() error = %v", err)
	}
	if records != 45_002 || events != 135_041 {
		t.Fatalf("trafficUpperBounds() = (%d, %d)", records, events)
	}
	configuration, err := trafficConfig(maximumOverallTimeout)
	if err != nil {
		t.Fatalf("trafficConfig() error = %v", err)
	}
	if uint64(configuration.RecordCapacity) < records ||
		uint64(configuration.EventCapacity) < events ||
		configuration.Read.RPS != streamRPS || configuration.Mutating.RPS != streamRPS {
		t.Fatalf("trafficConfig() = %+v", configuration)
	}
	if _, _, err := trafficUpperBounds(maximumOverallTimeout + time.Nanosecond); err == nil {
		t.Fatal("trafficUpperBounds(over maximum) error = nil")
	}
}

func TestRuntimeSLOUsesExactScenarioRecovery(t *testing.T) {
	t.Parallel()

	file, err := os.Open(filepath.Join("..", "..", "podchaos", "testdata", "scenario.json"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	scenario, decodeErr := spec.DecodeScenario(file)
	closeErr := file.Close()
	if decodeErr != nil || closeErr != nil {
		t.Fatalf("scenario errors = %v, %v", decodeErr, closeErr)
	}
	requirement, timeout, err := runtimeSLO(scenario)
	if err != nil {
		t.Fatalf("runtimeSLO() error = %v", err)
	}
	if requirement != (probe.SteadyRequirement{ReadSuccesses: 5, MutatingSuccesses: 5}) ||
		timeout != 10*time.Second {
		t.Fatalf("runtimeSLO() = (%+v, %s)", requirement, timeout)
	}
}

func TestWaitForRunnerIsBounded(t *testing.T) {
	t.Parallel()

	done := make(chan runnerResult, 1)
	want := runnerResult{snapshot: probe.Snapshot{IsComplete: true}}
	done <- want
	got, err := waitForRunner(done, time.Second)
	if err != nil || !got.snapshot.IsComplete {
		t.Fatalf("waitForRunner(ready) = (%+v, %v)", got, err)
	}

	if got, err := waitForRunner(make(chan runnerResult), time.Millisecond); err == nil ||
		got.snapshot.IsComplete || len(got.snapshot.IncompleteReasons) != 1 {
		t.Fatalf("waitForRunner(timeout) = (%+v, %v)", got, err)
	}
	if _, err := waitForRunner(nil, time.Second); err == nil {
		t.Fatal("waitForRunner(nil) error = nil")
	}
}

func TestCapacityEvidenceCoversCompleteRun(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	evidence := capacityEvidence(probe.Snapshot{
		StartedAt: startedAt, FinishedOffset: time.Minute,
	}, 10*time.Second)
	if len(evidence) != 1 || evidence[0].StartedAt != startedAt.Add(10*time.Second) ||
		evidence[0].EndedAt != startedAt.Add(time.Minute) ||
		evidence[0].PhysicallyAvailableDC != 2 {
		t.Fatalf("capacityEvidence() = %+v", evidence)
	}
	if evidence := capacityEvidence(probe.Snapshot{}, 0); len(evidence) != 0 {
		t.Fatalf("capacityEvidence(empty) = %+v", evidence)
	}
	if evidence := capacityEvidence(probe.Snapshot{
		StartedAt: startedAt, FinishedOffset: 10 * time.Second,
	}, 10*time.Second); len(evidence) != 0 {
		t.Fatalf("capacityEvidence(no measured interval) = %+v", evidence)
	}
	if evidence := capacityEvidence(probe.Snapshot{
		StartedAt: startedAt, FinishedOffset: time.Minute,
	}, -time.Second); len(evidence) != 0 {
		t.Fatalf("capacityEvidence(negative warm-up) = %+v", evidence)
	}
}

func writeTestFile(t *testing.T, directory string, name string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func writeExecutableTestFile(t *testing.T, directory string, name string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("executable\n"), 0o700); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
