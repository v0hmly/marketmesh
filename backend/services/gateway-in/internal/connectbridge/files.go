package connectbridge

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	filesv1 "github.com/v0hmly/marketmesh/api/gen/go/files/v1"
	filesv1connect "github.com/v0hmly/marketmesh/api/gen/go/files/v1/filesv1connect"
	gatewayv1 "github.com/v0hmly/marketmesh/api/gen/go/gateway/v1"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
	"google.golang.org/protobuf/proto"
	"net/http"
	"slices"
)

// NewFileHandler exposes only fixed control methods with browser context rebuilt locally.
func NewFileHandler(invoker Invoker, options ...connect.HandlerOption) (http.Handler, error) {
	if isNilInvoker(invoker) {
		return nil, errors.New("file bridge requires invoker")
	}
	mux := http.NewServeMux()
	if err := mountFile(mux, invoker, filesv1connect.FileServiceCreateUploadProcedure, contractv1.RouteId_ROUTE_ID_FILE_CREATE_UPLOAD,
		func(r *filesv1.CreateUploadRequest, browser *authv1.BrowserContext) proto.Message {
			return &gatewayv1.BrowserFileCreateUploadRequest{Request: r, Context: browser}
		},
		func(raw []byte) (*filesv1.CreateUploadResponse, gatewayv1.FileBrowserFailure, error) {
			result := new(gatewayv1.BrowserFileCreateUploadResponse)
			err := proto.Unmarshal(raw, result)
			return result.GetResponse(), result.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountFile(mux, invoker, filesv1connect.FileServiceCompleteUploadProcedure, contractv1.RouteId_ROUTE_ID_FILE_COMPLETE_UPLOAD,
		func(r *filesv1.CompleteUploadRequest, browser *authv1.BrowserContext) proto.Message {
			return &gatewayv1.BrowserFileCompleteUploadRequest{Request: r, Context: browser}
		},
		func(raw []byte) (*filesv1.CompleteUploadResponse, gatewayv1.FileBrowserFailure, error) {
			result := new(gatewayv1.BrowserFileCompleteUploadResponse)
			err := proto.Unmarshal(raw, result)
			return result.GetResponse(), result.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountFile(mux, invoker, filesv1connect.FileServiceGetStatusProcedure, contractv1.RouteId_ROUTE_ID_FILE_GET_STATUS,
		func(r *filesv1.GetStatusRequest, browser *authv1.BrowserContext) proto.Message {
			return &gatewayv1.BrowserFileGetStatusRequest{Request: r, Context: browser}
		},
		func(raw []byte) (*filesv1.GetStatusResponse, gatewayv1.FileBrowserFailure, error) {
			result := new(gatewayv1.BrowserFileGetStatusResponse)
			err := proto.Unmarshal(raw, result)
			return result.GetResponse(), result.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountFile(mux, invoker, filesv1connect.FileServiceCreateDownloadProcedure, contractv1.RouteId_ROUTE_ID_FILE_CREATE_DOWNLOAD,
		func(r *filesv1.CreateDownloadRequest, browser *authv1.BrowserContext) proto.Message {
			return &gatewayv1.BrowserFileCreateDownloadRequest{Request: r, Context: browser}
		},
		func(raw []byte) (*filesv1.CreateDownloadResponse, gatewayv1.FileBrowserFailure, error) {
			result := new(gatewayv1.BrowserFileCreateDownloadResponse)
			err := proto.Unmarshal(raw, result)
			return result.GetResponse(), result.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountFile(mux, invoker, filesv1connect.FileServiceDeleteProcedure, contractv1.RouteId_ROUTE_ID_FILE_DELETE,
		func(r *filesv1.DeleteRequest, browser *authv1.BrowserContext) proto.Message {
			return &gatewayv1.BrowserFileDeleteRequest{Request: r, Context: browser}
		},
		func(raw []byte) (*filesv1.DeleteResponse, gatewayv1.FileBrowserFailure, error) {
			result := new(gatewayv1.BrowserFileDeleteResponse)
			err := proto.Unmarshal(raw, result)
			return result.GetResponse(), result.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.TLS == nil {
			http.Error(w, "HTTPS required", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}
func mountFile[Request, Response any](mux *http.ServeMux, invoker Invoker, procedure string, route contractv1.RouteId, wrap func(*Request, *authv1.BrowserContext) proto.Message, unwrap func([]byte) (*Response, gatewayv1.FileBrowserFailure, error), options []connect.HandlerOption) error {
	policy, ok := invoker.RoutePolicy(route)
	if !ok || policy.MaxRequestBytes == 0 || policy.MaxRequestBytes > 16*1024 || policy.MaxResponseBytes == 0 || policy.MaxResponseBytes > 64*1024 {
		return errors.New("file bridge requires bounded control policy")
	}
	opts := append(slices.Clone(options), connect.WithReadMaxBytes(int(policy.MaxRequestBytes)))
	handler := connect.NewUnaryHandler(procedure, func(ctx context.Context, r *connect.Request[Request]) (*connect.Response[Response], error) {
		if _, err := requestIdempotencyKey(r.Header(), false); err != nil {
			return nil, publicError(connect.CodeInvalidArgument)
		}
		browser, err := browserContext(r.Header())
		if err != nil {
			return nil, publicError(connect.CodeInvalidArgument)
		}
		payload, err := proto.Marshal(wrap(r.Msg, browser))
		if err != nil {
			return nil, publicError(connect.CodeInvalidArgument)
		}
		if len(payload) > int(policy.MaxRequestBytes) {
			return nil, publicError(connect.CodeResourceExhausted)
		}
		result, err := invoker.Invoke(ctx, tunnel.Call{Route: route, Payload: payload})
		if err != nil {
			return nil, mapTunnelError(err)
		}
		if len(result.Payload) > int(policy.MaxResponseBytes) {
			return nil, publicError(connect.CodeResourceExhausted)
		}
		response, failure, err := unwrap(result.Payload)
		if err != nil || (response != nil && failure != 0) {
			return nil, publicError(connect.CodeInternal)
		}
		switch failure {
		case gatewayv1.FileBrowserFailure_FILE_BROWSER_FAILURE_NOT_FOUND:
			return nil, publicError(connect.CodeNotFound)
		case gatewayv1.FileBrowserFailure_FILE_BROWSER_FAILURE_CONFLICT:
			return nil, publicError(connect.CodeAborted)
		case gatewayv1.FileBrowserFailure_FILE_BROWSER_FAILURE_NOT_READY:
			return nil, publicError(connect.CodeFailedPrecondition)
		case gatewayv1.FileBrowserFailure_FILE_BROWSER_FAILURE_UNSPECIFIED:
		default:
			return nil, publicError(connect.CodeInternal)
		}
		if response == nil {
			return nil, publicError(connect.CodeInternal)
		}
		resultResponse := connect.NewResponse(response)
		resultResponse.Header().Set("Cache-Control", "no-store")
		return resultResponse, nil
	}, opts...)
	mux.Handle(procedure, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, int64(policy.MaxRequestBytes))
		handler.ServeHTTP(w, r)
	}))
	return nil
}
