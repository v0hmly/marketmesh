// Package identity contains verified caller identity without transport dependencies.
package identity

import (
	"errors"

	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
)

var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("forbidden")
)

type Principal struct {
	SubjectID                           profile.SubjectID
	CanRead, CanWrite                   bool
	CanReadAddresses, CanWriteAddresses bool
}
