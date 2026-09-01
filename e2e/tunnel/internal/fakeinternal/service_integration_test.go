//go:build integration

package fakeinternal_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	e2ev1 "github.com/v0hmly/marketmesh/api/gen/go/e2e/v1"
	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/fakeinternal"
	"github.com/v0hmly/marketmesh/platform/testkit"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

func TestServiceMutualTLSRoundTrip(t *testing.T) {
	t.Parallel()

	service, err := fakeinternal.New(fakeinternal.Config{
		InstanceID:       "fake-internal-integration",
		MaxLedgerEntries: 8,
	})
	if err != nil {
		t.Fatalf("fakeinternal.New() error = %v", err)
	}
	material := testkit.NewTLS(t, "fake-internal.test")
	harness := testkit.NewBufconn(
		t,
		testkit.BufconnConfig{
			ServerOptions: []grpcgo.ServerOption{
				grpcgo.Creds(credentials.NewTLS(material.ServerConfig(true))),
			},
			DialOptions: []grpcgo.DialOption{
				grpcgo.WithTransportCredentials(credentials.NewTLS(material.ClientConfig(true))),
			},
		},
		func(registrar grpcgo.ServiceRegistrar) {
			e2ev1.RegisterFakeInternalServiceServer(registrar, service)
		},
	)
	client := e2ev1.NewFakeInternalServiceClient(harness.Connection())
	callCtx, cancel := context.WithTimeout(
		metadata.NewOutgoingContext(
			t.Context(),
			metadata.Pairs(protocolv1.InternalIdempotencyKeyMetadata, "integration-key"),
		),
		time.Second,
	)
	defer cancel()

	response, err := client.Mutate(callCtx, &e2ev1.MutateRequest{
		RequestId: bytes.Repeat([]byte{0x55}, 16),
	})
	if err != nil {
		t.Fatalf("Mutate() error = %v", err)
	}
	if response.GetInstanceId() != "fake-internal-integration" || response.GetDuplicate() {
		t.Fatalf("Mutate() response = %v", response)
	}
	ledger, err := client.Ledger(t.Context(), &e2ev1.LedgerRequest{Limit: 8})
	if err != nil {
		t.Fatalf("Ledger() error = %v", err)
	}
	if len(ledger.GetEntries()) != 1 || len(ledger.GetEntries()[0].GetIdempotencyKeySha256()) != 32 {
		t.Fatalf("Ledger() entries = %v", ledger.GetEntries())
	}
}
