package app

import (
	filesv1connect "github.com/v0hmly/marketmesh/api/gen/go/files/v1/filesv1connect"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/connectbridge"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
	"net/http"
)

func fileRouteIDs() []contractv1.RouteId {
	return []contractv1.RouteId{
		contractv1.RouteId_ROUTE_ID_FILE_CREATE_UPLOAD,
		contractv1.RouteId_ROUTE_ID_FILE_COMPLETE_UPLOAD,
		contractv1.RouteId_ROUTE_ID_FILE_GET_STATUS,
		contractv1.RouteId_ROUTE_ID_FILE_CREATE_DOWNLOAD,
		contractv1.RouteId_ROUTE_ID_FILE_DELETE,
	}
}
func registerFileHandler(mux *http.ServeMux, registry *tunnel.Registry) error {
	handler, err := connectbridge.NewFileHandler(registry)
	if err != nil {
		return err
	}
	mux.Handle("/"+filesv1connect.FileServiceName+"/", handler)
	return nil
}
func fileRoutesReady(cfg config, registry routeReadiness) bool {
	if cfg.filesBrowserEnabled {
		for _, route := range fileRouteIDs() {
			if !registry.IsRouteReady(route) {
				return false
			}
		}
	}
	return true
}
