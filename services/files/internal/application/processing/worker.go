// Package processing advances durable jobs; file bytes never enter a control RPC.
package processing

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

const JobTimeout = 8 * time.Minute // Shorter than the repository's ten-minute lease.

type Repository interface {
	Claim(context.Context) (file.Record, error)
	Transition(context.Context, file.Record, file.State) (file.Record, error)
	MarkClean(context.Context, file.Record, file.Format, int64, file.Digest) (file.Record, error)
	DeferCleanup(context.Context, file.Record) error
}

type Storage interface {
	ReadUpload(context.Context, file.Record, io.Writer) error
	PutClean(context.Context, file.Record, file.Format, io.ReadSeeker, int64) (file.Digest, error)
	Replicate(context.Context, file.Record) error
	Purge(context.Context, file.Record) error
	CleanupReady(context.Context, file.Record) error
}

// Scanner accepts only exact CLEAN; infrastructure errors never become approval.
type Scanner interface {
	Scan(context.Context, io.Reader, int64) error
}

// Reconstructor accepts opaque bytes and returns a bounded passive derivative.
// Its implementation must keep document parsers outside the worker trust zone.
type Reconstructor interface {
	Clean(context.Context, io.Reader, int64, file.Format, io.Writer) (file.Format, error)
}

type Worker struct {
	repo          Repository
	storage       Storage
	scanner       Scanner
	reconstructor Reconstructor
	tempDir       string
}

func New(repo Repository, storage Storage, scanner Scanner, reconstructor Reconstructor, tempDir string) (*Worker, error) {
	if repo == nil || storage == nil || scanner == nil || reconstructor == nil || tempDir == "" {
		return nil, file.ErrInvalid
	}
	return &Worker{repo: repo, storage: storage, scanner: scanner, reconstructor: reconstructor, tempDir: tempDir}, nil
}

// Step claims at most one job. ErrNotFound means the durable queue is empty.
func (w *Worker) Step(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, JobTimeout)
	defer cancel()
	r, err := w.repo.Claim(ctx)
	if err != nil {
		return err
	}
	if (r.State == file.Scanning || r.State == file.Replicating) && !r.CreatedAt.IsZero() && time.Now().After(r.CreatedAt.Add(file.ProcessingTTL)) {
		_, err = w.repo.Transition(ctx, r, file.Expired)
		return err
	}
	switch r.State {
	case file.Uploading:
		_, err = w.repo.Transition(ctx, r, file.Expired)
	case file.Scanning:
		err = w.scan(ctx, r)
	case file.Replicating:
		err = w.storage.Replicate(ctx, r)
		if err == nil {
			_, err = w.repo.Transition(ctx, r, file.Ready)
		}
	case file.Rejected, file.Expired, file.Deleted:
		err = w.storage.Purge(ctx, r)
		if err == nil {
			err = w.repo.DeferCleanup(ctx, r)
		}
	case file.Ready:
		err = w.storage.CleanupReady(ctx, r)
		if err == nil {
			err = w.repo.DeferCleanup(ctx, r)
		}
	default:
		return file.ErrConflict
	}
	if errors.Is(err, file.ErrRejected) && (r.State == file.Scanning || r.State == file.Replicating) {
		_, transitionErr := w.repo.Transition(ctx, r, file.Rejected)
		return errors.Join(err, transitionErr)
	}
	return err // Unavailable jobs retain their lease and retry after its expiry.
}

func (w *Worker) scan(ctx context.Context, r file.Record) error {
	source, err := os.CreateTemp(w.tempDir, "source-*")
	if err != nil {
		return file.ErrUnavailable
	}
	defer os.Remove(source.Name())
	defer source.Close()
	if err = w.storage.ReadUpload(ctx, r, source); err != nil {
		return err
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return file.ErrUnavailable
	}
	clean, err := os.CreateTemp(w.tempDir, "clean-*")
	if err != nil {
		return file.ErrUnavailable
	}
	defer os.Remove(clean.Name())
	defer clean.Close()
	output := &boundedWriter{dest: clean, remaining: file.MaxSize}
	format, err := w.reconstructor.Clean(ctx, source, r.Manifest.Size, r.Manifest.Format, output)
	if err != nil {
		return err
	}
	size := file.MaxSize - output.remaining
	if size <= 0 || (format != file.PNG && format != file.PDF) {
		return file.ErrRejected
	}
	if _, err = clean.Seek(0, io.SeekStart); err != nil {
		return file.ErrUnavailable
	}
	// AV verdicts alone do not prove archive completeness (ClamAV #633).
	// The isolated structural/CDR limits must succeed first; scan both the bounded
	// original and the independently reconstructed passive derivative before upload.
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return file.ErrUnavailable
	}
	if err = w.scanner.Scan(ctx, source, r.Manifest.Size); err != nil {
		return err
	}
	if err = w.scanner.Scan(ctx, clean, size); err != nil {
		return err
	}
	if _, err = clean.Seek(0, io.SeekStart); err != nil {
		return file.ErrUnavailable
	}
	digest, err := w.storage.PutClean(ctx, r, format, clean, size)
	if err != nil {
		return err
	}
	_, err = w.repo.MarkClean(ctx, r, format, size, digest)
	return err
}

type boundedWriter struct {
	dest      io.Writer
	remaining int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, file.ErrRejected
	}
	n, err := w.dest.Write(p)
	w.remaining -= int64(n)
	return n, err
}
