package security

import (
	"context"
	"time"

	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/security"
)

type MailLease struct {
	Mail     Mail
	Token    domain.ID
	Attempts int
}
type MailQueue interface {
	ClaimMail(context.Context, time.Time) (MailLease, bool, error)
	FinishMail(context.Context, MailLease, time.Time, string, time.Time) error
}
type MailSender interface {
	Send(context.Context, Mail) error
	Permanent(error) bool
}
type MailObserver interface{ MailResult(outcome string) }

type MailWorker struct {
	queue    MailQueue
	sender   MailSender
	observer MailObserver
}

func NewMailWorker(queue MailQueue, sender MailSender, observer MailObserver) (*MailWorker, error) {
	if queue == nil || sender == nil || observer == nil {
		return nil, domain.Unavailable
	}
	return &MailWorker{queue, sender, observer}, nil
}
func (w *MailWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		// Drain a bounded batch before sleeping; old retrying messages are ordered
		// by available_at so they cannot starve freshly queued login codes.
		for range 20 {
			worked, err := w.Once(ctx, time.Now().UTC())
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				w.observer.MailResult("queue_unavailable")
				break
			}
			if !worked {
				break
			}
		}
	}
}
func (w *MailWorker) Once(ctx context.Context, now time.Time) (bool, error) {
	lease, found, err := w.queue.ClaimMail(ctx, now)
	if err != nil || !found {
		return found, err
	}
	outcome := "delivered"
	if !now.Before(lease.Mail.ExpiresAt) {
		outcome = "expired"
	} else {
		sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = w.sender.Send(sendCtx, lease.Mail)
		cancel()
		if err != nil {
			outcome = "retry"
			if w.sender.Permanent(err) {
				outcome = "rejected"
			} else if lease.Attempts >= 12 {
				outcome = "exhausted"
			}
		}
	}
	finished := time.Now().UTC()
	if outcome == "retry" && !finished.Before(lease.Mail.ExpiresAt) {
		outcome = "expired"
	}
	delay := time.Second * time.Duration(1<<min(lease.Attempts, 8))
	next := finished.Add(delay)
	if outcome != "retry" {
		next = time.Time{}
	}
	if err := w.queue.FinishMail(ctx, lease, finished, outcome, next); err != nil {
		return true, err
	}
	w.observer.MailResult(outcome)
	return true, nil
}
