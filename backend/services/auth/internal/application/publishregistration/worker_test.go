package publishregistration

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

type testStore struct {
	claims, marks, retries                  int
	delay                                   time.Duration
	claimErr, markErr, retryErr, pendingErr error
	lose                                    bool
}

func (s *testStore) Pending(context.Context) (int64, time.Time, error) {
	return 1, time.Unix(100, 0), s.pendingErr
}
func (s *testStore) Claim(context.Context, time.Duration) (Record, bool, error) {
	s.claims++
	return Record{ID: [16]byte{1}, LeaseToken: [16]byte{2}, Payload: []byte{3}, Attempts: 3}, true, s.claimErr
}
func (s *testStore) MarkPublished(context.Context, Record) (bool, error) {
	s.marks++
	return !s.lose, s.markErr
}
func (s *testStore) Retry(_ context.Context, _ Record, d time.Duration) (bool, error) {
	s.retries++
	s.delay = d
	return !s.lose, s.retryErr
}

type publisherFunc func(context.Context, Record) error

func (f publisherFunc) Publish(c context.Context, r Record) error { return f(c, r) }

type testObserver struct {
	outcomes []Outcome
	count    int64
	age      time.Duration
	after    func()
}

func (o *testObserver) ObserveBacklog(_ context.Context, c int64, a time.Duration) {
	o.count = c
	o.age = a
}
func (o *testObserver) Attempt(_ context.Context, out Outcome) {
	o.outcomes = append(o.outcomes, out)
	if o.after != nil {
		o.after()
	}
}
func TestAckOrderingRetryAndLeaseLoss(t *testing.T) {
	for _, tc := range []struct {
		name                string
		publishErr, markErr error
		lose                bool
		want                Outcome
		marks, retries      int
	}{{name: "ack", want: OutcomePublished, marks: 1}, {name: "publish error", publishErr: errors.New("secret"), want: OutcomePublishError, retries: 1}, {name: "mark failed", markErr: errors.New("secret"), want: OutcomeStoreError, marks: 1}, {name: "lease lost", lose: true, want: OutcomeLeaseLost, marks: 1}} {
		t.Run(tc.name, func(t *testing.T) {
			s := &testStore{markErr: tc.markErr, lose: tc.lose}
			o := &testObserver{}
			w, err := New(s, publisherFunc(func(ctx context.Context, r Record) error {
				if s.marks != 0 || s.retries != 0 {
					t.Fatal("store completion happened before publish")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("publish has no deadline")
				}
				return tc.publishErr
			}), o, Config{BatchSize: 1, Clock: func() time.Time { return time.Unix(110, 0) }})
			if err != nil {
				t.Fatal(err)
			}
			w.batch(context.Background())
			if s.marks != tc.marks || s.retries != tc.retries || len(o.outcomes) != 1 || o.outcomes[0] != tc.want {
				t.Fatalf("marks=%d retries=%d outcomes=%v", s.marks, s.retries, o.outcomes)
			}
			if s.retries > 0 && s.delay != 4*time.Second {
				t.Fatal("unexpected retry delay")
			}
			if o.count != 1 || o.age != 10*time.Second {
				t.Fatal("backlog observation mismatch")
			}
		})
	}
}
func TestCancellationLeavesLease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &testStore{}
	o := &testObserver{}
	w, _ := New(s, publisherFunc(func(context.Context, Record) error { cancel(); return nil }), o, Config{})
	w.batch(ctx)
	if s.claims != 1 || s.marks != 0 || s.retries != 0 || len(o.outcomes) != 0 {
		t.Fatal("cancellation mutated leased record")
	}
	w.batch(ctx)
	if s.claims != 1 {
		t.Fatal("cancelled worker queried")
	}
}
func TestBoundedBatchAndBackoff(t *testing.T) {
	s := &testStore{}
	w, _ := New(s, publisherFunc(func(context.Context, Record) error { return nil }), &testObserver{}, Config{BatchSize: 3})
	w.batch(context.Background())
	if s.claims != 3 || s.marks != 3 {
		t.Fatal("batch exceeded configured bound")
	}
	for _, attempt := range []int{30, 100, math.MaxInt} {
		if got := w.backoff(attempt); got != time.Minute {
			t.Fatalf("backoff=%v", got)
		}
	}
}
func TestRunSurvivesDependencyFailureAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &testStore{claimErr: errors.New("database unavailable")}
	o := &testObserver{}
	o.after = func() {
		if len(o.outcomes) == 3 {
			cancel()
		}
	}
	w, _ := New(s, publisherFunc(func(context.Context, Record) error { t.Fatal("publisher called"); return nil }), o, Config{PollInterval: time.Millisecond})
	if err := w.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if s.claims != 3 {
		t.Fatal("dependency outage terminated worker")
	}
	if err := w.Run(context.Background()); err == nil {
		t.Fatal("worker reused")
	}
}
func TestConstructorAndContextValidation(t *testing.T) {
	s, p, o := &testStore{}, publisherFunc(func(context.Context, Record) error { return nil }), &testObserver{}
	if _, err := New(nil, p, o, Config{}); err == nil {
		t.Fatal("nil store accepted")
	}
	if _, err := New(s, nil, o, Config{}); err == nil {
		t.Fatal("nil publisher accepted")
	}
	if _, err := New(s, p, nil, Config{}); err == nil {
		t.Fatal("nil observer accepted")
	}
	for _, c := range []Config{{BatchSize: -1}, {LeaseDuration: time.Second, PublishTimeout: time.Second}, {RetryInitial: time.Minute, RetryMax: time.Second}, {PollInterval: -time.Second}} {
		if _, err := New(s, p, o, c); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	w, _ := New(s, p, o, Config{})
	if err := w.Run(nil); err == nil {
		t.Fatal("nil context accepted")
	}
}

func TestPublishDeadlineSchedulesRetry(t *testing.T) {
	s := &testStore{}
	o := &testObserver{}
	w, err := New(s, publisherFunc(func(ctx context.Context, _ Record) error { <-ctx.Done(); return ctx.Err() }), o, Config{BatchSize: 1, PublishTimeout: time.Millisecond, LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	w.batch(context.Background())
	if s.marks != 0 || s.retries != 1 || len(o.outcomes) != 1 || o.outcomes[0] != OutcomePublishError {
		t.Fatal("publish timeout did not retry")
	}
}

func TestBacklogMetricsFailureDoesNotGateDelivery(t *testing.T) {
	s := &testStore{pendingErr: context.DeadlineExceeded}
	o := &testObserver{}
	w, err := New(s, publisherFunc(func(context.Context, Record) error { return nil }), o, Config{BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	w.batch(context.Background())
	if s.claims != 1 || s.marks != 1 {
		t.Fatal("aggregate timeout blocked indexed delivery")
	}
	if o.count != 0 || o.age != 0 {
		t.Fatal("failed backlog read reported misleading metrics")
	}
	if len(o.outcomes) != 2 || o.outcomes[0] != OutcomeStoreError || o.outcomes[1] != OutcomePublished {
		t.Fatal("unexpected outcomes", o.outcomes)
	}
}
