// Package identity defines identity verified by Auth, never supplied by a request.
package identity

import (
	"errors"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

var ErrUnauthenticated = errors.New("file authentication failed")
var ErrForbidden = errors.New("file permission denied")

type Principal struct {
	Owner             file.Owner
	SessionID         string // Verified Auth session identifier, never a bearer token.
	CanRead, CanWrite bool
}
