package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

var (
	// ErrComponentStopped обозначает неожиданный успешный выход компонента.
	ErrComponentStopped = errors.New("runtime: component stopped unexpectedly")

	// ErrRunnerUsed обозначает повторный вызов одноразового Runner.Run.
	ErrRunnerUsed = errors.New("runtime: runner has already been used")
)

// RunFunc выполняет компонент до cancellation, остановки или ошибки.
// Функция блокируется на время работы.
type RunFunc func(ctx context.Context) error

// ShutdownFunc прекращает приём новой работы и освобождает ресурсы.
// Реализация обязана учитывать deadline ctx.
type ShutdownFunc func(ctx context.Context) error

// Component — минимальная единица управляемого жизненного цикла.
type Component struct {
	Name     string
	Run      RunFunc
	Shutdown ShutdownFunc
}

// RunnerConfig настраивает общий shutdown deadline и необязательный readiness
// gate.
type RunnerConfig struct {
	ShutdownTimeout time.Duration
	Health          *Health
}

// Runner параллельно запускает компоненты, останавливает их в обратном
// порядке и возвращает объединённые ошибки без логирования.
type Runner struct {
	shutdownTimeout time.Duration
	health          *Health
	components      []Component
	used            atomic.Bool
}

// NewRunner проверяет и копирует компоненты. Компоненты запускаются в
// объявленном порядке; shutdown выполняется в обратном порядке.
func NewRunner(config RunnerConfig, components ...Component) (*Runner, error) {
	if config.ShutdownTimeout <= 0 {
		return nil, errors.New("runtime: shutdown timeout must be positive")
	}
	if len(components) == 0 {
		return nil, errors.New("runtime: at least one component is required")
	}

	copied := make([]Component, len(components))
	seen := make(map[string]struct{}, len(components))
	for index, component := range components {
		component.Name = strings.TrimSpace(component.Name)
		if component.Name == "" {
			return nil, errors.New("runtime: component name must not be empty")
		}
		if component.Run == nil {
			return nil, fmt.Errorf("runtime: component %q run function must not be nil", component.Name)
		}
		if component.Shutdown == nil {
			return nil, fmt.Errorf(
				"runtime: component %q shutdown function must not be nil",
				component.Name,
			)
		}
		if _, found := seen[component.Name]; found {
			return nil, fmt.Errorf("runtime: component name %q is duplicated", component.Name)
		}

		seen[component.Name] = struct{}{}
		copied[index] = component
	}

	return &Runner{
		shutdownTimeout: config.ShutdownTimeout,
		health:          config.Health,
		components:      copied,
	}, nil
}

type componentResult struct {
	index int
	err   error
}

// Run выполняет одноразовый жизненный цикл. Cancellation корневого context
// считается штатной остановкой. Ошибки сохраняются через errors.Join;
// логировать возвращённую ошибку должен только composition root.
func (runner *Runner) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("runtime: root context must not be nil")
	}
	if !runner.used.CompareAndSwap(false, true) {
		return ErrRunnerUsed
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	results := make(chan componentResult, len(runner.components))
	completed := make([]bool, len(runner.components))
	for index, component := range runner.components {
		go func() {
			results <- componentResult{
				index: index,
				err:   component.Run(runCtx),
			}
		}()
	}

	if runner.health != nil {
		runner.health.MarkReady()
	}

	runErrors := []error{}
	select {
	case <-ctx.Done():
	case result := <-results:
		completed[result.index] = true
		if ctx.Err() == nil {
			runErrors = append(runErrors, runner.componentRunError(result))
		}
	}

	if runner.health != nil {
		runner.health.MarkNotReady()
	}
	cancelRun()

	shutdownCtx, cancelShutdown := context.WithTimeout(
		context.WithoutCancel(ctx),
		runner.shutdownTimeout,
	)
	defer cancelShutdown()

	shutdownErr := runner.shutdown(shutdownCtx)
	waitErrors := runner.wait(shutdownCtx, results, completed)

	return errors.Join(
		errors.Join(runErrors...),
		shutdownErr,
		errors.Join(waitErrors...),
	)
}

func (runner *Runner) shutdown(ctx context.Context) error {
	errorsByComponent := []error{}
	for index := len(runner.components) - 1; index >= 0; index-- {
		component := runner.components[index]
		result := make(chan error, 1)
		go func() {
			result <- component.Shutdown(ctx)
		}()

		select {
		case err := <-result:
			if err != nil {
				errorsByComponent = append(
					errorsByComponent,
					fmt.Errorf("runtime: shutting down component %q: %w", component.Name, err),
				)
			}
		case <-ctx.Done():
			errorsByComponent = append(
				errorsByComponent,
				fmt.Errorf("runtime: shutting down component %q: %w", component.Name, ctx.Err()),
			)
			return errors.Join(errorsByComponent...)
		}
	}

	return errors.Join(errorsByComponent...)
}

func (runner *Runner) wait(
	ctx context.Context,
	results <-chan componentResult,
	completed []bool,
) []error {
	remaining := 0
	for _, isCompleted := range completed {
		if !isCompleted {
			remaining++
		}
	}

	runErrors := []error{}
	for remaining > 0 {
		select {
		case result := <-results:
			if completed[result.index] {
				continue
			}
			completed[result.index] = true
			remaining--
			if result.err != nil &&
				!errors.Is(result.err, context.Canceled) &&
				!errors.Is(result.err, context.DeadlineExceeded) {
				runErrors = append(runErrors, runner.componentRunError(result))
			}
		case <-ctx.Done():
			names := make([]string, 0, remaining)
			for index, isCompleted := range completed {
				if !isCompleted {
					names = append(names, runner.components[index].Name)
				}
			}
			runErrors = append(
				runErrors,
				fmt.Errorf(
					"runtime: waiting for components %q: %w",
					strings.Join(names, ","),
					ctx.Err(),
				),
			)
			return runErrors
		}
	}

	return runErrors
}

func (runner *Runner) componentRunError(result componentResult) error {
	component := runner.components[result.index]
	if result.err == nil {
		return fmt.Errorf("%w: %s", ErrComponentStopped, component.Name)
	}

	return fmt.Errorf("runtime: component %q failed: %w", component.Name, result.err)
}
