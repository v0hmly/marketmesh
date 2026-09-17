package app

import (
	"bytes"
	"context"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	gatewayv1 "github.com/v0hmly/marketmesh/api/gen/go/gateway/v1"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"testing"
	"time"
)

type settingsStub struct {
	userv1.UserServiceClient
	get    func(context.Context, *userv1.GetSettingsRequest) (*userv1.GetSettingsResponse, error)
	update func(context.Context, *userv1.UpdateSettingsRequest) (*userv1.UpdateSettingsResponse, error)
}

func (s settingsStub) GetSettings(ctx context.Context, r *userv1.GetSettingsRequest, _ ...grpc.CallOption) (*userv1.GetSettingsResponse, error) {
	return s.get(ctx, r)
}
func (s settingsStub) UpdateSettings(ctx context.Context, r *userv1.UpdateSettingsRequest, _ ...grpc.CallOption) (*userv1.UpdateSettingsResponse, error) {
	return s.update(ctx, r)
}
func TestSettingsSnapshotIsBoundedAndTyped(t *testing.T) {
	good := &userv1.AccountSettings{SubjectId: bytes.Repeat([]byte{1}, 16), Version: 1, Theme: userv1.Theme_THEME_SYSTEM}
	for _, theme := range []userv1.Theme{userv1.Theme_THEME_SYSTEM, userv1.Theme_THEME_LIGHT, userv1.Theme_THEME_DARK} {
		good.Theme = theme
		if !validSettings(good) {
			t.Fatal("supported theme rejected")
		}
	}
	for _, bad := range []*userv1.AccountSettings{nil, {SubjectId: good.SubjectId, Version: 1}, {SubjectId: good.SubjectId, Version: 1, Theme: 99}, {SubjectId: good.SubjectId, Theme: 1}, {SubjectId: make([]byte, 16), Version: 1, Theme: 1}} {
		if validSettings(bad) {
			t.Fatal("unsafe snapshot accepted")
		}
	}
	large := proto.Clone(good).(*userv1.AccountSettings)
	large.ProtoReflect().SetUnknown(bytes.Repeat([]byte{0x20, 0x01}, 9000))
	if validSettings(large) {
		t.Fatal("oversized snapshot accepted")
	}
	for i, spec := range settingsRoutes(time.Second) {
		if spec.Mutating != (i == 1) || spec.RequireIdempotencyKey || spec.MaxRequestBytes != 16*1024 || spec.MaxResponseBytes != 16*1024 {
			t.Fatal("unsafe policy")
		}
	}
	if userMessageLimit(config{userSettingsBrowserEnabled: true}) != 64*1024 {
		t.Fatal("settings widened transport")
	}
}
func TestSettingsInvocationsUseFreshAssertionAndNeverRepeatMutation(t *testing.T) {
	exchanges, reads, writes := 0, 0, 0
	check := func(ctx context.Context) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if len(md) != 1 || len(md.Get(userAssertionMetadata)) != 1 || md.Get(userAssertionMetadata)[0] != "a.b.c" {
			t.Fatal("untrusted User metadata")
		}
	}
	c := &userBrowserClient{auth: exchangeFunc(func(ctx context.Context, r *authv1.ExchangeBrowserSessionRequest) (*authv1.ExchangeBrowserSessionResponse, error) {
		exchanges++
		md, _ := metadata.FromOutgoingContext(ctx)
		if len(md) != 0 || r.GetAudience() != "user" {
			t.Fatal("untrusted Auth metadata")
		}
		return &authv1.ExchangeBrowserSessionResponse{Assertion: "a.b.c", ExpiresAtUnix: time.Now().Add(time.Minute).Unix()}, nil
	}), user: settingsStub{
		get: func(ctx context.Context, _ *userv1.GetSettingsRequest) (*userv1.GetSettingsResponse, error) {
			check(ctx)
			reads++
			return &userv1.GetSettingsResponse{Settings: &userv1.AccountSettings{SubjectId: bytes.Repeat([]byte{1}, 16), Version: 1, Theme: 1}}, nil
		},
		update: func(ctx context.Context, r *userv1.UpdateSettingsRequest) (*userv1.UpdateSettingsResponse, error) {
			check(ctx)
			writes++
			if r.GetExpectedVersion() != 7 || r.GetTheme() != 3 {
				t.Fatal("typed request changed")
			}
			return nil, status.Error(codes.Unavailable, "unknown commit outcome")
		},
	}}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("cookie", "private", "authorization", "private", userAssertionMetadata, "forged"))
	for range 2 {
		response := new(gatewayv1.BrowserGetSettingsResponse)
		err := c.Invoke(ctx, gatewayv1.UserBrowserService_BrowserGetSettings_FullMethodName, &gatewayv1.BrowserGetSettingsRequest{Request: new(userv1.GetSettingsRequest), Context: new(authv1.BrowserContext)}, response)
		if err != nil || response.Response == nil {
			t.Fatal("read failed", err)
		}
	}
	response := new(gatewayv1.BrowserUpdateSettingsResponse)
	err := c.Invoke(ctx, gatewayv1.UserBrowserService_BrowserUpdateSettings_FullMethodName, &gatewayv1.BrowserUpdateSettingsRequest{Request: &userv1.UpdateSettingsRequest{Theme: 3, ExpectedVersion: 7}, Context: new(authv1.BrowserContext)}, response)
	if status.Code(err) != codes.Unavailable || response.Response != nil || response.Failure != 0 || exchanges != 3 || reads != 2 || writes != 1 {
		t.Fatal("operation repeated or outcome hidden")
	}
	if err := c.Invoke(ctx, gatewayv1.UserBrowserService_BrowserUpdateSettings_FullMethodName, &gatewayv1.BrowserUpdateSettingsRequest{Context: new(authv1.BrowserContext)}, response); status.Code(err) != codes.InvalidArgument || exchanges != 3 {
		t.Fatal("nil request reached backend")
	}
	c.auth = exchangeFunc(func(context.Context, *authv1.ExchangeBrowserSessionRequest) (*authv1.ExchangeBrowserSessionResponse, error) {
		return nil, status.Error(codes.Unauthenticated, "private")
	})
	err = c.Invoke(ctx, gatewayv1.UserBrowserService_BrowserGetSettings_FullMethodName, &gatewayv1.BrowserGetSettingsRequest{Request: new(userv1.GetSettingsRequest), Context: new(authv1.BrowserContext)}, new(gatewayv1.BrowserGetSettingsResponse))
	if status.Code(err) != codes.Unauthenticated || reads != 2 {
		t.Fatal("Auth rejection reached User")
	}
}
