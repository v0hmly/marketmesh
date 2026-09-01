package probe

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

type journal struct {
	mu                sync.Mutex
	clock             Clock
	startedAt         time.Time
	lastOffset        time.Duration
	maxRecords        int
	maxEvents         int
	records           map[string]ClientRecord
	recordOrder       []string
	events            []Event
	incompleteReasons []string
	updates           chan struct{}
}

func newJournal(
	clock Clock,
	startedAt time.Time,
	maxRecords int,
	maxEvents int,
) *journal {
	return &journal{
		clock:       clock,
		startedAt:   startedAt,
		maxRecords:  maxRecords,
		maxEvents:   maxEvents,
		records:     make(map[string]ClientRecord, maxRecords),
		recordOrder: make([]string, 0, maxRecords),
		events:      make([]Event, 0, maxEvents),
		updates:     make(chan struct{}, 1),
	}
}

func (journal *journal) schedule(request Request) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()

	if _, exists := journal.records[request.ID]; exists {
		journal.markIncompleteLocked("duplicate_request_id")
		return fmt.Errorf("%w: %q", ErrDuplicateRequest, request.ID)
	}
	if len(journal.records) >= journal.maxRecords {
		journal.markIncompleteLocked("client_journal_capacity")
		return ErrJournalCapacity
	}

	offset, err := journal.offsetLocked()
	if err != nil {
		return err
	}
	record := ClientRecord{
		RequestID:       request.ID,
		IdempotencyKey:  request.IdempotencyKey,
		Class:           request.Class,
		Sequence:        request.Sequence,
		ScheduledOffset: offset,
		Outcome:         OutcomeUnknown,
	}
	journal.records[request.ID] = record
	journal.recordOrder = append(journal.recordOrder, request.ID)

	return journal.appendEventLocked(Event{
		Kind:      EventKindRequestScheduled,
		Class:     request.Class,
		RequestID: request.ID,
	})
}

func (journal *journal) start(requestID string, timeout time.Duration) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()

	record, exists := journal.records[requestID]
	if !exists {
		journal.markIncompleteLocked("unknown_client_record")
		return errors.New("probe: starting unknown request")
	}

	offset, err := journal.offsetLocked()
	if err != nil {
		return err
	}
	record.StartedOffset = offset
	record.DeadlineOffset = offset + timeout
	record.Dispatched = true
	journal.records[requestID] = record

	return journal.appendEventLocked(Event{
		Kind:      EventKindRequestStarted,
		Class:     record.Class,
		RequestID: requestID,
	})
}

func (journal *journal) finish(requestID string, response Response) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()

	record, exists := journal.records[requestID]
	if !exists {
		journal.markIncompleteLocked("unknown_client_record")
		return errors.New("probe: finishing unknown request")
	}
	if record.Outcome != OutcomeUnknown {
		journal.markIncompleteLocked("duplicate_client_result")
		return errors.New("probe: request already has a terminal result")
	}

	offset, err := journal.offsetLocked()
	if err != nil {
		return err
	}
	record.FinishedOffset = offset
	if record.Dispatched {
		record.Latency = max(0, offset-record.StartedOffset)
	}
	record.Outcome = response.Outcome
	record.RouteID = response.RouteID
	record.DataCenter = response.DataCenter
	record.Source = response.Source
	record.InternalSequence = response.InternalSequence
	record.Duplicate = response.Duplicate

	err = journal.appendEventLocked(Event{
		Kind:       EventKindRequestFinished,
		Class:      record.Class,
		RequestID:  requestID,
		Outcome:    response.Outcome,
		RouteID:    response.RouteID,
		DataCenter: response.DataCenter,
	})
	if err == nil {
		record.CompletionSequence = journal.events[len(journal.events)-1].Sequence
	}
	journal.records[requestID] = record
	journal.signalLocked()

	return err
}

func (journal *journal) steady(requirement SteadyRequirement) (SteadyState, bool, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()

	if len(journal.incompleteReasons) > 0 {
		return SteadyState{}, false, ErrIncompleteRun
	}
	terminalRecords := make([]ClientRecord, 0, len(journal.records))
	for _, record := range journal.records {
		if record.Outcome != OutcomeUnknown {
			terminalRecords = append(terminalRecords, record)
		}
	}
	slices.SortFunc(terminalRecords, func(left, right ClientRecord) int {
		return cmp.Compare(left.CompletionSequence, right.CompletionSequence)
	})

	state := SteadyState{}
	for _, record := range terminalRecords {
		switch record.Class {
		case TrafficClassRead:
			if record.Outcome == OutcomeSuccess {
				state.ReadSuccesses++
			} else {
				state.ReadSuccesses = 0
			}
		case TrafficClassMutating:
			if record.Outcome == OutcomeSuccess {
				state.MutatingSuccesses++
			} else {
				state.MutatingSuccesses = 0
			}
		}
	}

	offset, err := journal.offsetLocked()
	if err != nil {
		return SteadyState{}, false, err
	}
	state.ObservedOffset = offset
	isReached := state.ReadSuccesses >= requirement.ReadSuccesses &&
		state.MutatingSuccesses >= requirement.MutatingSuccesses

	return state, isReached, nil
}

func (journal *journal) signalLocked() {
	select {
	case journal.updates <- struct{}{}:
	default:
	}
}

func (journal *journal) event(event Event) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()

	return journal.appendEventLocked(event)
}

func (journal *journal) appendEventLocked(event Event) error {
	if len(journal.events) >= journal.maxEvents {
		journal.markIncompleteLocked("event_journal_capacity")
		return ErrEventCapacity
	}

	offset, err := journal.offsetLocked()
	if err != nil {
		return err
	}
	event.Sequence = uint64(len(journal.events) + 1)
	event.Offset = offset
	journal.events = append(journal.events, event)

	return nil
}

func (journal *journal) offsetLocked() (time.Duration, error) {
	offset := journal.clock.Now().Sub(journal.startedAt)
	if offset < journal.lastOffset {
		journal.markIncompleteLocked("non_monotonic_clock")
		return 0, ErrNonMonotonicClock
	}
	journal.lastOffset = offset

	return offset, nil
}

func (journal *journal) markIncomplete(reason string) {
	journal.mu.Lock()
	defer journal.mu.Unlock()

	journal.markIncompleteLocked(reason)
}

func (journal *journal) markIncompleteLocked(reason string) {
	for _, existing := range journal.incompleteReasons {
		if existing == reason {
			return
		}
	}
	journal.incompleteReasons = append(journal.incompleteReasons, reason)
}

func (journal *journal) snapshot() Snapshot {
	journal.mu.Lock()
	defer journal.mu.Unlock()

	records := make([]ClientRecord, 0, len(journal.recordOrder))
	for _, requestID := range journal.recordOrder {
		record := journal.records[requestID]
		if record.Outcome == OutcomeUnknown {
			journal.markIncompleteLocked("client_result_missing")
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		journal.markIncompleteLocked("client_ledger_empty")
	}

	finishedOffset, err := journal.offsetLocked()
	if err != nil {
		finishedOffset = journal.lastOffset
	}

	reasons := slices.Clone(journal.incompleteReasons)
	return Snapshot{
		StartedAt:         journal.startedAt.Round(0).UTC(),
		FinishedOffset:    finishedOffset,
		Records:           records,
		Events:            slices.Clone(journal.events),
		IsComplete:        len(reasons) == 0,
		IncompleteReasons: reasons,
	}
}
