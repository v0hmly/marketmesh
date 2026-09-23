package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	gatewayv1 "github.com/v0hmly/marketmesh/api/gen/go/gateway/v1"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func fullAddressBook() *userv1.AddressBook {
	b := &userv1.AddressBook{SubjectId: bytes.Repeat([]byte{1}, 16), Version: 2}
	for i := byte(1); i <= 20; i++ {
		b.Addresses = append(b.Addresses, &userv1.Address{AddressId: bytes.Repeat([]byte{i}, 16), IsDefault: i == 1, Fields: &userv1.AddressFields{Recipient: strings.Repeat("😀", 120), Phone: "+7 (999) 123-45-67", Country: strings.Repeat("😀", 80), PostalCode: strings.Repeat("😀", 20), City: strings.Repeat("😀", 120), StreetHouse: strings.Repeat("😀", 240), Apartment: strings.Repeat("😀", 40), Comment: strings.Repeat("😀", 500)}})
	}
	return b
}
func TestAddressSnapshotBounds(t *testing.T) {
	book := fullAddressBook()
	if proto.Size(book) <= 64*1024 || !validAddressBook(book) {
		t.Fatal("full valid book must fit 128 KiB and exceed old limit")
	}
	for _, mutate := range []func(*userv1.AddressBook){
		func(b *userv1.AddressBook) { b.SubjectId = make([]byte, 16) },
		func(b *userv1.AddressBook) { b.Version = 0 },
		func(b *userv1.AddressBook) { b.Addresses = append(b.Addresses, b.Addresses[0]) },
		func(b *userv1.AddressBook) { b.Addresses[1].AddressId = b.Addresses[0].AddressId },
		func(b *userv1.AddressBook) { b.Addresses[1].IsDefault = true },
		func(b *userv1.AddressBook) { b.Addresses[0].Fields = nil },
		func(b *userv1.AddressBook) { b.Addresses[0].Fields.Comment = strings.Repeat("x", addressResponseLimit) },
	} {
		copy := proto.Clone(book).(*userv1.AddressBook)
		mutate(copy)
		if validAddressBook(copy) {
			t.Fatal("invalid snapshot accepted")
		}
	}
}
func TestAddressFailureAllowlist(t *testing.T) {
	for _, tc := range []struct {
		code           codes.Code
		domain, reason string
		extra          bool
		want           gatewayv1.UserBrowserFailure
	}{
		{codes.NotFound, "marketmesh.user", "ADDRESS_NOT_FOUND", false, 3},
		{codes.ResourceExhausted, "marketmesh.user", "ADDRESS_LIMIT_REACHED", false, 4},
		{codes.Internal, "marketmesh.user", "ADDRESS_NOT_FOUND", false, 0},
		{codes.NotFound, "other", "ADDRESS_NOT_FOUND", false, 0},
		{codes.NotFound, "marketmesh.user", "ADDRESS_NOT_FOUND", true, 0},
		{codes.ResourceExhausted, "marketmesh.user", "ADDRESS_NOT_FOUND", false, 0},
	} {
		info := &errdetails.ErrorInfo{Domain: tc.domain, Reason: tc.reason}
		if tc.extra {
			info.Metadata = map[string]string{"private": "value"}
		}
		st, _ := status.New(tc.code, "private upstream message").WithDetails(info)
		if got := addressBrowserFailure(st.Err(), true); got != tc.want {
			t.Fatalf("unexpected failure %v", got)
		}
	}
}
func TestAddressMutationIsSingleCallWithOnlyFreshAssertion(t *testing.T) {
	calls, exchanges := 0, 0
	c := &userBrowserClient{auth: exchangeFunc(func(ctx context.Context, _ *authv1.ExchangeBrowserSessionRequest) (*authv1.ExchangeBrowserSessionResponse, error) {
		exchanges++
		md, _ := metadata.FromOutgoingContext(ctx)
		if len(md) != 0 {
			t.Fatal("credentials reached Auth")
		}
		return &authv1.ExchangeBrowserSessionResponse{Assertion: "a.b.c", ExpiresAtUnix: time.Now().Add(time.Minute).Unix()}, nil
	})}
	call := func(ctx context.Context, r *userv1.CreateAddressRequest, _ ...grpc.CallOption) (*userv1.CreateAddressResponse, error) {
		calls++
		md, _ := metadata.FromOutgoingContext(ctx)
		if len(md) != 1 || md.Get(userAssertionMetadata)[0] != "a.b.c" {
			t.Fatal("unexpected User credentials")
		}
		if r.ExpectedBookVersion != 4 {
			t.Fatal("request changed")
		}
		return nil, status.Error(codes.Unavailable, "unknown commit outcome")
	}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "private", "cookie", "private"))
	_, failure, err := invokeAddress(ctx, c, new(authv1.BrowserContext), &userv1.CreateAddressRequest{ExpectedBookVersion: 4}, call, true)
	if calls != 1 || exchanges != 1 || failure != 0 || status.Code(err) != codes.Unavailable {
		t.Fatal("mutation retried or error hidden")
	}
	var missing *userv1.CreateAddressRequest
	if _, _, err := invokeAddress(ctx, c, new(authv1.BrowserContext), missing, call, true); status.Code(err) != codes.InvalidArgument || calls != 1 {
		t.Fatal("nil request reached backend")
	}
	if addressBrowserFailure(errors.New("private"), true) != 0 {
		t.Fatal("unknown failure exposed")
	}
}
func TestAddressRoutePolicies(t *testing.T) {
	specs := addressRoutes(time.Second)
	if len(specs) != 5 {
		t.Fatal("missing route")
	}
	for i, s := range specs {
		if s.MaxRequestBytes != 16*1024 || s.MaxResponseBytes != 128*1024 || s.Mutating != (i > 0) || s.RequireIdempotencyKey || s.MaxDeadline != time.Second {
			t.Fatal("unsafe address policy")
		}
	}
	if userMessageLimit(config{}) != 64*1024 || userMessageLimit(config{userAddressesBrowserEnabled: true}) != 128*1024 {
		t.Fatal("message limits changed while disabled")
	}
}
