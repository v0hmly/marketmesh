// Package avatars binds only owned, verified Files derivatives to a User profile.
package avatars

import (
	"context"
	"errors"

	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/avatar"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
)

type Repository interface {
	Get(context.Context, profile.SubjectID) (avatar.Avatar, error)
	Set(context.Context, profile.SubjectID, avatar.FileID, uint64) (avatar.Avatar, error)
}

// Files is a fixed private workload-authenticated API, never a caller-selected URL.
type Files interface {
	Inspect(context.Context, profile.SubjectID, avatar.FileID, string) error
	Retire(context.Context, profile.SubjectID, avatar.FileID) error
}

type Service struct {
	repo  Repository
	files Files
}

func New(repo Repository, files Files) (*Service, error) {
	if repo == nil || files == nil {
		return nil, errors.New("avatar: required dependency missing")
	}
	return &Service{repo: repo, files: files}, nil
}
func (s *Service) Get(ctx context.Context, p identity.Principal) (avatar.Avatar, error) {
	if err := authorize(ctx, p, false); err != nil {
		return avatar.Avatar{}, err
	}
	return s.repo.Get(ctx, p.SubjectID)
}
func (s *Service) Set(ctx context.Context, p identity.Principal, id avatar.FileID, version uint64) (avatar.Avatar, error) {
	if err := authorize(ctx, p, true); err != nil {
		return avatar.Avatar{}, err
	}
	if id == (avatar.FileID{}) || !avatar.ValidExpectedVersion(version) {
		return avatar.Avatar{}, avatar.ErrInvalid
	}
	// Validation cannot extend across two databases. Reads still require current
	// Files READY authorization; a concurrent direct deletion never exposes bytes.
	if err := s.files.Inspect(ctx, p.SubjectID, id, p.SessionID); err != nil {
		return avatar.Avatar{}, err
	}
	return s.repo.Set(ctx, p.SubjectID, id, version)
}
func (s *Service) Clear(ctx context.Context, p identity.Principal, version uint64) (avatar.Avatar, error) {
	if err := authorize(ctx, p, true); err != nil {
		return avatar.Avatar{}, err
	}
	if !avatar.ValidExpectedVersion(version) {
		return avatar.Avatar{}, avatar.ErrInvalid
	}
	return s.repo.Set(ctx, p.SubjectID, avatar.FileID{}, version)
}
func authorize(ctx context.Context, p identity.Principal, write bool) error {
	if ctx == nil || p.SubjectID == (profile.SubjectID{}) {
		return identity.ErrUnauthenticated
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if (write && !p.CanWrite) || (!write && !p.CanRead) {
		return identity.ErrForbidden
	}
	return nil
}
