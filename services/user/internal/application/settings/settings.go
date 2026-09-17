// Package settings reads and updates only the verified caller's preferences.
package settings

import (
	"context"
	"errors"
	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	domain "github.com/v0hmly/marketmesh/services/user/internal/domain/settings"
)

type Store interface {
	Get(context.Context, profile.SubjectID) (domain.Settings, error)
	Update(context.Context, profile.SubjectID, domain.Theme, uint64) (domain.Settings, error)
}
type Command struct {
	Theme           domain.Theme
	ExpectedVersion uint64
}
type UseCase struct{ store Store }

func New(store Store) (*UseCase, error) {
	if store == nil {
		return nil, errors.New("settings: store required")
	}
	return &UseCase{store}, nil
}
func authorize(ctx context.Context, p identity.Principal, write bool) error {
	if ctx == nil {
		return errors.New("settings: context required")
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	if p.SubjectID == (profile.SubjectID{}) {
		return identity.ErrUnauthenticated
	}
	if (write && !p.CanWriteSettings) || (!write && !p.CanReadSettings) {
		return identity.ErrForbidden
	}
	return nil
}
func checked(s domain.Settings, p identity.Principal, e error) (domain.Settings, error) {
	if e != nil {
		return domain.Settings{}, e
	}
	if s.SubjectID != p.SubjectID {
		return domain.Settings{}, identity.ErrForbidden
	}
	if s.Validate() != nil {
		return domain.Settings{}, errors.New("settings: invalid stored settings")
	}
	return s, nil
}
func (u *UseCase) Get(ctx context.Context, p identity.Principal) (domain.Settings, error) {
	if e := authorize(ctx, p, false); e != nil {
		return domain.Settings{}, e
	}
	s, e := u.store.Get(ctx, p.SubjectID)
	return checked(s, p, e)
}
func (u *UseCase) Update(ctx context.Context, p identity.Principal, c Command) (domain.Settings, error) {
	if e := authorize(ctx, p, true); e != nil {
		return domain.Settings{}, e
	}
	if !c.Theme.Valid() || !domain.ValidExpectedVersion(c.ExpectedVersion) {
		return domain.Settings{}, domain.ErrInvalid
	}
	s, e := u.store.Update(ctx, p.SubjectID, c.Theme, c.ExpectedVersion)
	return checked(s, p, e)
}
