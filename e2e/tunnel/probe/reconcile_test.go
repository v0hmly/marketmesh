package probe

import (
	"testing"
	"time"
)

func TestReconcileDetectsMissingDuplicateLateReorderedAndUnexpected(t *testing.T) {
	t.Parallel()

	client := Snapshot{
		Records: []ClientRecord{
			clientSuccess(requestID1, 1, 10*time.Millisecond),
			clientSuccess(requestID2, 2, 10*time.Millisecond),
		},
		IsComplete: true,
	}
	internal := InternalSnapshot{
		Records: []InternalRecord{
			internalSuccess(requestID2, 2, 5*time.Millisecond),
			internalSuccess(requestID2, 2, 20*time.Millisecond),
			internalSuccess(requestID3, 1, 5*time.Millisecond),
		},
		IsComplete:        false,
		IncompleteReasons: []string{"ledger_page_missing"},
	}

	result := Reconcile(client, internal)
	if result.IsComplete {
		t.Fatal("Reconciliation.IsComplete = true, want false")
	}
	if !result.HasIntegrityFault {
		t.Fatal("Reconciliation.HasIntegrityFault = false, want true")
	}
	assertStrings(t, result.Missing, []string{requestID1})
	assertStrings(t, result.Unexpected, []string{requestID3})
	if len(result.Duplicate) != 1 || result.Duplicate[0].RequestID != requestID2 {
		t.Fatalf("Duplicate = %#v, want requestID2", result.Duplicate)
	}
	if len(result.Late) != 1 || result.Late[0].RequestID != requestID2 {
		t.Fatalf("Late = %#v, want requestID2", result.Late)
	}
	if len(result.Reordered) != 1 || result.Reordered[0].RequestID != requestID3 {
		t.Fatalf("Reordered = %#v, want requestID3", result.Reordered)
	}
	assertStrings(t, result.IncompleteReasons, []string{"ledger_page_missing"})
}

func TestReconcileTreatsNonDispatchedClientResultAsNotExpected(t *testing.T) {
	t.Parallel()

	client := Snapshot{
		Records: []ClientRecord{
			{
				RequestID:          requestID1,
				Class:              TrafficClassRead,
				Sequence:           1,
				CompletionSequence: 1,
				ScheduledOffset:    time.Millisecond,
				FinishedOffset:     2 * time.Millisecond,
				Outcome:            OutcomeBackpressure,
			},
		},
		IsComplete: true,
	}
	internal := InternalSnapshot{
		Records:    []InternalRecord{},
		IsComplete: true,
	}

	result := Reconcile(client, internal)
	if len(result.Missing) != 0 {
		t.Fatalf("Missing = %v, want empty", result.Missing)
	}
	if result.IsComplete {
		t.Fatal("Reconciliation.IsComplete = true for empty internal ledger")
	}
	if result.HasIntegrityFault {
		t.Fatal("Reconciliation.HasIntegrityFault = true, want false")
	}
}

func TestReconcileFailsClosedForInvalidMutationIdempotency(t *testing.T) {
	t.Parallel()

	clientRecord := clientSuccess(requestID1, 1, 10*time.Millisecond)
	clientRecord.Class = TrafficClassMutating
	clientRecord.IdempotencyKey = "different-id"
	result := Reconcile(
		Snapshot{Records: []ClientRecord{clientRecord}, IsComplete: true},
		InternalSnapshot{Records: []InternalRecord{}, IsComplete: true},
	)
	if result.IsComplete {
		t.Fatal("Reconciliation.IsComplete = true for invalid idempotency")
	}
	assertStrings(t, result.Invalid, []string{requestID1})
}

func TestReconcileFailsClosedWhenSnapshotOmitsIncompleteReason(t *testing.T) {
	t.Parallel()

	clientRecord := clientSuccess(requestID1, 1, 10*time.Millisecond)
	internalRecord := internalSuccess(requestID1, 1, 2*time.Millisecond)
	result := Reconcile(
		Snapshot{Records: []ClientRecord{clientRecord}, IsComplete: false},
		InternalSnapshot{
			Records:    []InternalRecord{internalRecord},
			IsComplete: false,
		},
	)

	if result.IsComplete {
		t.Fatal("Reconciliation.IsComplete = true for incomplete snapshots")
	}
	assertStrings(
		t,
		result.IncompleteReasons,
		[]string{"client_snapshot_incomplete", "internal_snapshot_incomplete"},
	)
}

