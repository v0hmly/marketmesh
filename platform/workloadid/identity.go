package workloadid

import (
	"crypto/x509"
	"fmt"
	"net/url"
	"strings"
)

const (
	// URIScheme — обязательная схема URI SAN идентичности.
	URIScheme = "spiffe"

	maxSegmentLength     = 32
	maxTrustDomainLength = 253
	maxDomainLabelLength = 63
)

// Identity — машинная идентичность рабочей нагрузки: домен доверия, среда
// и роль, опционально конкретный экземпляр. Политики авторизации сопоставляются
// по тройке TrustDomain/Environment/Role без Instance.
type Identity struct {
	TrustDomain string
	Environment string
	Role        string
	Instance    string
}

// String возвращает SPIFFE-совместимый URI идентичности вида
// spiffe://<trust-domain>/<env>/<role>[/<instance>].
func (id Identity) String() string {
	var builder strings.Builder
	builder.WriteString(URIScheme + "://" + id.TrustDomain + "/" + id.Environment + "/" + id.Role)
	if id.Instance != "" {
		builder.WriteString("/" + id.Instance)
	}

	return builder.String()
}

// Validate проверяет, что идентичность заполнена полностью и каждый сегмент
// соответствует формату: домен доверия — строчный DNS-хост из меток
// [a-z0-9-], сегменты env/role/instance — строчные [a-z0-9-] длиной до 32
// без ведущего и завершающего дефиса. Instance может быть пустым.
func (id Identity) Validate() error {
	if !validTrustDomain(id.TrustDomain) {
		return fmt.Errorf("%w: trust domain is outside bounds", ErrInvalidIdentity)
	}
	if !validSegment(id.Environment) {
		return fmt.Errorf("%w: environment is outside bounds", ErrInvalidIdentity)
	}
	if !validSegment(id.Role) {
		return fmt.Errorf("%w: role is outside bounds", ErrInvalidIdentity)
	}
	if id.Instance != "" && !validSegment(id.Instance) {
		return fmt.Errorf("%w: instance is outside bounds", ErrInvalidIdentity)
	}

	return nil
}

// ParseURI разбирает SPIFFE-совместимый URI идентичности. Принимается ровно
// один вид: схема spiffe, непустой домен доверия, путь /<env>/<role> или
// /<env>/<role>/<instance>; userinfo, query, fragment и percent-encoding
// запрещены.
func ParseURI(raw string) (Identity, error) {
	// net/url приводит схему к нижнему регистру, поэтому префикс проверяется
	// в исходной строке: схема обязана быть ровно "spiffe".
	if !strings.HasPrefix(raw, URIScheme+"://") {
		return Identity{}, fmt.Errorf("%w: uri must use the spiffe scheme", ErrInvalidIdentity)
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Scheme != URIScheme {
		return Identity{}, fmt.Errorf("%w: uri must use the spiffe scheme", ErrInvalidIdentity)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.RawPath != "" || parsed.Opaque != "" {
		return Identity{}, fmt.Errorf("%w: uri contains forbidden components", ErrInvalidIdentity)
	}
	if !validTrustDomain(parsed.Host) {
		return Identity{}, fmt.Errorf("%w: trust domain is outside bounds", ErrInvalidIdentity)
	}
	if !strings.HasPrefix(parsed.Path, "/") {
		return Identity{}, fmt.Errorf("%w: uri path is missing", ErrInvalidIdentity)
	}
	segments := strings.Split(parsed.Path[1:], "/")
	if len(segments) != 2 && len(segments) != 3 {
		return Identity{}, fmt.Errorf("%w: uri path must be /<env>/<role>[/<instance>]", ErrInvalidIdentity)
	}
	for _, segment := range segments {
		if !validSegment(segment) {
			return Identity{}, fmt.Errorf("%w: uri path segment is outside bounds", ErrInvalidIdentity)
		}
	}
	identity := Identity{
		TrustDomain: parsed.Host,
		Environment: segments[0],
		Role:        segments[1],
	}
	if len(segments) == 3 {
		identity.Instance = segments[2]
	}

	return identity, nil
}

// IdentityFromCertificate извлекает идентичность из leaf-сертификата:
// сертификат обязан нести ровно один URI SAN в формате ParseURI, иначе отказ.
func IdentityFromCertificate(certificate *x509.Certificate) (Identity, error) {
	if certificate == nil || len(certificate.URIs) != 1 || certificate.URIs[0] == nil {
		return Identity{}, fmt.Errorf("%w: certificate must carry exactly one uri san", ErrInvalidIdentity)
	}

	return ParseURI(certificate.URIs[0].String())
}

// validSegment проверяет сегмент идентичности: 1..32 символа [a-z0-9-]
// без ведущего и завершающего дефиса.
func validSegment(value string) bool {
	if value == "" || len(value) > maxSegmentLength ||
		value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}

	return lowerKebab(value)
}

// validTrustDomain проверяет домен доверия: строчный DNS-хост из меток
// [a-z0-9-] длиной до 63, общая длина до 253, без пустых меток.
func validTrustDomain(value string) bool {
	if value == "" || len(value) > maxTrustDomainLength {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > maxDomainLabelLength ||
			label[0] == '-' || label[len(label)-1] == '-' || !lowerKebab(label) {
			return false
		}
	}

	return true
}

func lowerKebab(value string) bool {
	for _, character := range []byte(value) {
		isLower := character >= 'a' && character <= 'z'
		isDigit := character >= '0' && character <= '9'
		if !isLower && !isDigit && character != '-' {
			return false
		}
	}

	return true
}
