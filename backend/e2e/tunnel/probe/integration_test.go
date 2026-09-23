//go:build integration

package probe

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestRunnerReconcilesSimulatedInternalLedger(t *testing.T) {
	t.Parallel()

	var ledgerMu sync.Mutex
	internalRecords := []InternalRecord{}
	invoker := invokerFunc(func(_ context.Context, request Request) Response {
		idempotencyDigest := ""
		if request.Class == TrafficClassMutating {
			idempotencyDigest = digestString(request.IdempotencyKey)
		}
		ledgerMu.Lock()
		internalRecords = append(internalRecords, InternalRecord{
			RequestID:            request.ID,
			IdempotencyKeySHA256: idempotencyDigest,
			Class:                request.Class,
			Sequence:             request.Sequence,
			Attempts:             1,
			AcceptedOffset:       time.Nanosecond,
			CompletedOffset:      2 * time.Nanosecond,
			Outcome:              OutcomeSuccess,
			RouteID:              "route-a",
			DataCenter:           "dc-a",
			Source:               "internal-a-1",
		})
		ledgerMu.Unlock()

		return Response{
			Outcome:          OutcomeSuccess,
			RouteID:          "route-a",
			DataCenter:       "dc-a",
			Source:           "internal-a-1",
			InternalSequence: request.Sequence,
		}
	})
	runner, err := New(Config{
		RunTimeout:     25 * time.Millisecond,
		StopTimeout:    time.Second,
		RequestTimeout: 20 * time.Millisecond,
		Read: StreamConfig{
			RPS:           100,
			Concurrency:   2,
			QueueCapacity: 2,
		},
		Mutating: StreamConfig{
			RPS:           100,
			Concurrency:   1,
			QueueCapacity: 1,
		},
		RecordCapacity: 32,
		EventCapacity:  128,
	}, invoker, Dependencies{IDGenerator: &sequenceIDGenerator{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	client, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Runner.Run() error = %v", err)
	}
	ledgerMu.Lock()
	internal := InternalSnapshot{
		Records:    append([]InternalRecord{}, internalRecords...),
		IsComplete: true,
	}
	ledgerMu.Unlock()

	result := Reconcile(client, internal)
	if !result.IsComplete {
		t.Fatalf("Reconciliation.IsComplete = false, reasons = %v", result.IncompleteReasons)
	}
	if result.HasIntegrityFault {
		t.Fatalf("Reconciliation integrity faults = %#v", result)
	}
}
