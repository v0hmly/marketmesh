package connectbridge

import (
	"crypto/tls"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	gatewayv1 "github.com/v0hmly/marketmesh/api/gen/go/gateway/v1"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
	"google.golang.org/protobuf/proto"
	"net/http/httptest"
	"strings"
	"testing"
)

type avatarInvoker struct{ userInvoker }

func (*avatarInvoker) RoutePolicy(route contractv1.RouteId) (tunnel.RoutePolicy, bool) {
	return tunnel.RoutePolicy{MaxRequestBytes: 16384, MaxResponseBytes: 16384}, route >= 102 && route <= 113
}
func TestAvatarBrowserBoundary(t *testing.T) {
	for _, tc := range []struct {
		name              string
		route             contractv1.RouteId
		request, response proto.Message
		want              int
	}{
		{"GetAvatar", 111, new(gatewayv1.BrowserGetAvatarRequest), &gatewayv1.BrowserGetAvatarResponse{Response: &userv1.GetAvatarResponse{Avatar: &userv1.Avatar{Version: 1}}}, 200},
		{"SetAvatar", 112, new(gatewayv1.BrowserSetAvatarRequest), &gatewayv1.BrowserSetAvatarResponse{Response: &userv1.SetAvatarResponse{Avatar: &userv1.Avatar{Version: 2}}}, 200},
		{"ClearAvatar", 113, new(gatewayv1.BrowserClearAvatarRequest), &gatewayv1.BrowserClearAvatarResponse{Response: &userv1.ClearAvatarResponse{Avatar: &userv1.Avatar{Version: 3}}}, 200},
		{"SetAvatar", 112, new(gatewayv1.BrowserSetAvatarRequest), &gatewayv1.BrowserSetAvatarResponse{Failure: 5}, 400},
		{"ClearAvatar", 113, new(gatewayv1.BrowserClearAvatarRequest), &gatewayv1.BrowserClearAvatarResponse{Failure: 2}, 409},
		{"GetAvatar", 111, new(gatewayv1.BrowserGetAvatarRequest), &gatewayv1.BrowserGetAvatarResponse{Failure: 99}, 500},
	} {
		payload, _ := proto.Marshal(tc.response)
		inv := &avatarInvoker{userInvoker{fakeInvoker: fakeInvoker{response: tunnel.Response{Payload: payload}}}}
		handler, err := NewAccountAvatarHandler(inv, false, true, true)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/user.v1.UserService/"+tc.name, strings.NewReader(`{"expectedVersion":"7","subjectId":"forged","context":{"cookie":["forged"]}}`))
		req.TLS = &tls.ConnectionState{}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")
		req.Header.Set("Cookie", "opaque=one")
		req.Header.Set("Origin", "https://shop.test")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Authorization", "forged")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != tc.want || rr.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: status %d want %d", tc.name, rr.Code, tc.want)
		}
		call := inv.lastCall()
		if call.Route != tc.route || len(call.Metadata) != 0 || inv.calls != 1 {
			t.Fatal("wrong route or context")
		}
		if err := proto.Unmarshal(call.Payload, tc.request); err != nil {
			t.Fatal(err)
		}
		msg := tc.request.ProtoReflect()
		browser := msg.Get(msg.Descriptor().Fields().ByName("context")).Message().Interface().(*authv1.BrowserContext)
		if !proto.Equal(browser, &authv1.BrowserContext{Cookie: []string{"opaque=one"}, Origin: []string{"https://shop.test"}, SecFetchSite: []string{"same-origin"}}) || strings.Contains(string(call.Payload), "forged") {
			t.Fatal("forged identity/context passed")
		}
	}
	for _, enabled := range []bool{false, true} {
		for _, secure := range []bool{false, true} {
			inv := &avatarInvoker{}
			handler, err := NewAccountAvatarHandler(inv, false, true, enabled)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/user.v1.UserService/ClearAvatar", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			if secure {
				req.TLS = &tls.ConnectionState{}
			}
			if enabled && secure {
				req.Header.Set("Cookie", strings.Repeat("x", 8193))
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code == 200 || inv.calls != 0 {
				t.Fatal("disabled/insecure/oversize avatar forwarded")
			}
		}
	}
}
