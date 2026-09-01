package probe

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxRPS            = 100_000
	maxConcurrency    = 1_024
	maxQueueCapacity  = 100_000
	maxRecordCapacity = 1_000_000
	maxEventCapacity  = 4_000_000
)

// StreamConfig задаёт отдельный bounded поток read или mutating запросов.
type StreamConfig struct {
	RPS           uint32
	Concurrency   int
	QueueCapacity int
}

// Config задаёт bounded run/stop/request lifecycle и ёмкость journal.
type Config struct {
	RunTimeout     time.Duration
	StopTimeout    time.Duration
	RequestTimeout time.Duration
	Read           StreamConfig
	Mutating       StreamConfig
	RecordCapacity int
	EventCapacity  int
}

// Dependencies содержит заменяемые deterministic boundaries. Nil значения
// получают production-safe реализации стандартной библиотеки.
type Dependencies struct {
	Clock       Clock
	IDGenerator IDGenerator
}

// Runner выполняет один bounded запуск. Повторное использование запрещено,
// чтобы ledger разных сценариев не смешивались.
type Runner struct {
	config      Config
	invoker     Invoker
	clock       Clock
	idGenerator IDGenerator
	used        atomic.Bool

	stateMu   sync.Mutex
	journal   *journal
	cancel    context.CancelFunc
	done      chan struct{}
	startup   chan struct{}
	isRunning bool
}

// New проверяет конфигурацию и создаёт одноразовый Runner.
func New(config Config, invoker Invoker, dependencies Dependencies) (*Runner, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if isNilDependency(invoker) {
		return nil, fmt.Errorf("%w: invoker must not be nil", ErrInvalidConfig)
	}

	clock := dependencies.Clock
	if isNilDependency(clock) {
		clock = systemClock{}
	}
	idGenerator := dependencies.IDGenerator
	if isNilDependency(idGenerator) {
		generated, err := defaultIDGenerator()
		if err != nil {
			return nil, err
		}
		idGenerator = generated
	}

	return &Runner{
		config:      config,
		invoker:     invoker,
		clock:       clock,
		idGenerator: idGenerator,
		startup:     make(chan struct{}),
	}, nil
}

// Run выполняет read и mutating потоки до RunTimeout либо cancellation ctx.
// Остановка ограничена StopTimeout. Snapshot возвращается даже при ошибке,
// чтобы diagnostics не терялись; неполный snapshot всегда даёт ErrIncompleteRun.
func (runner *Runner) Run(ctx context.Context) (Snapshot, error) {
	if ctx == nil {
		return Snapshot{}, fmt.Errorf("%w: context must not be nil", ErrInvalidConfig)
	}
	if !runner.used.CompareAndSwap(false, true) {
		return Snapshot{}, ErrRunnerUsed
	}

	runCtx, cancel := context.WithTimeout(ctx, runner.config.RunTimeout)
	defer cancel()

	startedAt := runner.clock.Now()
	clientJournal := newJournal(
		runner.clock,
		startedAt,
		runner.config.RecordCapacity,
		runner.config.EventCapacity,
	)
	runErrors := []error{}
	if err := clientJournal.event(Event{Kind: EventKindRunStarted}); err != nil {
		clientJournal.markIncomplete("run_start_event")
		runner.signalStartupFailure()
		return clientJournal.snapshot(), errors.Join(ErrIncompleteRun, err)
	}
	runner.setRunning(clientJournal, cancel)
	defer runner.setStopped()

	type streamResult struct {
		class TrafficClass
		err   error
	}
	streamCount := 0
	results := make(chan streamResult, 2)
	startStream := func(class TrafficClass, config StreamConfig) {
		if config.RPS == 0 {
			return
		}
		streamCount++
		go func() {
			results <- streamResult{
				class: class,
				err: runner.runStream(
					runCtx,
					clientJournal,
					class,
					config,
				),
			}
		}()
	}
	startStream(TrafficClassRead, runner.config.Read)
	startStream(TrafficClassMutating, runner.config.Mutating)

	completed := 0
waitForStop:
	for completed < streamCount {
		select {
		case result := <-results:
			completed++
			if result.err == nil {
				continue
			}
			runErrors = append(
				runErrors,
				fmt.Errorf("probe: %s stream: %w", result.class, result.err),
			)
			cancel()
		case <-runCtx.Done():
			break waitForStop
		}
	}
	if err := clientJournal.event(Event{Kind: EventKindRunStopping}); err != nil {
		runErrors = append(runErrors, err)
		clientJournal.markIncomplete("run_stopping_event")
	}
	cancel()
	runner.beginStopping()

	stopTimer := time.NewTimer(runner.config.StopTimeout)
	defer stopTimer.Stop()
	for completed < streamCount {
		select {
		case result := <-results:
			completed++
			if result.err != nil {
				runErrors = append(
					runErrors,
					fmt.Errorf("probe: %s stream: %w", result.class, result.err),
				)
			}
		case <-stopTimer.C:
			clientJournal.markIncomplete("runner_stop_timeout")
			runErrors = append(runErrors, ErrStopTimeout)
			completed = streamCount
		}
	}

	if err := clientJournal.event(Event{Kind: EventKindRunFinished}); err != nil {
		runErrors = append(runErrors, err)
		clientJournal.markIncomplete("run_finish_event")
	}
	snapshot := clientJournal.snapshot()
	if !snapshot.IsComplete {
		runErrors = append(runErrors, ErrIncompleteRun)
	}

	return snapshot, errors.Join(runErrors...)
}

