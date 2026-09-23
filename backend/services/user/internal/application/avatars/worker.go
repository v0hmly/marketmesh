package avatars

import (
	"context"
	"errors"
	"time"

	"github.com/v0hmly/marketmesh/services/user/internal/domain/avatar"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
)

type Retirement struct {
	SubjectID  profile.SubjectID
	FileID     avatar.FileID
	LeaseToken [16]byte
	Attempts   int
}
type Queue interface {
	Claim(context.Context, time.Duration) (Retirement, bool, error)
	Finish(context.Context, Retirement) (bool, error)
	Retry(context.Context, Retirement, time.Duration) (bool, error)
}

// CleanupError exposes a bounded diagnostic class and file identifier, never a raw RPC error.
type CleanupError struct {
	Class  string
	FileID avatar.FileID
}

func (e CleanupError) Error() string { return "avatar cleanup: " + e.Class }

type Worker struct {
	queue Queue
	files Files
}

func NewWorker(queue Queue, files Files) (*Worker, error) {
	if queue == nil || files == nil {
		return nil, errors.New("avatar cleanup: required dependency missing")
	}
	return &Worker{queue: queue, files: files}, nil
}

// Step claims at most one retirement. Files owns idempotent physical cleanup.
func (w *Worker) Step(ctx context.Context) error {
	if ctx == nil {
		return errors.New("avatar cleanup: context required")
	}
	record, ok, err := w.queue.Claim(ctx, 30*time.Second)
	if err != nil {
		return CleanupError{Class: "claim"}
	}
	if !ok {
		return nil
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = w.files.Retire(call, record.SubjectID, record.FileID)
	cancel()
	if err == nil {
		finished, err := w.queue.Finish(ctx, record)
		if err != nil {
			return CleanupError{Class: "finish", FileID: record.FileID}
		}
		if !finished {
			return CleanupError{Class: "lease_lost", FileID: record.FileID}
		}
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	delay := time.Second << min(max(record.Attempts-1, 0), 8)
	if _, retryErr := w.queue.Retry(ctx, record, delay); retryErr != nil {
		return CleanupError{Class: "retry", FileID: record.FileID}
	}
	return CleanupError{Class: "files_unavailable", FileID: record.FileID}
}
