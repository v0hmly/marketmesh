//go:build integration && !windows

package probe

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"

	e2ev1 "github.com/v0hmly/marketmesh/api/gen/go/e2e/v1"
	"google.golang.org/grpc"
)

func TestRunnerStopsOnSIGTERM(t *testing.T) {
	if os.Getenv("MM31_SIGTERM_HELPER") == "1" {
		runSIGTERMHelper(t)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestRunnerStopsOnSIGTERM$")
	command.Env = append(os.Environ(), "MM31_SIGTERM_HELPER=1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe() error = %v", err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatalf("command.Start() error = %v", err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil || !command.ProcessState.Exited() {
			_ = command.Process.Kill()
		}
	})

	ready := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() && scanner.Text() == "ready" {
			ready <- nil
			return
		}
		if scanErr := scanner.Err(); scanErr != nil {
			ready <- scanErr
			return
		}
		ready <- fmt.Errorf("probe helper did not report readiness")
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("probe helper readiness timed out")
	}

	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Process.Signal(SIGTERM) error = %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("probe helper exit error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("probe helper did not stop after SIGTERM")
	}
}

func runSIGTERMHelper(t *testing.T) {
	t.Helper()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	runner, err := New(
		Config{
			RunTimeout: time.Minute, StopTimeout: time.Second,
			RequestTimeout: time.Second,
			Read:           StreamConfig{RPS: 10, Concurrency: 1, QueueCapacity: 2},
			RecordCapacity: 32, EventCapacity: 128,
		},
		invokerFunc(func(ctx context.Context, _ Request) Response {
			<-ctx.Done()
			return Response{Outcome: OutcomeCanceled}
		}),
		Dependencies{IDGenerator: &sequenceIDGenerator{}},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	fmt.Println("ready")
	snapshot, err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("Runner.Run() error = %v", err)
	}
	if !snapshot.IsComplete {
		t.Fatalf("Snapshot.IsComplete = false, reasons = %v", snapshot.IncompleteReasons)
	}
}

func TestLedgerCollectorFinalizationStopsOnSIGTERM(t *testing.T) {
	if os.Getenv("MM39_LEDGER_SIGTERM_HELPER") == "1" {
		runLedgerSIGTERMHelper(t)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestLedgerCollectorFinalizationStopsOnSIGTERM$")
	command.Env = append(os.Environ(), "MM39_LEDGER_SIGTERM_HELPER=1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe() error = %v", err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatalf("command.Start() error = %v", err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil || !command.ProcessState.Exited() {
			_ = command.Process.Kill()
		}
	})

	ready := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() && scanner.Text() == "ready" {
			ready <- nil
			return
		}
		if scanErr := scanner.Err(); scanErr != nil {
			ready <- scanErr
			return
		}
		ready <- fmt.Errorf("ledger helper did not report readiness")
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ledger helper readiness timed out")
	}

	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Process.Signal(SIGTERM) error = %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ledger helper exit error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ledger helper did not finalize after SIGTERM")
	}
}

func runLedgerSIGTERMHelper(t *testing.T) {
	t.Helper()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	collector, err := NewLedgerCollector([]LedgerSource{{
		DataCenter: DataCenterA,
		Client:     signalLedgerClient{},
	}}, 1)
	if err != nil {
		t.Fatalf("NewLedgerCollector() error = %v", err)
	}

	fmt.Println("ready")
	snapshot := collector.Collect(ctx)
	if snapshot.IsComplete {
		t.Fatal("LedgerCollector.Collect() complete after SIGTERM")
	}
	assertStrings(t, snapshot.IncompleteReasons, []string{"ledger_rpc_failed"})
}

type signalLedgerClient struct{}

func (signalLedgerClient) Ledger(
	ctx context.Context,
	_ *e2ev1.LedgerRequest,
	_ ...grpc.CallOption,
) (*e2ev1.LedgerResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
