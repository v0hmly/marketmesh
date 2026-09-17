package app

import (
	"context"
	"errors"
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
)

type exchangeFunc func(context.Context, *authv1.ExchangeBrowserSessionRequest) (*authv1.ExchangeBrowserSessionResponse, error)

func (f exchangeFunc) ExchangeBrowserSession(ctx context.Context, r *authv1.ExchangeBrowserSessionRequest, _ ...grpc.CallOption) (*authv1.ExchangeBrowserSessionResponse, error) {
	return f(ctx, r)
}

type userStub struct {
	get    func(context.Context, *userv1.GetMeRequest) (*userv1.GetMeResponse, error)
	update func(context.Context, *userv1.UpdateMeRequest) (*userv1.UpdateMeResponse, error)
}

func (s userStub) GetMe(ctx context.Context, r *userv1.GetMeRequest, _ ...grpc.CallOption) (*userv1.GetMeResponse, error) {
	return s.get(ctx, r)
}
func (s userStub) UpdateMe(ctx context.Context, r *userv1.UpdateMeRequest, _ ...grpc.CallOption) (*userv1.UpdateMeResponse, error) {
	return s.update(ctx, r)
}

func TestUserBrowserExchangesPerCallAndReplacesCredentials(t *testing.T) {
	exchanges, gets := 0, 0
	browser := &authv1.BrowserContext{Cookie: []string{"opaque cookie"}, Origin: []string{"https://app.test"}}
	c := &userBrowserClient{
		auth: exchangeFunc(func(ctx context.Context, r *authv1.ExchangeBrowserSessionRequest) (*authv1.ExchangeBrowserSessionResponse, error) {
			exchanges++
			md, _ := metadata.FromOutgoingContext(ctx)
			if len(md) != 0 || r.GetAudience() != "user" || r.GetContext() != browser {
				t.Fatal("Auth received inherited metadata or altered browser context")
			}
			return &authv1.ExchangeBrowserSessionResponse{Assertion: "fresh.signed.assertion", ExpiresAtUnix: time.Now().Add(time.Minute).Unix()}, nil
		}),
		user: userStub{get: func(ctx context.Context, _ *userv1.GetMeRequest) (*userv1.GetMeResponse, error) {
			gets++
			md, _ := metadata.FromOutgoingContext(ctx)
			if len(md) != 1 || len(md.Get(userAssertionMetadata)) != 1 || md.Get(userAssertionMetadata)[0] != "fresh.signed.assertion" {
				t.Fatal("User received untrusted metadata")
			}
			return &userv1.GetMeResponse{Profile: &userv1.Profile{SubjectId: make([]byte, 16), Version: 1}}, nil
		}},
	}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(userAssertionMetadata, "forged", "cookie", "secret", "authorization", "Bearer forged", "traceparent", "opaque"))
	for range 2 {
		response := new(gatewayv1.BrowserGetMeResponse)
		if err := c.Invoke(ctx, gatewayv1.UserBrowserService_BrowserGetMe_FullMethodName, &gatewayv1.BrowserGetMeRequest{Request: new(userv1.GetMeRequest), Context: browser}, response); err != nil || response.GetResponse() == nil || response.GetFailure() != 0 {
			t.Fatalf("GetMe: %v", err)
		}
	}
	if exchanges != 2 || gets != 2 {
		t.Fatal("exchange cached or RPC repeated")
	}
}

func TestUserBrowserFailsBeforeUserOnAuthFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *authv1.ExchangeBrowserSessionResponse
		err      error
	}{
		{name: "unavailable", err: status.Error(codes.Unavailable, "private diagnostic")},
		{name: "empty"},
		{name: "expired", response: &authv1.ExchangeBrowserSessionResponse{Assertion: "a.b.c", ExpiresAtUnix: 1}},
		{name: "malformed", response: &authv1.ExchangeBrowserSessionResponse{Assertion: "Bearer a.b.c", ExpiresAtUnix: time.Now().Add(time.Minute).Unix()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &userBrowserClient{auth: exchangeFunc(func(context.Context, *authv1.ExchangeBrowserSessionRequest) (*authv1.ExchangeBrowserSessionResponse, error) {
				return tc.response, tc.err
			}), user: userStub{get: func(context.Context, *userv1.GetMeRequest) (*userv1.GetMeResponse, error) {
				t.Fatal("User called after invalid exchange")
				return nil, nil
			}}}
			if err := c.Invoke(context.Background(), gatewayv1.UserBrowserService_BrowserGetMe_FullMethodName, &gatewayv1.BrowserGetMeRequest{Request: new(userv1.GetMeRequest), Context: new(authv1.BrowserContext)}, new(gatewayv1.BrowserGetMeResponse)); err == nil {
				t.Fatal("invalid exchange accepted")
			}
		})
	}
}

