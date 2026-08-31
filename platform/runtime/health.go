package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// ErrNotReady обозначает, что живой сервис пока не должен получать работу.
var ErrNotReady = errors.New("runtime: service is not ready")

// CheckFunc проверяет критическую зависимость с учётом cancellation и deadline.
type CheckFunc func(ctx context.Context) error

// CriticalDependency связывает стабильное имя зависимости с readiness check.
type CriticalDependency struct {
	Name  string
	Check CheckFunc
}

// HealthConfig настраивает readiness checks.
type HealthConfig struct {
	CheckTimeout time.Duration
	Dependencies []CriticalDependency
}

// Health хранит transport-agnostic readiness и проверяет критические
// зависимости. Конкретный transport самостоятельно отображает результат.
type Health struct {
	ready        atomic.Bool
	checkTimeout time.Duration
	dependencies []CriticalDependency
}

// NewHealth создаёт независимый набор health checks.
func NewHealth(config HealthConfig) (*Health, error) {
	if config.CheckTimeout <= 0 {
		return nil, errors.New("runtime: health check timeout must be positive")
	}

	dependencies := make([]CriticalDependency, len(config.Dependencies))
	seen := make(map[string]struct{}, len(config.Dependencies))
	for index, dependency := range config.Dependencies {
		dependency.Name = strings.TrimSpace(dependency.Name)
		if dependency.Name == "" {
			return nil, errors.New("runtime: critical dependency name must not be empty")
		}
		if dependency.Check == nil {
			return nil, fmt.Errorf(
				"runtime: critical dependency %q check must not be nil",
				dependency.Name,
			)
		}
		if _, found := seen[dependency.Name]; found {
			return nil, fmt.Errorf(
				"runtime: critical dependency name %q is duplicated",
				dependency.Name,
			)
		}

		seen[dependency.Name] = struct{}{}
		dependencies[index] = dependency
	}

	return &Health{
		checkTimeout: config.CheckTimeout,
		dependencies: dependencies,
	}, nil
}

// MarkReady разрешает readiness после успешного запуска компонентов.
func (health *Health) MarkReady() {
	health.ready.Store(true)
}

// MarkNotReady немедленно запрещает readiness перед graceful shutdown.
func (health *Health) MarkNotReady() {
	health.ready.Store(false)
}

// Ready проверяет локальный readiness gate и все критические зависимости.
func (health *Health) Ready(ctx context.Context) error {
	if !health.ready.Load() {
		return ErrNotReady
	}
	if len(health.dependencies) == 0 {
		return nil
	}

	checkCtx, cancel := context.WithTimeout(ctx, health.checkTimeout)
	defer cancel()

	type result struct {
		name string
		err  error
	}

	results := make(chan result, len(health.dependencies))
	for _, dependency := range health.dependencies {
		go func() {
			results <- result{
				name: dependency.Name,
				err:  dependency.Check(checkCtx),
			}
		}()
	}

	for range health.dependencies {
		select {
		case dependencyResult := <-results:
			if dependencyResult.err != nil {
				return errors.Join(
					ErrNotReady,
					fmt.Errorf(
						"runtime: critical dependency %q: %w",
						dependencyResult.name,
						dependencyResult.err,
					),
				)
			}
		case <-checkCtx.Done():
			return errors.Join(
				ErrNotReady,
				fmt.Errorf("runtime: checking critical dependencies: %w", checkCtx.Err()),
			)
		}
	}

	return nil
}
