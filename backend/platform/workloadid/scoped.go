package workloadid

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/url"
	"strings"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// Scope explicitly authorizes an environment, cluster, namespace and service account.
// It is separate from the legacy role identity so old role policies cannot grant
// access to newly scoped workloads by accidentally ignoring additional fields.
type Scope struct{ TrustDomain, Environment, Cluster, Namespace, ServiceAccount string }

func (s Scope) String() string {
	return "spiffe://" + s.TrustDomain + "/env/" + s.Environment + "/cluster/" + s.Cluster + "/ns/" + s.Namespace + "/sa/" + s.ServiceAccount
}
func (s Scope) Validate() error {
	if !validTrustDomain(s.TrustDomain) {
		return ErrInvalidIdentity
	}
	for _, segment := range []string{s.Environment, s.Cluster, s.Namespace, s.ServiceAccount} {
		if len(segment) == 0 || len(segment) > 63 || segment[0] == '-' || segment[len(segment)-1] == '-' || !lowerKebab(segment) {
			return ErrInvalidIdentity
		}
	}
	return nil
}

// ParseScopedURI returns a scope and optional audit-only pod UID. The pod never
// forms part of authorization, so restarting a pod cannot change its privileges.
func ParseScopedURI(raw string) (Scope, string, error) {
	if len(raw) > 512 || !strings.HasPrefix(raw, "spiffe://") {
		return Scope{}, "", ErrInvalidIdentity
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "spiffe" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" || strings.Contains(raw, "%") {
		return Scope{}, "", ErrInvalidIdentity
	}
	p := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if (len(p) != 8 && len(p) != 10) || p[0] != "env" || p[2] != "cluster" || p[4] != "ns" || p[6] != "sa" {
		return Scope{}, "", ErrInvalidIdentity
	}
	s := Scope{TrustDomain: u.Host, Environment: p[1], Cluster: p[3], Namespace: p[5], ServiceAccount: p[7]}
	if s.Validate() != nil {
		return Scope{}, "", ErrInvalidIdentity
	}
	pod := ""
	if len(p) == 10 {
		pod = p[9]
		if p[8] != "pod" || len(pod) != 36 {
			return Scope{}, "", ErrInvalidIdentity
		}
		for i, c := range []byte(pod) {
			if i == 8 || i == 13 || i == 18 || i == 23 {
				if c != '-' {
					return Scope{}, "", ErrInvalidIdentity
				}
			} else if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return Scope{}, "", ErrInvalidIdentity
			}
		}
	}
	return s, pod, nil
}

func scopedCertificate(cert *x509.Certificate) (Scope, string, error) {
	if cert == nil || len(cert.URIs) != 1 || cert.URIs[0] == nil || cert.NotAfter.Sub(cert.NotBefore) > 24*time.Hour || time.Now().Before(cert.NotBefore) || !time.Now().Before(cert.NotAfter) {
		return Scope{}, "", ErrInvalidIdentity
	}
	return ParseScopedURI(cert.URIs[0].String())
}

func ScopedFromContext(ctx context.Context) (Scope, string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok || p.AuthInfo == nil {
		return Scope{}, "", ErrInvalidIdentity
	}
	auth, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || !auth.State.HandshakeComplete || len(auth.State.VerifiedChains) == 0 || len(auth.State.VerifiedChains[0]) == 0 {
		return Scope{}, "", ErrInvalidIdentity
	}
	return scopedCertificate(auth.State.VerifiedChains[0][0])
}

// ScopedTLS validates both PKI and the full peer scope. ServerName is required for
// clients in addition to URI authorization; successful TLS alone grants no RPC.
func ScopedTLS(cert tls.Certificate, roots *x509.CertPool, own, expected Scope, serverName string, server bool) (*tls.Config, error) {
	if own.Validate() != nil || expected.Validate() != nil || roots == nil || len(cert.Certificate) == 0 || (!server && serverName == "") {
		return nil, ErrInvalidIdentity
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, ErrInvalidIdentity
	}
	self, _, err := scopedCertificate(leaf)
	if err != nil || self != own {
		return nil, ErrInvalidIdentity
	}
	result := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, RootCAs: roots, ClientCAs: roots, ServerName: serverName}
	if server {
		result.ClientAuth = tls.RequireAndVerifyClientCert
	}
	result.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.VerifiedChains) == 0 || len(state.VerifiedChains[0]) == 0 {
			return errors.New("workload scope denied")
		}
		got, _, err := scopedCertificate(state.VerifiedChains[0][0])
		if err != nil || got != expected {
			return errors.New("workload scope denied")
		}
		return nil
	}
	return result, nil
}
