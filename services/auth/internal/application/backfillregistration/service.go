// Package backfillregistration reconciles Auth-owned registration facts one bounded page at a time.
package backfillregistration

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/registrationevent"
	"time"
)

type Options struct {
	After                  string
	Limit                  int
	Apply, ReplayPublished bool
}
type Result struct {
	Scanned, Missing, Published, Inserted, Requeued int
	NextCursor                                      string
	Done                                            bool
}
type Candidate struct {
	Subject     credential.SubjectID
	Missing     bool
	PublishedAt *time.Time
}
type Store interface {
	Scan(context.Context, []byte, int) ([]Candidate, error)
	Ensure(context.Context, registrationevent.Event) (bool, error)
	Replay(context.Context, credential.SubjectID, time.Time) (bool, error)
}
type Factory interface {
	New(context.Context, credential.SubjectID) (registrationevent.Event, error)
}
type Service struct {
	store   Store
	factory Factory
}

func New(store Store, factory Factory) (*Service, error) {
	if store == nil || factory == nil {
		return nil, errors.New("registration backfill: dependencies required")
	}
	return &Service{store, factory}, nil
}
func (o Options) Validate() ([]byte, error) {
	if o.Limit < 1 || o.Limit > 1000 {
		return nil, errors.New("registration backfill: limit must be 1..1000")
	}
	if o.After == "" {
		return []byte{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(o.After)
	if err != nil || len(raw) != 16 || base64.RawURLEncoding.EncodeToString(raw) != o.After {
		return nil, errors.New("registration backfill: invalid cursor")
	}
	return raw, nil
}

// Page returns the last fully processed cursor even on failure; retrying that cursor is safe.
func (s *Service) Page(ctx context.Context, o Options) (r Result, err error) {
	r.NextCursor = o.After
	after, err := o.Validate()
	if err != nil {
		return r, err
	}
	if ctx == nil {
		return r, errors.New("registration backfill: context required")
	}
	if err = ctx.Err(); err != nil {
		return r, err
	}
	rows, err := s.store.Scan(ctx, after, o.Limit)
	if err != nil {
		return r, err
	}
	for _, row := range rows {
		if err = ctx.Err(); err != nil {
			return r, err
		}
		if row.Missing {
			r.Missing++
			if o.Apply {
				event, e := s.factory.New(ctx, row.Subject)
				if e != nil {
					return r, e
				}
				if e = ctx.Err(); e != nil {
					return r, e
				}
				changed, e := s.store.Ensure(ctx, event)
				if e != nil {
					return r, e
				}
				if changed {
					r.Inserted++
				}
			}
		} else if row.PublishedAt != nil {
			r.Published++
			if o.Apply && o.ReplayPublished {
				changed, e := s.store.Replay(ctx, row.Subject, *row.PublishedAt)
				if e != nil {
					return r, e
				}
				if changed {
					r.Requeued++
				}
			}
		}
		r.Scanned++
		r.NextCursor = base64.RawURLEncoding.EncodeToString(row.Subject.Bytes())
	}
	r.Done = len(rows) < o.Limit
	return r, nil
}
