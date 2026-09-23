package probe

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunnerSeparatesStreamsAndRecordsMarker(t *testing.T) {
	t.Parallel()

	clock := newManualClock(t)
	requests := make(chan Request, 4)
	invoker := invokerFunc(func(_ context.Context, request Request) Response {
		requests <- request
		return Response{
			Outcome:    OutcomeSuccess,
			RouteID:    "route-a",
			DataCenter: "dc-a",
		}
	})
	config := defaultTestConfig()
	config.Mutating = StreamConfig{
		RPS:           10,
		Concurrency:   1,
		QueueCapacity: 2,
	}
	runner, err := New(config, invoker, Dependencies{
		Clock:       clock,
		IDGenerator: &sequenceIDGenerator{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan struct {
		snapshot Snapshot
		err      error
	}, 1)
	go func() {
		snapshot, runErr := runner.Run(ctx)
		result <- struct {
			snapshot Snapshot
			err      error
		}{snapshot: snapshot, err: runErr}
	}()

	first := waitValue(t, requests)
	second := waitValue(t, requests)
	if first.Class == second.Class {
		t.Fatalf("initial request classes = %q and %q, want separate streams", first.Class, second.Class)
	}
	for _, request := range []Request{first, second} {
		if request.Class == TrafficClassMutating && request.IdempotencyKey != request.ID {
			t.Fatalf("mutating idempotency key = %q, want request id %q", request.IdempotencyKey, request.ID)
		}
		if request.Class == TrafficClassRead && request.IdempotencyKey != "" {
			t.Fatalf("read idempotency key = %q, want empty", request.IdempotencyKey)
		}
	}

	if err := runner.Mark(Marker{
		FaultID:    "rolling_update",
		DataCenter: "dc-a",
		Zone:       "dmz",
		Component:  "gateway-in",
		Phase:      MarkerPhaseSteady,
		Result:     MarkerResultSuccess,
		Revision:   "rev-2",
	}); err != nil {
		t.Fatalf("Runner.Mark() error = %v", err)
	}
	cancel()

	runResult := waitValue(t, result)
	if runResult.err != nil {
		t.Fatalf("Runner.Run() error = %v", runResult.err)
	}
	if !runResult.snapshot.IsComplete {
		t.Fatalf("Snapshot.IsComplete = false, reasons = %v", runResult.snapshot.IncompleteReasons)
	}
	if len(runResult.snapshot.Records) != 2 {
		t.Fatalf("record count = %d, want 2", len(runResult.snapshot.Records))
	}

	markerCount := 0
	var previousOffset time.Duration
	for index, event := range runResult.snapshot.Events {
		if event.Sequence != uint64(index+1) {
			t.Fatalf("event sequence = %d, want %d", event.Sequence, index+1)
		}
		if event.Offset < previousOffset {
			t.Fatalf("event offset moved backwards: %s < %s", event.Offset, previousOffset)
		}
		previousOffset = event.Offset
		if event.Kind == EventKindMarker {
			markerCount++
		}
	}
	if firstEvent := runResult.snapshot.Events[0]; firstEvent.Kind != EventKindRunStarted {
		t.Fatalf("first event = %q, want %q", firstEvent.Kind, EventKindRunStarted)
	}
	lastEvent := runResult.snapshot.Events[len(runResult.snapshot.Events)-1]
	if lastEvent.Kind != EventKindRunFinished {
		t.Fatalf("last event = %q, want %q", lastEvent.Kind, EventKindRunFinished)
	}
	if markerCount != 1 {
		t.Fatalf("marker event count = %d, want 1", markerCount)
	}
	_, secondRunErr := runner.Run(context.Background())
	if !errors.Is(secondRunErr, ErrRunnerUsed) {
		t.Fatal("second Runner.Run() did not return ErrRunnerUsed")
	}
}

func TestRunnerRecordsBackpressureWithoutDispatch(t *testing.T) {
	t.Parallel()

	clock := newManualClock(t)
	started := make(chan Request, 1)
	invoker := invokerFunc(func(ctx context.Context, request Request) Response {
		started <- request
		<-ctx.Done()
		return Response{Outcome: OutcomeCanceled}
	})
	config := defaultTestConfig()
	config.Read.QueueCapacity = 1
	runner, err := New(config, invoker, Dependencies{
		Clock:       clock,
		IDGenerator: &sequenceIDGenerator{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan struct {
		snapshot Snapshot
		err      error
	}, 1)
	go func() {
		snapshot, runErr := runner.Run(ctx)
		result <- struct {
			snapshot Snapshot
			err      error
		}{snapshot: snapshot, err: runErr}
	}()
	waitValue(t, started)
	clock.waitForTickers(t, 1)
	clock.advance(t, 100*time.Millisecond)
	clock.advance(t, 100*time.Millisecond)
	waitForOutcome(t, runner, OutcomeBackpressure, 1)
	cancel()

	runResult := waitValue(t, result)
	if runResult.err != nil {
		t.Fatalf("Runner.Run() error = %v", runResult.err)
	}
	if !runResult.snapshot.IsComplete {
		t.Fatalf("Snapshot.IsComplete = false, reasons = %v", runResult.snapshot.IncompleteReasons)
	}
	if len(runResult.snapshot.Records) != 3 {
		t.Fatalf("record count = %d, want 3", len(runResult.snapshot.Records))
	}

	backpressure := 0
	dispatched := 0
	for _, record := range runResult.snapshot.Records {
		if record.Outcome == OutcomeBackpressure {
			backpressure++
			if record.Dispatched {
				t.Fatal("backpressure record was marked dispatched")
			}
		}
		if record.Dispatched {
			dispatched++
		}
	}
	if backpressure != 1 {
		t.Fatalf("backpressure count = %d, want 1", backpressure)
	}
	if dispatched != 1 {
		t.Fatalf("dispatched count = %d, want 1", dispatched)
	}
}

func TestRunnerFailsClosedWhenJournalCapacityIsExceeded(t *testing.T) {
	t.Parallel()

	clock := newManualClock(t)
	started := make(chan Request, 1)
	invoker := invokerFunc(func(ctx context.Context, request Request) Response {
		started <- request
		<-ctx.Done()
		return Response{Outcome: OutcomeCanceled}
	})
	config := defaultTestConfig()
	config.RecordCapacity = 1
	runner, err := New(config, invoker, Dependencies{
		Clock:       clock,
		IDGenerator: &sequenceIDGenerator{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result := make(chan struct {
		snapshot Snapshot
		err      error
	}, 1)
	go func() {
		snapshot, runErr := runner.Run(context.Background())
		result <- struct {
			snapshot Snapshot
			err      error
		}{snapshot: snapshot, err: runErr}
	}()
	waitValue(t, started)
	clock.waitForTickers(t, 1)
	clock.advance(t, 100*time.Millisecond)

	runResult := waitValue(t, result)
	if !errors.Is(runResult.err, ErrJournalCapacity) {
		t.Fatalf("Runner.Run() error = %v, want ErrJournalCapacity", runResult.err)
	}
	if !errors.Is(runResult.err, ErrIncompleteRun) {
		t.Fatalf("Runner.Run() error = %v, want ErrIncompleteRun", runResult.err)
	}
	if runResult.snapshot.IsComplete {
		t.Fatal("Snapshot.IsComplete = true after journal overflow")
	}
}

func TestRunnerFailsClosedAndKeepsDiagnosticsWhenInvokerPanics(t *testing.T) {
	t.Parallel()

	runner, err := New(
		defaultTestConfig(),
		invokerFunc(func(context.Context, Request) Response {
			panic("sensitive panic payload")
		}),
		Dependencies{IDGenerator: &sequenceIDGenerator{}},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	snapshot, err := runner.Run(context.Background())
	if !errors.Is(err, ErrIncompleteRun) {
		t.Fatalf("Runner.Run() error = %v, want ErrIncompleteRun", err)
	}
	if snapshot.IsComplete {
		t.Fatal("Snapshot.IsComplete = true after invoker panic")
	}
	assertStrings(t, snapshot.IncompleteReasons, []string{"invoker_panic"})
	if len(snapshot.Records) != 1 || snapshot.Records[0].Outcome != OutcomeInternalError {
		t.Fatalf("Snapshot.Records = %#v, want one internal error", snapshot.Records)
	}
}

func TestRunnerBoundsStopWhenInvokerIgnoresCancellation(t *testing.T) {
	t.Parallel()

	clock := newManualClock(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	invoker := invokerFunc(func(context.Context, Request) Response {
		started <- struct{}{}
		<-release
		return Response{
			Outcome:    OutcomeSuccess,
			RouteID:    "route-a",
			DataCenter: "dc-a",
		}
	})
	config := defaultTestConfig()
	config.StopTimeout = 10 * time.Millisecond
	runner, err := New(config, invoker, Dependencies{
		Clock:       clock,
		IDGenerator: &sequenceIDGenerator{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan struct {
		snapshot Snapshot
		err      error
	}, 1)
	go func() {
		snapshot, runErr := runner.Run(ctx)
		result <- struct {
			snapshot Snapshot
			err      error
		}{snapshot: snapshot, err: runErr}
	}()
	waitValue(t, started)
	clock.waitForTickers(t, 1)

	startedAt := time.Now()
	cancel()
	runResult := waitValue(t, result)
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("Runner.Run() stop elapsed = %s, want bounded stop", elapsed)
	}
	if !errors.Is(runResult.err, ErrStopTimeout) {
		t.Fatalf("Runner.Run() error = %v, want ErrStopTimeout", runResult.err)
	}
	if !errors.Is(runResult.err, ErrIncompleteRun) {
		t.Fatalf("Runner.Run() error = %v, want ErrIncompleteRun", runResult.err)
	}
	if runResult.snapshot.IsComplete {
		t.Fatal("Snapshot.IsComplete = true after stop timeout")
	}

	close(release)
	clock.waitForTickerStops(t, 1)
}

func TestRunnerWaitSteadyObservesExistingTrafficWithoutRetry(t *testing.T) {
	t.Parallel()

	requests := make(chan Request, 2)
	invoker := invokerFunc(func(_ context.Context, request Request) Response {
		requests <- request
		return Response{
			Outcome:    OutcomeSuccess,
			RouteID:    "route-a",
			DataCenter: DataCenterA,
		}
	})
	config := defaultTestConfig()
	config.Read.RPS = 1
	runner, err := New(
		config,
		invoker,
		Dependencies{IDGenerator: &sequenceIDGenerator{}},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() {
		_, runErr := runner.Run(ctx)
		runResult <- runErr
	}()
	waitValue(t, requests)

	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	state, err := runner.WaitSteady(waitCtx, SteadyRequirement{ReadSuccesses: 1})
	if err != nil {
		t.Fatalf("Runner.WaitSteady() error = %v", err)
	}
	if state.ReadSuccesses < 1 {
		t.Fatalf("SteadyState.ReadSuccesses = %d, want at least 1", state.ReadSuccesses)
	}
	select {
	case extraRequest := <-requests:
		t.Fatalf("WaitSteady triggered extra request %#v", extraRequest)
	default:
	}

	cancel()
	if err := waitValue(t, runResult); err != nil {
		t.Fatalf("Runner.Run() error = %v", err)
	}
}

func TestRunnerWaitSteadyCanWaitBeforeRunStarts(t *testing.T) {
	t.Parallel()

	requests := make(chan Request, 1)
	runner, err := New(
		defaultTestConfig(),
		invokerFunc(func(_ context.Context, request Request) Response {
			requests <- request
			return Response{
				Outcome:    OutcomeSuccess,
				RouteID:    "route-a",
				DataCenter: DataCenterA,
			}
		}),
		Dependencies{IDGenerator: &sequenceIDGenerator{}},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	steadyResult := make(chan error, 1)
	go func() {
		_, waitErr := runner.WaitSteady(
			waitCtx,
			SteadyRequirement{ReadSuccesses: 1},
		)
		steadyResult <- waitErr
	}()

	runCtx, cancelRun := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() {
		_, runErr := runner.Run(runCtx)
		runResult <- runErr
	}()
	waitValue(t, requests)
	if waitErr := waitValue(t, steadyResult); waitErr != nil {
		t.Fatalf("Runner.WaitSteady() error = %v", waitErr)
	}

	cancelRun()
	if runErr := waitValue(t, runResult); runErr != nil {
		t.Fatalf("Runner.Run() error = %v", runErr)
	}
}

func TestRunnerWaitSteadyIsBoundedByContext(t *testing.T) {
	t.Parallel()

	started := make(chan struct{}, 1)
	invoker := invokerFunc(func(ctx context.Context, _ Request) Response {
		started <- struct{}{}
		<-ctx.Done()
		return Response{Outcome: OutcomeCanceled}
	})
	runner, err := New(
		defaultTestConfig(),
		invoker,
		Dependencies{IDGenerator: &sequenceIDGenerator{}},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() {
		_, runErr := runner.Run(ctx)
		runResult <- runErr
	}()
	waitValue(t, started)

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 10*time.Millisecond)
	_, err = runner.WaitSteady(waitCtx, SteadyRequirement{ReadSuccesses: 1})
	cancelWait()
	if !errors.Is(err, ErrSteadyNotReached) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Runner.WaitSteady() error = %v, want bounded deadline error", err)
	}

	cancel()
	if err := waitValue(t, runResult); err != nil {
		t.Fatalf("Runner.Run() error = %v", err)
	}
}

func TestRunnerRejectsMarkersAndWakesWaitersWhileStopping(t *testing.T) {
	t.Parallel()

	started := make(chan struct{}, 1)
	invoker := invokerFunc(func(ctx context.Context, _ Request) Response {
		started <- struct{}{}
		<-ctx.Done()
		return Response{Outcome: OutcomeCanceled}
	})
	runner, err := New(
		defaultTestConfig(),
		invoker,
		Dependencies{IDGenerator: &sequenceIDGenerator{}},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() {
		_, runErr := runner.Run(ctx)
		runResult <- runErr
	}()
	waitValue(t, started)

	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	steadyResult := make(chan error, 1)
	go func() {
		_, waitErr := runner.WaitSteady(
			waitCtx,
			SteadyRequirement{ReadSuccesses: 1},
		)
		steadyResult <- waitErr
	}()

	cancel()
	if waitErr := waitValue(t, steadyResult); !errors.Is(waitErr, ErrSteadyNotReached) &&
		!errors.Is(waitErr, ErrRunnerNotRunning) {
		t.Fatalf(
			"Runner.WaitSteady() error = %v, want stopping result",
			waitErr,
		)
	}
	if err := runner.Mark(Marker{
		FaultID: "rolling_update",
		Phase:   MarkerPhaseAfter,
		Result:  MarkerResultSuccess,
	}); !errors.Is(err, ErrRunnerNotRunning) {
		t.Fatalf("Runner.Mark() error = %v, want ErrRunnerNotRunning", err)
	}
	if err := waitValue(t, runResult); err != nil {
		t.Fatalf("Runner.Run() error = %v", err)
	}
}

func TestRunnerWaitSteadyRequiresDeadline(t *testing.T) {
	t.Parallel()

	runner, err := New(
		defaultTestConfig(),
		invokerFunc(func(context.Context, Request) Response { return Response{} }),
		Dependencies{IDGenerator: &sequenceIDGenerator{}},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = runner.WaitSteady(
		context.Background(),
		SteadyRequirement{ReadSuccesses: 1},
	)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Runner.WaitSteady() error = %v, want ErrInvalidConfig", err)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(*Config)
	}{
		{
			name: "zero run timeout",
			change: func(config *Config) {
				config.RunTimeout = 0
			},
		},
		{
			name: "no streams",
			change: func(config *Config) {
				config.Read = StreamConfig{}
			},
		},
		{
			name: "disabled stream has workers",
			change: func(config *Config) {
				config.Mutating = StreamConfig{Concurrency: 1}
			},
		},
		{
			name: "zero queue",
			change: func(config *Config) {
				config.Read.QueueCapacity = 0
			},
		},
		{
			name: "small event journal",
			change: func(config *Config) {
				config.EventCapacity = 2
			},
		},
		{
			name: "excessive workers",
			change: func(config *Config) {
				config.Read.Concurrency = maxConcurrency + 1
			},
		},
		{
			name: "excessive queue",
			change: func(config *Config) {
				config.Read.QueueCapacity = maxQueueCapacity + 1
			},
		},
		{
			name: "excessive record journal",
			change: func(config *Config) {
				config.RecordCapacity = maxRecordCapacity + 1
			},
		},
		{
			name: "excessive event journal",
			change: func(config *Config) {
				config.EventCapacity = maxEventCapacity + 1
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := defaultTestConfig()
			test.change(&config)
			_, err := New(
				config,
				invokerFunc(func(context.Context, Request) Response { return Response{} }),
				Dependencies{IDGenerator: &sequenceIDGenerator{}},
			)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("New() error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestNewRejectsTypedNilInvoker(t *testing.T) {
	t.Parallel()

	var invoker *nilInvoker
	_, err := New(
		defaultTestConfig(),
		invoker,
		Dependencies{IDGenerator: &sequenceIDGenerator{}},
	)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("New() error = %v, want ErrInvalidConfig", err)
	}
}

func TestNewDefaultsTypedNilDependencies(t *testing.T) {
	t.Parallel()

	var clock *nilClock
	var idGenerator *nilIDGenerator
	runner, err := New(
		defaultTestConfig(),
		invokerFunc(func(context.Context, Request) Response { return Response{} }),
		Dependencies{Clock: clock, IDGenerator: idGenerator},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, ok := runner.clock.(systemClock); !ok {
		t.Fatalf("Runner clock = %T, want systemClock", runner.clock)
	}
	if _, ok := runner.idGenerator.(*randomIDGenerator); !ok {
		t.Fatalf("Runner ID generator = %T, want *randomIDGenerator", runner.idGenerator)
	}
}

func TestRunnerRejectsUnsafeMarkerAndResponseMetadata(t *testing.T) {
	t.Parallel()

	if err := validateMarker(Marker{
		FaultID:  "rollout\ntoken",
		Phase:    MarkerPhaseStarted,
		Result:   MarkerResultUnknown,
		Revision: "secret value",
	}); !errors.Is(err, ErrInvalidMarker) {
		t.Fatalf("validateMarker() error = %v, want ErrInvalidMarker", err)
	}

	response := normalizeResponse(Response{
		Outcome:    OutcomeSuccess,
		RouteID:    "route-a\npayload",
		DataCenter: "dc-a",
	}, nil)
	if response.Outcome != OutcomeInvalidMetadata ||
		response.RouteID != "" ||
		response.DataCenter != "" {
		t.Fatalf(
			"normalizeResponse() = %#v, want invalid metadata with empty dimensions",
			response,
		)
	}
}
