package app

import (
	"context"
	"testing"
	"time"

	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	filesv1 "github.com/v0hmly/marketmesh/api/gen/go/files/v1"
	gatewayv1 "github.com/v0hmly/marketmesh/api/gen/go/gateway/v1"
	"github.com/v0hmly/marketmesh/services/gateway-out/internal/tunnel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type fileStub struct {
	filesv1.FileServiceClient
	get    func(context.Context, *filesv1.GetStatusRequest) (*filesv1.GetStatusResponse, error)
	remove func(context.Context, *filesv1.DeleteRequest) (*filesv1.DeleteResponse, error)
}

func (s fileStub) GetStatus(ctx context.Context, r *filesv1.GetStatusRequest, _ ...grpc.CallOption) (*filesv1.GetStatusResponse, error) {
	return s.get(ctx, r)
}
func (s fileStub) Delete(ctx context.Context, r *filesv1.DeleteRequest, _ ...grpc.CallOption) (*filesv1.DeleteResponse, error) {
	return s.remove(ctx, r)
}

func TestFilesBrowserExchangesPerCallAndReplacesCredentials(t *testing.T) {
	exchanges, gets := 0, 0
	browser := &authv1.BrowserContext{Cookie: []string{"opaque cookie"}, Origin: []string{"https://app.test"}}
	c := &fileBrowserClient{
		auth: exchangeFunc(func(ctx context.Context, r *authv1.ExchangeBrowserSessionRequest) (*authv1.ExchangeBrowserSessionResponse, error) {
			exchanges++
			md, _ := metadata.FromOutgoingContext(ctx)
			if len(md) != 0 || r.GetAudience() != "files" || r.GetContext() != browser {
				t.Fatal("Auth received inherited metadata or altered browser context")
			}
			return &authv1.ExchangeBrowserSessionResponse{Assertion: "fresh.signed.assertion", ExpiresAtUnix: time.Now().Add(time.Minute).Unix()}, nil
		}),
		files: fileStub{get: func(ctx context.Context, _ *filesv1.GetStatusRequest) (*filesv1.GetStatusResponse, error) {
			gets++
			md, _ := metadata.FromOutgoingContext(ctx)
			if len(md) != 1 || len(md.Get(userAssertionMetadata)) != 1 || md.Get(userAssertionMetadata)[0] != "fresh.signed.assertion" {
				t.Fatal("Files received untrusted metadata")
			}
			return &filesv1.GetStatusResponse{State: filesv1.FileState_FILE_STATE_SCANNING}, nil
		}},
	}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(userAssertionMetadata, "forged", "cookie", "secret", "authorization", "Bearer forged", "traceparent", "opaque"))
	for range 2 {
		response := new(gatewayv1.BrowserFileGetStatusResponse)
		if err := c.Invoke(ctx, gatewayv1.FileBrowserService_BrowserFileGetStatus_FullMethodName, &gatewayv1.BrowserFileGetStatusRequest{Request: new(filesv1.GetStatusRequest), Context: browser}, response); err != nil || response.GetResponse() == nil || response.GetFailure() != 0 {
			t.Fatalf("GetStatus: %v", err)
		}
	}
	if exchanges != 2 || gets != 2 {
		t.Fatal("exchange cached or RPC repeated")
	}
}

func TestFilesBrowserFailsBeforeUserOnAuthFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *authv1.ExchangeBrowserSessionResponse
		err      error
	}{
		{name: "unavailable", err: status.Error(codes.Unavailable, "private diagnostic")},
		{name: "empty"},
		{name: "expired", response: &authv1.ExchangeBrowserSessionResponse{Assertion: "a.b.c", ExpiresAtUnix: 1}},
		{name: "malformed", response: &authv1.ExchangeBrowserSessionResponse{Assertion: "Bearer a.b.c", ExpiresAtUnix: time.Now().Add(time.Minute).Unix()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &fileBrowserClient{auth: exchangeFunc(func(context.Context, *authv1.ExchangeBrowserSessionRequest) (*authv1.ExchangeBrowserSessionResponse, error) {
				return tc.response, tc.err
			}), files: fileStub{get: func(context.Context, *filesv1.GetStatusRequest) (*filesv1.GetStatusResponse, error) {
				t.Fatal("Files called after invalid exchange")
				return nil, nil
			}}}
			if err := c.Invoke(context.Background(), gatewayv1.FileBrowserService_BrowserFileGetStatus_FullMethodName, &gatewayv1.BrowserFileGetStatusRequest{Request: new(filesv1.GetStatusRequest), Context: new(authv1.BrowserContext)}, new(gatewayv1.BrowserFileGetStatusResponse)); err == nil {
				t.Fatal("invalid exchange accepted")
			}
		})
	}
}

func TestFileRoutesRegisterWithTunnel(t *testing.T) {
	client := &fileBrowserClient{}
	if _, err := tunnel.NewRegistry(tunnel.ClassClients{Regular: client}, fileRoutes(time.Second)...); err != nil {
		t.Fatalf("file route registration failed: %v", err)
	}
}
