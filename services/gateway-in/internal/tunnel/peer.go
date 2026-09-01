package tunnel

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"net/url"
	"slices"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

type peerAuthorizer struct {
	dataCenterByURI map[string]string
}

func newPeerAuthorizer(policy PeerPolicy) (peerAuthorizer, error) {
	if len(policy.AllowedURIs) == 0 || len(policy.AllowedURIs) > maxPeerIdentities {
		return peerAuthorizer{}, errors.New("tunnel: peer uri allowlist is outside bounds")
	}

	dataCenterByURI := make(map[string]string, len(policy.AllowedURIs))
	dataCenters := map[string]struct{}{}
	for _, identity := range policy.AllowedURIs {
		parsed, err := url.Parse(identity)
		if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return peerAuthorizer{}, errors.New("tunnel: peer uri allowlist contains an invalid identity")
		}
		if _, exists := dataCenterByURI[identity]; exists {
			return peerAuthorizer{}, errors.New("tunnel: peer uri allowlist contains a duplicate")
		}
		dataCenter := defaultDataCenter
		if len(policy.DataCenterByURI) > 0 {
			mapped, exists := policy.DataCenterByURI[identity]
			if !exists {
				return peerAuthorizer{}, errors.New("tunnel: peer uri is missing a data center")
			}
			dataCenter = mapped
		}
		if !validDataCenter(dataCenter) {
			return peerAuthorizer{}, errors.New("tunnel: peer data center is invalid")
		}
		dataCenterByURI[identity] = dataCenter
		dataCenters[dataCenter] = struct{}{}
	}
	if len(policy.DataCenterByURI) > 0 && len(policy.DataCenterByURI) != len(dataCenterByURI) {
		return peerAuthorizer{}, errors.New("tunnel: peer data center map contains an unknown uri")
	}
	if len(dataCenters) > maxDataCenters {
		return peerAuthorizer{}, errors.New("tunnel: peer data center count is outside bounds")
	}

	return peerAuthorizer{dataCenterByURI: dataCenterByURI}, nil
}

func (a peerAuthorizer) authorize(ctx context.Context) (string, error) {
	transportPeer, found := peer.FromContext(ctx)
	if !found || transportPeer == nil || transportPeer.AuthInfo == nil {
		return "", ErrPeerUnauthorized
	}
	tlsInfo, validTLS := transportPeer.AuthInfo.(credentials.TLSInfo)
	if !validTLS {
		return "", ErrPeerUnauthorized
	}
	if len(tlsInfo.State.VerifiedChains) == 0 ||
		len(tlsInfo.State.VerifiedChains[0]) == 0 ||
		len(tlsInfo.State.PeerCertificates) == 0 {
		return "", ErrPeerUnauthorized
	}

	leaf := tlsInfo.State.PeerCertificates[0]
	if leaf == nil || len(leaf.URIs) != 1 || leaf.URIs[0] == nil {
		return "", ErrPeerUnauthorized
	}
	verifiedLeaf := tlsInfo.State.VerifiedChains[0][0]
	if verifiedLeaf == nil || !bytes.Equal(leaf.Raw, verifiedLeaf.Raw) {
		return "", ErrPeerUnauthorized
	}
	if !certificateAllowsClientAuth(leaf) {
		return "", ErrPeerUnauthorized
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return "", ErrPeerUnauthorized
	}
	dataCenter, allowed := a.dataCenterByURI[leaf.URIs[0].String()]
	if !allowed {
		return "", ErrPeerUnauthorized
	}

	return dataCenter, nil
}

func validDataCenter(value string) bool {
	if len(value) == 0 || len(value) > 32 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		isLower := character >= 'a' && character <= 'z'
		isDigit := character >= '0' && character <= '9'
		if !isLower && !isDigit && character != '-' {
			return false
		}
	}

	return true
}

func certificateAllowsClientAuth(certificate *x509.Certificate) bool {
	return slices.Contains(certificate.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
}
