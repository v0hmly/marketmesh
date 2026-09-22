package app

import (
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	userv1connect "github.com/v0hmly/marketmesh/api/gen/go/user/v1/userv1connect"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/connectbridge"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
	"net/http"
)

func registerUserHandler(mux *http.ServeMux, cfg config, registry *tunnel.Registry) error {
	handler, err := connectbridge.NewAccountAvatarHandler(registry, cfg.userAddressesBrowserEnabled, cfg.userSettingsBrowserEnabled, cfg.userAvatarBrowserEnabled)
	if err != nil {
		return err
	}
	mux.Handle("/"+userv1connect.UserServiceName+"/", handler)
	return nil
}

// Profile codecs have disjoint wire routes so mixed deployments cannot forward
// browser credentials to the legacy E2E backend.
func profileRouteIDs(cfg config) [2]contractv1.RouteId {
	if cfg.userBrowserEnabled {
		return [2]contractv1.RouteId{contractv1.RouteId_ROUTE_ID_USER_BROWSER_GET_ME, contractv1.RouteId_ROUTE_ID_USER_BROWSER_UPDATE_ME}
	}
	return [2]contractv1.RouteId{contractv1.RouteId_ROUTE_ID_USER_GET_ME, contractv1.RouteId_ROUTE_ID_USER_UPDATE_ME}
}

type routeReadiness interface{ IsRouteReady(contractv1.RouteId) bool }

func profileRoutesReady(cfg config, registry routeReadiness) bool {
	for _, route := range profileRouteIDs(cfg) {
		if !registry.IsRouteReady(route) {
			return false
		}
	}
	if cfg.userAddressesBrowserEnabled {
		for _, route := range addressRouteIDs() {
			if !registry.IsRouteReady(route) {
				return false
			}
		}
	}
	if cfg.userAvatarBrowserEnabled {
		for _, route := range avatarRouteIDs() {
			if !registry.IsRouteReady(route) {
				return false
			}
		}
	}
	if cfg.userSettingsBrowserEnabled {
		for _, route := range settingsRouteIDs() {
			if !registry.IsRouteReady(route) {
				return false
			}
		}
	}
	return true
}

func addressRouteIDs() []contractv1.RouteId {
	return []contractv1.RouteId{contractv1.RouteId_ROUTE_ID_USER_BROWSER_LIST_ADDRESSES, contractv1.RouteId_ROUTE_ID_USER_BROWSER_CREATE_ADDRESS, contractv1.RouteId_ROUTE_ID_USER_BROWSER_UPDATE_ADDRESS, contractv1.RouteId_ROUTE_ID_USER_BROWSER_DELETE_ADDRESS, contractv1.RouteId_ROUTE_ID_USER_BROWSER_SET_DEFAULT_ADDRESS}
}

func settingsRouteIDs() []contractv1.RouteId {
	return []contractv1.RouteId{contractv1.RouteId_ROUTE_ID_USER_BROWSER_GET_SETTINGS, contractv1.RouteId_ROUTE_ID_USER_BROWSER_UPDATE_SETTINGS}
}

func avatarRouteIDs() []contractv1.RouteId {
	return []contractv1.RouteId{contractv1.RouteId_ROUTE_ID_USER_BROWSER_GET_AVATAR, contractv1.RouteId_ROUTE_ID_USER_BROWSER_SET_AVATAR, contractv1.RouteId_ROUTE_ID_USER_BROWSER_CLEAR_AVATAR}
}
