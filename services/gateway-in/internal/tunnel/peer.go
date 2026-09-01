package tunnel

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"net/url"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

type peerAuthorizer struct {
	allowedURIs map[string]struct{}
}

func newPeerAuthorizer(policy PeerPolicy) (peerAuthorizer, error) {
	if len(policy.AllowedURIs) == 0 {
		return peerAuthorizer{}, errors.New("tunnel: peer uri allowlist must not be empty")
	}

	allowed := make(map[string]struct{}, len(policy.AllowedURIs))
	for _, identity := range policy.AllowedURIs {
		parsed, err := url.Parse(identity)
		if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return peerAuthorizer{}, errors.New("tunnel: peer uri allowlist contains an invalid identity")
		}
		if _, exists := allowed[identity]; exists {
			return peerAuthorizer{}, errors.New("tunnel: peer uri allowlist contains a duplicate")
		}
		allowed[identity] = struct{}{}
	}

	return peerAuthorizer{allowedURIs: allowed}, nil
}

func (a peerAuthorizer) authorize(ctx context.Context) error {
	transportPeer, found := peer.FromContext(ctx)
	if !found || transportPeer == nil || transportPeer.AuthInfo == nil {
		return ErrPeerUnauthorized
	}
	tlsInfo, validTLS := transportPeer.AuthInfo.(credentials.TLSInfo)
	if !validTLS {
		return ErrPeerUnauthorized
	}
	if len(tlsInfo.State.VerifiedChains) == 0 ||
		len(tlsInfo.State.VerifiedChains[0]) == 0 ||
		len(tlsInfo.State.PeerCertificates) == 0 {
		return ErrPeerUnauthorized
	}

	leaf := tlsInfo.State.PeerCertificates[0]
	if leaf == nil || len(leaf.URIs) != 1 || leaf.URIs[0] == nil {
		return ErrPeerUnauthorized
	}
	verifiedLeaf := tlsInfo.State.VerifiedChains[0][0]
	if verifiedLeaf == nil || !bytes.Equal(leaf.Raw, verifiedLeaf.Raw) {
		return ErrPeerUnauthorized
	}
	if !certificateAllowsClientAuth(leaf) {
		return ErrPeerUnauthorized
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return ErrPeerUnauthorized
	}
	if _, allowed := a.allowedURIs[leaf.URIs[0].String()]; !allowed {
		return ErrPeerUnauthorized
	}

	return nil
}

func certificateAllowsClientAuth(certificate *x509.Certificate) bool {
	for _, usage := range certificate.ExtKeyUsage {
		if usage == x509.ExtKeyUsageClientAuth {
			return true
		}
	}

	return false
}