// Mark добавляет безопасное внешнее lifecycle событие в monotonic timeline.
// Метод ничего не меняет в окружении и возвращает ошибку при заполненном journal.
func (runner *Runner) Mark(marker Marker) error {
	if err := validateMarker(marker); err != nil {
		return err
	}

	runner.stateMu.Lock()
	if !runner.isRunning {
		runner.stateMu.Unlock()
		return ErrRunnerNotRunning
	}
	clientJournal := runner.journal
	cancel := runner.cancel
	err := clientJournal.event(Event{
		Kind:   EventKindMarker,
		Marker: marker,
	})
	runner.stateMu.Unlock()

	if err != nil {
		clientJournal.markIncomplete("marker_event")
		cancel()
		return err
	}

	return nil
}

// WaitSteady наблюдает terminal results и ждёт требуемый success streak. Он не
// создаёт запросы и не выполняет retry. ctx обязан иметь deadline, поэтому
// ожидание не может быть неограниченным.
func (runner *Runner) WaitSteady(
	ctx context.Context,
	requirement SteadyRequirement,
) (SteadyState, error) {
	if ctx == nil {
		return SteadyState{}, fmt.Errorf("%w: context must not be nil", ErrInvalidConfig)
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		return SteadyState{}, fmt.Errorf("%w: wait context must have a deadline", ErrInvalidConfig)
	}
	if requirement.ReadSuccesses == 0 && requirement.MutatingSuccesses == 0 {
		return SteadyState{}, fmt.Errorf("%w: steady requirement must not be empty", ErrInvalidConfig)
	}
	if requirement.ReadSuccesses > 0 && runner.config.Read.RPS == 0 {
		return SteadyState{}, fmt.Errorf("%w: read stream is disabled", ErrInvalidConfig)
	}
	if requirement.MutatingSuccesses > 0 && runner.config.Mutating.RPS == 0 {
		return SteadyState{}, fmt.Errorf("%w: mutating stream is disabled", ErrInvalidConfig)
	}

	clientJournal, done, err := runner.waitUntilRunning(ctx)
	if err != nil {
		return SteadyState{}, err
	}

	for {
		state, isReached, err := clientJournal.steady(requirement)
		if err != nil {
			return SteadyState{}, err
		}
		if isReached {
			return state, nil
		}

		select {
		case <-ctx.Done():
			return SteadyState{}, errors.Join(ErrSteadyNotReached, ctx.Err())
		case <-done:
			return SteadyState{}, ErrSteadyNotReached
		case <-clientJournal.updates:
		}
	}
}

