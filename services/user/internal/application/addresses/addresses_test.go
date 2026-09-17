package addresses

import (
	"context"
	"errors"
	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/address"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	"testing"
)

type fakeStore struct {
	calls   int
	owner   profile.SubjectID
	command Command
	result  address.Book
}

func (f *fakeStore) List(_ context.Context, o profile.SubjectID) (address.Book, error) {
	f.calls++
	f.owner = o
	return f.result, nil
}
func (f *fakeStore) Mutate(_ context.Context, o profile.SubjectID, c Command) (address.Book, error) {
	f.command = c
	return f.List(context.Background(), o)
}
func TestAuthorizationOwnershipAndNormalization(t *testing.T) {
	f := &fakeStore{result: address.Book{SubjectID: profile.SubjectID{1}, Version: 1}}
	u, _ := New(f)
	p := identity.Principal{SubjectID: profile.SubjectID{1}, CanRead: true, CanWrite: true}
	if _, e := u.List(context.Background(), p); !errors.Is(e, identity.ErrForbidden) {
		t.Fatal(e)
	}
	cmd := Command{Operation: Create, ExpectedVersion: 1, Fields: address.Fields{Recipient: " A ", Phone: "1234567", Country: "Country", City: "City", StreetHouse: "Street"}}
	if _, e := u.Mutate(context.Background(), p, cmd); !errors.Is(e, identity.ErrForbidden) || f.calls != 0 {
		t.Fatal(e)
	}
	p.CanReadAddresses = true
	p.CanWriteAddresses = true
	if _, e := u.Mutate(context.Background(), p, cmd); e != nil || f.owner != p.SubjectID || f.command.Fields.Recipient != "A" {
		t.Fatal(e)
	}
	f.result.SubjectID = profile.SubjectID{2}
	if _, e := u.List(context.Background(), p); !errors.Is(e, identity.ErrForbidden) {
		t.Fatal("owner leak", e)
	}
	count := f.calls
	cmd.ExpectedVersion = 0
	if _, e := u.Mutate(context.Background(), p, cmd); !errors.Is(e, address.ErrInvalid) || f.calls != count {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := u.List(ctx, p); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
