package app

import (
	"crypto/tls"
	"errors"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	platformgrpc "github.com/v0hmly/marketmesh/platform/grpc"
	"github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/in/internalgrpc"
	"time"
)

func (r *sessionResources) startFilesListener(c filesSessionConfig, cfg config, handler *internalgrpc.Handler, log *logger.Logger, pipeline *telemetry.Telemetry, listen listenFunc) error {
	cert, err := tls.LoadX509KeyPair(c.certificate, c.privateKey)
	if err != nil {
		return errors.New("auth files: TLS unavailable")
	}
	ca, err := loadSessionCA(c.rootCA)
	if err != nil {
		return err
	}
	security, err := workloadid.ScopedTLS(cert, ca, c.own, c.peer, "", true)
	if err != nil {
		return errors.New("auth files: TLS scope invalid")
	}
	scoped, err := internalgrpc.NewScopedFiles(handler, c.peer)
	if err != nil {
		return err
	}
	r.filesServer, err = platformgrpc.NewServer(platformgrpc.ServerConfig{
		Environment: cfg.environment, ConnectionTimeout: 3 * time.Second, RequestTimeout: cfg.httpRequestTimeout,
		KeepaliveTime: 2 * time.Minute, KeepaliveTimeout: 20 * time.Second, MaxReceiveMessageBytes: 20 * 1024, MaxSendMessageBytes: 65 * 1024,
		Security: platformgrpc.ServerSecurity{TLSConfig: security, RequireClientCertificate: true}, Logger: log, Telemetry: pipeline,
	})
	if err != nil {
		return err
	}
	authv1.RegisterAuthInternalServiceServer(r.filesServer.GRPCServer(), scoped)
	r.filesListener, err = listen("tcp", c.address)
	if err != nil {
		return errors.New("auth files: listener unavailable")
	}
	component, err := r.filesServer.Component("auth-files", r.filesListener)
	if err != nil {
		return err
	}
	r.components = append(r.components, component)
	return nil
}
