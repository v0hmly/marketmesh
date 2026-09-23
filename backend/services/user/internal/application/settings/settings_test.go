package settings

import (
	"context"
	"errors"
	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	domain "github.com/v0hmly/marketmesh/services/user/internal/domain/settings"
	"testing"
)

type storeStub struct {
	calls   int
	owner   profile.SubjectID
	theme   domain.Theme
	version uint64
	result  domain.Settings
}

func (s *storeStub) Get(_ context.Context, id profile.SubjectID) (domain.Settings, error) {
	s.calls++
	s.owner = id
	return s.result, nil
}
func (s *storeStub) Update(ctx context.Context, id profile.SubjectID, theme domain.Theme, version uint64) (domain.Settings, error) {
	s.theme = theme
	s.version = version
	return s.Get(ctx, id)
}
func TestScopesOwnerAndCommandValidation(t *testing.T) {
	store := &storeStub{result: domain.Settings{SubjectID: profile.SubjectID{1}, Version: 2, Theme: domain.Dark}}
	u, _ := New(store)
	p := identity.Principal{SubjectID: profile.SubjectID{1}, CanRead: true, CanWrite: true, CanReadAddresses: true, CanWriteAddresses: true}
	if _, e := u.Get(context.Background(), p); !errors.Is(e, identity.ErrForbidden) {
		t.Fatal(e)
	}
	c := Command{Theme: domain.Dark, ExpectedVersion: 1}
	if _, e := u.Update(context.Background(), p, c); !errors.Is(e, identity.ErrForbidden) || store.calls != 0 {
		t.Fatal(e)
	}
	p.CanReadSettings = true
	if _, e := u.Get(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	if _, e := u.Update(context.Background(), p, c); !errors.Is(e, identity.ErrForbidden) {
		t.Fatal(e)
	}
	p.CanWriteSettings = true
	if _, e := u.Update(context.Background(), p, c); e != nil || store.owner != p.SubjectID || store.theme != domain.Dark || store.version != 1 {
		t.Fatal(e)
	}
	for _, cmd := range []Command{{Theme: domain.Dark}, {Theme: "unknown", ExpectedVersion: 1}} {
		before := store.calls
		if _, e := u.Update(context.Background(), p, cmd); !errors.Is(e, domain.ErrInvalid) || store.calls != before {
			t.Fatal(e)
		}
	}
	store.result.SubjectID = profile.SubjectID{2}
	if _, e := u.Get(context.Background(), p); !errors.Is(e, identity.ErrForbidden) {
		t.Fatal("owner leak", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := u.Get(ctx, p); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	p.SubjectID = profile.SubjectID{}
	if _, e := u.Get(context.Background(), p); !errors.Is(e, identity.ErrUnauthenticated) {
		t.Fatal(e)
	}
}
