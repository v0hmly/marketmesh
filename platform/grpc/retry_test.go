package grpc

import (
	"context"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRetryPolicyOnlyRetriesDeclaredIdempotentMethod(t *testing.T) {
	t.Parallel()

	settings, err := newRetrySettings(&RetryPolicy{
		IdempotentMethods: []string{"/test.Service/Get"},
		RetryableCodes:    []codes.Code{codes.Unavailable},
		MaxAttempts:       3,
		InitialBackoff:    time.Millisecond,
		MaxBackoff:        2 * time.Millisecond,
		BackoffMultiplier: 2,
	})
	if err != nil {
		t.Fatalf("newRetrySettings() error = %v", err)
	}

	tests := []struct {
		name         string
		method       string
		wantAttempts int32
		wantError    bool
	}{
		{name: "idempotent", method: "/test.Service/Get", wantAttempts: 2},
		{name: "not declared", method: "/test.Service/Create", wantAttempts: 1, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var attempts atomic.Int32
			err := settings.interceptor()(
				context.Background(),
				test.method,
				nil,
				nil,
				nil,
				func(
					context.Context,
					string,
					any,
					any,
					*grpcgo.ClientConn,
					...grpcgo.CallOption,
				) error {
					if attempts.Add(1) == 1 {
						return status.Error(codes.Unavailable, "temporary")
					}
					return nil
				},
			)
			if test.wantError && err == nil {
				t.Fatal("retry interceptor error = nil, want unavailable")
			}
			if !test.wantError && err != nil {
				t.Fatalf("retry interceptor error = %v", err)
			}
			if attempts.Load() != test.wantAttempts {
				t.Fatalf("attempts = %d, want %d", attempts.Load(), test.wantAttempts)
			}
		})
	}
}

func TestRetryPolicyRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()

	base := RetryPolicy{
		IdempotentMethods: []string{"/test.Service/Get"},
		RetryableCodes:    []codes.Code{codes.Unavailable},
		MaxAttempts:       3,
		InitialBackoff:    time.Millisecond,
		MaxBackoff:        time.Second,
		BackoffMultiplier: 2,
	}
	tests := []struct {
		name      string
		mutate    func(*RetryPolicy)
		errorPart string
	}{
		{
			name:      "unsafe code",
			mutate:    func(policy *RetryPolicy) { policy.RetryableCodes = []codes.Code{codes.Internal} },
			errorPart: "unsafe",
		},
		{
			name:      "invalid method",
			mutate:    func(policy *RetryPolicy) { policy.IdempotentMethods = []string{"Get"} },
			errorPart: "invalid idempotent method",
		},
		{
			name:      "unbounded attempts",
			mutate:    func(policy *RetryPolicy) { policy.MaxAttempts = maxRetryAttempts + 1 },
			errorPart: "max attempts",
		},
		{
			name:      "non-finite multiplier",
			mutate:    func(policy *RetryPolicy) { policy.BackoffMultiplier = math.NaN() },
			errorPart: "backoff multiplier",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			policy := base
			test.mutate(&policy)
			_, err := newRetrySettings(&policy)
			if err == nil || !strings.Contains(err.Error(), test.errorPart) {
				t.Fatalf("newRetrySettings() error = %v, want %q", err, test.errorPart)
			}
		})
	}
}
