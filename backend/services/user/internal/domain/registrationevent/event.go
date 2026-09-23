// Package registrationevent defines the account-registration fact without transport dependencies.
package registrationevent

import (
	"errors"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	"time"
)

const (
	Type          = "auth.account.registered.v1"
	Producer      = "auth"
	SchemaVersion = 1
)

var ErrInvalidEvent = errors.New("invalid registration event")

type Event struct {
	ID          [16]byte
	SubjectID   profile.SubjectID
	OccurredAt  time.Time
	TraceID     [16]byte
	CausationID [16]byte
}

func (e Event) Validate() error {
	if e.ID == ([16]byte{}) || e.SubjectID == (profile.SubjectID{}) || e.OccurredAt.IsZero() || e.OccurredAt.UnixNano() <= 0 || !time.Unix(0, e.OccurredAt.UnixNano()).Equal(e.OccurredAt) || e.OccurredAt.Nanosecond()%1000 != 0 {
		return ErrInvalidEvent
	}
	return nil
}
