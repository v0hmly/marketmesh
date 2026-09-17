package connectbridge

import (
	"crypto/tls"
	gatewayv1 "github.com/v0hmly/marketmesh/api/gen/go/gateway/v1"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
	"google.golang.org/protobuf/proto"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type addressInvoker struct{ userInvoker }

func (*addressInvoker) RoutePolicy(route contractv1.RouteId) (tunnel.RoutePolicy, bool) {
	limit := 16 * 1024
	if route >= 104 {
		limit = 128 * 1024
	}
	return tunnel.RoutePolicy{MaxRequestBytes: 16 * 1024, MaxResponseBytes: limit}, route >= 102 && route <= 108
}
func TestAddressPublicBridgeRoutes(t *testing.T) {
	for _, tc := range []struct {
		name              string
		route             contractv1.RouteId
		request, response proto.Message
	}{
		{"ListAddresses", 104, new(gatewayv1.BrowserListAddressesRequest), &gatewayv1.BrowserListAddressesResponse{Response: &userv1.ListAddressesResponse{Book: &userv1.AddressBook{Version: 1}}}},
		{"CreateAddress", 105, new(gatewayv1.BrowserCreateAddressRequest), &gatewayv1.BrowserCreateAddressResponse{Response: &userv1.CreateAddressResponse{Book: &userv1.AddressBook{Version: 2}}}},
		{"UpdateAddress", 106, new(gatewayv1.BrowserUpdateAddressRequest), &gatewayv1.BrowserUpdateAddressResponse{Failure: 2}},
		{"DeleteAddress", 107, new(gatewayv1.BrowserDeleteAddressRequest), &gatewayv1.BrowserDeleteAddressResponse{Failure: 3}},
		{"SetDefaultAddress", 108, new(gatewayv1.BrowserSetDefaultAddressRequest), &gatewayv1.BrowserSetDefaultAddressResponse{Failure: 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, _ := proto.Marshal(tc.response)
			inv := &addressInvoker{userInvoker{fakeInvoker: fakeInvoker{response: tunnel.Response{Payload: payload}}}}
			handler, err := NewAccountHandler(inv, true)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/user.v1.UserService/"+tc.name, strings.NewReader(`{"subjectId":"forged","context":{"cookie":["forged"]}}`))
			req.TLS = &tls.ConnectionState{}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Connect-Protocol-Version", "1")
			req.Header.Set("Cookie", "session=opaque")
			req.Header.Set("Authorization", "Bearer forged")
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			want := map[string]int{"ListAddresses": 200, "CreateAddress": 200, "UpdateAddress": 409, "DeleteAddress": 404, "SetDefaultAddress": 429}[tc.name]
			if rr.Code != want || rr.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(rr.Code, rr.Body.String())
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
				t.Fatal("caller injected browser context")
			}
			if tc.name == "DeleteAddress" && !strings.Contains(rr.Body.String(), "ADDRESS_NOT_FOUND") {
				t.Fatal("typed failure lost")
			}
		})
	}
}
func TestAddressPublicBridgeDisabledAndBounded(t *testing.T) {
	inv := &addressInvoker{}
	for _, enabled := range []bool{false, true} {
		handler, err := NewAccountHandler(inv, enabled)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			path, body string
			secure     bool
			want       int
		}{
			{"/user.v1.UserService/CreateAddress", strings.Repeat(" ", 16385), true, 429},
			{"/user.v1.UserService/ListAddresses", "{}", false, 403},
			{"/gateway.v1.UserBrowserService/BrowserListAddresses", "{}", true, 404},
		} {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.secure {
				req.TLS = &tls.ConnectionState{}
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			want := tc.want
			if !enabled && tc.secure && strings.HasPrefix(tc.path, "/user.") {
				want = 404
			}
			if rr.Code != want || rr.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(enabled, rr.Code, rr.Body.String())
			}
		}
	}
	if inv.calls != 0 {
		t.Fatal("invalid request forwarded")
	}
}
