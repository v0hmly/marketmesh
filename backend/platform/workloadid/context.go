package workloadid

import (
	"bytes"
	"context"
	"crypto/x509"
	"fmt"
	"slices"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// VerifyOption настраивает проверку идентичности пира в FromContext.
type VerifyOption func(*verifyConfig)

type verifyConfig struct {
	peerExtKeyUsage x509.ExtKeyUsage
}

// WithPeerExtKeyUsage задаёт требуемое расширенное назначение сертификата
// пира. По умолчанию FromContext требует x509.ExtKeyUsageClientAuth (сервер
// проверяет клиента); для клиентской проверки сервера передайте
// x509.ExtKeyUsageServerAuth.
func WithPeerExtKeyUsage(usage x509.ExtKeyUsage) VerifyOption {
	return func(cfg *verifyConfig) { cfg.peerExtKeyUsage = usage }
}

// FromContext извлекает машинную идентичность пира из контекста gRPC-вызова.
// Проверяется: наличие peer с TLS-состоянием, непустая верифицированная
// цепочка, совпадение leaf PeerCertificates[0] с верифицированным leaf
// VerifiedChains[0][0] по сырым байтам, требуемое расширенное назначение
// (по умолчанию ClientAuth) и срок действия на момент вызова. При успехе
// возвращаются идентичность и leaf-сертификат (для отзыва и наблюдаемости);
// при отказе — ошибка семейства ErrUnauthenticated.
func FromContext(ctx context.Context, opts ...VerifyOption) (Identity, *x509.Certificate, error) {
	cfg := verifyConfig{peerExtKeyUsage: x509.ExtKeyUsageClientAuth}
	for _, opt := range opts {
		opt(&cfg)
	}

	transportPeer, found := peer.FromContext(ctx)
	if !found || transportPeer == nil || transportPeer.AuthInfo == nil {
		return Identity{}, nil, fmt.Errorf("%w: missing peer credentials", ErrUnauthenticated)
	}
	tlsInfo, validTLS := transportPeer.AuthInfo.(credentials.TLSInfo)
	if !validTLS {
		return Identity{}, nil, fmt.Errorf("%w: peer credentials are not tls", ErrUnauthenticated)
	}
	if len(tlsInfo.State.VerifiedChains) == 0 ||
		len(tlsInfo.State.VerifiedChains[0]) == 0 ||
		len(tlsInfo.State.PeerCertificates) == 0 {
		return Identity{}, nil, fmt.Errorf("%w: incomplete verified chain", ErrUnauthenticated)
	}

	leaf := tlsInfo.State.PeerCertificates[0]
	verifiedLeaf := tlsInfo.State.VerifiedChains[0][0]
	if leaf == nil || verifiedLeaf == nil || !bytes.Equal(leaf.Raw, verifiedLeaf.Raw) {
		return Identity{}, nil, fmt.Errorf("%w: leaf does not match verified chain", ErrUnauthenticated)
	}
	if !slices.Contains(leaf.ExtKeyUsage, cfg.peerExtKeyUsage) {
		return Identity{}, nil, fmt.Errorf("%w: unexpected extended key usage", ErrUnauthenticated)
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return Identity{}, nil, fmt.Errorf("%w: certificate is not valid at this time", ErrUnauthenticated)
	}
	identity, err := IdentityFromCertificate(leaf)
	if err != nil {
		return Identity{}, nil, fmt.Errorf("%w: peer identity is not a valid workload identity", ErrUnauthenticated)
	}

	return identity, leaf, nil
}

// ExpiryInfo возвращает оставшийся срок действия сертификата и признак того,
// что сертификат недействителен на текущий момент (ещё не наступил или истёк).
// Отрицательный остаток обрезается до нуля. Предназначено для метрик
// оставшегося срока по ADR-0004.
func ExpiryInfo(certificate *x509.Certificate) (remaining time.Duration, expired bool) {
	return ExpiryInfoAt(certificate, time.Now())
}

// ExpiryInfoAt — вариант ExpiryInfo с явным моментом времени.
func ExpiryInfoAt(certificate *x509.Certificate, now time.Time) (remaining time.Duration, expired bool) {
	if certificate == nil {
		return 0, true
	}
	expired = now.Before(certificate.NotBefore) || now.After(certificate.NotAfter)
	remaining = certificate.NotAfter.Sub(now)
	if remaining < 0 {
		remaining = 0
	}

	return remaining, expired
}
