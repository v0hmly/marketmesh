package connectbridge

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
	"google.golang.org/protobuf/proto"
)

type authInvoker struct {
	fakeInvoker
	calls int
}

func (f *authInvoker) Invoke(ctx context.Context, call tunnel.Call) (tunnel.Response, error) {
	f.calls++
	return f.fakeInvoker.Invoke(ctx, call)
}
func (*authInvoker) RoutePolicy(route contractv1.RouteId) (tunnel.RoutePolicy, bool) {
	return tunnel.RoutePolicy{MaxRequestBytes: 16384, MaxResponseBytes: 16384}, strings.HasPrefix(route.String(), "ROUTE_ID_AUTH_") && route != contractv1.RouteId_ROUTE_ID_AUTH_SESSION_ASSERTION
}
func authHTTP(t *testing.T, invoker *authInvoker, method, procedure, body string, secure bool, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	handler, err := NewAuthHandler(invoker)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, "/auth.v1.AuthService/"+procedure, strings.NewReader(body))
	if secure {
		request.TLS = &tls.ConnectionState{}
	}
	request.Header = headers.Clone()
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Connect-Protocol-Version", "1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("response may be cached")
	}
	return recorder
}

func TestAuthFixedRoutesAndPrivateContext(t *testing.T) {
	for _, test := range []struct {
		method            string
		route             contractv1.RouteId
		request, response proto.Message
		body              string
	}{
		{"RegisterCredentials", 1, &authv1.BrowserRegisterCredentialsRequest{}, &authv1.BrowserRegisterCredentialsResponse{Response: &authv1.RegisterCredentialsResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{"identifier":"alice","password":"c2VjcmV0"}`},
		{"Login", 2, &authv1.BrowserLoginRequest{}, &authv1.BrowserLoginResponse{Response: &authv1.LoginResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{"identifier":"alice","password":"c2VjcmV0"}`},
		{"RefreshSession", 3, &authv1.BrowserRefreshSessionRequest{}, &authv1.BrowserRefreshSessionResponse{Response: &authv1.RefreshSessionResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"Logout", 4, &authv1.BrowserLogoutRequest{}, &authv1.BrowserLogoutResponse{Response: &authv1.LogoutResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"LogoutAll", 6, &authv1.BrowserLogoutAllRequest{}, &authv1.BrowserLogoutAllResponse{Response: &authv1.LogoutAllResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"StartLoginCodeChange", contractv1.RouteId_ROUTE_ID_AUTH_START_LOGIN_CODE_CHANGE, &authv1.BrowserStartLoginCodeChangeRequest{}, &authv1.BrowserStartLoginCodeChangeResponse{Response: &authv1.StartLoginCodeChangeResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"CompleteLoginCodeChange", contractv1.RouteId_ROUTE_ID_AUTH_COMPLETE_LOGIN_CODE_CHANGE, &authv1.BrowserCompleteLoginCodeChangeRequest{}, &authv1.BrowserCompleteLoginCodeChangeResponse{Response: &authv1.CompleteLoginCodeChangeResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"ChangePassword", contractv1.RouteId_ROUTE_ID_AUTH_CHANGE_PASSWORD, &authv1.BrowserChangePasswordRequest{}, &authv1.BrowserChangePasswordResponse{Response: &authv1.ChangePasswordResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"StartLogin", contractv1.RouteId_ROUTE_ID_AUTH_START_LOGIN, &authv1.BrowserStartLoginRequest{}, &authv1.BrowserStartLoginResponse{Response: &authv1.StartLoginResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"CompleteLogin", contractv1.RouteId_ROUTE_ID_AUTH_COMPLETE_LOGIN, &authv1.BrowserCompleteLoginRequest{}, &authv1.BrowserCompleteLoginResponse{Response: &authv1.CompleteLoginResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"ResendLoginCode", contractv1.RouteId_ROUTE_ID_AUTH_RESEND_LOGIN_CODE, &authv1.BrowserResendLoginCodeRequest{}, &authv1.BrowserResendLoginCodeResponse{Response: &authv1.ResendLoginCodeResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"RequestEmailVerification", contractv1.RouteId_ROUTE_ID_AUTH_REQUEST_EMAIL_VERIFICATION, &authv1.BrowserRequestEmailVerificationRequest{}, &authv1.BrowserRequestEmailVerificationResponse{Response: &authv1.RequestEmailVerificationResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"ConfirmEmail", contractv1.RouteId_ROUTE_ID_AUTH_CONFIRM_EMAIL, &authv1.BrowserConfirmEmailRequest{}, &authv1.BrowserConfirmEmailResponse{Response: &authv1.ConfirmEmailResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"RequestPasswordReset", contractv1.RouteId_ROUTE_ID_AUTH_REQUEST_PASSWORD_RESET, &authv1.BrowserRequestPasswordResetRequest{}, &authv1.BrowserRequestPasswordResetResponse{Response: &authv1.RequestPasswordResetResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"ConfirmPasswordReset", contractv1.RouteId_ROUTE_ID_AUTH_CONFIRM_PASSWORD_RESET, &authv1.BrowserConfirmPasswordResetRequest{}, &authv1.BrowserConfirmPasswordResetResponse{Response: &authv1.ConfirmPasswordResetResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"GetCredentials", contractv1.RouteId_ROUTE_ID_AUTH_GET_CREDENTIALS, &authv1.BrowserGetCredentialsRequest{}, &authv1.BrowserGetCredentialsResponse{Response: &authv1.GetCredentialsResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"StartEmailChange", contractv1.RouteId_ROUTE_ID_AUTH_START_EMAIL_CHANGE, &authv1.BrowserStartEmailChangeRequest{}, &authv1.BrowserStartEmailChangeResponse{Response: &authv1.StartEmailChangeResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"ConfirmEmailChange", contractv1.RouteId_ROUTE_ID_AUTH_CONFIRM_EMAIL_CHANGE, &authv1.BrowserConfirmEmailChangeRequest{}, &authv1.BrowserConfirmEmailChangeResponse{Response: &authv1.ConfirmEmailChangeResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"CancelEmailChange", contractv1.RouteId_ROUTE_ID_AUTH_CANCEL_EMAIL_CHANGE, &authv1.BrowserCancelEmailChangeRequest{}, &authv1.BrowserCancelEmailChangeResponse{Response: &authv1.CancelEmailChangeResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"ListSessions", contractv1.RouteId_ROUTE_ID_AUTH_LIST_SESSIONS, &authv1.BrowserListSessionsRequest{}, &authv1.BrowserListSessionsResponse{Response: &authv1.ListSessionsResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"RevokeSession", contractv1.RouteId_ROUTE_ID_AUTH_REVOKE_OWNED_SESSION, &authv1.BrowserRevokeSessionRequest{}, &authv1.BrowserRevokeSessionResponse{Response: &authv1.RevokeSessionResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"RequestAccountDeletion", contractv1.RouteId_ROUTE_ID_AUTH_REQUEST_ACCOUNT_DELETION, &authv1.BrowserRequestAccountDeletionRequest{}, &authv1.BrowserRequestAccountDeletionResponse{Response: &authv1.RequestAccountDeletionResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
		{"CancelAccountDeletion", contractv1.RouteId_ROUTE_ID_AUTH_CANCEL_ACCOUNT_DELETION, &authv1.BrowserCancelAccountDeletionRequest{}, &authv1.BrowserCancelAccountDeletionResponse{Response: &authv1.CancelAccountDeletionResponse{}, SetCookie: []string{"a=secret; Secure", "b=secret; HttpOnly"}}, `{}`},
	} {
		t.Run(test.method, func(t *testing.T) {
			payload, err := proto.Marshal(test.response)
			if err != nil {
				t.Fatal(err)
			}
			invoker := &authInvoker{fakeInvoker: fakeInvoker{response: tunnel.Response{Payload: payload}}}
			headers := http.Header{"Cookie": {"opaque==; odd=value", "second=2"}, "Origin": {"https://a.test", "https://b.test"}, "Sec-Fetch-Site": {"same-origin"}, "Authorization": {"Bearer never-forward"}, "Forwarded": {"host=private"}, "X-Forwarded-Proto": {"https"}}
			result := authHTTP(t, invoker, http.MethodPost, test.method, test.body, true, headers)
			if result.Code != 200 {
				t.Fatal(result.Code, result.Body.String())
			}
			if len(result.Header().Values("Set-Cookie")) != 2 {
				t.Fatal("lost cookie multiplicity")
			}
			if strings.Contains(result.Body.String(), "secret") || strings.Contains(result.Body.String(), "setCookie") {
				t.Fatal("private data in public response")
			}
			call := invoker.lastCall()
			if call.Route != test.route || len(call.Metadata) != 0 || len(call.IdempotencyKey) != 0 || invoker.calls != 1 {
				t.Fatal("unexpected tunnel call")
			}
			if err := proto.Unmarshal(call.Payload, test.request); err != nil {
				t.Fatal(err)
			}
			contextMessage := test.request.ProtoReflect().Get(test.request.ProtoReflect().Descriptor().Fields().ByName("context")).Message().Interface().(*authv1.BrowserContext)
			if !proto.Equal(contextMessage, &authv1.BrowserContext{Cookie: headers.Values("Cookie"), Origin: headers.Values("Origin"), SecFetchSite: headers.Values("Sec-Fetch-Site")}) {
				t.Fatal("browser context changed")
			}
			if test.method == "Login" {
				req := test.request.(*authv1.BrowserLoginRequest).GetRequest()
				if req.Identifier != "alice" || string(req.Password) != "secret" {
					t.Fatal("credentials changed")
				}
			}
		})
	}
}

func TestAuthRejectsBeforeInvoke(t *testing.T) {
	for _, test := range []struct {
		name, method, procedure, body string
		secure                        bool
		headers                       http.Header
	}{
		{"plain HTTP", "POST", "Login", "{}", false, http.Header{"X-Forwarded-Proto": {"https"}}},
		{"GET", "GET", "Login", "{}", true, nil},
		{"private RPC", "POST", "SessionAssertion", "{}", true, nil},
		{"malformed", "POST", "Login", "{", true, nil},
		{"body limit", "POST", "Login", strings.Repeat(" ", 16385), true, nil},
		{"cookie limit", "POST", "Login", "{}", true, http.Header{"Cookie": {strings.Repeat("x", 8193)}}},
		{"context injection", "POST", "Login", "{}", true, http.Header{"Origin": {"https://a.test\r\nx: y"}}},
		{"idempotency forbidden", "POST", "Login", "{}", true, http.Header{"Idempotency-Key": {"x"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			invoker := new(authInvoker)
			result := authHTTP(t, invoker, test.method, test.procedure, test.body, test.secure, test.headers)
			if result.Code == 200 || invoker.calls != 0 {
				t.Fatal("request accepted", result.Code)
			}
		})
	}
}

func TestAuthResponseValidationAndSafeErrors(t *testing.T) {
	for _, test := range []struct {
		name     string
		response *authv1.BrowserLoginResponse
		payload  []byte
		err      error
	}{
		{name: "invalid cookie after valid", response: &authv1.BrowserLoginResponse{Response: &authv1.LoginResponse{}, SetCookie: []string{"valid=1", "bad=2\r\nInjected: yes"}}},
		{name: "oversized cookies", response: &authv1.BrowserLoginResponse{Response: &authv1.LoginResponse{}, SetCookie: []string{strings.Repeat("x", 8193)}}},
		{name: "missing response", response: &authv1.BrowserLoginResponse{}},
		{name: "malformed private response", payload: []byte{255}},
		{name: "oversized private response", payload: make([]byte, 16385)},
		{name: "safe tunnel failure", err: errors.Join(tunnel.ErrNoTunnel, errors.New("password=private backend=auth.internal"))},
		{name: "cancelled", err: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := test.payload
			if test.response != nil {
				var err error
				payload, err = proto.Marshal(test.response)
				if err != nil {
					t.Fatal(err)
				}
			}
			invoker := &authInvoker{fakeInvoker: fakeInvoker{response: tunnel.Response{Payload: payload}, err: test.err}}
			result := authHTTP(t, invoker, "POST", "Login", "{}", true, nil)
			if result.Code == 200 || len(result.Header().Values("Set-Cookie")) != 0 || strings.Contains(result.Body.String(), "private") || invoker.calls != 1 {
				t.Fatal("unsafe response", result.Code, result.Body.String())
			}
		})
	}
}

func TestBrowserContextBounds(t *testing.T) {
	for _, field := range []struct {
		name  string
		limit int
	}{{"Cookie", 8192}, {"Origin", 2048}, {"Sec-Fetch-Site", 256}} {
		for _, values := range [][]string{{strings.Repeat("x", field.limit+1)}, make([]string, 17), {"bad\x00value"}} {
			if _, err := browserContext(http.Header{field.name: values}); err == nil {
				t.Fatal("accepted invalid field", field.name)
			}
		}
		if _, err := browserContext(http.Header{field.name: {strings.Repeat("x", field.limit)}}); err != nil {
			t.Fatal("rejected boundary", field.name)
		}
	}
}

func TestNewAuthHandlerRejectsMissingPolicy(t *testing.T) {
	var typedNil *authInvoker
	for _, invoker := range []Invoker{nil, typedNil, &fakeInvoker{}} {
		if _, err := NewAuthHandler(invoker); err == nil {
			t.Fatal("accepted unavailable auth policies")
		}
	}
}
