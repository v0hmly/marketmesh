package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunTargetsRejectsPathSnapshotWithoutOutput(t *testing.T) {
	t.Parallel()

	stdout := &bytes.Buffer{}
	err := runTargets(
		context.Background(),
		nil,
		[]string{"validate", "--snapshot", "/tmp/snapshot.json", "--expected-state", "running"},
		commandStreams{stdin: strings.NewReader("{}"), stdout: stdout},
	)
	if err == nil {
		t.Fatal("runTargets() error = nil, want path input rejection")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty on validation failure", stdout.String())
	}
}

func TestRunTargetsRejectsInvalidConsumerWithoutOutput(t *testing.T) {
	t.Parallel()

	stdout := &bytes.Buffer{}
	err := runTargets(
		context.Background(),
		nil,
		[]string{"resolve", "--consumer-task", "foreign", "--consumer-run-id", "foreign-run"},
		commandStreams{stdout: stdout},
	)
	if err == nil {
		t.Fatal("runTargets() error = nil, want consumer identity rejection")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty on resolution failure", stdout.String())
	}
}

func TestRunTargetsRebindRejectsPathInputWithoutOutput(t *testing.T) {
	t.Parallel()

	stdout := &bytes.Buffer{}
	err := runTargets(
		context.Background(),
		nil,
		[]string{"rebind", "--transition", "/tmp/transition.json", "--target", "dc-a-dmz"},
		commandStreams{stdin: strings.NewReader("{}"), stdout: stdout},
	)
	if err == nil {
		t.Fatal("runTargets() error = nil, want transition path rejection")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty on rebind failure", stdout.String())
	}
}
