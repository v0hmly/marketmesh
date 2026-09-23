package connectbridge

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	gatewayv1 "github.com/v0hmly/marketmesh/api/gen/go/gateway/v1"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/proto"
)

type userInvoker struct {
	fakeInvoker
	calls int
}

func (f *userInvoker) Invoke(ctx context.Context, call tunnel.Call) (tunnel.Response, error) {
	f.calls++
	return f.fakeInvoker.Invoke(ctx, call)
}
func (*userInvoker) RoutePolicy(route contractv1.RouteId) (tunnel.RoutePolicy, bool) {
	return tunnel.RoutePolicy{MaxRequestBytes: 16384, MaxResponseBytes: 16384}, route == 102 || route == 103
}
func userHTTP(t *testing.T, invoker *userInvoker, method, path, body string, secure bool, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	handler, err := NewUserHandler(invoker)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if secure {
		request.TLS = &tls.ConnectionState{}
	}
	request.Header = headers.Clone()
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Connect-Protocol-Version", "1")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable response")
	}
	return result
}
func TestUserFixedRoutesAndOpaqueContext(t *testing.T) {
	for _, test := range []struct {
		method, body      string
		route             contractv1.RouteId
		request, response proto.Message
	}{
		{"GetMe", `{"subjectId":"forged","context":{"cookie":["evil"]}}`, 102, &gatewayv1.BrowserGetMeRequest{}, &gatewayv1.BrowserGetMeResponse{Response: &userv1.GetMeResponse{Profile: &userv1.Profile{DisplayName: "Alice"}}}},
		{"UpdateMe", `{"displayName":"Alice","bio":"hello","expectedVersion":"1"}`, 103, &gatewayv1.BrowserUpdateMeRequest{}, &gatewayv1.BrowserUpdateMeResponse{Response: &userv1.UpdateMeResponse{Profile: &userv1.Profile{DisplayName: "Alice", Version: 2}}}},
	} {
		t.Run(test.method, func(t *testing.T) {
			payload, err := proto.Marshal(test.response)
			if err != nil {
				t.Fatal(err)
			}
			invoker := &userInvoker{fakeInvoker: fakeInvoker{response: tunnel.Response{Payload: payload}}}
			headers := http.Header{"Cookie": {"a=opaque==", "b=second"}, "Origin": {"https://shop.test"}, "Sec-Fetch-Site": {"same-origin"}, "Authorization": {"Bearer private"}, "X-Session-Assertion": {"forged"}}
			result := userHTTP(t, invoker, "POST", "/user.v1.UserService/"+test.method, test.body, true, headers)
			if result.Code != 200 || !strings.Contains(result.Body.String(), "Alice") {
				t.Fatal(result.Code, result.Body.String())
			}
			call := invoker.lastCall()
			if call.Route != test.route || invoker.calls != 1 || len(call.Metadata) != 0 || len(call.IdempotencyKey) != 0 {
				t.Fatal("unexpected call")
			}
			if err := proto.Unmarshal(call.Payload, test.request); err != nil {
				t.Fatal(err)
			}
			browser := test.request.ProtoReflect().Get(test.request.ProtoReflect().Descriptor().Fields().ByName("context")).Message().Interface().(*authv1.BrowserContext)
			if !proto.Equal(browser, &authv1.BrowserContext{Cookie: headers.Values("Cookie"), Origin: headers.Values("Origin"), SecFetchSite: headers.Values("Sec-Fetch-Site")}) {
				t.Fatal("context changed")
			}
			if strings.Contains(string(call.Payload), "private") || strings.Contains(string(call.Payload), "forged") {
				t.Fatal("client assertion forwarded")
			}
		})
	}
}
func TestUserRejectsBeforeInvoke(t *testing.T) {
	for _, test := range []struct {
		name, method, path, body string
		secure                   bool
		headers                  http.Header
	}{
		{"HTTP", "POST", "/user.v1.UserService/GetMe", "{}", false, http.Header{"X-Forwarded-Proto": {"https"}}},
		{"GET", "GET", "/user.v1.UserService/GetMe", "{}", true, nil},
		{"private RPC", "POST", "/gateway.v1.UserBrowserService/BrowserGetMe", "{}", true, nil},
		{"malformed", "POST", "/user.v1.UserService/GetMe", "{", true, nil},
		{"body bound", "POST", "/user.v1.UserService/UpdateMe", strings.Repeat(" ", 16385), true, nil},
		{"cookie bound", "POST", "/user.v1.UserService/GetMe", "{}", true, http.Header{"Cookie": {strings.Repeat("x", 8193)}}},
		{"context injection", "POST", "/user.v1.UserService/GetMe", "{}", true, http.Header{"Origin": {"https://a\r\nx:y"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			invoker := new(userInvoker)
			result := userHTTP(t, invoker, test.method, test.path, test.body, test.secure, test.headers)
			if result.Code == 200 || invoker.calls != 0 {
				t.Fatal("request accepted", result.Code)
			}
		})
	}
}
func TestUserFailureEnvelopeAndSafeErrors(t *testing.T) {
	for _, test := range []struct {
		name     string
		response *gatewayv1.BrowserGetMeResponse
		payload  []byte
		err      error
		status   int
		reason   string
	}{
		{name: "preparing", response: &gatewayv1.BrowserGetMeResponse{Failure: 1}, status: 404, reason: "PROFILE_NOT_READY"},
		{name: "conflict", response: &gatewayv1.BrowserGetMeResponse{Failure: 2}, status: 409, reason: "aborted"},
		{name: "unknown", response: &gatewayv1.BrowserGetMeResponse{Failure: 99}, status: 500},
		{name: "ambiguous", response: &gatewayv1.BrowserGetMeResponse{Failure: 1, Response: &userv1.GetMeResponse{}}, status: 500},
		{name: "missing", response: &gatewayv1.BrowserGetMeResponse{}, status: 500},
		{name: "malformed", payload: []byte{255}, status: 500},
		{name: "oversized", payload: make([]byte, 16385), status: 429},
		{name: "private error", err: errors.New("secret backend user.internal"), status: 500},
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
			invoker := &userInvoker{fakeInvoker: fakeInvoker{response: tunnel.Response{Payload: payload}, err: test.err}}
			result := userHTTP(t, invoker, "POST", "/user.v1.UserService/GetMe", "{}", true, nil)
			if result.Code != test.status {
				t.Fatal(result.Code, result.Body.String())
			}
			// ErrorInfo is a protobuf detail encoded as base64 in Connect JSON.
			if test.reason == "aborted" && !strings.Contains(result.Body.String(), test.reason) {
				t.Fatal(result.Body.String())
			}
			if test.reason == "PROFILE_NOT_READY" && !strings.Contains(result.Body.String(), "google.rpc.ErrorInfo") {
				t.Fatal(result.Body.String())
			}
			if strings.Contains(result.Body.String(), "secret") || strings.Contains(result.Body.String(), "user.internal") {
				t.Fatal("private error leaked")
			}
		})
	}
}

func TestUserPreparingDetailHasExactPublicContract(t *testing.T) {
	err := userFailure(gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_PROFILE_NOT_READY)
	var connectError *connect.Error
	if !errors.As(err, &connectError) || connectError.Code() != connect.CodeNotFound || len(connectError.Details()) != 1 {
		t.Fatal("invalid public error")
	}
	value, err := connectError.Details()[0].Value()
	if err != nil {
		t.Fatal(err)
	}
	detail, ok := value.(*errdetails.ErrorInfo)
	if !ok || detail.Domain != "marketmesh.user" || detail.Reason != "PROFILE_NOT_READY" || len(detail.Metadata) != 0 {
		t.Fatal("invalid public detail")
	}
}
