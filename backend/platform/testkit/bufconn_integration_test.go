//go:build integration

package testkit_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/platform/testkit"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func TestBufconnRunsTLSAndMutualTLSRoundTrip(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		mutualTLS bool
	}{
		{name: "tls"},
		{name: "mutual tls", mutualTLS: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			material := testkit.NewTLS(t, "bufconn.test")
			healthServer := health.NewServer()
			healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
			harness := testkit.NewBufconn(
				t,
				testkit.BufconnConfig{
					ServerOptions: []grpcgo.ServerOption{
						grpcgo.Creds(credentials.NewTLS(material.ServerConfig(testCase.mutualTLS))),
					},
					DialOptions: []grpcgo.DialOption{
						grpcgo.WithTransportCredentials(
							credentials.NewTLS(material.ClientConfig(testCase.mutualTLS)),
						),
					},
				},
				func(registrar grpcgo.ServiceRegistrar) {
					grpc_health_v1.RegisterHealthServer(registrar, healthServer)
				},
			)

			callCtx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			response, err := grpc_health_v1.NewHealthClient(harness.Connection()).Check(
				callCtx,
				&grpc_health_v1.HealthCheckRequest{},
			)
			if err != nil {
				t.Fatalf("health check: %v", err)
			}
			if response.Status != grpc_health_v1.HealthCheckResponse_SERVING {
				t.Fatalf("health status = %s, want SERVING", response.Status)
			}
		})
	}
}

func TestBufconnCloseIsConcurrentAndIdempotent(t *testing.T) {
	t.Parallel()

	harness := testkit.NewBufconn(
		t,
		testkit.BufconnConfig{},
		func(grpcgo.ServiceRegistrar) {},
	)

	const callers = 16
	errorsByCaller := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Go(func() {
			errorsByCaller <- harness.Close()
		})
	}
	group.Wait()
	close(errorsByCaller)
	for err := range errorsByCaller {
		if err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}
}
