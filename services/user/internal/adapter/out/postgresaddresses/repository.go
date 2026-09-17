// Package postgresaddresses persists atomic address-book snapshots on User's primary.
package postgresaddresses

import (
	"context"
	"crypto/rand"
	"errors"

	"github.com/jackc/pgx/v5"
	platformpostgres "github.com/v0hmly/marketmesh/platform/postgres"
	"github.com/v0hmly/marketmesh/services/user/internal/application/addresses"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/address"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
)

type Transactor interface {
	WithinTransaction(context.Context, platformpostgres.TransactionOptions, platformpostgres.TransactionFunc) error
}
type Repository struct {
	rw platformpostgres.Executor
	tx Transactor
}

func New(rw platformpostgres.Executor, tx Transactor) (*Repository, error) {
	if rw == nil || tx == nil {
		return nil, errors.New("address postgres: dependencies required")
	}
	return &Repository{rw, tx}, nil
}

type safeError struct{ cause error }

func (safeError) Error() string   { return "address postgres: operation failed" }
func (e safeError) Unwrap() error { return e.cause }
func valid(ctx context.Context, owner profile.SubjectID) error {
	if ctx == nil {
		return errors.New("address postgres: context required")
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	if owner == (profile.SubjectID{}) {
		return address.ErrInvalid
	}
	return nil
}

const listSQL = `SELECT p.subject_id,p.address_book_version,a.address_id,a.recipient,a.phone,a.country,a.postal_code,a.city,a.street_house,a.apartment,a.comment,a.is_default FROM users.profiles p LEFT JOIN users.addresses a ON a.subject_id=p.subject_id WHERE p.subject_id=$1 ORDER BY a.address_id`

func read(ctx context.Context, e platformpostgres.Executor, owner profile.SubjectID) (address.Book, error) {
	rows, err := e.Query(ctx, listSQL, owner.Bytes())
	if err != nil {
		return address.Book{}, safeError{err}
	}
	defer rows.Close()
	b := address.Book{Addresses: []address.Address{}}
	for rows.Next() {
		var subject, id []byte
		var version int64
		var recipient, phone, country, postal, city, street, apartment, comment *string
		var primary *bool
		if err = rows.Scan(&subject, &version, &id, &recipient, &phone, &country, &postal, &city, &street, &apartment, &comment, &primary); err != nil {
			return address.Book{}, safeError{err}
		}
		b.SubjectID, err = profile.NewSubjectID(subject)
		if err != nil || version < 1 {
			return address.Book{}, safeError{errors.New("invalid stored book owner or version")}
		}
		b.Version = uint64(version)
		if id == nil {
			continue
		}
		aid, e := address.NewID(id)
		if e != nil || recipient == nil || phone == nil || country == nil || postal == nil || city == nil || street == nil || apartment == nil || comment == nil || primary == nil {
			return address.Book{}, safeError{errors.New("invalid stored address")}
		}
		b.Addresses = append(b.Addresses, address.Address{ID: aid, Fields: address.Fields{Recipient: *recipient, Phone: *phone, Country: *country, PostalCode: *postal, City: *city, StreetHouse: *street, Apartment: *apartment, Comment: *comment}, IsDefault: *primary})
	}
	if err = rows.Err(); err != nil {
		return address.Book{}, safeError{err}
	}
	if b.Version == 0 {
		return address.Book{}, profile.ErrNotReady
	}
	if err = b.Validate(); err != nil {
		return address.Book{}, safeError{errors.New("invalid stored book")}
	}
	return b, nil
}
func (r *Repository) List(ctx context.Context, owner profile.SubjectID) (address.Book, error) {
	if e := valid(ctx, owner); e != nil {
		return address.Book{}, e
	}
	return read(ctx, r.rw, owner)
}
func (r *Repository) Mutate(ctx context.Context, owner profile.SubjectID, c addresses.Command) (address.Book, error) {
	if e := valid(ctx, owner); e != nil {
		return address.Book{}, e
	}
	if e := c.Validate(); e != nil {
		return address.Book{}, e
	}
	var generated address.ID
	if c.Operation == addresses.Create {
		for generated == (address.ID{}) {
			if _, e := rand.Read(generated[:]); e != nil {
				return address.Book{}, safeError{e}
			}
		}
	}
	var result address.Book
	err := r.tx.WithinTransaction(ctx, platformpostgres.TransactionOptions{Isolation: platformpostgres.IsolationReadCommitted}, func(ctx context.Context, e platformpostgres.Executor) error {
		var version int64
		err := e.QueryRow(ctx, `SELECT address_book_version FROM users.profiles WHERE subject_id=$1 FOR UPDATE`, owner.Bytes()).Scan(&version)
		if errors.Is(err, pgx.ErrNoRows) {
			return profile.ErrNotReady
		}
		if err != nil {
			return err
		}
		if version < 1 {
			return errors.New("invalid stored book version")
		}
		if uint64(version) != c.ExpectedVersion {
			return address.ErrConflict
		}
		book, err := read(ctx, e, owner)
		if err != nil {
			return err
		}
		found := false
		for _, a := range book.Addresses {
			if a.ID == c.ID {
				found = true
			}
		}
		if c.Operation != addresses.Create && !found {
			return address.ErrNotFound
		}
		f := c.Fields
		switch c.Operation {
		case addresses.Create:
			if len(book.Addresses) >= address.MaxAddresses {
				return address.ErrLimit
			}
			_, err = e.Exec(ctx, `INSERT INTO users.addresses(subject_id,address_id,recipient,phone,country,postal_code,city,street_house,apartment,comment,is_default) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, owner.Bytes(), generated.Bytes(), f.Recipient, f.Phone, f.Country, f.PostalCode, f.City, f.StreetHouse, f.Apartment, f.Comment, len(book.Addresses) == 0)
		case addresses.Update:
			_, err = e.Exec(ctx, `UPDATE users.addresses SET recipient=$3,phone=$4,country=$5,postal_code=$6,city=$7,street_house=$8,apartment=$9,comment=$10 WHERE subject_id=$1 AND address_id=$2`, owner.Bytes(), c.ID.Bytes(), f.Recipient, f.Phone, f.Country, f.PostalCode, f.City, f.StreetHouse, f.Apartment, f.Comment)
		case addresses.Delete:
			_, err = e.Exec(ctx, `DELETE FROM users.addresses WHERE subject_id=$1 AND address_id=$2`, owner.Bytes(), c.ID.Bytes())
		case addresses.SetDefault:
			// Two statements avoid transient partial-unique-index violations.
			_, err = e.Exec(ctx, `UPDATE users.addresses SET is_default=false WHERE subject_id=$1 AND is_default`, owner.Bytes())
			if err == nil {
				_, err = e.Exec(ctx, `UPDATE users.addresses SET is_default=true WHERE subject_id=$1 AND address_id=$2`, owner.Bytes(), c.ID.Bytes())
			}
		}
		if err != nil {
			return err
		}
		_, err = e.Exec(ctx, `UPDATE users.profiles SET address_book_version=address_book_version+1 WHERE subject_id=$1`, owner.Bytes())
		if err != nil {
			return err
		}
		result, err = read(ctx, e, owner)
		return err
	})
	if err != nil {
		return address.Book{}, safeError{err}
	}
	return result, nil
}
