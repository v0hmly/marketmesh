package connectbridge

import (
	"crypto/tls"
	gatewayv1 "github.com/v0hmly/marketmesh/api/gen/go/gateway/v1"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
	"google.golang.org/protobuf/proto"
	"net/http/httptest"
	"strings"
	"testing"
)

type settingsInvoker struct{ userInvoker }

func (*settingsInvoker) RoutePolicy(route contractv1.RouteId) (tunnel.RoutePolicy, bool) {
	return tunnel.RoutePolicy{MaxRequestBytes: 16 * 1024, MaxResponseBytes: 16 * 1024}, route == 102 || route == 103 || route == 109 || route == 110
}
func TestSettingsBridgeTypedEnvelopesAndClosedFailures(t *testing.T) {
	for _, tc := range []struct {
		name              string
		request, response proto.Message
		want              int
		route             contractv1.RouteId
	}{
		{"GetSettings", new(gatewayv1.BrowserGetSettingsRequest), &gatewayv1.BrowserGetSettingsResponse{Response: &userv1.GetSettingsResponse{Settings: &userv1.AccountSettings{Version: 1, Theme: 1}}}, 200, 109},
		{"UpdateSettings", new(gatewayv1.BrowserUpdateSettingsRequest), &gatewayv1.BrowserUpdateSettingsResponse{Response: &userv1.UpdateSettingsResponse{Settings: &userv1.AccountSettings{Version: 2, Theme: 3}}}, 200, 110},
		{"GetSettings", new(gatewayv1.BrowserGetSettingsRequest), &gatewayv1.BrowserGetSettingsResponse{Failure: 1}, 404, 109},
		{"GetSettings", new(gatewayv1.BrowserGetSettingsRequest), &gatewayv1.BrowserGetSettingsResponse{Failure: 2}, 500, 109},
		{"UpdateSettings", new(gatewayv1.BrowserUpdateSettingsRequest), &gatewayv1.BrowserUpdateSettingsResponse{Failure: 2}, 409, 110},
		{"UpdateSettings", new(gatewayv1.BrowserUpdateSettingsRequest), &gatewayv1.BrowserUpdateSettingsResponse{Failure: 3}, 500, 110},
		{"UpdateSettings", new(gatewayv1.BrowserUpdateSettingsRequest), &gatewayv1.BrowserUpdateSettingsResponse{Failure: 4}, 500, 110},
		{"UpdateSettings", new(gatewayv1.BrowserUpdateSettingsRequest), &gatewayv1.BrowserUpdateSettingsResponse{Failure: 2, Response: new(userv1.UpdateSettingsResponse)}, 500, 110},
	} {
		payload, _ := proto.Marshal(tc.response)
		inv := &settingsInvoker{userInvoker{fakeInvoker: fakeInvoker{response: tunnel.Response{Payload: payload}}}}
		handler, err := NewAccountHandler(inv, false, true)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/user.v1.UserService/"+tc.name, strings.NewReader(`{"theme":"THEME_DARK","expectedVersion":"7","subjectId":"forged","context":{"cookie":["forged"]}}`))
		req.TLS = &tls.ConnectionState{}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")
		req.Header.Set("Cookie", "session=opaque")
		req.Header.Set("Authorization", "forged")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != tc.want || rr.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s status=%d want=%d", tc.name, rr.Code, tc.want)
		}
		call := inv.lastCall()
		if call.Route != tc.route || len(call.Metadata) != 0 || len(call.IdempotencyKey) != 0 || inv.calls != 1 {
			t.Fatal("wrong private route")
		}
		if err := proto.Unmarshal(call.Payload, tc.request); err != nil {
			t.Fatal(err)
		}
		msg := tc.request.ProtoReflect()
		browser := msg.Get(msg.Descriptor().Fields().ByName("context")).Message()
		cookies := browser.Get(browser.Descriptor().Fields().ByName("cookie")).List()
		if cookies.Len() != 1 || cookies.Get(0).String() != "session=opaque" {
			t.Fatal("forged context forwarded")
		}
		if r, ok := tc.request.(*gatewayv1.BrowserUpdateSettingsRequest); ok && (r.GetRequest().GetTheme() != 3 || r.GetRequest().GetExpectedVersion() != 7) {
			t.Fatal("settings input lost")
		}
	}
}
func TestSettingsBridgeDisabledAndBounded(t *testing.T) {
	inv := &settingsInvoker{}
	for _, enabled := range []bool{false, true} {
		handler, err := NewAccountHandler(inv, false, enabled)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			path, body string
			secure     bool
			want       int
		}{
			{"/user.v1.UserService/UpdateSettings", strings.Repeat(" ", 16385), true, 429},
			{"/user.v1.UserService/GetSettings", "{}", false, 403},
			{"/gateway.v1.UserBrowserService/BrowserGetSettings", "{}", true, 404},
		} {
			req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.secure {
				req.TLS = &tls.ConnectionState{}
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			want := tc.want
			if !enabled && tc.secure {
				want = 404
			}
			if rr.Code != want {
				t.Fatalf("status=%d want=%d", rr.Code, want)
			}
		}
	}
	if inv.calls != 0 {
		t.Fatal("invalid request forwarded")
	}
}
