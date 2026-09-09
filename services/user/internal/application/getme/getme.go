// Package getme reads the authenticated caller's current profile.
package getme

import (
	"context"
	"errors"

	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
)

type Reader interface {
	Get(context.Context, profile.SubjectID) (profile.Profile, error)
}
type UseCase struct{ reader Reader }

func New(reader Reader) (*UseCase, error) {
	if reader == nil {
		return nil, errors.New("getme: reader required")
	}
	return &UseCase{reader: reader}, nil
}
func (uc *UseCase) Execute(ctx context.Context, p identity.Principal) (profile.Profile, error) {
	if ctx == nil {
		return profile.Profile{}, errors.New("getme: context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return profile.Profile{}, err
	}
	if p.SubjectID == (profile.SubjectID{}) {
		return profile.Profile{}, identity.ErrUnauthenticated
	}
	if !p.CanRead {
		return profile.Profile{}, identity.ErrForbidden
	}
	result, err := uc.reader.Get(ctx, p.SubjectID)
	if err != nil {
		return profile.Profile{}, err
	}
	if result.SubjectID != p.SubjectID {
		return profile.Profile{}, identity.ErrForbidden
	}
	return result, nil
}
