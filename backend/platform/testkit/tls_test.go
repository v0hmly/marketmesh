package testkit_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/platform/testkit"
)

func TestTLSMaterialSupportsTLSAndMutualTLS(t *testing.T) {
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

			material := testkit.NewTLS(t, "service.test")
			clientErr, serverErr := handshake(
				t,
				material.ServerConfig(testCase.mutualTLS),
				material.ClientConfig(testCase.mutualTLS),
			)
			if clientErr != nil || serverErr != nil {
				t.Fatalf("TLS handshake errors = client %v, server %v", clientErr, serverErr)
			}
		})
	}
}

func TestTLSMaterialRejectsWrongServerNameAndMissingClientCertificate(t *testing.T) {
	t.Parallel()

	material := testkit.NewTLS(t, "service.test")

	wrongName := material.ClientConfig(false)
	wrongName.ServerName = "other.test"
	clientErr, _ := handshake(t, material.ServerConfig(false), wrongName)
	if clientErr == nil {
		t.Fatal("wrong server name handshake error = nil")
	}
	var hostnameErr x509.HostnameError
	if !errors.As(clientErr, &hostnameErr) {
		t.Fatalf("wrong server name error = %v, want x509.HostnameError", clientErr)
	}

	withoutClientCertificate := material.ClientConfig(false)
	clientErr, serverErr := handshake(t, material.ServerConfig(true), withoutClientCertificate)
	if clientErr == nil && serverErr == nil {
		t.Fatal("mTLS without client certificate unexpectedly succeeded")
	}
}

func TestTLSMaterialReturnsIndependentConfigsAndPEM(t *testing.T) {
	t.Parallel()

	material := testkit.NewTLS(t, "127.0.0.1")
	first := material.ClientConfig(true)
	second := material.ClientConfig(true)
	first.ServerName = "changed"
	first.Certificates[0].Certificate[0][0] ^= 0xff
	first.Certificates[0].Leaf.DNSNames[0] = "changed.test"
	if second.ServerName != "127.0.0.1" {
		t.Fatalf("second server name = %q", second.ServerName)
	}
	if first.Certificates[0].Certificate[0][0] == second.Certificates[0].Certificate[0][0] {
		t.Fatal("client certificate DER shares mutable storage")
	}
	if first.Certificates[0].PrivateKey == second.Certificates[0].PrivateKey {
		t.Fatal("client certificate shares private key pointer")
	}
	if second.Certificates[0].Leaf.DNSNames[0] != "marketmesh-test-client" {
		t.Fatal("parsed client certificate shares mutable storage")
	}

	firstPEM := material.CAPEM()
	secondPEM := material.CAPEM()
	firstPEM[0] ^= 0xff
	if firstPEM[0] == secondPEM[0] {
		t.Fatal("CA PEM shares mutable storage")
	}
}

func handshake(
	t *testing.T,
	serverConfig *tls.Config,
	clientConfig *tls.Config,
) (error, error) {
	t.Helper()

	serverSide, clientSide := net.Pipe()
	server := tls.Server(serverSide, serverConfig)
	client := tls.Client(clientSide, clientConfig)
	t.Cleanup(func() {
		_ = clientSide.Close()
		_ = serverSide.Close()
	})

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	serverResult := make(chan error, 1)
	go func() {
		serverResult <- server.HandshakeContext(ctx)
	}()
	clientErr := client.HandshakeContext(ctx)
	_ = clientSide.Close()
	serverErr := testkit.Wait(t, time.Second, serverResult)

	return clientErr, serverErr
}
