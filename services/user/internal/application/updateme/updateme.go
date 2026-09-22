// Package updateme changes only the authenticated caller's editable profile fields.
package updateme

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
)

type Updater interface {
	Update(context.Context, profile.SubjectID, profile.Fields, uint64) (profile.Profile, error)
}
type Command struct {
	DisplayName, Bio, LastName, BirthDate, Phone, City string
	Gender                                             profile.Gender
	ShowAge                                            bool
	ExpectedVersion                                    uint64
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
	fields, err := profile.NormalizeFields(profile.Fields{DisplayName: cmd.DisplayName, Bio: cmd.Bio, LastName: cmd.LastName, BirthDate: cmd.BirthDate, Gender: cmd.Gender, Phone: cmd.Phone, City: cmd.City, ShowAge: cmd.ShowAge}, time.Now())
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
