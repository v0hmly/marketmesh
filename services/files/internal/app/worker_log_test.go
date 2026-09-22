package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/v0hmly/marketmesh/services/files/internal/application/processing"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

type logRepository struct {
	processing.Repository
	claimError error
}

func (r logRepository) Claim(context.Context) (file.Record, error) {
	if r.claimError != nil {
		return file.Record{}, r.claimError
	}
	return file.Record{ID: file.ID{1}, State: file.Replicating, Version: 2}, nil
}
func (r logRepository) Transition(_ context.Context, v file.Record, to file.State) (file.Record, error) {
	v.State = to
	return v, nil
}

type logStorage struct {
	processing.Storage
	err error
}

func (s logStorage) Replicate(context.Context, file.Record) error { return s.err }

type unusedScanner struct{ processing.Scanner }
type unusedReconstructor struct{ processing.Reconstructor }

func TestWorkerLogsSafeContextAndDoesNotHideMissingObjects(t *testing.T) {
	const secret = "https://private/key?X-Amz-Signature=secret"
	for _, tc := range []struct {
		name, class, stage string
		claimErr, jobErr   error
		silent             bool
	}{
		{name: "success", silent: true},
		{name: "empty queue", claimErr: file.ErrNotFound, silent: true},
		{name: "database", claimErr: fmt.Errorf("%s: %w", secret, file.ErrUnavailable), class: "unavailable", stage: "claim"},
		{name: "missing object", jobErr: fmt.Errorf("%s: %w", secret, file.ErrNotFound), class: "not_found", stage: "replicate"},
		{name: "replica", jobErr: file.ErrUnavailable, class: "unavailable", stage: "replicate"},
		{name: "stale worker", jobErr: file.ErrConflict, class: "conflict", stage: "replicate"},
		{name: "rejected", jobErr: file.ErrRejected, class: "rejected", stage: "replicate"},
		{name: "timeout", jobErr: context.DeadlineExceeded, class: "deadline_exceeded", stage: "replicate"},
		{name: "unknown", jobErr: errors.New(secret), class: "unknown", stage: "replicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			worker, err := processing.New(logRepository{claimError: tc.claimErr}, logStorage{err: tc.jobErr}, unusedScanner{}, unusedReconstructor{}, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = worker.Step(context.Background())
			logWorkerError(context.Background(), slog.New(slog.NewJSONHandler(&out, nil)), err)
			if tc.silent {
				if out.Len() != 0 {
					t.Fatal("routine step logged", out.String())
				}
				return
			}
			if strings.Contains(out.String(), "private") || strings.Contains(out.String(), "Signature") {
				t.Fatal("dependency error leaked")
			}
			var event map[string]any
			if err := json.Unmarshal(out.Bytes(), &event); err != nil {
				t.Fatal(err)
			}
			if event["stage"] != tc.stage || event["error_class"] != tc.class {
				t.Fatal("missing failure context", event)
			}
			if tc.stage != "claim" {
				if event["file_id"] != (file.ID{1}).String() || event["state"] != string(file.Replicating) {
					t.Fatal("missing job identity", event)
				}
			} else if _, exists := event["file_id"]; exists {
				t.Fatal("fabricated file ID for failed claim")
			}
		})
	}
}