func TestReconcileDetectsLostAndDuplicateMutationResponses(t *testing.T) {
	t.Parallel()

	clientRecord := clientSuccess(requestID1, 1, 10*time.Millisecond)
	clientRecord.Class = TrafficClassMutating
	clientRecord.IdempotencyKey = requestID1
	clientRecord.Outcome = OutcomeTimeout
	clientRecord.Duplicate = true
	internalRecord := InternalRecord{
		RequestID:            requestID1,
		IdempotencyKeySHA256: digestString(requestID1),
		Class:                TrafficClassMutating,
		Sequence:             1,
		Attempts:             2,
		Outcome:              OutcomeSuccess,
		RouteID:              "route-a",
		DataCenter:           DataCenterA,
		Source:               "internal-a-1",
	}

	result := Reconcile(
		Snapshot{Records: []ClientRecord{clientRecord}, IsComplete: true},
		InternalSnapshot{Records: []InternalRecord{internalRecord}, IsComplete: true},
	)
	assertStrings(t, result.LostResponses, []string{requestID1})
	if len(result.Duplicate) != 2 {
		t.Fatalf("Duplicate count = %d, want client and internal evidence", len(result.Duplicate))
	}
	if !result.HasIntegrityFault {
		t.Fatal("Reconciliation.HasIntegrityFault = false, want true")
	}
}

func TestReconcileDetectsClientResponseReordering(t *testing.T) {
	t.Parallel()

	first := clientSuccess(requestID1, 1, 10*time.Millisecond)
	first.FinishedOffset = 2 * time.Millisecond
	first.InternalSequence = 2
	first.Source = "internal-a-1"
	second := clientSuccess(requestID2, 2, 10*time.Millisecond)
	second.InternalSequence = 1
	second.Source = "internal-a-1"
	internalFirst := internalSuccess(requestID2, 1, time.Millisecond)
	internalFirst.Source = "internal-a-1"
	internalSecond := internalSuccess(requestID1, 2, time.Millisecond)
	internalSecond.Source = "internal-a-1"

	result := Reconcile(
		Snapshot{Records: []ClientRecord{first, second}, IsComplete: true},
		InternalSnapshot{
			Records:    []InternalRecord{internalFirst, internalSecond},
			IsComplete: true,
		},
	)
	if len(result.Reordered) != 1 {
		t.Fatalf("Reordered = %#v, want one client response reorder", result.Reordered)
	}
	if result.Reordered[0].Stage != "client_response" ||
		result.Reordered[0].RequestID != requestID2 {
		t.Fatalf("Reordered = %#v, want requestID2 client_response", result.Reordered)
	}
	if !result.HasIntegrityFault {
		t.Fatal("Reconciliation.HasIntegrityFault = false for reordered response")
	}
}

