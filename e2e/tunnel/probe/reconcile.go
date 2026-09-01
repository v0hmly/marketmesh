package probe

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"time"
)

// Duplicate связывает повторную internal ledger запись с request/idempotency.
type Duplicate struct {
	RequestID         string
	IdempotencyDigest string
	Count             int
}

// LateResult фиксирует internal completion после client deadline.
type LateResult struct {
	RequestID       string
	DeadlineOffset  time.Duration
	CompletedOffset time.Duration
}

// ReorderedResult фиксирует уменьшение request sequence в порядке наблюдения
// internal ledger. Само наличие reorder измеряется, а решение о допустимости
// остаётся за SLO contract.
type ReorderedResult struct {
	RequestID        string
	Stage            string
	PreviousSequence uint64
	Sequence         uint64
}

// Reconciliation — детерминированный integrity diff client/internal ledger.
type Reconciliation struct {
	Missing           []string
	LostResponses     []string
	Unexpected        []string
	Duplicate         []Duplicate
	Late              []LateResult
	Reordered         []ReorderedResult
	Invalid           []string
	IsComplete        bool
	HasIntegrityFault bool
	IncompleteReasons []string
}

// Reconcile сопоставляет snapshots без I/O и fail closed обрабатывает пустые,
// неполные и некорректные записи.
func Reconcile(client Snapshot, internal InternalSnapshot) Reconciliation {
	result := Reconciliation{
		Missing:           []string{},
		LostResponses:     []string{},
		Unexpected:        []string{},
		Duplicate:         []Duplicate{},
		Late:              []LateResult{},
		Reordered:         []ReorderedResult{},
		Invalid:           []string{},
		IncompleteReasons: []string{},
	}
	if !client.IsComplete || len(client.IncompleteReasons) > 0 {
		result.IncompleteReasons = append(
			result.IncompleteReasons,
			client.IncompleteReasons...,
		)
		if len(client.IncompleteReasons) == 0 {
			result.IncompleteReasons = append(
				result.IncompleteReasons,
				"client_snapshot_incomplete",
			)
		}
	}
	if !internal.IsComplete || len(internal.IncompleteReasons) > 0 {
		result.IncompleteReasons = append(
			result.IncompleteReasons,
			internal.IncompleteReasons...,
		)
		if len(internal.IncompleteReasons) == 0 {
			result.IncompleteReasons = append(
				result.IncompleteReasons,
				"internal_snapshot_incomplete",
			)
		}
	}
	if len(client.Records) == 0 {
		result.IncompleteReasons = append(result.IncompleteReasons, "client_ledger_empty")
	}
	if len(internal.Records) == 0 {
		result.IncompleteReasons = append(result.IncompleteReasons, "internal_ledger_empty")
	}

	clientByID := make(map[string]ClientRecord, len(client.Records))
	clientSequences := make(
		map[struct {
			class    TrafficClass
			sequence uint64
		}]string,
		len(client.Records),
	)
	clientCompletions := make(map[uint64]string, len(client.Records))
	for _, record := range client.Records {
		if !validClientRecord(record) {
			result.Invalid = append(result.Invalid, record.RequestID)
			continue
		}
		if _, exists := clientByID[record.RequestID]; exists {
			result.Invalid = append(result.Invalid, record.RequestID)
			continue
		}
		sequenceKey := struct {
			class    TrafficClass
			sequence uint64
		}{
			class:    record.Class,
			sequence: record.Sequence,
		}
		if _, exists := clientSequences[sequenceKey]; exists {
			result.Invalid = append(result.Invalid, record.RequestID)
			continue
		}
		if _, exists := clientCompletions[record.CompletionSequence]; exists {
			result.Invalid = append(result.Invalid, record.RequestID)
			continue
		}
		clientByID[record.RequestID] = record
		clientSequences[sequenceKey] = record.RequestID
		clientCompletions[record.CompletionSequence] = record.RequestID
	}
	clientCompletionOrder := make([]ClientRecord, 0, len(clientByID))
	for _, record := range clientByID {
		clientCompletionOrder = append(clientCompletionOrder, record)
	}
	slices.SortStableFunc(clientCompletionOrder, func(left, right ClientRecord) int {
		return cmp.Or(
			cmp.Compare(left.CompletionSequence, right.CompletionSequence),
			cmp.Compare(left.RequestID, right.RequestID),
		)
	})
	maxClientSequence := make(map[string]uint64, len(clientCompletionOrder))
	for _, record := range clientCompletionOrder {
		if record.InternalSequence == 0 || record.Outcome == OutcomeUnknown {
			continue
		}
		sequenceKey := ledgerSequenceKey(
			record.Source,
			record.DataCenter,
			record.Class,
		)
		previousSequence := maxClientSequence[sequenceKey]
		if record.InternalSequence < previousSequence {
			result.Reordered = append(result.Reordered, ReorderedResult{
				RequestID:        record.RequestID,
				Stage:            "client_response",
				PreviousSequence: previousSequence,
				Sequence:         record.InternalSequence,
			})
		} else {
			maxClientSequence[sequenceKey] = record.InternalSequence
		}
	}

	internalByID := make(map[string][]InternalRecord, len(internal.Records))
	idempotencyOwners := make(map[string]string, len(internal.Records))
	maxInternalSequence := make(map[string]uint64, len(internal.Records))
	for _, record := range internal.Records {
		if !validInternalRecord(record) {
			result.Invalid = append(result.Invalid, record.RequestID)
			continue
		}
		internalByID[record.RequestID] = append(internalByID[record.RequestID], record)

		if record.IdempotencyKeySHA256 != "" {
			owner, exists := idempotencyOwners[record.IdempotencyKeySHA256]
			if exists && owner != record.RequestID {
				result.Duplicate = append(result.Duplicate, Duplicate{
					RequestID:         record.RequestID,
					IdempotencyDigest: record.IdempotencyKeySHA256,
					Count:             2,
				})
			} else {
				idempotencyOwners[record.IdempotencyKeySHA256] = record.RequestID
			}
		}

		sequenceKey := ledgerSequenceKey(
			record.Source,
			record.DataCenter,
			record.Class,
		)
		previousSequence := maxInternalSequence[sequenceKey]
		if record.Sequence < previousSequence {
			result.Reordered = append(result.Reordered, ReorderedResult{
				RequestID:        record.RequestID,
				Stage:            "internal_ledger",
				PreviousSequence: previousSequence,
				Sequence:         record.Sequence,
			})
		} else {
			maxInternalSequence[sequenceKey] = record.Sequence
		}
	}

	for requestID, record := range clientByID {
		internalRecords := internalByID[requestID]
		if !record.Dispatched {
			if len(internalRecords) > 0 {
				result.Unexpected = append(result.Unexpected, requestID)
			}
			continue
		}
		if record.Duplicate {
			result.Duplicate = append(result.Duplicate, Duplicate{
				RequestID:         requestID,
				IdempotencyDigest: digestString(record.IdempotencyKey),
				Count:             2,
			})
		}
		if record.FinishedOffset > record.DeadlineOffset {
			result.Late = append(result.Late, LateResult{
				RequestID:       requestID,
				DeadlineOffset:  record.DeadlineOffset,
				CompletedOffset: record.FinishedOffset,
			})
		}
		if len(internalRecords) == 0 {
			result.Missing = append(result.Missing, requestID)
			continue
		}
		if record.Outcome != OutcomeSuccess {
			result.LostResponses = append(result.LostResponses, requestID)
		}
		if len(internalRecords) > 1 {
			result.Duplicate = append(result.Duplicate, Duplicate{
				RequestID:         requestID,
				IdempotencyDigest: digestString(record.IdempotencyKey),
				Count:             len(internalRecords),
			})
		}
		for _, internalRecord := range internalRecords {
			if !recordsSemanticallyMatch(record, internalRecord) {
				result.Invalid = append(result.Invalid, requestID)
			}
			if record.Class == TrafficClassMutating &&
				internalRecord.IdempotencyKeySHA256 != digestString(record.IdempotencyKey) {
				result.Invalid = append(result.Invalid, requestID)
			}
			if internalRecord.Attempts > 1 {
				result.Duplicate = append(result.Duplicate, Duplicate{
					RequestID:         requestID,
					IdempotencyDigest: internalRecord.IdempotencyKeySHA256,
					Count:             int(internalRecord.Attempts),
				})
			}
			if internalRecord.CompletedOffset > 0 &&
				internalRecord.CompletedOffset > record.DeadlineOffset {
				result.Late = append(result.Late, LateResult{
					RequestID:       requestID,
					DeadlineOffset:  record.DeadlineOffset,
					CompletedOffset: internalRecord.CompletedOffset,
				})
			}
		}
	}

	for requestID := range internalByID {
		if _, exists := clientByID[requestID]; !exists {
			result.Unexpected = append(result.Unexpected, requestID)
		}
	}

	slices.Sort(result.Missing)
	slices.Sort(result.LostResponses)
	slices.Sort(result.Unexpected)
	result.Invalid = uniqueSortedStrings(result.Invalid)
	slices.SortFunc(result.Duplicate, func(left, right Duplicate) int {
		return cmp.Or(
			cmp.Compare(left.RequestID, right.RequestID),
			cmp.Compare(left.IdempotencyDigest, right.IdempotencyDigest),
		)
	})
	slices.SortFunc(result.Late, func(left, right LateResult) int {
		return cmp.Compare(left.RequestID, right.RequestID)
	})
	slices.SortFunc(result.Reordered, func(left, right ReorderedResult) int {
		return cmp.Or(
			cmp.Compare(left.Stage, right.Stage),
			cmp.Compare(left.Sequence, right.Sequence),
			cmp.Compare(left.RequestID, right.RequestID),
		)
	})
	result.IncompleteReasons = normalizedReasons(result.IncompleteReasons)
	result.HasIntegrityFault = len(result.Missing) > 0 ||
		len(result.LostResponses) > 0 ||
		len(result.Unexpected) > 0 ||
		len(result.Duplicate) > 0 ||
		len(result.Late) > 0 ||
		len(result.Reordered) > 0 ||
		len(result.Invalid) > 0
	result.IsComplete = len(result.IncompleteReasons) == 0 &&
		len(result.Missing) == 0 &&
		len(result.Unexpected) == 0 &&
		len(result.Duplicate) == 0 &&
		len(result.Invalid) == 0

	return result
}

