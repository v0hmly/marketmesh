package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/url"
	"strings"

	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/security"
)

func newID() (domain.ID, error) {
	var id domain.ID
	if _, err := rand.Read(id[:]); err != nil {
		return domain.ID{}, domain.Unavailable
	}
	return id, nil
}
func newSecret() (string, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", domain.Unavailable
	}
	defer clear(secret[:])
	return base64.RawURLEncoding.EncodeToString(secret[:]), nil
}
func newCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", domain.Unavailable
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
func (s *Service) digest(purpose string, id domain.ID, value string) domain.Digest {
	mac := hmac.New(sha256.New, s.key[:])
	_, _ = mac.Write([]byte(purpose))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(id[:])
	_, _ = mac.Write([]byte(value))
	var result domain.Digest
	copy(result[:], mac.Sum(nil))
	return result
}
func token(id domain.ID, secret string) string { return hex.EncodeToString(id[:]) + "." + secret }
func parseToken(value string) (domain.ID, string, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || len(parts[0]) != 32 || len(parts[1]) != 43 {
		return domain.ID{}, "", domain.TokenExpired
	}
	decoded, err := hex.DecodeString(parts[0])
	if err != nil {
		return domain.ID{}, "", domain.TokenExpired
	}
	secret, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(secret) != 32 {
		return domain.ID{}, "", domain.TokenExpired
	}
	var id domain.ID
	copy(id[:], decoded)
	return id, parts[1], nil
}
func (s *Service) link(action, secret string) string {
	// Fragment keeps the bearer token out of HTTP/access logs and Referer.
	return s.origin + "/account/security/" + action + "#token=" + url.QueryEscape(secret)
}