func (runner *Runner) runStream(
	ctx context.Context,
	clientJournal *journal,
	class TrafficClass,
	config StreamConfig,
) error {
	requests := make(chan Request, config.QueueCapacity)
	var workers sync.WaitGroup
	for range config.Concurrency {
		workers.Go(func() {
			for request := range requests {
				runner.invoke(ctx, clientJournal, request)
			}
		})
	}

	var sequence uint64
	enqueue := func() error {
		sequence++
		requestID := runner.idGenerator.Next()
		if !validateRequestID(requestID) {
			clientJournal.markIncomplete("invalid_request_id")
			return errors.New("probe: id generator returned an invalid request id")
		}
		request := Request{
			ID:       requestID,
			Class:    class,
			Sequence: sequence,
		}
		if class == TrafficClassMutating {
			request.IdempotencyKey = requestID
		}
		if err := clientJournal.schedule(request); err != nil {
			return err
		}

		select {
		case requests <- request:
			return nil
		default:
			return clientJournal.finish(request.ID, Response{Outcome: OutcomeBackpressure})
		}
	}

	if err := enqueue(); err != nil {
		runner.cancelCurrent()
		close(requests)
		workers.Wait()
		return err
	}
	interval := time.Second / time.Duration(config.RPS)
	ticker := runner.clock.NewTicker(interval)
	defer ticker.Stop()

	var streamErr error
	for streamErr == nil {
		select {
		case <-ctx.Done():
			streamErr = nil
		case <-ticker.C():
			streamErr = enqueue()
		}
		if ctx.Err() != nil {
			break
		}
	}
	if streamErr != nil {
		runner.cancelCurrent()
	}
	close(requests)
	workers.Wait()

	return streamErr
}

func (runner *Runner) invoke(
	ctx context.Context,
	clientJournal *journal,
	request Request,
) {
	if ctx.Err() != nil {
		if err := clientJournal.finish(
			request.ID,
			Response{Outcome: OutcomeCanceled},
		); err != nil {
			runner.cancelCurrent()
		}
		return
	}
	if err := clientJournal.start(request.ID, runner.config.RequestTimeout); err != nil {
		runner.cancelCurrent()
		return
	}

	requestCtx, cancel := context.WithTimeout(ctx, runner.config.RequestTimeout)
	response, panicked := invokeSafely(runner.invoker, requestCtx, request)
	requestErr := requestCtx.Err()
	cancel()

	response = normalizeResponse(response, requestErr)
	if err := clientJournal.finish(request.ID, response); err != nil {
		runner.cancelCurrent()
	}
	if panicked {
		clientJournal.markIncomplete("invoker_panic")
		runner.cancelCurrent()
	}
}

func invokeSafely(
	invoker Invoker,
	ctx context.Context,
	request Request,
) (response Response, panicked bool) {
	defer func() {
		if recover() != nil {
			response = Response{Outcome: OutcomeInternalError}
			panicked = true
		}
	}()

	return invoker.Invoke(ctx, request), false
}

func normalizeResponse(response Response, requestErr error) Response {
	if errors.Is(requestErr, context.DeadlineExceeded) && response.Outcome != OutcomeSuccess {
		return Response{Outcome: OutcomeTimeout}
	}
	if errors.Is(requestErr, context.Canceled) && response.Outcome != OutcomeSuccess {
		return Response{Outcome: OutcomeCanceled}
	}
	if !validateOutcome(response.Outcome) {
		return Response{Outcome: OutcomeInternalError}
	}
	if !validateDimension(response.RouteID, response.Outcome != OutcomeSuccess) ||
		!validateDataCenter(response.DataCenter, response.Outcome != OutcomeSuccess) ||
		!validateDimension(response.Source, true) {
		return Response{Outcome: OutcomeInvalidMetadata}
	}

	return response
}

func (runner *Runner) setRunning(clientJournal *journal, cancel context.CancelFunc) {
	runner.stateMu.Lock()
	defer runner.stateMu.Unlock()

	runner.journal = clientJournal
	runner.cancel = cancel
	runner.done = make(chan struct{})
	runner.isRunning = true
	close(runner.startup)
}

