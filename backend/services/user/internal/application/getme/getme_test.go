package getme

import (
	"context"
	"errors"
	"testing"

	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
)

type readerFunc func(context.Context, profile.SubjectID) (profile.Profile, error)

func (f readerFunc) Get(c context.Context, id profile.SubjectID) (profile.Profile, error) {
	return f(c, id)
}
func TestOwnershipAndAuthorization(t *testing.T) {
	id := profile.SubjectID{1}
	called := 0
	uc, _ := New(readerFunc(func(_ context.Context, got profile.SubjectID) (profile.Profile, error) {
		called++
		if got != id {
			t.Fatal("wrong subject")
		}
		return profile.Profile{SubjectID: profile.SubjectID{2}}, nil
	}))
	for _, tc := range []struct {
		p   identity.Principal
		err error
	}{{identity.Principal{}, identity.ErrUnauthenticated}, {identity.Principal{SubjectID: id}, identity.ErrForbidden}, {identity.Principal{SubjectID: id, CanRead: true}, identity.ErrForbidden}} {
		if _, e := uc.Execute(context.Background(), tc.p); !errors.Is(e, tc.err) {
			t.Fatalf("error=%v", e)
		}
	}
	if called != 1 {
		t.Fatalf("unauthorized storage access: calls=%d", called)
	}
}
func TestMissingProfilePropagates(t *testing.T) {
	uc, _ := New(readerFunc(func(context.Context, profile.SubjectID) (profile.Profile, error) {
		return profile.Profile{}, profile.ErrNotReady
	}))
	if _, e := uc.Execute(context.Background(), identity.Principal{SubjectID: profile.SubjectID{1}, CanRead: true}); !errors.Is(e, profile.ErrNotReady) {
		t.Fatal(e)
	}
	if _, e := New(nil); e == nil {
		t.Fatal("nil accepted")
	}
}

func TestContextStopsBeforeReader(t *testing.T) {
	uc, _ := New(readerFunc(func(context.Context, profile.SubjectID) (profile.Profile, error) {
		t.Fatal("reader called")
		return profile.Profile{}, nil
	}))
	p := identity.Principal{SubjectID: profile.SubjectID{1}, CanRead: true}
	if _, err := uc.Execute(nil, p); err == nil {
		t.Fatal("nil context accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := uc.Execute(ctx, p); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
