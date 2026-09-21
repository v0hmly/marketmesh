package files

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

type memoryRepo struct {
	mu                                     sync.Mutex
	records                                map[file.ID]file.Record
	attachAckFailure, transitionAckFailure bool
}

func (r *memoryRepo) Create(_ context.Context, want file.Record) (file.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range r.records {
		if v.Owner == want.Owner && v.IdempotencyKey == want.IdempotencyKey {
			return v, nil
		}
	}
	r.records[want.ID] = want
	return want, nil
}
func (r *memoryRepo) Get(_ context.Context, owner file.Owner, id file.ID) (file.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.records[id]
	if !ok || v.Owner != owner {
		return file.Record{}, file.ErrNotFound
	}
	return v, nil
}
func (r *memoryRepo) AttachUpload(_ context.Context, want file.Record, upload string) (file.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.records[want.ID]
	if v.Owner != want.Owner || v.Version != want.Version || v.State != file.Uploading || v.UploadID != "" {
		return file.Record{}, file.ErrConflict
	}
	v.UploadID = upload
	v.Version++
	r.records[v.ID] = v
	if r.attachAckFailure {
		r.attachAckFailure = false
		return file.Record{}, file.ErrUnavailable
	}
	return v, nil
}
func (r *memoryRepo) Transition(_ context.Context, want file.Record, to file.State) (file.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.records[want.ID]
	if v.Owner != want.Owner || v.Version != want.Version || !file.CanTransition(v.State, to) {
		return file.Record{}, file.ErrConflict
	}
	if r.transitionAckFailure {
		r.transitionAckFailure = false
		return file.Record{}, file.ErrUnavailable
	}
	v.State = to
	v.Version++
	r.records[v.ID] = v
	return v, nil
}

type memoryStorage struct {
	mu            sync.Mutex
	next          int
	aborted       map[string]bool
	completed     map[file.ID]bool
	afterComplete func()
}

func (s *memoryStorage) Begin(context.Context, file.Record) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	return fmt.Sprint("upload-", s.next), nil
}
func (s *memoryStorage) Abort(_ context.Context, r file.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aborted[r.UploadID] = true
	return nil
}
func (s *memoryStorage) Parts(_ context.Context, r file.Record) ([]ReceivedPart, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.aborted[r.UploadID] || s.completed[r.ID] {
		return nil, file.ErrUnavailable
	}
	parts := make([]ReceivedPart, len(r.Manifest.Parts))
	for i, p := range r.Manifest.Parts {
		parts[i] = ReceivedPart{Number: int32(i + 1), Size: p.Size, SHA256: p.SHA256, ETag: "etag"}
	}
	return parts, nil
}
func (s *memoryStorage) SignPart(context.Context, file.Record, int32, time.Time) (Capability, error) {
	return Capability{}, nil
}
func (s *memoryStorage) Complete(_ context.Context, r file.Record, _ []ReceivedPart) error {
	s.mu.Lock()
	s.completed[r.ID] = true
	s.mu.Unlock()
	if s.afterComplete != nil {
		s.afterComplete()
	}
	return nil
}
func (s *memoryStorage) Completed(_ context.Context, r file.Record) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completed[r.ID], nil
}
func (s *memoryStorage) SignDownload(context.Context, file.Record, time.Time) (Capability, error) {
	return Capability{Method: "GET"}, nil
}
func (s *memoryStorage) Delete(context.Context, file.Record) error { return nil }

