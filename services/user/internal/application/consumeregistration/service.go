// Package consumeregistration applies immutable Auth facts to User-owned state.
package consumeregistration

import (
	"context"
	"errors"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/registrationevent"
)

type Outcome string

const (
	Applied   Outcome = "applied"
	Duplicate Outcome = "duplicate"
)

var ErrConflict = errors.New("registration event identity conflict")

type Store interface {
	Apply(context.Context, registrationevent.Event) (Outcome, error)
}
type Service struct{ store Store }

func New(store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("registration consumer: store required")
	}
	return &Service{store: store}, nil
}
func (s *Service) Apply(ctx context.Context, e registrationevent.Event) (Outcome, error) {
	if ctx == nil {
		return "", errors.New("registration consumer: context required")
	}
	if err := e.Validate(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return s.store.Apply(ctx, e)
}
