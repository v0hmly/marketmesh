package fakeapp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func loadServerTLS(certificateFile string, keyFile string, clientCAFile string) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(certificateFile, keyFile)
	if err != nil {
		return nil, errors.New("fake internal: loading server key pair")
	}
	caPEM, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, errors.New("fake internal: reading client ca")
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("fake internal: parsing client ca")
	}

	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
	}, nil
}

func peerAuthorizer(expectedIdentity string) (grpcgo.UnaryServerInterceptor, error) {
	expected, err := url.Parse(expectedIdentity)
	if err != nil || expected.Scheme == "" || expected.Host == "" || expected.User != nil ||
		expected.RawQuery != "" || expected.Fragment != "" {
		return nil, errors.New("fake internal: expected client identity is invalid")
	}

	return func(
		ctx context.Context,
		request any,
		_ *grpcgo.UnaryServerInfo,
		handler grpcgo.UnaryHandler,
	) (any, error) {
		transportPeer, found := peer.FromContext(ctx)
		if !found {
			return nil, status.Error(codes.PermissionDenied, "peer is not authorized")
		}
		tlsInfo, valid := transportPeer.AuthInfo.(credentials.TLSInfo)
		if !valid || len(tlsInfo.State.VerifiedChains) == 0 ||
			len(tlsInfo.State.VerifiedChains[0]) == 0 {
			return nil, status.Error(codes.PermissionDenied, "peer is not authorized")
		}
		leaf := tlsInfo.State.VerifiedChains[0][0]
		if len(leaf.URIs) != 1 || leaf.URIs[0] == nil || leaf.URIs[0].String() != expected.String() {
			return nil, status.Error(codes.PermissionDenied, "peer is not authorized")
		}

		response, handlerErr := handler(ctx, request)
		if handlerErr != nil {
			return nil, handlerErr
		}

		return response, nil
	}, nil
}

func wrapServeError(operation string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("fake internal: %s: %w", operation, err)
}
