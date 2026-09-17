// Package addresses operates only on the verified caller's address book.
package addresses

import (
	"context"
	"errors"
	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/address"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
)

type Operation uint8

const (
	Create Operation = iota + 1
	Update
	Delete
	SetDefault
)

type Command struct {
	Operation       Operation
	ID              address.ID
	Fields          address.Fields
	ExpectedVersion uint64
}

func (c Command) Validate() error {
	if !address.ValidVersion(c.ExpectedVersion) || c.Operation < Create || c.Operation > SetDefault {
		return address.ErrInvalid
	}
	if c.Operation != Create && c.ID == (address.ID{}) {
		return address.ErrInvalid
	}
	if c.Operation == Create || c.Operation == Update {
		f, e := address.NewFields(c.Fields)
		if e != nil || f != c.Fields {
			return address.ErrInvalid
		}
	}
	return nil
}

type Store interface {
	List(context.Context, profile.SubjectID) (address.Book, error)
	Mutate(context.Context, profile.SubjectID, Command) (address.Book, error)
}
type UseCase struct{ store Store }

func New(store Store) (*UseCase, error) {
	if store == nil {
		return nil, errors.New("addresses: store required")
	}
	return &UseCase{store}, nil
}
func authorize(ctx context.Context, p identity.Principal, write bool) error {
	if ctx == nil {
		return errors.New("addresses: context required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.SubjectID == (profile.SubjectID{}) {
		return identity.ErrUnauthenticated
	}
	if (write && !p.CanWriteAddresses) || (!write && !p.CanReadAddresses) {
		return identity.ErrForbidden
	}
	return nil
}
func checked(book address.Book, owner profile.SubjectID, err error) (address.Book, error) {
	if err != nil {
		return address.Book{}, err
	}
	if book.SubjectID != owner {
		return address.Book{}, identity.ErrForbidden
	}
	if book.Validate() != nil {
		return address.Book{}, errors.New("addresses: invalid stored book")
	}
	return book, nil
}
func (u *UseCase) List(ctx context.Context, p identity.Principal) (address.Book, error) {
	if err := authorize(ctx, p, false); err != nil {
		return address.Book{}, err
	}
	b, e := u.store.List(ctx, p.SubjectID)
	return checked(b, p.SubjectID, e)
}
func (u *UseCase) Mutate(ctx context.Context, p identity.Principal, c Command) (address.Book, error) {
	if err := authorize(ctx, p, true); err != nil {
		return address.Book{}, err
	}
	if c.Operation == Create || c.Operation == Update {
		f, e := address.NewFields(c.Fields)
		if e != nil {
			return address.Book{}, e
		}
		c.Fields = f
	}
	if err := c.Validate(); err != nil {
		return address.Book{}, err
	}
	b, e := u.store.Mutate(ctx, p.SubjectID, c)
	return checked(b, p.SubjectID, e)
}
