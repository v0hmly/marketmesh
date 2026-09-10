package internalgrpc

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
	"testing"
)

func TestBrowserContextBoundaries(t *testing.T) {
	for _, value := range []*authv1.BrowserContext{nil, {Cookie: []string{strings.Repeat("a", 8193)}}, {Origin: []string{strings.Repeat("a", 2049)}}, {SecFetchSite: []string{strings.Repeat("a", 257)}}, {Cookie: make([]string, 17)}, {Cookie: []string{"x\r\ny"}}, {Origin: []string{"x\x00y"}}} {
		if _, err := browserHeader(value); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid context accepted: %v", err)
		}
	}
	h, err := browserHeader(&authv1.BrowserContext{Cookie: []string{"opaque=a", "opaque=b"}, Origin: []string{"https://app.example", "https://app.example"}})
	if err != nil || len(h.Values("Origin")) != 2 || len(h.Values("Cookie")) != 2 {
		t.Fatal("header lines were changed")
	}
}
func TestBrowserDirectCallsRequireWorkload(t *testing.T) {
	h := &BrowserHandler{}
	ctx := context.Background()
	_, err := h.BrowserRegisterCredentials(ctx, nil)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	_, err = h.BrowserLogin(ctx, nil)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	_, err = h.BrowserRefreshSession(ctx, nil)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	_, err = h.BrowserLogout(ctx, nil)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	_, err = h.BrowserLogoutAll(ctx, nil)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
}
func TestBrowserErrorsNeverForwardDetails(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeInternal, connect.CodeInvalidArgument, connect.CodeUnauthenticated, connect.CodeUnavailable, connect.CodeUnimplemented} {
		err := connect.NewError(code, errors.New("secret-cookie-and-database-details"))
		err.Meta().Set("Set-Cookie", "secret")
		mapped := browserFailure(err)
		if strings.Contains(mapped.Error(), "secret") || len(status.Convert(mapped).Details()) != 0 {
			t.Fatal("error leaked private metadata")
		}
	}
}
