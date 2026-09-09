// Package updateme changes only the authenticated caller's editable profile fields.
package updateme

import (
	"context"
	"errors"
	"math"

	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
)

type Updater interface {
	Update(context.Context, profile.SubjectID, profile.Fields, uint64) (profile.Profile, error)
}
type Command struct {
	DisplayName, Bio string
	ExpectedVersion  uint64
}
type UseCase struct{ updater Updater }

func New(updater Updater) (*UseCase, error) {
	if updater == nil {
		return nil, errors.New("updateme: updater required")
	}
	return &UseCase{updater: updater}, nil
}
func (uc *UseCase) Execute(ctx context.Context, p identity.Principal, cmd Command) (profile.Profile, error) {
	if ctx == nil {
		return profile.Profile{}, errors.New("updateme: context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return profile.Profile{}, err
	}
	if p.SubjectID == (profile.SubjectID{}) {
		return profile.Profile{}, identity.ErrUnauthenticated
	}
	if !p.CanWrite {
		return profile.Profile{}, identity.ErrForbidden
	}
	if cmd.ExpectedVersion == 0 || cmd.ExpectedVersion >= math.MaxInt64 {
		return profile.Profile{}, profile.ErrInvalidProfile
	}
	fields, err := profile.NewFields(cmd.DisplayName, cmd.Bio)
	if err != nil {
		return profile.Profile{}, err
	}
	result, err := uc.updater.Update(ctx, p.SubjectID, fields, cmd.ExpectedVersion)
	if err != nil {
		return profile.Profile{}, err
	}
	if result.SubjectID != p.SubjectID {
		return profile.Profile{}, identity.ErrForbidden
	}
	return result, nil
}
