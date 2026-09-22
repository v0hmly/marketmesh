package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"time"

	filesv1 "github.com/v0hmly/marketmesh/api/gen/go/files/v1"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	"github.com/v0hmly/marketmesh/platform/logger"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/user/internal/adapter/in/internalgrpc"
	"github.com/v0hmly/marketmesh/services/user/internal/adapter/out/filesavatar"
	"github.com/v0hmly/marketmesh/services/user/internal/adapter/out/postgresavatars"
	"github.com/v0hmly/marketmesh/services/user/internal/application/avatars"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type avatarConfig struct {
	enabled                                             bool
	target, serverName, cert, key, ca, ownURI, filesURI string
}

func loadAvatarConfig(env serviceruntime.Env, profileEnabled bool) (avatarConfig, error) {
	var c avatarConfig
	var err error
	c.enabled, err = env.Bool("USER_AVATAR_ENABLED", false)
	if err != nil || !c.enabled {
		return c, err
	}
	if !profileEnabled {
		return c, errors.New("USER_AVATAR_ENABLED requires USER_PROFILE_ENABLED")
	}
	for _, item := range []struct {
		name  string
		value *string
	}{
		{"USER_FILES_TARGET", &c.target}, {"USER_FILES_SERVER_NAME", &c.serverName},
		{"USER_FILES_TLS_CERT_FILE", &c.cert}, {"USER_FILES_TLS_KEY_FILE", &c.key}, {"USER_FILES_TLS_CA_FILE", &c.ca},
		{"USER_FILES_OWN_URI", &c.ownURI}, {"USER_FILES_EXPECTED_URI", &c.filesURI},
	} {
		if *item.value, err = env.RequiredString(item.name); err != nil {
			return c, err
		}
	}
	for _, p := range []string{c.cert, c.key, c.ca} {
		if !filepath.IsAbs(p) {
			return c, errors.New("user avatar: TLS paths must be absolute")
		}
	}
	if _, _, err = net.SplitHostPort(c.target); err != nil {
		return c, errors.New("user avatar: target must contain host and port")
	}
	return c, nil
}
func newAvatarConnection(c avatarConfig, environment string) (*grpc.ClientConn, error) {
	own, pod, err := workloadid.ParseScopedURI(c.ownURI)
	if err != nil || pod != "" || own.ServiceAccount != "user" || own.Environment != environment {
		return nil, errors.New("user avatar: invalid own scope")
	}
	peer, pod, err := workloadid.ParseScopedURI(c.filesURI)
	if err != nil || pod != "" || peer.ServiceAccount != "files" || peer.Environment != own.Environment || peer.TrustDomain != own.TrustDomain || peer.Cluster != own.Cluster || peer.Namespace != own.Namespace {
		return nil, errors.New("user avatar: invalid Files scope")
	}
	cert, err := tls.LoadX509KeyPair(c.cert, c.key)
	if err != nil {
		return nil, errors.New("user avatar: certificate unavailable")
	}
	ca, err := os.ReadFile(c.ca)
	if err != nil {
		return nil, errors.New("user avatar: CA unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("user avatar: CA invalid")
	}
	tlsConfig, err := workloadid.ScopedTLS(cert, roots, own, peer, c.serverName, false)
	if err != nil {
		return nil, errors.New("user avatar: TLS scope invalid")
	}
	conn, err := grpc.NewClient(c.target, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)), grpc.WithDisableRetry(), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(4096), grpc.MaxCallSendMsgSize(4096)))
	if err != nil {
		return nil, errors.New("user avatar: Files connection unavailable")
	}
	return conn, nil
}
func (r *profileResources) enableAvatar(c config, h *internalgrpc.Handler, log *logger.Logger) error {
	if !c.profile.avatar.enabled {
		return nil
	}
	conn, err := newAvatarConnection(c.profile.avatar, c.environment)
	if err != nil {
		return err
	}
	r.avatarConnection = conn
	client, err := filesavatar.New(filesv1.NewFileAvatarServiceClient(conn))
	if err != nil {
		return err
	}
	repo, err := postgresavatars.New(r.database.RW(), r.database)
	if err != nil {
		return err
	}
	service, err := avatars.New(repo, client)
	if err != nil {
		return err
	}
	if err = h.EnableAvatar(service); err != nil {
		return err
	}
	worker, err := avatars.NewWorker(repo, client)
	if err != nil {
		return err
	}
	r.avatarWorker = serviceruntime.Component{Name: "user-avatar-cleanup", Run: func(ctx context.Context) error {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if err := worker.Step(ctx); err != nil && ctx.Err() == nil {
				var failure avatars.CleanupError
				if errors.As(err, &failure) {
					log.Slog().WarnContext(ctx, "avatar cleanup deferred", "error_class", failure.Class, "file_id", hex.EncodeToString(failure.FileID[:]))
				} else {
					log.Slog().WarnContext(ctx, "avatar cleanup deferred", "error_class", "internal")
				}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
	}, Shutdown: func(context.Context) error { return conn.Close() }}
	return nil
}
func avatarMethods() []string {
	return []string{userv1.UserService_GetAvatar_FullMethodName, userv1.UserService_SetAvatar_FullMethodName, userv1.UserService_ClearAvatar_FullMethodName}
}