func TestReconcileFailsClosedForSemanticMismatchMissingAndDuplicate(t *testing.T) {
	t.Parallel()

	matchingClient := clientSuccess(requestID1, 1, 10*time.Millisecond)
	matchingClient.Source = "internal-a-1"
	matchingClient.InternalSequence = 1
	matchingInternal := internalSuccess(requestID1, 1, 2*time.Millisecond)
	matchingInternal.Source = "internal-a-1"

	tests := []struct {
		name     string
		client   []ClientRecord
		internal []InternalRecord
	}{
		{
			name:   "request id",
			client: []ClientRecord{matchingClient},
			internal: []InternalRecord{func() InternalRecord {
				record := matchingInternal
				record.RequestID = requestID2
				return record
			}()},
		},
		{
			name:   "class",
			client: []ClientRecord{matchingClient},
			internal: []InternalRecord{func() InternalRecord {
				record := matchingInternal
				record.Class = TrafficClassMutating
				record.IdempotencyKeySHA256 = digestString(requestID1)
				return record
			}()},
		},
		{
			name:   "route id",
			client: []ClientRecord{matchingClient},
			internal: []InternalRecord{func() InternalRecord {
				record := matchingInternal
				record.RouteID = "route-b"
				return record
			}()},
		},
		{
			name:   "data center",
			client: []ClientRecord{matchingClient},
			internal: []InternalRecord{func() InternalRecord {
				record := matchingInternal
				record.DataCenter = DataCenterB
				return record
			}()},
		},
		{
			name:   "source",
			client: []ClientRecord{matchingClient},
			internal: []InternalRecord{func() InternalRecord {
				record := matchingInternal
				record.Source = "internal-a-2"
				return record
			}()},
		},
		{
			name: "source missing from both ledgers",
			client: []ClientRecord{func() ClientRecord {
				record := matchingClient
				record.Source = ""
				return record
			}()},
			internal: []InternalRecord{func() InternalRecord {
				record := matchingInternal
				record.Source = ""
				return record
			}()},
		},
		{
			name:   "internal sequence",
			client: []ClientRecord{matchingClient},
			internal: []InternalRecord{func() InternalRecord {
				record := matchingInternal
				record.Sequence = 2
				return record
			}()},
		},
		{
			name:     "missing",
			client:   []ClientRecord{matchingClient},
			internal: []InternalRecord{},
		},
		{
			name:   "duplicate",
			client: []ClientRecord{matchingClient},
			internal: []InternalRecord{
				matchingInternal,
				matchingInternal,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result := Reconcile(
				Snapshot{Records: test.client, IsComplete: true},
				InternalSnapshot{Records: test.internal, IsComplete: true},
			)
			if result.IsComplete {
				t.Fatal("Reconciliation.IsComplete = true for integrity fault")
			}
			if !result.HasIntegrityFault {
				t.Fatal("Reconciliation.HasIntegrityFault = false")
			}
		})
	}
}

func FuzzReconcileNeverPassesInvalidRequestID(f *testing.F) {
	f.Add(requestID1)
	f.Add("bad\nvalue")
	f.Fuzz(func(t *testing.T, requestID string) {
		record := clientSuccess(requestID, 1, time.Second)
		result := Reconcile(
			Snapshot{Records: []ClientRecord{record}, IsComplete: true},
			InternalSnapshot{
				Records: []InternalRecord{
					internalSuccess(requestID, 1, time.Millisecond),
				},
				IsComplete: true,
			},
		)
		if !validateRequestID(requestID) && result.IsComplete {
			t.Fatalf("Reconcile() passed invalid request id %q", requestID)
		}
	})
}

func clientSuccess(requestID string, sequence uint64, deadline time.Duration) ClientRecord {
	return ClientRecord{
		RequestID:          requestID,
		Class:              TrafficClassRead,
		Sequence:           sequence,
		CompletionSequence: sequence,
		ScheduledOffset:    time.Millisecond,
		StartedOffset:      time.Millisecond,
		DeadlineOffset:     deadline,
		FinishedOffset:     2 * time.Millisecond,
		Latency:            time.Millisecond,
		Outcome:            OutcomeSuccess,
		RouteID:            "route-a",
		DataCenter:         "dc-a",
		Source:             "internal-a-1",
		InternalSequence:   sequence,
		Dispatched:         true,
	}
}

func internalSuccess(requestID string, sequence uint64, completed time.Duration) InternalRecord {
	return InternalRecord{
		RequestID:       requestID,
		Class:           TrafficClassRead,
		Sequence:        sequence,
		Attempts:        1,
		AcceptedOffset:  time.Millisecond,
		CompletedOffset: completed,
		Outcome:         OutcomeSuccess,
		RouteID:         "route-a",
		DataCenter:      "dc-a",
		Source:          "internal-a-1",
	}
}

func assertStrings(t *testing.T, actual []string, expected []string) {
	t.Helper()

	if len(actual) != len(expected) {
		t.Fatalf("strings = %v, want %v", actual, expected)
	}
	for index := range expected {
		if actual[index] != expected[index] {
			t.Fatalf("strings = %v, want %v", actual, expected)
		}
	}
}

const (
	requestID1 = "00000000000000000000000000000001"
	requestID2 = "00000000000000000000000000000002"
	requestID3 = "00000000000000000000000000000003"
)