func TestUserBrowserEnvelopeAndErrorAllowlist(t *testing.T) {
	c := &userBrowserClient{}
	for _, request := range []any{new(userv1.GetMeRequest), new(gatewayv1.BrowserGetMeRequest), &gatewayv1.BrowserGetMeRequest{Request: new(userv1.GetMeRequest)}} {
		if status.Code(c.Invoke(context.Background(), gatewayv1.UserBrowserService_BrowserGetMe_FullMethodName, request, new(gatewayv1.BrowserGetMeResponse))) != codes.InvalidArgument {
			t.Fatal("missing private envelope accepted")
		}
	}
	for _, tc := range []struct {
		domain, reason string
		metadata       map[string]string
		want           gatewayv1.UserBrowserFailure
	}{
		{"marketmesh.user", "PROFILE_NOT_READY", nil, gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_PROFILE_NOT_READY},
		{"attacker", "PROFILE_NOT_READY", nil, 0}, {"marketmesh.user", "OTHER", nil, 0}, {"marketmesh.user", "PROFILE_NOT_READY", map[string]string{"private": "secret"}, 0},
	} {
		st, err := status.New(codes.NotFound, "private error").WithDetails(&errdetails.ErrorInfo{Domain: tc.domain, Reason: tc.reason, Metadata: tc.metadata})
		if err != nil {
			t.Fatal(err)
		}
		if got := userFailure(st.Err(), false); got != tc.want {
			t.Fatalf("unexpected failure %v", got)
		}
	}
	if userFailure(errors.New("private"), true) != 0 || userFailure(status.Error(codes.Aborted, "private"), false) != 0 || userFailure(status.Error(codes.Aborted, "private"), true) != gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_VERSION_CONFLICT {
		t.Fatal("unsafe failure mapping")
	}
}

func TestUserBrowserUpdateConflictAndNoRetries(t *testing.T) {
	calls := 0
	c := &userBrowserClient{auth: exchangeFunc(func(context.Context, *authv1.ExchangeBrowserSessionRequest) (*authv1.ExchangeBrowserSessionResponse, error) {
		return &authv1.ExchangeBrowserSessionResponse{Assertion: "a.b.c", ExpiresAtUnix: time.Now().Add(time.Minute).Unix()}, nil
	}), user: userStub{update: func(_ context.Context, r *userv1.UpdateMeRequest) (*userv1.UpdateMeResponse, error) {
		calls++
		if r.GetExpectedVersion() != 7 {
			t.Fatal("version lost")
		}
		return nil, status.Error(codes.Aborted, "private conflict")
	}}}
	response := new(gatewayv1.BrowserUpdateMeResponse)
	if err := c.Invoke(context.Background(), gatewayv1.UserBrowserService_BrowserUpdateMe_FullMethodName, &gatewayv1.BrowserUpdateMeRequest{Request: &userv1.UpdateMeRequest{ExpectedVersion: 7}, Context: new(authv1.BrowserContext)}, response); err != nil || response.GetFailure() != gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_VERSION_CONFLICT || response.GetResponse() != nil || calls != 1 {
		t.Fatalf("update result: %v", err)
	}
	for _, spec := range userRoutes(time.Second) {
		if spec.RequireIdempotencyKey {
			t.Fatal("real profile route inherited fake idempotency")
		}
	}
}

func TestUserBrowserChecksSuccessfulProfileAndPreservesDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	for _, profile := range []*userv1.Profile{nil, {SubjectId: make([]byte, 15), Version: 1}, {SubjectId: make([]byte, 16)}, {SubjectId: make([]byte, 16), Version: 1, Bio: string(make([]byte, 16*1024))}} {
		c := &userBrowserClient{auth: exchangeFunc(func(authCtx context.Context, _ *authv1.ExchangeBrowserSessionRequest) (*authv1.ExchangeBrowserSessionResponse, error) {
			if got, ok := authCtx.Deadline(); !ok || !got.Equal(deadline) {
				t.Fatal("Auth deadline lost")
			}
			return &authv1.ExchangeBrowserSessionResponse{Assertion: "a.b.c", ExpiresAtUnix: time.Now().Add(time.Minute).Unix()}, nil
		}), user: userStub{get: func(userCtx context.Context, _ *userv1.GetMeRequest) (*userv1.GetMeResponse, error) {
			if got, ok := userCtx.Deadline(); !ok || !got.Equal(deadline) {
				t.Fatal("User deadline lost")
			}
			return &userv1.GetMeResponse{Profile: profile}, nil
		}}}
		if err := c.Invoke(ctx, gatewayv1.UserBrowserService_BrowserGetMe_FullMethodName, &gatewayv1.BrowserGetMeRequest{Request: new(userv1.GetMeRequest), Context: new(authv1.BrowserContext)}, new(gatewayv1.BrowserGetMeResponse)); status.Code(err) != codes.Internal {
			t.Fatalf("invalid profile accepted: %v", err)
		}
	}
}
