package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	filesv1 "github.com/v0hmly/marketmesh/api/gen/go/files/v1"
	platformpostgres "github.com/v0hmly/marketmesh/platform/postgres"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/files/internal/adapter/in/internalgrpc"
	"github.com/v0hmly/marketmesh/services/files/internal/adapter/out/authsession"
	"github.com/v0hmly/marketmesh/services/files/internal/adapter/out/clamav"
	filespostgres "github.com/v0hmly/marketmesh/services/files/internal/adapter/out/postgres"
	"github.com/v0hmly/marketmesh/services/files/internal/adapter/out/s3store"
	"github.com/v0hmly/marketmesh/services/files/internal/adapter/out/sandbox"
	"github.com/v0hmly/marketmesh/services/files/internal/application/files"
	"github.com/v0hmly/marketmesh/services/files/internal/application/processing"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health/grpc_health_v1"
)

type bucketConfig struct {
	APIEndpoint, PublicEndpoint, Bucket, KMSKey string
	Control, Capability                         s3store.Credential
}
type tlsFiles struct{ Certificate, PrivateKey, RootCA string }
type controlConfig struct {
	Enabled                                     bool
	Address                                     string
	TLS                                         tlsFiles
	Own, Gateway                                workloadid.Scope
	AuthTLS                                     tlsFiles
	AuthTarget, AuthServerName, AuthURI, Issuer string
}
type filesConfig struct {
	Role                                                                  string
	DatabaseRW, DatabaseRO, StorageCA, TempDir, ClamSocket, SandboxSocket string
	Quarantine, Internal                                                  bucketConfig
	Delivery                                                              []bucketConfig
	Control                                                               controlConfig
}

