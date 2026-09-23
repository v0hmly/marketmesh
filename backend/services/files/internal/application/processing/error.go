package processing

import (
	"context"
	"errors"

	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

// JobError identifies the failed job and stage without exposing dependency output.
// Unwrap preserves error matching; logs must use Class, never the underlying cause.
type JobError struct {
	FileID file.ID
	State  file.State
	Stage  string
	cause  error
}

func (e *JobError) Error() string { return "files worker: " + e.Stage + ": " + e.Class() }
func (e *JobError) Unwrap() error { return e.cause }

func (e *JobError) Class() string {
	// A failed rejection CAS must remain visible as an infrastructure/conflict error.
	for _, candidate := range []struct {
		err   error
		class string
	}{
		{context.DeadlineExceeded, "deadline_exceeded"},
		{context.Canceled, "canceled"},
		{file.ErrUnavailable, "unavailable"},
		{file.ErrConflict, "conflict"},
		{file.ErrRejected, "rejected"},
		{file.ErrNotFound, "not_found"},
		{file.ErrInvalid, "invalid"},
		{file.ErrLimit, "limit"},
	} {
		if errors.Is(e.cause, candidate.err) {
			return candidate.class
		}
	}
	return "unknown"
}
