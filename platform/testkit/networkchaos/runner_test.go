package networkchaos

import (
	"context"
	"errors"
	"math"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

const testRunID = "mm36-20260901-a1b2c3d4"

var (
	testContainerRef = ResourceRef{
		ID:   strings.Repeat("a", 64),
		Name: testRunID + "-gateway-out",
	}
	testNetworkRef = ResourceRef{
		ID:   strings.Repeat("b", 64),
		Name: testRunID + "-dc-a-internal",
	}
	testPeerNetworkRef = ResourceRef{
		ID:   strings.Repeat("c", 64),
		Name: testRunID + "-dc-a-dmz",
	}
)

type fakeDriver struct {
	mu            sync.Mutex
	snapshot      Snapshot
	applyErrors   []error
	restoreErrors []error
	events        []string
	applyCount    int
	waitOnInspect bool
	nilRestore    bool
}

func (driver *fakeDriver) Inspect(ctx context.Context, _ Fault) (Snapshot, error) {
	driver.record("inspect")
	if driver.waitOnInspect {
		<-ctx.Done()
		return Snapshot{}, ctx.Err()
	}
	return driver.snapshot, nil
}

func (driver *fakeDriver) Apply(
	_ context.Context,
	_ Snapshot,
	_ Fault,
) (RestoreFunc, error) {
	driver.mu.Lock()
	index := driver.applyCount
	driver.applyCount++
	var applyErr error
	if index < len(driver.applyErrors) {
		applyErr = driver.applyErrors[index]
	}
	driver.events = append(driver.events, "apply")
	isNilRestore := driver.nilRestore
	driver.mu.Unlock()
	if isNilRestore {
		return nil, applyErr
	}

	return func(context.Context) error {
		driver.record("restore")
		if index < len(driver.restoreErrors) {
			return driver.restoreErrors[index]
		}
		return nil
	}, applyErr
}

func (driver *fakeDriver) record(event string) {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	driver.events = append(driver.events, event)
}

func (driver *fakeDriver) recordedEvents() []string {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	return append([]string{}, driver.events...)
}

type fakeCapacity struct {
	mu     sync.Mutex
	values []uint
	calls  int
	err    error
}

func (capacity *fakeCapacity) Ready(context.Context) (uint, error) {
	capacity.mu.Lock()
	defer capacity.mu.Unlock()
	if capacity.err != nil {
		return 0, capacity.err
	}
	index := capacity.calls
	capacity.calls++
	if index >= len(capacity.values) {
		index = len(capacity.values) - 1
	}
	return capacity.values[index], nil
}

type fakeDiagnostics struct {
	driver *fakeDriver
	mu     sync.Mutex
	points []DiagnosticPoint
	err    error
}

func (diagnostics *fakeDiagnostics) Capture(
	_ context.Context,
	point DiagnosticPoint,
) error {
	if diagnostics.driver != nil {
		diagnostics.driver.record("diagnostics:" + string(point.Phase))
	}
	diagnostics.mu.Lock()
	defer diagnostics.mu.Unlock()
	diagnostics.points = append(diagnostics.points, point)
	return diagnostics.err
}

type fakeWaiter struct {
	err error
}

func (waiter fakeWaiter) Wait(context.Context, time.Duration) error {
	return waiter.err
}

func TestRunnerRestoresFaultsAfterDiagnosticsAndRecovers(t *testing.T) {
	t.Parallel()

	driver := &fakeDriver{snapshot: validSnapshot()}
	diagnostics := &fakeDiagnostics{driver: driver}
	runner := newTestRunner(
		t,
		driver,
		&fakeCapacity{values: []uint{3, 2, 3}},
		diagnostics,
		fakeWaiter{},
	)

	err := runner.Run(context.Background(), Plan{
		Seed: 42,
		Steps: []Step{
			{
				Name: "partition-dc-a",
				Hold: time.Second,
				Faults: []Fault{
					validPartitionFault("partition-internal-dmz", 1),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wantEvents := []string{
		"inspect",
		"apply",
		"diagnostics:faulted",
		"restore",
		"diagnostics:recovered",
	}
	assertStrings(t, driver.recordedEvents(), wantEvents)
	if len(diagnostics.points) != 2 {
		t.Fatalf("diagnostic points = %d, want 2", len(diagnostics.points))
	}
	if diagnostics.points[0].Seed != 42 || diagnostics.points[0].StepIndex != 0 {
		t.Fatalf("first diagnostic point = %+v", diagnostics.points[0])
	}
	if !errors.Is(runner.Run(context.Background(), singleStepPlan()), ErrRunnerUsed) {
		t.Fatal("second Run() did not return ErrRunnerUsed")
	}
}

func TestRunnerRefusesFaultThatCanExhaustCapacity(t *testing.T) {
	t.Parallel()

	driver := &fakeDriver{snapshot: validSnapshot()}
	runner := newTestRunner(
		t,
		driver,
		&fakeCapacity{values: []uint{2}},
		&fakeDiagnostics{},
		fakeWaiter{},
	)
	secondFault := validPartitionFault("partition-b", 1)
	secondFault.Interface = "eth2"

	err := runner.Run(context.Background(), Plan{
		Steps: []Step{
			{
				Name: "unsafe-combined-faults",
				Hold: time.Second,
				Faults: []Fault{
					validPartitionFault("partition-a", 1),
					secondFault,
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "capacity loss beyond safe budget") {
		t.Fatalf("Run() error = %v, want safe capacity rejection", err)
	}
	if len(driver.recordedEvents()) != 0 {
		t.Fatalf("driver events = %v, want none", driver.recordedEvents())
	}
}

func TestRunnerRejectsResourceOutsideDisposableRunScope(t *testing.T) {
	t.Parallel()

	snapshot := validSnapshot()
	snapshot.Network.Resource.Labels[RunLabel] = "mm36-another-run-id"
	driver := &fakeDriver{snapshot: snapshot}
	runner := newTestRunner(
		t,
		driver,
		&fakeCapacity{values: []uint{3}},
		&fakeDiagnostics{driver: driver},
		fakeWaiter{},
	)

	err := runner.Run(context.Background(), singleStepPlan())
	if err == nil || !strings.Contains(err.Error(), "invalid run label") {
		t.Fatalf("Run() error = %v, want run scope rejection", err)
	}
	assertStrings(t, driver.recordedEvents(), []string{"inspect", "diagnostics:failed"})
}

func TestRunnerPreservesApplyDiagnosticsAndRestoreErrors(t *testing.T) {
	t.Parallel()

	applyErr := errors.New("partial tc failure")
	diagnosticsErr := errors.New("events unavailable")
	restoreErr := errors.New("qdisc cleanup failure")
	driver := &fakeDriver{
		snapshot:      validSnapshot(),
		applyErrors:   []error{applyErr},
		restoreErrors: []error{restoreErr},
	}
	runner := newTestRunner(
		t,
		driver,
		&fakeCapacity{values: []uint{3}},
		&fakeDiagnostics{driver: driver, err: diagnosticsErr},
		fakeWaiter{},
	)

	err := runner.Run(context.Background(), singleStepPlan())
	for _, expected := range []error{applyErr, diagnosticsErr, restoreErr} {
		if !errors.Is(err, expected) {
			t.Fatalf("Run() error = %v, want %v preserved", err, expected)
		}
	}
	assertStrings(
		t,
		driver.recordedEvents(),
		[]string{"inspect", "apply", "diagnostics:failed", "restore", "diagnostics:recovered"},
	)
}

func TestRunnerRestoresAfterParentCancellation(t *testing.T) {
	t.Parallel()

	driver := &fakeDriver{snapshot: validSnapshot()}
	parentErr := context.Canceled
	runner := newTestRunner(
		t,
		driver,
		&fakeCapacity{values: []uint{3, 2, 3}},
		&fakeDiagnostics{driver: driver},
		fakeWaiter{err: parentErr},
	)

	err := runner.Run(context.Background(), singleStepPlan())
	if !errors.Is(err, parentErr) {
		t.Fatalf("Run() error = %v, want context cancellation preserved", err)
	}
	assertStrings(
		t,
		driver.recordedEvents(),
		[]string{"inspect", "apply", "diagnostics:failed", "restore", "diagnostics:recovered"},
	)
}

func TestRunnerBoundsResourceInspection(t *testing.T) {
	t.Parallel()

	driver := &fakeDriver{
		snapshot:      validSnapshot(),
		waitOnInspect: true,
	}
	runner := newTestRunner(
		t,
		driver,
		&fakeCapacity{values: []uint{3}},
		&fakeDiagnostics{driver: driver},
		fakeWaiter{},
	)
	runner.config.OperationTimeout = 10 * time.Millisecond

	started := time.Now()
	err := runner.Run(context.Background(), singleStepPlan())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want inspection deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Run() elapsed = %s, want bounded inspection", elapsed)
	}
	assertStrings(t, driver.recordedEvents(), []string{"inspect", "diagnostics:failed"})
}

func TestRunnerFailsClosedWhenDriverOmitsRestore(t *testing.T) {
	t.Parallel()

	driver := &fakeDriver{
		snapshot:   validSnapshot(),
		nilRestore: true,
	}
	runner := newTestRunner(
		t,
		driver,
		&fakeCapacity{values: []uint{3}},
		&fakeDiagnostics{driver: driver},
		fakeWaiter{},
	)

	err := runner.Run(context.Background(), singleStepPlan())
	if err == nil || !strings.Contains(err.Error(), "driver returned nil restore") {
		t.Fatalf("Run() error = %v, want missing restore rejection", err)
	}
	assertStrings(
		t,
		driver.recordedEvents(),
		[]string{"inspect", "apply", "diagnostics:failed"},
	)
}

func TestRunnerFailsWhenSteadyStateDoesNotRecover(t *testing.T) {
	t.Parallel()

	driver := &fakeDriver{snapshot: validSnapshot()}
	runner := newTestRunner(
		t,
		driver,
		&fakeCapacity{values: []uint{3, 2, 0}},
		&fakeDiagnostics{driver: driver},
		fakeWaiter{},
	)
	runner.config.RecoveryTimeout = 10 * time.Millisecond
	runner.config.PollInterval = time.Millisecond

	err := runner.Run(context.Background(), singleStepPlan())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want recovery deadline", err)
	}
	assertStrings(
		t,
		driver.recordedEvents(),
		[]string{"inspect", "apply", "diagnostics:faulted", "restore"},
	)
}

func TestNewRejectsInvalidDependencies(t *testing.T) {
	t.Parallel()

	config := testConfig()
	driver := &fakeDriver{snapshot: validSnapshot()}
	capacity := &fakeCapacity{values: []uint{3}}
	diagnostics := &fakeDiagnostics{}
	tests := []struct {
		name        string
		driver      Driver
		capacity    CapacitySource
		diagnostics Diagnostics
		wantErr     string
	}{
		{
			name:        "missing driver",
			capacity:    capacity,
			diagnostics: diagnostics,
			wantErr:     "driver must not be nil",
		},
		{
			name:        "missing capacity",
			driver:      driver,
			diagnostics: diagnostics,
			wantErr:     "capacity source must not be nil",
		},
		{
			name:     "missing diagnostics",
			driver:   driver,
			capacity: capacity,
			wantErr:  "diagnostics must not be nil",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := New(
				config,
				test.driver,
				test.capacity,
				test.diagnostics,
			)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("New() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestTimerWaiterHonorsDurationAndCancellation(t *testing.T) {
	t.Parallel()

	waiter := timerWaiter{}
	if err := waiter.Wait(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waiter.Wait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want context cancellation", err)
	}
}

func TestValidateFault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*Fault)
		wantErr string
	}{
		{
			name: "valid partition",
		},
		{
			name: "missing peer",
			mutate: func(fault *Fault) {
				fault.PeerNetworks = nil
			},
			wantErr: "between 1 and",
		},
		{
			name: "broad interface",
			mutate: func(fault *Fault) {
				fault.Interface = "any"
			},
			wantErr: "explicit ethN",
		},
		{
			name: "partition mixed with degradation",
			mutate: func(fault *Fault) {
				fault.LossPercent = 1
			},
			wantErr: "must not contain degradation",
		},
		{
			name: "invalid immutable id",
			mutate: func(fault *Fault) {
				fault.Network.ID = "dc-a-network"
			},
			wantErr: "explicit Docker id",
		},
		{
			name: "unbounded fault name",
			mutate: func(fault *Fault) {
				fault.Name = "partition with spaces"
			},
			wantErr: "bounded lowercase slug",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fault := validPartitionFault("partition", 1)
			if test.mutate != nil {
				test.mutate(&fault)
			}
			err := validateFault(fault)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("validateFault() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateFault() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestValidateDegradation(t *testing.T) {
	t.Parallel()

	fault := Fault{
		Name:          "latency-jitter-loss-bandwidth",
		Kind:          KindDegradation,
		Container:     testContainerRef,
		Network:       testNetworkRef,
		Interface:     "eth1",
		Delay:         100 * time.Millisecond,
		Jitter:        20 * time.Millisecond,
		LossPercent:   2.5,
		BandwidthKbit: 1024,
	}
	if err := validateFault(fault); err != nil {
		t.Fatalf("validateFault() error = %v", err)
	}

	fault.LossPercent = 100
	if err := validateFault(fault); err == nil || !strings.Contains(err.Error(), "[0, 100)") {
		t.Fatalf("validateFault() error = %v, want total-loss rejection", err)
	}

	fault.LossPercent = math.NaN()
	if err := validateFault(fault); err == nil || !strings.Contains(err.Error(), "[0, 100)") {
		t.Fatalf("validateFault() error = %v, want nan rejection", err)
	}

	fault.LossPercent = 1
	fault.BandwidthKbit = maxBandwidthKbit + 1
	if err := validateFault(fault); err == nil || !strings.Contains(err.Error(), "bandwidth") {
		t.Fatalf("validateFault() error = %v, want bandwidth rejection", err)
	}

	fault.BandwidthKbit = 1024
	fault.Delay = maxNetworkDelay + time.Microsecond
	if err := validateFault(fault); err == nil || !strings.Contains(err.Error(), "must not exceed") {
		t.Fatalf("validateFault() error = %v, want delay rejection", err)
	}

	fault.Delay = time.Nanosecond
	fault.Jitter = 0
	if err := validateFault(fault); err == nil || !strings.Contains(err.Error(), "whole microseconds") {
		t.Fatalf("validateFault() error = %v, want precision rejection", err)
	}
}

func TestValidatePlanRejectsMultipleMutationsOfOneInterface(t *testing.T) {
	t.Parallel()

	secondFault := validPartitionFault("partition-second", 0)
	plan := singleStepPlan()
	plan.Steps[0].Faults = append(plan.Steps[0].Faults, secondFault)

	err := validatePlan(testConfig(), plan)
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("validatePlan() error = %v, want duplicate interface rejection", err)
	}
}

func TestValidateSnapshotRejectsPublicNetwork(t *testing.T) {
	t.Parallel()

	snapshot := validSnapshot()
	snapshot.PeerNetworks[0].Prefixes = []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}
	err := validateSnapshot(
		testRunID,
		validPartitionFault("partition", 1),
		snapshot,
	)
	if err == nil || !strings.Contains(err.Error(), "not a private test subnet") {
		t.Fatalf("validateSnapshot() error = %v, want public prefix rejection", err)
	}
}

func TestValidateSnapshotRejectsPrefixOutsidePrivateRange(t *testing.T) {
	t.Parallel()

	snapshot := validSnapshot()
	snapshot.Network.Prefixes = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/7")}
	err := validateSnapshot(
		testRunID,
		validPartitionFault("partition", 1),
		snapshot,
	)
	if err == nil || !strings.Contains(err.Error(), "not a private test subnet") {
		t.Fatalf("validateSnapshot() error = %v, want broad prefix rejection", err)
	}
}

func newTestRunner(
	t *testing.T,
	driver Driver,
	capacity CapacitySource,
	diagnostics Diagnostics,
	waiter waiter,
) *Runner {
	t.Helper()

	runner, err := newWithWaiter(
		testConfig(),
		driver,
		capacity,
		diagnostics,
		waiter,
	)
	if err != nil {
		t.Fatalf("NewWithWaiter() error = %v", err)
	}

	return runner
}

func testConfig() Config {
	return Config{
		RunID:              testRunID,
		OperationTimeout:   time.Second,
		DiagnosticsTimeout: time.Second,
		RestoreTimeout:     time.Second,
		RecoveryTimeout:    time.Second,
		PollInterval:       time.Millisecond,
		MaxStepDuration:    time.Hour,
		MinimumCapacity:    1,
	}
}

func singleStepPlan() Plan {
	return Plan{
		Seed: 42,
		Steps: []Step{
			{
				Name: "partition-dc-a",
				Hold: time.Second,
				Faults: []Fault{
					validPartitionFault("partition", 1),
				},
			},
		},
	}
}

func validPartitionFault(name string, capacityLoss uint) Fault {
	return Fault{
		Name:         name,
		Kind:         KindPartition,
		Container:    testContainerRef,
		Network:      testNetworkRef,
		PeerNetworks: []ResourceRef{testPeerNetworkRef},
		Interface:    "eth1",
		CapacityLoss: capacityLoss,
	}
}

func validSnapshot() Snapshot {
	labels := func() map[string]string {
		return map[string]string{
			TaskLabel:       TaskKey,
			RunLabel:        testRunID,
			DisposableLabel: "true",
		}
	}

	return Snapshot{
		Container: Resource{
			ID:     testContainerRef.ID,
			Name:   testContainerRef.Name,
			Labels: labels(),
		},
		Network: Network{
			Resource: Resource{
				ID:     testNetworkRef.ID,
				Name:   testNetworkRef.Name,
				Labels: labels(),
			},
			Prefixes: []netip.Prefix{netip.MustParsePrefix("10.36.1.0/24")},
		},
		PeerNetworks: []Network{
			{
				Resource: Resource{
					ID:     testPeerNetworkRef.ID,
					Name:   testPeerNetworkRef.Name,
					Labels: labels(),
				},
				Prefixes: []netip.Prefix{netip.MustParsePrefix("10.36.2.0/24")},
			},
		},
		Interface: "eth1",
	}
}

func assertStrings(t *testing.T, got, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("values = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("values = %v, want %v", got, want)
		}
	}
}
