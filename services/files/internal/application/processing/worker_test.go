package processing

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

type repository struct {
	record    file.Record
	markError bool
}

func (r *repository) Claim(context.Context) (file.Record, error) {
	r.record.Version++
	return r.record, nil
}
func (r *repository) Transition(_ context.Context, expected file.Record, to file.State) (file.Record, error) {
	if r.record.Version != expected.Version || !file.CanTransition(r.record.State, to) {
		return file.Record{}, file.ErrConflict
	}
	r.record.State = to
	r.record.Version++
	return r.record, nil
}
func (r *repository) MarkClean(ctx context.Context, expected file.Record, format file.Format, size int64, sum file.Digest) (file.Record, error) {
	if r.markError {
		r.markError = false
		return file.Record{}, file.ErrUnavailable
	}
	next, err := r.Transition(ctx, expected, file.Replicating)
	if err != nil {
		return next, err
	}
	r.record.CleanFormat = format
	r.record.CleanSize = size
	r.record.CleanSHA256 = sum
	return r.record, nil
}
func (r *repository) DeferCleanup(context.Context, file.Record) error { return nil }

type storage struct {
	replicationError           error
	afterReplicate             func()
	puts, replications, purges int
}

func (s *storage) ReadUpload(_ context.Context, _ file.Record, w io.Writer) error {
	_, err := io.WriteString(w, "untrusted")
	return err
}
func (s *storage) PutClean(_ context.Context, _ file.Record, _ file.Format, r io.ReadSeeker, size int64) (file.Digest, error) {
	s.puts++
	data, err := io.ReadAll(r)
	if err != nil || int64(len(data)) != size {
		return file.Digest{}, file.ErrRejected
	}
	return file.Digest(sha256.Sum256(data)), nil
}
func (s *storage) Replicate(context.Context, file.Record) error {
	s.replications++
	if s.afterReplicate != nil {
		s.afterReplicate()
	}
	return s.replicationError
}
func (s *storage) Purge(context.Context, file.Record) error        { s.purges++; return nil }
func (s *storage) CleanupReady(context.Context, file.Record) error { return nil }

type scan struct {
	err   error
	calls int
}

func (s *scan) Scan(context.Context, io.Reader, int64) error { s.calls++; return s.err }

type reconstruct struct {
	calls int
	err   error
}

func (r *reconstruct) Clean(_ context.Context, _ io.Reader, _ int64, _ file.Format, w io.Writer) (file.Format, error) {
	r.calls++
	if r.err != nil {
		return "", r.err
	}
	_, err := io.WriteString(w, "passive pixels")
	return file.PNG, err
}

func workerFixture(t *testing.T) (*Worker, *repository, *storage, *scan, *reconstruct) {
	t.Helper()
	r := &repository{record: file.Record{ID: file.ID{1}, State: file.Scanning, Version: 1}}
	s := &storage{}
	av := &scan{}
	cdr := &reconstruct{}
	w, err := New(r, s, av, cdr, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return w, r, s, av, cdr
}
func TestReadyRequiresSuccessfulAVCDRAndReplication(t *testing.T) {
	for _, stage := range []string{"AV unavailable", "AV rejected", "CDR unavailable", "CDR rejected", "replica unavailable"} {
		t.Run(stage, func(t *testing.T) {
			w, r, s, av, cdr := workerFixture(t)
			switch stage {
			case "AV unavailable":
				av.err = file.ErrUnavailable
			case "AV rejected":
				av.err = file.ErrRejected
			case "CDR unavailable":
				cdr.err = file.ErrUnavailable
			case "CDR rejected":
				cdr.err = file.ErrRejected
			case "replica unavailable":
				s.replicationError = file.ErrUnavailable
			}
			err := w.Step(context.Background())
			if stage == "replica unavailable" {
				if err != nil || r.record.State != file.Replicating {
					t.Fatal("missing replication boundary", err)
				}
				err = w.Step(context.Background())
			}
			if err == nil || r.record.State == file.Ready {
				t.Fatal("failed dependency published file")
			}
			if av.err != nil && s.puts != 0 {
				t.Fatal("AV bypassed")
			}
			entries, e := os.ReadDir(w.tempDir)
			if e != nil || len(entries) != 0 {
				t.Fatal("private temporary files retained")
			}
		})
	}
}
func TestWorkerRestartAndDeletionFence(t *testing.T) {
	w, r, s, _, _ := workerFixture(t)
	r.markError = true
	if err := w.Step(context.Background()); !errors.Is(err, file.ErrUnavailable) {
		t.Fatal("missing simulated lost write")
	}
	if r.record.State != file.Scanning {
		t.Fatal("unacknowledged clean advanced state")
	}
	if err := w.Step(context.Background()); err != nil || r.record.State != file.Replicating {
		t.Fatal("retry failed", err)
	}
	s.afterReplicate = func() { r.record.State = file.Deleted; r.record.Version++ }
	if err := w.Step(context.Background()); !errors.Is(err, file.ErrConflict) || r.record.State != file.Deleted {
		t.Fatal("stale worker resurrected deleted file", err)
	}
	if err := w.Step(context.Background()); err != nil || s.purges != 1 {
		t.Fatal("cleanup not reconciled", err)
	}
}
