package updateme

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
)

type updaterFunc func(context.Context, profile.SubjectID, profile.Fields, uint64) (profile.Profile, error)

func (f updaterFunc) Update(c context.Context, id profile.SubjectID, fields profile.Fields, v uint64) (profile.Profile, error) {
	return f(c, id, fields, v)
}
func TestUpdateOwnershipValidationAndCAS(t *testing.T) {
	id := profile.SubjectID{1}
	calls := 0
	p := identity.Principal{SubjectID: id, CanWrite: true}
	uc, _ := New(updaterFunc(func(_ context.Context, got profile.SubjectID, f profile.Fields, v uint64) (profile.Profile, error) {
		calls++
		if got != id || v != 7 || f.DisplayName != "Alice" {
			t.Fatal("bad port arguments")
		}
		return profile.Profile{}, profile.ErrConflict
	}))
	for _, v := range []uint64{0, math.MaxInt64, math.MaxUint64} {
		if _, e := uc.Execute(context.Background(), p, Command{ExpectedVersion: v}); !errors.Is(e, profile.ErrInvalidProfile) {
			t.Fatal(e)
		}
	}
	if _, e := uc.Execute(context.Background(), identity.Principal{}, Command{ExpectedVersion: 7}); !errors.Is(e, identity.ErrUnauthenticated) {
		t.Fatal(e)
	}
	if _, e := uc.Execute(context.Background(), identity.Principal{SubjectID: id}, Command{ExpectedVersion: 7}); !errors.Is(e, identity.ErrForbidden) {
		t.Fatal(e)
	}
	if calls != 0 {
		t.Fatal("invalid call reached storage")
	}
	if _, e := uc.Execute(context.Background(), p, Command{DisplayName: " Alice ", ExpectedVersion: 7}); !errors.Is(e, profile.ErrConflict) {
		t.Fatal(e)
	}
	uc, _ = New(updaterFunc(func(context.Context, profile.SubjectID, profile.Fields, uint64) (profile.Profile, error) {
		return profile.Profile{SubjectID: profile.SubjectID{2}}, nil
	}))
	if _, e := uc.Execute(context.Background(), p, Command{ExpectedVersion: 1}); !errors.Is(e, identity.ErrForbidden) {
		t.Fatal("foreign profile disclosed")
	}
	if _, e := New(nil); e == nil {
		t.Fatal("nil accepted")
	}
}

func TestContextStopsBeforeUpdater(t *testing.T) {
	uc, _ := New(updaterFunc(func(context.Context, profile.SubjectID, profile.Fields, uint64) (profile.Profile, error) {
		t.Fatal("updater called")
		return profile.Profile{}, nil
	}))
	p := identity.Principal{SubjectID: profile.SubjectID{1}, CanWrite: true}
	cmd := Command{ExpectedVersion: 1}
	if _, err := uc.Execute(nil, p, cmd); err == nil {
		t.Fatal("nil context accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := uc.Execute(ctx, p, cmd); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