func (runner *Runner) signalStartupFailure() {
	runner.stateMu.Lock()
	defer runner.stateMu.Unlock()

	close(runner.startup)
}

func (runner *Runner) waitUntilRunning(
	ctx context.Context,
) (*journal, <-chan struct{}, error) {
	runner.stateMu.Lock()
	if runner.isRunning {
		clientJournal := runner.journal
		done := runner.done
		runner.stateMu.Unlock()
		return clientJournal, done, nil
	}
	startup := runner.startup
	runner.stateMu.Unlock()

	select {
	case <-ctx.Done():
		return nil, nil, errors.Join(ErrSteadyNotReached, ctx.Err())
	case <-startup:
	}

	runner.stateMu.Lock()
	defer runner.stateMu.Unlock()
	if !runner.isRunning {
		return nil, nil, ErrRunnerNotRunning
	}

	return runner.journal, runner.done, nil
}

func (runner *Runner) setStopped() {
	runner.stateMu.Lock()
	defer runner.stateMu.Unlock()

	if runner.done != nil {
		select {
		case <-runner.done:
		default:
			close(runner.done)
		}
	}
	runner.journal = nil
	runner.cancel = nil
	runner.done = nil
	runner.isRunning = false
}

func (runner *Runner) beginStopping() {
	runner.stateMu.Lock()
	defer runner.stateMu.Unlock()

	if !runner.isRunning {
		return
	}
	runner.isRunning = false
	close(runner.done)
}

func (runner *Runner) cancelCurrent() {
	runner.stateMu.Lock()
	cancel := runner.cancel
	runner.stateMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func validateConfig(config Config) error {
	if config.RunTimeout <= 0 {
		return fmt.Errorf("%w: run timeout must be positive", ErrInvalidConfig)
	}
	if config.StopTimeout <= 0 {
		return fmt.Errorf("%w: stop timeout must be positive", ErrInvalidConfig)
	}
	if config.RequestTimeout <= 0 {
		return fmt.Errorf("%w: request timeout must be positive", ErrInvalidConfig)
	}
	if config.RecordCapacity <= 0 {
		return fmt.Errorf("%w: record capacity must be positive", ErrInvalidConfig)
	}
	if config.EventCapacity < 3 {
		return fmt.Errorf("%w: event capacity must be at least 3", ErrInvalidConfig)
	}
	if config.RecordCapacity > maxRecordCapacity {
		return fmt.Errorf("%w: record capacity is too high", ErrInvalidConfig)
	}
	if config.EventCapacity > maxEventCapacity {
		return fmt.Errorf("%w: event capacity is too high", ErrInvalidConfig)
	}

	readEnabled, err := validateStream("read", config.Read)
	if err != nil {
		return err
	}
	mutatingEnabled, err := validateStream("mutating", config.Mutating)
	if err != nil {
		return err
	}
	if !readEnabled && !mutatingEnabled {
		return fmt.Errorf("%w: at least one stream must be enabled", ErrInvalidConfig)
	}

	return nil
}

func validateStream(name string, config StreamConfig) (bool, error) {
	if config.RPS == 0 {
		if config.Concurrency != 0 || config.QueueCapacity != 0 {
			return false, fmt.Errorf(
				"%w: disabled %s stream must have zero concurrency and queue capacity",
				ErrInvalidConfig,
				name,
			)
		}
		return false, nil
	}
	if config.RPS > maxRPS {
		return false, fmt.Errorf("%w: %s rps is too high", ErrInvalidConfig, name)
	}
	if config.Concurrency <= 0 {
		return false, fmt.Errorf("%w: %s concurrency must be positive", ErrInvalidConfig, name)
	}
	if config.Concurrency > maxConcurrency {
		return false, fmt.Errorf("%w: %s concurrency is too high", ErrInvalidConfig, name)
	}
	if config.QueueCapacity <= 0 {
		return false, fmt.Errorf("%w: %s queue capacity must be positive", ErrInvalidConfig, name)
	}
	if config.QueueCapacity > maxQueueCapacity {
		return false, fmt.Errorf("%w: %s queue capacity is too high", ErrInvalidConfig, name)
	}

	return true, nil
}
