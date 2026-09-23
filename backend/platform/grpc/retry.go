package grpc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const maxRetryAttempts = 5

// RetryPolicy разрешает ограниченные повторы только перечисленных
// идемпотентных unary methods.
type RetryPolicy struct {
	IdempotentMethods []string
	RetryableCodes    []codes.Code
	MaxAttempts       int
	InitialBackoff    time.Duration
	MaxBackoff        time.Duration
	BackoffMultiplier float64
}

type retrySettings struct {
	methods           map[string]struct{}
	codes             map[codes.Code]struct{}
	maxAttempts       int
	initialBackoff    time.Duration
	maxBackoff        time.Duration
	backoffMultiplier float64
}

func newRetrySettings(policy *RetryPolicy) (retrySettings, error) {
	if policy == nil {
		return retrySettings{}, nil
	}
	if len(policy.IdempotentMethods) == 0 {
		return retrySettings{}, errors.New("grpc: retry idempotent methods must not be empty")
	}
	if policy.MaxAttempts < 2 || policy.MaxAttempts > maxRetryAttempts {
		return retrySettings{}, fmt.Errorf(
			"grpc: retry max attempts must be between 2 and %d",
			maxRetryAttempts,
		)
	}
	if policy.InitialBackoff <= 0 {
		return retrySettings{}, errors.New("grpc: retry initial backoff must be positive")
	}
	if policy.MaxBackoff < policy.InitialBackoff {
		return retrySettings{}, errors.New("grpc: retry max backoff must not be less than initial backoff")
	}
	if math.IsNaN(policy.BackoffMultiplier) ||
		math.IsInf(policy.BackoffMultiplier, 0) ||
		policy.BackoffMultiplier < 1 {
		return retrySettings{}, errors.New("grpc: retry backoff multiplier must be finite and at least one")
	}
	if len(policy.RetryableCodes) == 0 {
		return retrySettings{}, errors.New("grpc: retryable codes must not be empty")
	}

	settings := retrySettings{
		methods:           make(map[string]struct{}, len(policy.IdempotentMethods)),
		codes:             make(map[codes.Code]struct{}, len(policy.RetryableCodes)),
		maxAttempts:       policy.MaxAttempts,
		initialBackoff:    policy.InitialBackoff,
		maxBackoff:        policy.MaxBackoff,
		backoffMultiplier: policy.BackoffMultiplier,
	}
	for _, method := range policy.IdempotentMethods {
		method = strings.TrimSpace(method)
		if !validFullMethod(method) {
			return retrySettings{}, fmt.Errorf("grpc: invalid idempotent method %q", method)
		}
		if _, duplicated := settings.methods[method]; duplicated {
			return retrySettings{}, fmt.Errorf("grpc: duplicated idempotent method %q", method)
		}
		settings.methods[method] = struct{}{}
	}

	allowedCodes := []codes.Code{codes.Aborted, codes.ResourceExhausted, codes.Unavailable}
	for _, code := range policy.RetryableCodes {
		if !slices.Contains(allowedCodes, code) {
			return retrySettings{}, fmt.Errorf("grpc: code %s is unsafe for automatic retry", code)
		}
		if _, duplicated := settings.codes[code]; duplicated {
			return retrySettings{}, fmt.Errorf("grpc: duplicated retryable code %s", code)
		}
		settings.codes[code] = struct{}{}
	}

	return settings, nil
}

func validFullMethod(method string) bool {
	if !strings.HasPrefix(method, "/") || strings.Count(method, "/") != 2 {
		return false
	}

	parts := strings.Split(method[1:], "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}

func (settings retrySettings) interceptor() grpcgo.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		request any,
		response any,
		connection *grpcgo.ClientConn,
		invoker grpcgo.UnaryInvoker,
		options ...grpcgo.CallOption,
	) error {
		if _, idempotent := settings.methods[method]; !idempotent {
			return invoker(ctx, method, request, response, connection, options...)
		}

		backoff := settings.initialBackoff
		for attempt := 1; ; attempt++ {
			err := invoker(ctx, method, request, response, connection, options...)
			if err == nil || attempt >= settings.maxAttempts || !settings.retryable(err) {
				return err
			}
			if err := waitForRetry(ctx, backoff); err != nil {
				return status.FromContextError(err).Err()
			}
			backoff = settings.nextBackoff(backoff)
		}
	}
}

func (settings retrySettings) retryable(err error) bool {
	_, found := settings.codes[status.Code(err)]
	return found
}

func (settings retrySettings) nextBackoff(current time.Duration) time.Duration {
	next := time.Duration(float64(current) * settings.backoffMultiplier)
	if next < current || next > settings.maxBackoff {
		return settings.maxBackoff
	}

	return next
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
