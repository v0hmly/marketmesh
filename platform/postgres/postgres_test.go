package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReadinessDependenciesCheckBothPools(t *testing.T) {
	t.Parallel()

	rwErr := errors.New("rw unavailable")
	roErr := errors.New("ro unavailable")
	database := newTestDatabase(
		&fakePool{pingErr: rwErr},
		&fakePool{pingErr: roErr},
		time.Second,
	)
	dependencies := database.ReadinessDependencies()

	if len(dependencies) != 2 ||
		dependencies[0].Name != "postgres-rw" ||
		dependencies[1].Name != "postgres-ro" {
		t.Fatalf("ReadinessDependencies() = %+v", dependencies)
	}
	if err := dependencies[0].Check(t.Context()); !errors.Is(err, rwErr) {
		t.Fatalf("RW readiness error = %v, want preserved error", err)
	}
	if err := dependencies[1].Check(t.Context()); !errors.Is(err, roErr) {
		t.Fatalf("RO readiness error = %v, want preserved error", err)
	}

	dependencies[0].Name = "mutated"
	if fresh := database.ReadinessDependencies(); fresh[0].Name != "postgres-rw" {
		t.Fatal("ReadinessDependencies() returned shared mutable state")
	}
}

func TestComponentBlocksUntilCancellationAndCanOnlyBeCreatedOnce(t *testing.T) {
	t.Parallel()

	database := newTestDatabase(nil, nil, time.Second)
	component, err := database.Component("postgres")
	if err != nil {
		t.Fatalf("Component() error = %v", err)
	}
	if _, err := database.Component("duplicate"); err == nil {
		t.Fatal("second Component() error = nil")
	}

	ctx, cancel := context.WithCancel(t.Context())
	runResult := make(chan error, 1)
	go func() {
		runResult <- component.Run(ctx)
	}()

	select {
	case err := <-runResult:
		t.Fatalf("Component.Run() returned before cancellation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-runResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Component.Run() error = %v, want Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Component.Run() did not stop after cancellation")
	}
}

func TestCloseClosesPoolsOnceUnderConcurrency(t *testing.T) {
	t.Parallel()

	rw := &fakePool{}
	ro := &fakePool{}
	database := newTestDatabase(rw, ro, time.Second)

	var waitGroup sync.WaitGroup
	errorsChannel := make(chan error, 8)
	for range 8 {
		waitGroup.Go(func() {
			errorsChannel <- database.Close(t.Context())
		})
	}
	waitGroup.Wait()
	close(errorsChannel)

	for err := range errorsChannel {
		if err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}
	if rw.closeCalls.Load() != 1 || ro.closeCalls.Load() != 1 {
		t.Fatalf(
			"RW/RO close calls = %d/%d, want 1/1",
			rw.closeCalls.Load(),
			ro.closeCalls.Load(),
		)
	}
}

func TestCloseIsBoundedAndStillStartsBothPools(t *testing.T) {
	t.Parallel()

	releaseRW := make(chan struct{})
	rwStarted := make(chan struct{})
	rw := &fakePool{
		closeStarted: rwStarted,
		closeRelease: releaseRW,
	}
	ro := &fakePool{}
	database := newTestDatabase(rw, ro, time.Second)
	t.Cleanup(func() { close(releaseRW) })

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	err := database.Close(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v, want DeadlineExceeded", err)
	}
	select {
	case <-rwStarted:
	default:
		t.Fatal("RW close did not start")
	}
	if ro.closeCalls.Load() != 1 {
		t.Fatalf("RO close calls = %d, want 1", ro.closeCalls.Load())
	}
}

func TestOperationErrorHidesSensitiveDetailsAndPreservesCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("postgres://user:private-password@database/internal")
	err := wrapOperation("connect", roleRW, cause)
	if !errors.Is(err, cause) {
		t.Fatalf("wrapped error = %v, want preserved cause", err)
	}
	if strings.Contains(err.Error(), "private-password") ||
		strings.Contains(err.Error(), "database/internal") {
		t.Fatalf("wrapped error exposed sensitive details: %v", err)
	}
}

func TestNilDatabaseAccessorsAreSafe(t *testing.T) {
	t.Parallel()

	var database *Database
	if database.RW() != nil || database.RO() != nil {
		t.Fatal("nil database returned executor")
	}
	if dependencies := database.ReadinessDependencies(); len(dependencies) != 0 {
		t.Fatalf("nil readiness dependencies = %+v", dependencies)
	}
	if err := database.Close(t.Context()); err != nil {
		t.Fatalf("nil database Close() error = %v", err)
	}
}
