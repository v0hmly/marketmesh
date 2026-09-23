// Package files coordinates durable upload sessions without carrying file bytes.
package files

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

// Repository atomically compares owner, version and state on every mutation.
// All successful writes require the deployment's synchronous replication policy.
type Repository interface {
	Create(context.Context, file.Record) (file.Record, error)
	Get(context.Context, file.Owner, file.ID) (file.Record, error)
	AttachUpload(context.Context, file.Record, string) (file.Record, error)
	Transition(context.Context, file.Record, file.State) (file.Record, error)
}

type Capability struct {
	URL        string
	Method     string
	Headers    map[string]string
	ExpiresAt  time.Time
	PartNumber int32
}

type ReceivedPart struct {
	Number int32
	Size   int64
	SHA256 file.Digest
	ETag   string
}

// Storage implements only fixed, server-selected objects and constrained operations.
type Storage interface {
	Begin(context.Context, file.Record) (string, error)
	Parts(context.Context, file.Record) ([]ReceivedPart, error)
	SignPart(context.Context, file.Record, int32, time.Time) (Capability, error)
	Complete(context.Context, file.Record, []ReceivedPart) error
	Completed(context.Context, file.Record) (bool, error)
	SignDownload(context.Context, file.Record, time.Time) (Capability, error)
}

type Service struct {
	repo    Repository
	storage Storage
	now     func() time.Time
}

func New(repo Repository, storage Storage, clock func() time.Time) (*Service, error) {
	if repo == nil || storage == nil || clock == nil {
		return nil, errors.New("files: required dependency missing")
	}
	return &Service{repo: repo, storage: storage, now: clock}, nil
}

type Upload struct {
	File     file.Record
	Parts    []Capability
	Received []int32
}

// Create repeats the same business operation for the same owner/idempotency key.
// It refreshes capabilities only after checking the entire stored manifest.
func (s *Service) Create(ctx context.Context, owner file.Owner, key file.ID, manifest file.Manifest) (Upload, error) {
	if !owner.Valid() || key == (file.ID{}) || manifest.Validate() != nil {
		return Upload{}, file.ErrInvalid
	}
	var id, objectID file.ID
	if _, err := rand.Read(id[:]); err != nil {
		return Upload{}, file.ErrUnavailable
	}
	if _, err := rand.Read(objectID[:]); err != nil {
		return Upload{}, file.ErrUnavailable
	}
	when := s.now()
	record, err := s.repo.Create(ctx, file.Record{ID: id, Owner: owner, IdempotencyKey: key, Manifest: manifest, ObjectKey: objectID.String(), State: file.Uploading, Version: 1, CreatedAt: when, ExpiresAt: when.Add(file.UploadTTL)})
	if err != nil {
		return Upload{}, err
	}
	if record.Manifest.Fingerprint() != manifest.Fingerprint() {
		return Upload{}, file.ErrConflict
	}
	if record.State != file.Uploading {
		return Upload{File: record}, nil
	}
	if !s.now().Before(record.ExpiresAt) {
		return Upload{}, file.ErrConflict
	}
	if record.UploadID == "" {
		uploadID, err := s.storage.Begin(ctx, record)
		if err != nil {
			return Upload{}, err
		}
		attached, err := s.repo.AttachUpload(ctx, record, uploadID)
		if err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			// A failed acknowledgement is not evidence of a rollback. Never abort
			// an upload that may have been attached by the committed transaction.
			current, readErr := s.repo.Get(cleanupCtx, owner, record.ID)
			if readErr != nil {
				return Upload{}, err
			}
			if current.UploadID == uploadID {
				return s.capabilities(ctx, current)
			}
			// Immutable parts share the manifest's upload ID across all attempts.
			// Only terminal reconciliation may delete them, never a losing creator.
			if errors.Is(err, file.ErrConflict) {
				return s.capabilities(ctx, current)
			}
			return Upload{}, err
		}
		record = attached
	}
	return s.capabilities(ctx, record)
}

