package internalgrpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	"github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/user/internal/application/getme"
	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	application "github.com/v0hmly/marketmesh/services/user/internal/application/settings"
	"github.com/v0hmly/marketmesh/services/user/internal/application/updateme"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/settings"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"io"
	"math"
	"net"
	"testing"
	"time"
)

type settingsTestStore struct{}

func (settingsTestStore) Get(_ context.Context, id profile.SubjectID) (settings.Settings, error) {
	if id[0] == 2 {
		return settings.Settings{}, profile.ErrNotReady
	}
	return settings.Settings{SubjectID: id, Version: 1, Theme: settings.System}, nil
}
func (settingsTestStore) Update(_ context.Context, id profile.SubjectID, theme settings.Theme, v uint64) (settings.Settings, error) {
	if v != 1 {
		return settings.Settings{}, settings.ErrConflict
	}
	return settings.Settings{SubjectID: id, Version: 2, Theme: theme}, nil
}

type settingsVerifier struct{}

func (settingsVerifier) Verify(_ context.Context, token string) (identity.Principal, error) {
	if token == "revoked" {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	p := identity.Principal{SubjectID: profile.SubjectID{1}, CanRead: true, CanWrite: true, CanReadAddresses: true, CanWriteAddresses: true, CanReadSettings: token == "read" || token == "write" || token == "missing", CanWriteSettings: token == "write"}
	if token == "missing" {
		p.SubjectID[0] = 2
	}
	return p, nil
}
func TestSettingsRPCScopesValidationAndPrivacy(t *testing.T) {
	ps := &testStore{}
	get, _ := getme.New(ps)
	update, _ := updateme.New(ps)
	policy, _ := workloadid.NewPolicy(map[workloadid.Identity][]string{{TrustDomain: "marketmesh.test", Environment: "dev", Role: "gateway-out"}: {userv1.UserService_GetSettings_FullMethodName, userv1.UserService_UpdateSettings_FullMethodName}})
	log, _ := logger.New(logger.Config{Service: "user", Version: "test", Environment: "local", Level: "info", Output: io.Discard})
	h, _ := New(get, update, settingsVerifier{}, policy, log)
	if _, e := h.GetSettings(context.Background(), &userv1.GetSettingsRequest{}); status.Code(e) != codes.Unimplemented {
		t.Fatal(e)
	}
	uc, _ := application.New(settingsTestStore{})
	if e := h.EnableSettings(uc); e != nil {
		t.Fatal(e)
	}
	ca, key := certificateAuthority(t)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{issueCertificate(t, ca, key, "", true)}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert})), grpc.UnaryInterceptor(workloadid.UnaryServerInterceptor(policy)))
	userv1.RegisterUserServiceServer(server, h)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	client := func(role string) userv1.UserServiceClient {
		cfg := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: "localhost", RootCAs: roots, Certificates: []tls.Certificate{issueCertificate(t, ca, key, role, false)}}
		conn, e := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return userv1.NewUserServiceClient(conn)
	}
	c := client("spiffe://marketmesh.test/dev/gateway-out")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tokenctx := func(token string) context.Context {
		return metadata.AppendToOutgoingContext(ctx, AssertionMetadata, token)
	}
	for _, tc := range []struct {
		token string
		code  codes.Code
	}{{"read", codes.OK}, {"profile", codes.PermissionDenied}, {"revoked", codes.Unauthenticated}, {"missing", codes.NotFound}} {
		var headers metadata.MD
		r, e := c.GetSettings(tokenctx(tc.token), &userv1.GetSettingsRequest{}, grpc.Header(&headers))
		if status.Code(e) != tc.code || len(headers.Get("cache-control")) != 1 || headers.Get("cache-control")[0] != "no-store" {
			t.Fatal(e, headers)
		}
		if e == nil && (r.GetSettings().GetVersion() != 1 || r.GetSettings().GetTheme() != userv1.Theme_THEME_SYSTEM) {
			t.Fatal(r)
		}
		if tc.token == "missing" {
			details := status.Convert(e).Details()
			if len(details) != 1 {
				t.Fatal(details)
			}
			info, ok := details[0].(*errdetails.ErrorInfo)
			if !ok || info.GetReason() != "PROFILE_NOT_READY" || info.GetDomain() != "marketmesh.user" || len(info.GetMetadata()) != 0 {
				t.Fatal(details)
			}
		}
	}
	wrong := client("spiffe://marketmesh.test/dev/gateway-in")
	if _, e = wrong.GetSettings(tokenctx("read"), &userv1.GetSettingsRequest{}); status.Code(e) != codes.PermissionDenied {
		t.Fatal(e)
	}
	if _, e = c.GetSettings(metadata.AppendToOutgoingContext(tokenctx("read"), "authorization", "Bearer external"), &userv1.GetSettingsRequest{}); status.Code(e) != codes.Unauthenticated {
		t.Fatal(e)
	}
	if _, e = c.UpdateSettings(tokenctx("read"), &userv1.UpdateSettingsRequest{Theme: userv1.Theme_THEME_DARK, ExpectedVersion: 1}); status.Code(e) != codes.PermissionDenied {
		t.Fatal(e)
	}
	for _, theme := range []userv1.Theme{userv1.Theme_THEME_SYSTEM, userv1.Theme_THEME_LIGHT, userv1.Theme_THEME_DARK} {
		r, e := c.UpdateSettings(tokenctx("write"), &userv1.UpdateSettingsRequest{Theme: theme, ExpectedVersion: 1})
		if e != nil || r.GetSettings().GetTheme() != theme || r.GetSettings().GetVersion() != 2 {
			t.Fatal(r, e)
		}
	}
	for _, theme := range []userv1.Theme{userv1.Theme_THEME_UNSPECIFIED, userv1.Theme(99), userv1.Theme(-1)} {
		if _, e = c.UpdateSettings(tokenctx("write"), &userv1.UpdateSettingsRequest{Theme: theme, ExpectedVersion: 1}); status.Code(e) != codes.InvalidArgument {
			t.Fatal(e)
		}
	}
	for _, v := range []uint64{0, math.MaxInt64, math.MaxUint64} {
		if _, e = c.UpdateSettings(tokenctx("write"), &userv1.UpdateSettingsRequest{Theme: userv1.Theme_THEME_DARK, ExpectedVersion: v}); status.Code(e) != codes.InvalidArgument {
			t.Fatal(e)
		}
	}
	if _, e = c.UpdateSettings(tokenctx("write"), &userv1.UpdateSettingsRequest{Theme: userv1.Theme_THEME_DARK, ExpectedVersion: 2}); status.Code(e) != codes.Aborted {
		t.Fatal(e)
	}
}