func readFilesConfig(path string) (filesConfig, error) {
	var cfg filesConfig
	if !filepath.IsAbs(path) {
		return cfg, file.ErrInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return cfg, file.ErrUnavailable
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64*1024 {
		return cfg, file.ErrInvalid
	}
	decoder := json.NewDecoder(io.LimitReader(f, 64*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil || decoder.Decode(new(any)) != io.EOF || (cfg.Role != "worker" && cfg.Role != "control") {
		return filesConfig{}, file.ErrInvalid
	}
	return cfg, nil
}

func roots(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, file.ErrUnavailable
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, file.ErrInvalid
	}
	return pool, nil
}
func pair(c tlsFiles) (tls.Certificate, *x509.CertPool, error) {
	cert, err := tls.LoadX509KeyPair(c.Certificate, c.PrivateKey)
	if err != nil {
		return tls.Certificate{}, nil, file.ErrUnavailable
	}
	pool, err := roots(c.RootCA)
	return cert, pool, err
}
func storage(c filesConfig) (*s3store.Store, error) {
	pool, err := roots(c.StorageCA)
	if err != nil {
		return nil, err
	}
	build := func(b bucketConfig) (*s3store.Bucket, error) {
		return s3store.NewBucket(s3store.Config{APIEndpoint: b.APIEndpoint, PublicEndpoint: b.PublicEndpoint, Bucket: b.Bucket, KMSKey: b.KMSKey, Control: b.Control, Capability: b.Capability, TLS: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}})
	}
	quarantine, err := build(c.Quarantine)
	if err != nil {
		return nil, err
	}
	var internal *s3store.Bucket
	if c.Role == "worker" {
		internal, err = build(c.Internal)
		if err != nil {
			return nil, err
		}
	}
	var copies []*s3store.Bucket
	if len(c.Delivery) != 2 {
		return nil, file.ErrInvalid
	}
	for _, b := range c.Delivery {
		copy, err := build(b)
		if err != nil {
			return nil, err
		}
		copies = append(copies, copy)
	}
	if c.Role == "control" { // The control process has neither internal-clean nor worker credentials.
		if c.Quarantine.Capability.AccessKey == "" || c.Delivery[0].Capability.AccessKey == "" || c.Delivery[1].Capability.AccessKey == "" || c.Internal.Control.AccessKey != "" {
			return nil, file.ErrInvalid
		}
		return s3store.NewControl(quarantine, copies)
	}
	if c.Quarantine.Capability.AccessKey != "" || c.Internal.Capability.AccessKey != "" || c.Delivery[0].Capability.AccessKey != "" || c.Delivery[1].Capability.AccessKey != "" {
		return nil, file.ErrInvalid
	}
	return s3store.New(quarantine, internal, copies)
}

func database(ctx context.Context, c filesConfig) (*platformpostgres.Database, error) {
	rw, err := pgx.ParseConfig(c.DatabaseRW)
	if err != nil {
		return nil, file.ErrInvalid
	}
	ro, err := pgx.ParseConfig(c.DatabaseRO)
	if err != nil {
		return nil, file.ErrInvalid
	}
	if rw.User == ro.User || rw.Database != ro.Database {
		return nil, file.ErrInvalid
	}
	for _, conn := range []*pgx.ConnConfig{rw, ro} {
		if conn.TLSConfig == nil || conn.TLSConfig.InsecureSkipVerify || conn.TLSConfig.RootCAs == nil || conn.TLSConfig.ServerName == "" || len(conn.Fallbacks) != 0 {
			return nil, file.ErrInvalid
		}
	}
	// Explicit remote_apply is mandatory even if server defaults are weakened.
	if rw.RuntimeParams["synchronous_commit"] != "remote_apply" {
		return nil, file.ErrInvalid
	}
	env := serviceruntime.MapEnv(map[string]string{"RW": c.DatabaseRW, "RO": c.DatabaseRO})
	pool := func(key string) platformpostgres.PoolConfig {
		secret, _ := env.Secret(key, true)
		return platformpostgres.PoolConfig{DSN: secret, MaxConns: 4, ConnectTimeout: 3 * time.Second, QueryTimeout: 5 * time.Second, MaxConnLifetime: 15 * time.Minute, MaxConnLifetimeJitter: time.Minute, MaxConnIdleTime: time.Minute, HealthCheckPeriod: 10 * time.Second, PingTimeout: 2 * time.Second}
	}
	db, err := platformpostgres.New(ctx, platformpostgres.Config{ApplicationName: "files-" + c.Role, RW: pool("RW"), RO: pool("RO")}, telemetry.NewNoop())
	if err != nil {
		return nil, file.ErrUnavailable
	}
	var replicas string
	if db.RW().QueryRow(ctx, "SHOW synchronous_standby_names").Scan(&replicas) != nil || strings.TrimSpace(replicas) == "" {
		db.Close(context.Background())
		return nil, file.ErrUnavailable
	}
	return db, nil
}

// RunFiles starts either a credentialed worker or the private mTLS control service.
func RunFiles() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	c, err := readFilesConfig(os.Getenv("FILES_CONFIG_FILE"))
	if err != nil {
		return err
	}
	if c.Role == "control" && !c.Control.Enabled {
		return errors.New("files control disabled")
	}
	db, err := database(ctx, c)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		db.Close(cleanup)
	}()
	repo, err := filespostgres.New(db.RW())
	if err != nil {
		return err
	}
	store, err := storage(c)
	if err != nil {
		return err
	}
	if c.Role == "worker" {
		if !filepath.IsAbs(c.TempDir) {
			return file.ErrInvalid
		}
		av, err := clamav.New(c.ClamSocket)
		if err != nil {
			return err
		}
		cdr, err := sandbox.New(c.SandboxSocket)
		if err != nil {
			return err
		}
		worker, err := processing.New(repo, store, av, cdr, c.TempDir)
		if err != nil {
			return err
		}
		for ctx.Err() == nil {
			err = worker.Step(ctx)
			if err != nil && !errors.Is(err, file.ErrNotFound) && !errors.Is(err, file.ErrConflict) {
				os.Stderr.WriteString("files worker: job deferred or rejected\n")
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
		return nil
	}
	return serveFiles(ctx, c.Control, repo, store)
}

func serveFiles(ctx context.Context, c controlConfig, repo *filespostgres.Repository, store *s3store.Store) error {
	cert, ca, err := pair(c.TLS)
	if err != nil {
		return err
	}
	if c.Own.ServiceAccount != "files" || c.Own.Environment != c.Gateway.Environment || c.Own.TrustDomain != c.Gateway.TrustDomain {
		return file.ErrInvalid
	}
	serverTLS, err := workloadid.ScopedTLS(cert, ca, c.Own, c.Gateway, "", true)
	if err != nil {
		return file.ErrInvalid
	}
	authCert, authCA, err := pair(c.AuthTLS)
	if err != nil {
		return err
	}
	expected, pod, err := workloadid.ParseScopedURI(c.AuthURI)
	if err != nil || pod != "" || expected.ServiceAccount != "auth" || expected.Environment != c.Own.Environment || expected.TrustDomain != c.Own.TrustDomain {
		return file.ErrInvalid
	}
	authTLS, err := workloadid.ScopedTLS(authCert, authCA, c.Own, expected, c.AuthServerName, false)
	if err != nil {
		return file.ErrInvalid
	}
	connection, err := grpc.NewClient(c.AuthTarget, grpc.WithTransportCredentials(credentials.NewTLS(authTLS)), grpc.WithDisableRetry(), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(65*1024), grpc.MaxCallSendMsgSize(20*1024)))
	if err != nil {
		return file.ErrUnavailable
	}
	defer connection.Close()
	verifier, err := authsession.New(authv1.NewAuthInternalServiceClient(connection), authsession.Config{Issuer: c.Issuer, MaxTTL: time.Minute, Timeout: 2 * time.Second})
	if err != nil {
		return err
	}
	if err = verifier.Ready(ctx); err != nil {
		return errors.New("files control: Auth readiness failed")
	}
	service, err := files.New(repo, store, time.Now)
	if err != nil {
		return err
	}
	handler, err := internalgrpc.New(service, verifier, c.Gateway)
	if err != nil {
		return err
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)), grpc.MaxRecvMsgSize(16*1024), grpc.MaxSendMsgSize(64*1024), grpc.MaxConcurrentStreams(32), grpc.ChainUnaryInterceptor(internalgrpc.AuditInterceptor(os.Stdout), func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return next(ctx, req)
	}))
	filesv1.RegisterFileServiceServer(server, handler)
	grpc_health_v1.RegisterHealthServer(server, &filesHealth{ready: func(ctx context.Context) error {
		if err := repo.Ready(ctx); err != nil {
			return err
		}
		return verifier.Ready(ctx)
	}})
	listener, err := net.Listen("tcp", c.Address)
	if err != nil {
		return file.ErrUnavailable
	}
	defer listener.Close()
	go func() { <-ctx.Done(); server.Stop() }()
	if err = server.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return file.ErrUnavailable
	}
	return nil
}