func recordsSemanticallyMatch(client ClientRecord, internal InternalRecord) bool {
	return client.RequestID == internal.RequestID &&
		client.Class == internal.Class &&
		client.RouteID == internal.RouteID &&
		client.DataCenter == internal.DataCenter &&
		client.Source == internal.Source &&
		client.InternalSequence == internal.Sequence
}

func digestString(value string) string {
	if value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func ledgerSequenceKey(
	source string,
	dataCenter DataCenter,
	class TrafficClass,
) string {
	if source != "" {
		return source + "/" + string(class)
	}

	return string(dataCenter) + "/" + string(class)
}

func uniqueSortedStrings(values []string) []string {
	slices.Sort(values)
	return slices.Compact(values)
}

func validClientRecord(record ClientRecord) bool {
	if !validateRequestID(record.RequestID) || !validateClass(record.Class) {
		return false
	}
	if record.Sequence == 0 || record.CompletionSequence == 0 {
		return false
	}
	if record.Class == TrafficClassMutating && record.IdempotencyKey != record.RequestID {
		return false
	}
	if record.Class == TrafficClassRead && record.IdempotencyKey != "" {
		return false
	}
	if !validateOutcome(record.Outcome) {
		return false
	}
	if record.ScheduledOffset < 0 || record.FinishedOffset < record.ScheduledOffset {
		return false
	}
	if !validateDimension(record.RouteID, record.Outcome != OutcomeSuccess) ||
		!validateDataCenter(record.DataCenter, record.Outcome != OutcomeSuccess) ||
		!validateDimension(record.Source, record.Outcome != OutcomeSuccess) {
		return false
	}
	if record.Dispatched {
		if record.Outcome == OutcomeBackpressure ||
			record.DeadlineOffset <= record.StartedOffset ||
			record.FinishedOffset < record.StartedOffset ||
			record.Latency != record.FinishedOffset-record.StartedOffset {
			return false
		}
	} else if (record.Outcome != OutcomeBackpressure && record.Outcome != OutcomeCanceled) ||
		record.StartedOffset != 0 ||
		record.DeadlineOffset != 0 ||
		record.Latency != 0 ||
		record.RouteID != "" ||
		record.DataCenter != DataCenterUnknown ||
		record.Source != "" ||
		record.InternalSequence != 0 ||
		record.Duplicate {
		return false
	}

	return true
}

func validInternalRecord(record InternalRecord) bool {
	if !validateRequestID(record.RequestID) || !validateClass(record.Class) {
		return false
	}
	if record.Class == TrafficClassMutating && !validateDigest(record.IdempotencyKeySHA256) {
		return false
	}
	if record.Class == TrafficClassRead && record.IdempotencyKeySHA256 != "" {
		return false
	}
	if record.Sequence == 0 || record.Attempts == 0 {
		return false
	}
	if !validateOutcome(record.Outcome) {
		return false
	}
	if record.CompletedOffset < record.AcceptedOffset {
		return false
	}
	if !validateDimension(record.RouteID, record.Outcome != OutcomeSuccess) ||
		!validateDataCenter(record.DataCenter, record.Outcome != OutcomeSuccess) ||
		!validateDimension(record.Source, record.Outcome != OutcomeSuccess) {
		return false
	}

	return true
}

func validateDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		isDigit := character >= '0' && character <= '9'
		isLowerHex := character >= 'a' && character <= 'f'
		if !isDigit && !isLowerHex {
			return false
		}
	}

	return true
}
