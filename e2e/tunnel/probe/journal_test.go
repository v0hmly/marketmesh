package probe

import "testing"

func TestJournalSteadyUsesCompletionOrder(t *testing.T) {
	t.Parallel()

	clock := newManualClock(t)
	clientJournal := newJournal(clock, clock.Now(), 2, 8)
	first := Request{ID: requestID1, Class: TrafficClassRead, Sequence: 1}
	second := Request{ID: requestID2, Class: TrafficClassRead, Sequence: 2}
	for _, request := range []Request{first, second} {
		if err := clientJournal.schedule(request); err != nil {
			t.Fatalf("journal.schedule(%q) error = %v", request.ID, err)
		}
		if err := clientJournal.start(request.ID, defaultTestConfig().RequestTimeout); err != nil {
			t.Fatalf("journal.start(%q) error = %v", request.ID, err)
		}
	}

	if err := clientJournal.finish(second.ID, Response{Outcome: OutcomeSuccess}); err != nil {
		t.Fatalf("journal.finish(second) error = %v", err)
	}
	if err := clientJournal.finish(first.ID, Response{Outcome: OutcomeUnavailable}); err != nil {
		t.Fatalf("journal.finish(first) error = %v", err)
	}

	state, reached, err := clientJournal.steady(SteadyRequirement{ReadSuccesses: 1})
	if err != nil {
		t.Fatalf("journal.steady() error = %v", err)
	}
	if reached || state.ReadSuccesses != 0 {
		t.Fatalf("journal.steady() = %#v, %v; want zero streak after latest failure", state, reached)
	}
}