func (s *Service) capabilities(ctx context.Context, record file.Record) (Upload, error) {
	result := Upload{File: record}
	if record.State != file.Uploading {
		return result, nil
	}
	if record.UploadID == "" || !s.now().Before(record.ExpiresAt) {
		return Upload{}, file.ErrConflict
	}
	received, err := s.storage.Parts(ctx, record)
	if err != nil {
		return Upload{}, err
	}
	seen := make(map[int32]bool, len(received))
	for _, part := range received {
		if !validPart(record, part) || seen[part.Number] {
			return Upload{}, file.ErrRejected
		}
		seen[part.Number] = true
		result.Received = append(result.Received, part.Number)
	}
	until := minTime(s.now().Add(file.CapabilityTTL), record.ExpiresAt)
	for i := range record.Manifest.Parts {
		number := int32(i + 1)
		if seen[number] {
			continue
		}
		capability, err := s.storage.SignPart(ctx, record, number, until)
		if err != nil {
			return Upload{}, err
		}
		result.Parts = append(result.Parts, capability)
	}
	return result, nil
}

func (s *Service) Complete(ctx context.Context, owner file.Owner, id file.ID) (file.Record, error) {
	record, err := s.Status(ctx, owner, id)
	if err != nil {
		return file.Record{}, err
	}
	if record.State == file.Scanning || record.State == file.Replicating || record.State == file.Ready {
		return record, nil
	}
	if record.State != file.Uploading || record.UploadID == "" || !s.now().Before(record.ExpiresAt) {
		return file.Record{}, file.ErrConflict
	}
	complete, err := s.storage.Completed(ctx, record)
	if err != nil {
		return file.Record{}, err
	}
	if complete {
		return s.advanceComplete(ctx, record)
	}
	parts, err := s.storage.Parts(ctx, record)
	if err != nil {
		return file.Record{}, err
	}
	if len(parts) != len(record.Manifest.Parts) {
		return file.Record{}, file.ErrConflict
	}
	for i, part := range parts {
		if part.Number != int32(i+1) || !validPart(record, part) {
			return file.Record{}, file.ErrRejected
		}
	}
	if err = s.storage.Complete(ctx, record, parts); err != nil {
		return file.Record{}, err
	}
	return s.advanceComplete(ctx, record)
}

func (s *Service) advanceComplete(ctx context.Context, record file.Record) (file.Record, error) {
	updated, err := s.repo.Transition(ctx, record, file.Scanning)
	if errors.Is(err, file.ErrConflict) {
		return s.Status(ctx, record.Owner, record.ID)
	}
	return updated, err
}

func (s *Service) Status(ctx context.Context, owner file.Owner, id file.ID) (file.Record, error) {
	if !owner.Valid() || id == (file.ID{}) {
		return file.Record{}, file.ErrInvalid
	}
	return s.repo.Get(ctx, owner, id)
}

func (s *Service) Download(ctx context.Context, owner file.Owner, id file.ID) (Capability, error) {
	record, err := s.Status(ctx, owner, id)
	if err != nil {
		return Capability{}, err
	}
	if record.State != file.Ready {
		return Capability{}, file.ErrNotReady
	}
	return s.storage.SignDownload(ctx, record, s.now().Add(file.CapabilityTTL))
}

func (s *Service) Delete(ctx context.Context, owner file.Owner, id file.ID) error {
	record, err := s.Status(ctx, owner, id)
	if err != nil {
		return err
	}
	if record.State != file.Deleted {
		record, err = s.repo.Transition(ctx, record, file.Deleted)
		if err != nil {
			return err
		}
	}
	return nil // Durable reconciliation owns cleanup, even while storage is offline.
}

func validPart(record file.Record, part ReceivedPart) bool {
	if part.Number < 1 || int(part.Number) > len(record.Manifest.Parts) || part.ETag == "" {
		return false
	}
	expected := record.Manifest.Parts[part.Number-1]
	return expected.Size == part.Size && expected.SHA256 == part.SHA256
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
