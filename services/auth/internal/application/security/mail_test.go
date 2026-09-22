package security

import (
	"context"
	"errors"
	"testing"
	"time"

	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/security"
)

type queueProbe struct {
	lease       MailLease
	outcome     string
	next        time.Time
	finishError error
}

func (q *queueProbe) ClaimMail(context.Context, time.Time) (MailLease, bool, error) {
	return q.lease, true, nil
}
func (q *queueProbe) FinishMail(_ context.Context, lease MailLease, _ time.Time, outcome string, next time.Time) error {
	if lease.Token != q.lease.Token {
		panic("wrong lease")
	}
	q.outcome, q.next = outcome, next
	return q.finishError
}

type senderProbe struct {
	calls     int
	err       error
	permanent bool
}

func (s *senderProbe) Send(ctx context.Context, _ Mail) error {
	s.calls++
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded send")
	}
	return s.err
}
func (s *senderProbe) Permanent(error) bool { return s.permanent }

type observerProbe struct{ outcomes []string }

func (o *observerProbe) MailResult(value string) { o.outcomes = append(o.outcomes, value) }
func TestMailWorkerDeliveryOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, outcome                           string
		attempts                                int
		expired, permanent, failed, finishFails bool
	}{
		{name: "accepted", outcome: "delivered", attempts: 1},
		{name: "transient disconnect", outcome: "retry", attempts: 2, failed: true},
		{name: "permanent rejection", outcome: "rejected", attempts: 2, failed: true, permanent: true},
		{name: "exhausted retries", outcome: "exhausted", attempts: 12, failed: true},
		{name: "expired code", outcome: "expired", attempts: 1, expired: true},
		{name: "lost completion", outcome: "delivered", attempts: 1, finishFails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UTC()
			expires := now.Add(time.Minute)
			if tc.expired {
				expires = now
			}
			q := &queueProbe{lease: MailLease{Mail: Mail{ExpiresAt: expires}, Token: domain.ID{3}, Attempts: tc.attempts}}
			if tc.finishFails {
				q.finishError = errors.New("database unavailable")
			}
			sender := &senderProbe{permanent: tc.permanent}
			if tc.failed {
				sender.err = errors.New("transient")
			}
			observer := &observerProbe{}
			worker, err := NewMailWorker(q, sender, observer)
			if err != nil {
				t.Fatal(err)
			}
			worked, err := worker.Once(t.Context(), now)
			if !worked || (err != nil) != tc.finishFails {
				t.Fatal("unexpected completion")
			}
			if q.outcome != tc.outcome {
				t.Fatal(q.outcome)
			}
			if tc.expired && sender.calls != 0 {
				t.Fatal("expired secret sent")
			}
			if tc.outcome == "retry" {
				if !q.next.After(now.Add(3*time.Second)) || q.next.After(now.Add(10*time.Second)) {
					t.Fatal("retry was not durably delayed")
				}
			} else if !q.next.IsZero() {
				t.Fatal("terminal result rescheduled")
			}
			if tc.finishFails && len(observer.outcomes) != 0 {
				t.Fatal("uncommitted outcome reported")
			}
		})
	}
}
