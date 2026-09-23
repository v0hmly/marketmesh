package app

import (
	"context"
	"net"
	"os"
	"time"

	filesv1 "github.com/v0hmly/marketmesh/api/gen/go/files/v1"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/files/internal/adapter/in/internalgrpc"
	"github.com/v0hmly/marketmesh/services/files/internal/application/files"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type avatarConfig struct {
	Enabled bool
	Address string
	User    workloadid.Scope
}

// avatarServer deliberately has a separate TLS policy and RPC registration.
func avatarServer(c controlConfig, service *files.Service) (*grpc.Server, net.Listener, error) {
	if !c.Avatar.Enabled {
		return nil, nil, nil
	}
	user := c.Avatar.User
	if user.ServiceAccount != "user" || user.TrustDomain != c.Own.TrustDomain || user.Environment != c.Own.Environment || user.Cluster != c.Own.Cluster || user.Namespace != c.Own.Namespace {
		return nil, nil, file.ErrInvalid
	}
	cert, ca, err := pair(c.TLS)
	if err != nil {
		return nil, nil, err
	}
	tlsConfig, err := workloadid.ScopedTLS(cert, ca, c.Own, user, "", true)
	if err != nil {
		return nil, nil, err
	}
	handler, err := internalgrpc.NewAvatar(service, user)
	if err != nil {
		return nil, nil, err
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsConfig)), grpc.MaxRecvMsgSize(4096), grpc.MaxSendMsgSize(4096), grpc.MaxConcurrentStreams(32), grpc.ChainUnaryInterceptor(internalgrpc.AuditInterceptor(os.Stdout), func(ctx context.Context, r any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		return next(ctx, r)
	}))
	filesv1.RegisterFileAvatarServiceServer(server, handler)
	listener, err := net.Listen("tcp", c.Avatar.Address)
	if err != nil {
		server.Stop()
		return nil, nil, file.ErrUnavailable
	}
	return server, listener, nil
}
