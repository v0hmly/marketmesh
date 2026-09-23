package workloadid

import (
	"context"
	"testing"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testMethod = "/marketmesh.user.v1.UserService/GetUser"

func testPolicy(t *testing.T) *Policy {
	t.Helper()
	policy, err := NewPolicy(map[Identity][]string{
		gatewayInIdentity(): {testMethod},
	})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}

	return policy
}

// fakeServerStream — минимальная реализация ServerStream для тестов.
type fakeServerStream struct {
	grpcgo.ServerStream
	ctx context.Context
}

func (s fakeServerStream) Context() context.Context { return s.ctx }

func TestUnaryServerInterceptor(t *testing.T) {
	ca := newTestCA(t)
	leaf := ca.issueLeaf(t, testLeafConfig{})
	policy := testPolicy(t)
	interceptor := UnaryServerInterceptor(policy)

	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}
	info := &grpcgo.UnaryServerInfo{FullMethod: testMethod}

	t.Run("allowed call reaches the handler", func(t *testing.T) {
		response, err := interceptor(tlsPeerContext(leaf, leaf), nil, info, handler)
		if err != nil {
			t.Fatalf("interceptor: %v", err)
		}
		if response != "ok" {
			t.Fatalf("got response %v, want ok", response)
		}
	})

	t.Run("missing identity is unauthenticated", func(t *testing.T) {
		called := false
		_, err := interceptor(context.Background(), nil, info,
			func(ctx context.Context, req any) (any, error) {
				called = true
				return nil, nil
			})
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("got code %v, want Unauthenticated", status.Code(err))
		}
		if called {
			t.Fatal("handler called for unauthenticated peer")
		}
	})

	t.Run("valid identity without rule is denied", func(t *testing.T) {
		strangerLeaf := ca.issueLeaf(t, testLeafConfig{})
		strangerCtx := tlsPeerContext(strangerLeaf, strangerLeaf)
		unknownMethod := &grpcgo.UnaryServerInfo{FullMethod: "/marketmesh.user.v1.UserService/DeleteUser"}
		_, err := interceptor(strangerCtx, nil, unknownMethod, handler)
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("got code %v, want PermissionDenied", status.Code(err))
		}
	})

	t.Run("identity of another role is denied", func(t *testing.T) {
		otherRole := ca.issueLeaf(t, testLeafConfig{
			uris: mustURIs(t, "spiffe://marketmesh.test/prod/gateway-out"),
		})
		_, err := interceptor(tlsPeerContext(otherRole, otherRole), nil, info, handler)
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("got code %v, want PermissionDenied", status.Code(err))
		}
	})

	t.Run("identity of another environment is denied", func(t *testing.T) {
		devLeaf := ca.issueLeaf(t, testLeafConfig{
			uris: mustURIs(t, "spiffe://marketmesh.test/dev/gateway-in"),
		})
		_, err := interceptor(tlsPeerContext(devLeaf, devLeaf), nil, info, handler)
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("got code %v, want PermissionDenied", status.Code(err))
		}
	})

	t.Run("nil policy is fail-closed", func(t *testing.T) {
		nilInterceptor := UnaryServerInterceptor(nil)
		_, err := nilInterceptor(tlsPeerContext(leaf, leaf), nil, info, handler)
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("got code %v, want PermissionDenied", status.Code(err))
		}
	})
}

func TestStreamServerInterceptor(t *testing.T) {
	ca := newTestCA(t)
	leaf := ca.issueLeaf(t, testLeafConfig{})
	policy := testPolicy(t)
	interceptor := StreamServerInterceptor(policy)
	info := &grpcgo.StreamServerInfo{FullMethod: testMethod}

	t.Run("allowed call reaches the handler", func(t *testing.T) {
		called := false
		err := interceptor(nil,
			fakeServerStream{ctx: tlsPeerContext(leaf, leaf)}, info,
			func(srv any, stream grpcgo.ServerStream) error {
				called = true
				return nil
			})
		if err != nil {
			t.Fatalf("interceptor: %v", err)
		}
		if !called {
			t.Fatal("handler was not called for an allowed stream")
		}
	})

	t.Run("denied stream does not reach the handler", func(t *testing.T) {
		called := false
		err := interceptor(nil,
			fakeServerStream{ctx: context.Background()}, info,
			func(srv any, stream grpcgo.ServerStream) error {
				called = true
				return nil
			})
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("got code %v, want Unauthenticated", status.Code(err))
		}
		if called {
			t.Fatal("handler called for unauthenticated stream")
		}
	})

	t.Run("valid identity without rule is denied", func(t *testing.T) {
		otherRole := ca.issueLeaf(t, testLeafConfig{
			uris: mustURIs(t, "spiffe://marketmesh.test/prod/gateway-out"),
		})
		err := interceptor(nil,
			fakeServerStream{ctx: tlsPeerContext(otherRole, otherRole)}, info,
			func(srv any, stream grpcgo.ServerStream) error { return nil })
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("got code %v, want PermissionDenied", status.Code(err))
		}
	})
}

func TestInterceptorsRevocation(t *testing.T) {
	ca := newTestCA(t)
	revokedLeaf := ca.issueLeaf(t, testLeafConfig{})
	cleanLeaf := ca.issueLeaf(t, testLeafConfig{})
	policy := testPolicy(t)

	revocation := NewInMemoryRevocationList()
	revocation.Revoke(SerialString(revokedLeaf.SerialNumber))
	interceptor := UnaryServerInterceptor(policy, WithRevocationList(revocation))
	info := &grpcgo.UnaryServerInfo{FullMethod: testMethod}
	handler := func(ctx context.Context, req any) (any, error) { return "ok", nil }

	t.Run("revoked serial is rejected", func(t *testing.T) {
		_, err := interceptor(tlsPeerContext(revokedLeaf, revokedLeaf), nil, info, handler)
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("got code %v, want Unauthenticated", status.Code(err))
		}
	})

	t.Run("other identities keep working", func(t *testing.T) {
		if _, err := interceptor(tlsPeerContext(cleanLeaf, cleanLeaf), nil, info, handler); err != nil {
			t.Fatalf("non-revoked certificate rejected: %v", err)
		}
	})
}

func TestInterceptorsOnVerify(t *testing.T) {
	ca := newTestCA(t)
	leaf := ca.issueLeaf(t, testLeafConfig{})
	policy := testPolicy(t)
	info := &grpcgo.UnaryServerInfo{FullMethod: testMethod}
	handler := func(ctx context.Context, req any) (any, error) { return "ok", nil }

	type report struct {
		identity Identity
		err      error
	}
	reports := make(chan report, 2)
	interceptor := UnaryServerInterceptor(policy,
		WithOnVerify(func(identity Identity, err error) {
			reports <- report{identity: identity, err: err}
		}))

	if _, err := interceptor(tlsPeerContext(leaf, leaf), nil, info, handler); err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	first := <-reports
	if first.err != nil || first.identity != gatewayInIdentity() {
		t.Fatalf("success report: got %+v", first)
	}

	_, err := interceptor(context.Background(), nil, info, handler)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("got code %v, want Unauthenticated", status.Code(err))
	}
	second := <-reports
	if second.err == nil || second.identity != (Identity{}) {
		t.Fatalf("failure report: got %+v", second)
	}
}
