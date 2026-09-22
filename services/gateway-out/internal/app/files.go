package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	filesv1 "github.com/v0hmly/marketmesh/api/gen/go/files/v1"
	gatewayv1 "github.com/v0hmly/marketmesh/api/gen/go/gateway/v1"
	tunnelv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/gateway-out/internal/tunnel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"os"
	"strings"
	"time"
)

type fileBrowserClient struct {
	auth  browserExchanger
	files filesv1.FileServiceClient
	next  grpc.ClientConnInterface
}

func (c *fileBrowserClient) Invoke(ctx context.Context, method string, args, reply any, options ...grpc.CallOption) error {
	switch method {
	case gatewayv1.FileBrowserService_BrowserFileCreateUpload_FullMethodName:
		request, ok := args.(*gatewayv1.BrowserFileCreateUploadRequest)
		response, responseOK := reply.(*gatewayv1.BrowserFileCreateUploadResponse)
		if !ok || !responseOK || request == nil || response == nil || request.Request == nil || request.Context == nil {
			return status.Error(codes.InvalidArgument, "invalid file request")
		}
		*response = gatewayv1.BrowserFileCreateUploadResponse{}
		authenticated, err := c.authenticateFile(ctx, request.Context)
		if err != nil {
			return err
		}
		result, err := c.files.CreateUpload(authenticated, request.Request, grpc.WaitForReady(false), grpc.MaxCallRecvMsgSize(64*1024), grpc.MaxCallSendMsgSize(16*1024))
		if err != nil {
			response.Failure = fileFailure(err)
			if response.Failure != 0 {
				return nil
			}
			return err
		}
		if result == nil || proto.Size(result) > 64*1024 {
			return status.Error(codes.Internal, "invalid file response")
		}
		response.Response = result
		return nil
	case gatewayv1.FileBrowserService_BrowserFileCompleteUpload_FullMethodName:
		request, ok := args.(*gatewayv1.BrowserFileCompleteUploadRequest)
		response, responseOK := reply.(*gatewayv1.BrowserFileCompleteUploadResponse)
		if !ok || !responseOK || request == nil || response == nil || request.Request == nil || request.Context == nil {
			return status.Error(codes.InvalidArgument, "invalid file request")
		}
		*response = gatewayv1.BrowserFileCompleteUploadResponse{}
		authenticated, err := c.authenticateFile(ctx, request.Context)
		if err != nil {
			return err
		}
		result, err := c.files.CompleteUpload(authenticated, request.Request, grpc.WaitForReady(false), grpc.MaxCallRecvMsgSize(64*1024), grpc.MaxCallSendMsgSize(16*1024))
		if err != nil {
			response.Failure = fileFailure(err)
			if response.Failure != 0 {
				return nil
			}
			return err
		}
		if result == nil || proto.Size(result) > 64*1024 {
			return status.Error(codes.Internal, "invalid file response")
		}
		response.Response = result
		return nil
	case gatewayv1.FileBrowserService_BrowserFileGetStatus_FullMethodName:
		request, ok := args.(*gatewayv1.BrowserFileGetStatusRequest)
		response, responseOK := reply.(*gatewayv1.BrowserFileGetStatusResponse)
		if !ok || !responseOK || request == nil || response == nil || request.Request == nil || request.Context == nil {
			return status.Error(codes.InvalidArgument, "invalid file request")
		}
		*response = gatewayv1.BrowserFileGetStatusResponse{}
		authenticated, err := c.authenticateFile(ctx, request.Context)
		if err != nil {
			return err
		}
		result, err := c.files.GetStatus(authenticated, request.Request, grpc.WaitForReady(false), grpc.MaxCallRecvMsgSize(64*1024), grpc.MaxCallSendMsgSize(16*1024))
		if err != nil {
			response.Failure = fileFailure(err)
			if response.Failure != 0 {
				return nil
			}
			return err
		}
		if result == nil || proto.Size(result) > 64*1024 {
			return status.Error(codes.Internal, "invalid file response")
		}
		response.Response = result
		return nil
	case gatewayv1.FileBrowserService_BrowserFileCreateDownload_FullMethodName:
		request, ok := args.(*gatewayv1.BrowserFileCreateDownloadRequest)
		response, responseOK := reply.(*gatewayv1.BrowserFileCreateDownloadResponse)
		if !ok || !responseOK || request == nil || response == nil || request.Request == nil || request.Context == nil {
			return status.Error(codes.InvalidArgument, "invalid file request")
		}
		*response = gatewayv1.BrowserFileCreateDownloadResponse{}
		authenticated, err := c.authenticateFile(ctx, request.Context)
		if err != nil {
			return err
		}
		result, err := c.files.CreateDownload(authenticated, request.Request, grpc.WaitForReady(false), grpc.MaxCallRecvMsgSize(64*1024), grpc.MaxCallSendMsgSize(16*1024))
		if err != nil {
			response.Failure = fileFailure(err)
			if response.Failure != 0 {
				return nil
			}
			return err
		}
		if result == nil || proto.Size(result) > 64*1024 {
			return status.Error(codes.Internal, "invalid file response")
		}
		response.Response = result
		return nil
	case gatewayv1.FileBrowserService_BrowserFileDelete_FullMethodName:
		request, ok := args.(*gatewayv1.BrowserFileDeleteRequest)
		response, responseOK := reply.(*gatewayv1.BrowserFileDeleteResponse)
		if !ok || !responseOK || request == nil || response == nil || request.Request == nil || request.Context == nil {
			return status.Error(codes.InvalidArgument, "invalid file request")
		}
		*response = gatewayv1.BrowserFileDeleteResponse{}
		authenticated, err := c.authenticateFile(ctx, request.Context)
		if err != nil {
			return err
		}
		result, err := c.files.Delete(authenticated, request.Request, grpc.WaitForReady(false), grpc.MaxCallRecvMsgSize(64*1024), grpc.MaxCallSendMsgSize(16*1024))
		if err != nil {
			response.Failure = fileFailure(err)
			if response.Failure != 0 {
				return nil
			}
			return err
		}
		if result == nil || proto.Size(result) > 64*1024 {
			return status.Error(codes.Internal, "invalid file response")
		}
		response.Response = result
		return nil
	default:
		if c.next != nil {
			return c.next.Invoke(ctx, method, args, reply, options...)
		}
		return status.Error(codes.PermissionDenied, "route unavailable")
	}
}
func (c *fileBrowserClient) authenticateFile(ctx context.Context, browser *authv1.BrowserContext) (context.Context, error) {
	clean := metadata.NewOutgoingContext(ctx, metadata.MD{})
	result, err := c.auth.ExchangeBrowserSession(clean, &authv1.ExchangeBrowserSessionRequest{Context: browser, Audience: "files"}, grpc.WaitForReady(false), grpc.MaxCallRecvMsgSize(16*1024), grpc.MaxCallSendMsgSize(16*1024))
	if err != nil {
		return nil, err
	}
	token := result.GetAssertion()
	if len(token) == 0 || len(token) > 16*1024 || strings.Count(token, ".") != 2 || strings.ContainsAny(token, " \t\r\n;=") || result.GetExpiresAtUnix() <= time.Now().Unix() {
		return nil, status.Error(codes.Unauthenticated, "authentication failed")
	}
	return metadata.NewOutgoingContext(ctx, metadata.Pairs(userAssertionMetadata, token)), nil
}
func (c *fileBrowserClient) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, status.Error(codes.Unimplemented, "file streaming unavailable")
}
func fileFailure(err error) gatewayv1.FileBrowserFailure {
	switch status.Code(err) {
	case codes.NotFound:
		return gatewayv1.FileBrowserFailure_FILE_BROWSER_FAILURE_NOT_FOUND
	case codes.Aborted:
		return gatewayv1.FileBrowserFailure_FILE_BROWSER_FAILURE_CONFLICT
	case codes.FailedPrecondition:
		return gatewayv1.FileBrowserFailure_FILE_BROWSER_FAILURE_NOT_READY
	default:
		return 0
	}
}
func fileRoutes(timeout time.Duration) []tunnel.RouteSpec {
	specs := []tunnel.RouteSpec{
		{ID: tunnelv1.RouteId_ROUTE_ID_FILE_CREATE_UPLOAD, Method: gatewayv1.FileBrowserService_BrowserFileCreateUpload_FullMethodName, NewRequest: func() proto.Message { return new(gatewayv1.BrowserFileCreateUploadRequest) }, NewResponse: func() proto.Message { return new(gatewayv1.BrowserFileCreateUploadResponse) }, Mutating: true},
		{ID: tunnelv1.RouteId_ROUTE_ID_FILE_COMPLETE_UPLOAD, Method: gatewayv1.FileBrowserService_BrowserFileCompleteUpload_FullMethodName, NewRequest: func() proto.Message { return new(gatewayv1.BrowserFileCompleteUploadRequest) }, NewResponse: func() proto.Message { return new(gatewayv1.BrowserFileCompleteUploadResponse) }, Mutating: true},
		{ID: tunnelv1.RouteId_ROUTE_ID_FILE_GET_STATUS, Method: gatewayv1.FileBrowserService_BrowserFileGetStatus_FullMethodName, NewRequest: func() proto.Message { return new(gatewayv1.BrowserFileGetStatusRequest) }, NewResponse: func() proto.Message { return new(gatewayv1.BrowserFileGetStatusResponse) }, Mutating: false},
		{ID: tunnelv1.RouteId_ROUTE_ID_FILE_CREATE_DOWNLOAD, Method: gatewayv1.FileBrowserService_BrowserFileCreateDownload_FullMethodName, NewRequest: func() proto.Message { return new(gatewayv1.BrowserFileCreateDownloadRequest) }, NewResponse: func() proto.Message { return new(gatewayv1.BrowserFileCreateDownloadResponse) }, Mutating: false},
		{ID: tunnelv1.RouteId_ROUTE_ID_FILE_DELETE, Method: gatewayv1.FileBrowserService_BrowserFileDelete_FullMethodName, NewRequest: func() proto.Message { return new(gatewayv1.BrowserFileDeleteRequest) }, NewResponse: func() proto.Message { return new(gatewayv1.BrowserFileDeleteResponse) }, Mutating: true},
	}
	for i := range specs {
		specs[i].TrafficClass = tunnelv1.TrafficClass_TRAFFIC_CLASS_REGULAR
		specs[i].MaxRequestBytes = 16 * 1024
		specs[i].MaxResponseBytes = 64 * 1024
		specs[i].MaxDeadline = timeout
	}
	return specs
}
func newFilesClient(ctx context.Context, cfg config) (*grpc.ClientConn, error) {
	cert, err := tls.LoadX509KeyPair(cfg.filesCertificate, cfg.filesPrivateKey)
	if err != nil {
		return nil, errors.New("files TLS unavailable")
	}
	ca, err := os.ReadFile(cfg.filesRootCA)
	if err != nil {
		return nil, errors.New("files CA unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("files CA invalid")
	}
	if len(cert.Certificate) == 0 {
		return nil, errors.New("files client identity missing")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil || len(leaf.URIs) != 1 || leaf.URIs[0] == nil {
		return nil, errors.New("files client identity invalid")
	}
	own, _, err := workloadid.ParseScopedURI(leaf.URIs[0].String())
	if err != nil || own.Environment != cfg.environment || own.ServiceAccount != "gateway-out" {
		return nil, errors.New("files client scope invalid")
	}
	expected, pod, err := workloadid.ParseScopedURI(cfg.expectedFilesURI)
	if err != nil || pod != "" || expected.Environment != cfg.environment || expected.ServiceAccount != "files" || expected.TrustDomain != own.TrustDomain {
		return nil, errors.New("files target scope invalid")
	}
	config, err := workloadid.ScopedTLS(cert, roots, own, expected, cfg.filesServerName, false)
	if err != nil {
		return nil, errors.New("files TLS invalid")
	}
	client, err := grpc.NewClient(cfg.filesTarget, grpc.WithTransportCredentials(credentials.NewTLS(config)), grpc.WithDisableRetry(), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(64*1024), grpc.MaxCallSendMsgSize(16*1024)))
	if err != nil {
		return nil, errors.New("files client unavailable")
	}
	ready, cancel := context.WithTimeout(ctx, cfg.connectTimeout)
	defer cancel()
	client.Connect()
	if err := checkFiles(ready, client); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}
func checkFiles(ctx context.Context, connection *grpc.ClientConn) error {
	result, err := grpc_health_v1.NewHealthClient(connection).Check(ctx, &grpc_health_v1.HealthCheckRequest{}, grpc.WaitForReady(true))
	if err != nil || result.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		return errors.New("files dependency unavailable")
	}
	return nil
}