func fixture(t *testing.T) (*Service, *memoryRepo, *memoryStorage, file.Owner, file.Manifest) {
	t.Helper()
	repo := &memoryRepo{records: make(map[file.ID]file.Record)}
	storage := &memoryStorage{aborted: make(map[string]bool), completed: make(map[file.ID]bool)}
	service, err := New(repo, storage, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	owner := file.Owner{Tenant: file.ID{1}, Subject: file.ID{2}}
	sum := file.Digest(sha256.Sum256([]byte("pdf")))
	manifest := file.Manifest{Format: file.PDF, Size: 3, SHA256: sum, Parts: []file.Part{{Size: 3, SHA256: sum}}}
	return service, repo, storage, owner, manifest
}

func TestConcurrentCreateAndOwnerIsolation(t *testing.T) {
	s, repo, _, owner, manifest := fixture(t)
	ctx := context.Background()
	results := make(chan file.ID, 24)
	errs := make(chan error, 24)
	var wg sync.WaitGroup
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := s.Create(ctx, owner, file.ID{3}, manifest)
			if err != nil {
				errs <- err
				return
			}
			results <- result.File.ID
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var id file.ID
	for got := range results {
		if id != (file.ID{}) && id != got {
			t.Fatal("duplicate business operation")
		}
		id = got
	}
	if len(repo.records) != 1 {
		t.Fatal("duplicate record")
	}
	other := owner
	other.Tenant[0]++
	if _, err := s.Status(ctx, other, id); !errors.Is(err, file.ErrNotFound) {
		t.Fatal("cross-tenant access", err)
	}
	if _, err := s.Download(ctx, owner, id); !errors.Is(err, file.ErrNotReady) {
		t.Fatal("download before READY", err)
	}
	manifest.Parts = append([]file.Part(nil), manifest.Parts...)
	manifest.Parts[0].SHA256[0]++
	if _, err := s.Create(ctx, owner, file.ID{3}, manifest); !errors.Is(err, file.ErrConflict) {
		t.Fatal("changed manifest reused idempotency key", err)
	}
}

func TestAttachLostAcknowledgementDoesNotAbortWinner(t *testing.T) {
	s, repo, storage, owner, manifest := fixture(t)
	repo.attachAckFailure = true
	result, err := s.Create(context.Background(), owner, file.ID{3}, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if result.File.UploadID == "" || storage.aborted[result.File.UploadID] {
		t.Fatal("committed upload aborted")
	}
	if _, err = s.Create(context.Background(), owner, file.ID{3}, manifest); err != nil {
		t.Fatal("resume failed", err)
	}
}

func TestCompleteRecoversAfterStorageCommitAndDatabaseFailure(t *testing.T) {
	s, repo, storage, owner, manifest := fixture(t)
	ctx := context.Background()
	upload, err := s.Create(ctx, owner, file.ID{3}, manifest)
	if err != nil {
		t.Fatal(err)
	}
	repo.transitionAckFailure = true
	if _, err = s.Complete(ctx, owner, upload.File.ID); !errors.Is(err, file.ErrUnavailable) {
		t.Fatal("expected injected database failure", err)
	}
	if !storage.completed[upload.File.ID] {
		t.Fatal("storage not completed")
	}
	record, err := s.Complete(ctx, owner, upload.File.ID)
	if err != nil || record.State != file.Scanning {
		t.Fatal("completion recovery failed", record.State, err)
	}
	again, err := s.Complete(ctx, owner, upload.File.ID)
	if err != nil || again.Version != record.Version {
		t.Fatal("repeated completion changed state", err)
	}
}

func TestDeleteDuringCompleteCannotResurrectFile(t *testing.T) {
	s, _, storage, owner, manifest := fixture(t)
	ctx := context.Background()
	upload, err := s.Create(ctx, owner, file.ID{3}, manifest)
	if err != nil {
		t.Fatal(err)
	}
	storage.afterComplete = func() {
		if err := s.Delete(ctx, owner, upload.File.ID); err != nil {
			t.Fatal(err)
		}
	}
	record, err := s.Complete(ctx, owner, upload.File.ID)
	if err != nil || record.State != file.Deleted {
		t.Fatal("deleted file resurrected", record.State, err)
	}
	if _, err = s.Download(ctx, owner, upload.File.ID); !errors.Is(err, file.ErrNotReady) {
		t.Fatal("deleted download allowed")
	}
}
