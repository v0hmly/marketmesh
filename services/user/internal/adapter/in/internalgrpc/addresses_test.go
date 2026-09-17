package internalgrpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	"github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/user/internal/application/addresses"
	"github.com/v0hmly/marketmesh/services/user/internal/application/getme"
	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/user/internal/application/updateme"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/address"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type addressTestStore struct{}

func (addressTestStore) List(_ context.Context, id profile.SubjectID) (address.Book, error) {
	return address.Book{SubjectID: id, Version: 1}, nil
}
func (addressTestStore) Mutate(_ context.Context, id profile.SubjectID, c addresses.Command) (address.Book, error) {
	return address.Book{SubjectID: id, Version: c.ExpectedVersion + 1}, nil
}

type addressVerifier struct{}

func (addressVerifier) Verify(_ context.Context, token string) (identity.Principal, error) {
	if token == "revoked" {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	return identity.Principal{SubjectID: profile.SubjectID{1}, CanRead: true, CanWrite: true, CanReadAddresses: token == "read" || token == "write", CanWriteAddresses: token == "write"}, nil
}
func TestAddressRPCWorkloadScopesAndNoStore(t *testing.T) {
	ps := &testStore{}
	get, _ := getme.New(ps)
	update, _ := updateme.New(ps)
	methods := []string{userv1.UserService_ListAddresses_FullMethodName, userv1.UserService_CreateAddress_FullMethodName, userv1.UserService_UpdateAddress_FullMethodName, userv1.UserService_DeleteAddress_FullMethodName, userv1.UserService_SetDefaultAddress_FullMethodName}
	policy, _ := workloadid.NewPolicy(map[workloadid.Identity][]string{{TrustDomain: "marketmesh.test", Environment: "dev", Role: "gateway-out"}: methods})
	log, _ := logger.New(logger.Config{Service: "user", Version: "test", Environment: "local", Level: "info", Output: io.Discard})
	h, _ := New(get, update, addressVerifier{}, policy, log)
	if _, e := h.ListAddresses(context.Background(), &userv1.ListAddressesRequest{}); status.Code(e) != codes.Unimplemented {
		t.Fatal("disabled handler", e)
	}
	uc, _ := addresses.New(addressTestStore{})
	if e := h.EnableAddresses(uc); e != nil {
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
	}{{"read", codes.OK}, {"profile", codes.PermissionDenied}, {"revoked", codes.Unauthenticated}} {
		var headers metadata.MD
		r, e := c.ListAddresses(tokenctx(tc.token), &userv1.ListAddressesRequest{}, grpc.Header(&headers))
		if status.Code(e) != tc.code || len(headers.Get("cache-control")) != 1 || headers.Get("cache-control")[0] != "no-store" {
			t.Fatal(e, headers)
		}
		if e == nil && r.GetBook().GetVersion() != 1 {
			t.Fatal(r)
		}
	}
	if _, e = c.ListAddresses(metadata.AppendToOutgoingContext(tokenctx("read"), "authorization", "Bearer external"), &userv1.ListAddressesRequest{}); status.Code(e) != codes.Unauthenticated {
		t.Fatal(e)
	}
	wrong := client("spiffe://marketmesh.test/dev/gateway-in")
	if _, e = wrong.ListAddresses(tokenctx("read"), &userv1.ListAddressesRequest{}); status.Code(e) != codes.PermissionDenied {
		t.Fatal(e)
	}
	fields := &userv1.AddressFields{Recipient: "A", Phone: "1234567", Country: "Country", City: "City", StreetHouse: "Street"}
	id := address.ID{1}.Bytes()
	writes := []func(context.Context) error{
		func(ctx context.Context) error {
			_, e := c.CreateAddress(ctx, &userv1.CreateAddressRequest{Fields: fields, ExpectedBookVersion: 1})
			return e
		},
		func(ctx context.Context) error {
			_, e := c.UpdateAddress(ctx, &userv1.UpdateAddressRequest{AddressId: id, Fields: fields, ExpectedBookVersion: 1})
			return e
		},
		func(ctx context.Context) error {
			_, e := c.DeleteAddress(ctx, &userv1.DeleteAddressRequest{AddressId: id, ExpectedBookVersion: 1})
			return e
		},
		func(ctx context.Context) error {
			_, e := c.SetDefaultAddress(ctx, &userv1.SetDefaultAddressRequest{AddressId: id, ExpectedBookVersion: 1})
			return e
		},
	}
	for _, write := range writes {
		if e := write(tokenctx("read")); status.Code(e) != codes.PermissionDenied {
			t.Fatal(e)
		}
		if e := write(tokenctx("write")); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = c.CreateAddress(tokenctx("write"), &userv1.CreateAddressRequest{ExpectedBookVersion: 1}); status.Code(e) != codes.InvalidArgument {
		t.Fatal(e)
	}
	if _, e = c.DeleteAddress(tokenctx("write"), &userv1.DeleteAddressRequest{AddressId: make([]byte, 16), ExpectedBookVersion: 1}); status.Code(e) != codes.InvalidArgument {
		t.Fatal(e)
	}
}
func TestAddressErrorDetailsAndMaximumResponse(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   codes.Code
		reason string
	}{{profile.ErrNotReady, codes.NotFound, "PROFILE_NOT_READY"}, {address.ErrNotFound, codes.NotFound, "ADDRESS_NOT_FOUND"}, {address.ErrLimit, codes.ResourceExhausted, "ADDRESS_LIMIT_REACHED"}} {
		s := status.Convert(addressError(tc.err))
		d := s.Details()
		if s.Code() != tc.code || len(d) != 1 {
			t.Fatal(s)
		}
		info, ok := d[0].(*errdetails.ErrorInfo)
		if !ok || info.GetDomain() != "marketmesh.user" || info.GetReason() != tc.reason || len(info.GetMetadata()) != 0 {
			t.Fatal(d)
		}
	}
	if status.Code(addressError(address.ErrConflict)) != codes.Aborted {
		t.Fatal("conflict mapping")
	}
	b := address.Book{SubjectID: profile.SubjectID{1}, Version: 1}
	for i := range address.MaxAddresses {
		b.Addresses = append(b.Addresses, address.Address{ID: address.ID{byte(i + 1)}, Fields: address.Fields{Recipient: strings.Repeat("😀", 120), Phone: strings.Repeat(" ", 17) + "123456789012345", Country: strings.Repeat("😀", 80), PostalCode: strings.Repeat("😀", 20), City: strings.Repeat("😀", 120), StreetHouse: strings.Repeat("😀", 240), Apartment: strings.Repeat("😀", 40), Comment: strings.Repeat("😀", 500)}})
	}
	size := proto.Size(&userv1.ListAddressesResponse{Book: wireBook(b)})
	if size <= 32*1024 || size > 128*1024 {
		t.Fatal("unexpected maximum response size", size)
	}
}
