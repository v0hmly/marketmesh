package connectbridge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
	"google.golang.org/protobuf/proto"
)

func TestProfileHTTPResponsesNeverCachePersonalData(t *testing.T) {
	for _, route := range []contractv1.RouteId{contractv1.RouteId_ROUTE_ID_USER_GET_ME, contractv1.RouteId_ROUTE_ID_USER_UPDATE_ME} {
		for _, failure := range []bool{false, true} {
			procedure := userv1.UserService_GetMe_FullMethodName
			var wireResponse proto.Message = &userv1.GetMeResponse{Profile: &userv1.Profile{DisplayName: "Private buyer"}}
			if route == contractv1.RouteId_ROUTE_ID_USER_UPDATE_ME {
				procedure = userv1.UserService_UpdateMe_FullMethodName
				wireResponse = &userv1.UpdateMeResponse{Profile: &userv1.Profile{DisplayName: "Private buyer"}}
			}
			payload, err := proto.Marshal(wireResponse)
			if err != nil {
				t.Fatal(err)
			}
			invoker := &profileInvoker{fakeInvoker: fakeInvoker{response: tunnel.Response{Payload: payload}}}
			if failure {
				invoker.err = tunnel.ErrNoTunnel
			}
			var handler http.Handler
			config := Config{Procedure: procedure, Route: route, Invoker: invoker}
			if route == contractv1.RouteId_ROUTE_ID_USER_UPDATE_ME {
				handler, err = NewUnaryHandler[userv1.UpdateMeRequest, userv1.UpdateMeResponse](config)
			} else {
				handler, err = NewUnaryHandler[userv1.GetMeRequest, userv1.GetMeResponse](config)
			}
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, procedure, strings.NewReader("{}"))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Connect-Protocol-Version", "1")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("profile response could be cached", route, failure)
			}
			if (response.Code == http.StatusOK) == failure {
				t.Fatal("unexpected HTTP result", response.Code, response.Body.String())
			}
			if !failure && !strings.Contains(response.Body.String(), "Private buyer") {
				t.Fatal("profile response missing")
			}
			if invoker.lastCall().Route != route {
				t.Fatal("unexpected profile route")
			}
			malformed := httptest.NewRequest(http.MethodPost, procedure, strings.NewReader("invalid"))
			malformed.Header.Set("Content-Type", "application/json")
			response = httptest.NewRecorder()
			handler.ServeHTTP(response, malformed)
			if response.Code == http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("malformed profile request could be cached")
			}
		}
	}
}

type profileInvoker struct{ fakeInvoker }

func (*profileInvoker) RoutePolicy(route contractv1.RouteId) (tunnel.RoutePolicy, bool) {
	return tunnel.RoutePolicy{}, route == contractv1.RouteId_ROUTE_ID_USER_GET_ME || route == contractv1.RouteId_ROUTE_ID_USER_UPDATE_ME
}
