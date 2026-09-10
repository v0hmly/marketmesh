// Package internalgrpc exposes session assertions solely on Auth's mTLS gRPC listener.
package internalgrpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	"github.com/v0hmly/marketmesh/platform/sessionassert"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	public "github.com/v0hmly/marketmesh/services/auth/internal/adapter/in/connectrpc"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/sessionkeys"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const serviceName = "auth.v1.AuthInternalService"

// SessionOperations is the private session surface. It never accepts browser requests directly.
type SessionOperations interface {
	Exchange(context.Context, string, string) (string, time.Time, error)
	Check(context.Context, domain.ID, credential.SubjectID, time.Time) error
}

// Config is server-owned policy for the private assertion service.
type Config struct {
	TrustDomain  string
	Environment  string
	Issuer       string
	AssertionTTL time.Duration
	Clock        func() time.Time
	Audiences    map[string][]string
}

// Handler serves the private mTLS-only AuthInternalService.
type Handler struct {
	authv1.UnimplementedAuthInternalServiceServer
	service SessionOperations
	keys    *sessionkeys.Manager
	config  Config
	policy  *workloadid.Policy
}

// New constructs a fail-closed private handler and its direct-call authorization policy.
func New(service SessionOperations, keys *sessionkeys.Manager, config Config) (*Handler, error) {
	if service == nil || keys == nil || config.AssertionTTL < time.Second || config.AssertionTTL > 5*time.Minute || config.AssertionTTL%time.Second != 0 || config.Issuer == "" || len(config.Audiences) == 0 {
		return nil, errors.New("auth internal grpc: required dependencies or configuration missing")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	base := workloadid.Identity{TrustDomain: config.TrustDomain, Environment: config.Environment, Role: "gateway-out"}
	if err := base.Validate(); err != nil {
		return nil, errors.New("auth internal grpc: invalid workload domain or environment")
	}
	audiences := make(map[string][]string, len(config.Audiences))
	rules := map[workloadid.Identity][]string{base: {method("ExchangeSession"), method("GetSigningKeys")}}
	rules[base] = append(rules[base], browserMethods()...)
	for audience, scopes := range config.Audiences {
		identity := workloadid.Identity{TrustDomain: config.TrustDomain, Environment: config.Environment, Role: audience}
		if err := identity.Validate(); err != nil {
			return nil, errors.New("auth internal grpc: invalid audience configuration")
		}
		if existing, found := rules[identity]; found {
			rules[identity] = append(existing, method("VerifyAssertion"), method("GetSigningKeys"))
		} else {
			rules[identity] = []string{method("VerifyAssertion"), method("GetSigningKeys")}
		}
		audiences[audience] = append([]string(nil), scopes...)
	}
	policy, err := workloadid.NewPolicy(rules)
	if err != nil {
		return nil, errors.New("auth internal grpc: invalid authorization policy")
	}
	config.Audiences = audiences
	return &Handler{service: service, keys: keys, config: config, policy: policy}, nil
}

// Policy returns the same policy used by direct handler authorization and gRPC interceptors.
func (h *Handler) Policy() *workloadid.Policy { return h.policy }

func (h *Handler) ExchangeSession(ctx context.Context, request *authv1.ExchangeSessionRequest) (*authv1.ExchangeSessionResponse, error) {
	identity, err := h.authorize(ctx, method("ExchangeSession"))
	if err != nil {
		return nil, err
	}
	if identity.Role != "gateway-out" || request == nil || request.GetCookie() == "" {
		return nil, denied()
	}
	audience := request.GetAudience()
	if _, ok := h.config.Audiences[audience]; !ok {
		return nil, denied()
	}
	access, err := public.AccessTokenFromHeader(http.Header{"Cookie": {request.GetCookie()}})
	if err != nil {
		return nil, denied()
	}
	assertion, expiry, err := h.service.Exchange(ctx, access, audience)
	if err != nil {
		return nil, operationFailure(err)
	}
	return &authv1.ExchangeSessionResponse{Assertion: assertion, ExpiresAtUnix: expiry.Unix()}, nil
}

func (h *Handler) VerifyAssertion(ctx context.Context, request *authv1.VerifyAssertionRequest) (*authv1.VerifyAssertionResponse, error) {
	identity, err := h.authorize(ctx, method("VerifyAssertion"))
	if err != nil {
		return nil, err
	}
	if request == nil || request.GetAssertion() == "" {
		return nil, denied()
	}
	if _, ok := h.config.Audiences[identity.Role]; !ok {
		return nil, denied()
	}
	verifier, err := sessionassert.NewVerifier(h.config.Issuer, identity.Role, h.keys.KeySource(), sessionassert.WithLeeway(0), sessionassert.WithVerifierClock(h.config.Clock), sessionassert.WithVerifierMaxTTL(h.config.AssertionTTL), sessionassert.WithSessionChecker(sessionChecker{service: h.service}))
	if err != nil {
		return nil, unavailable()
	}
	claims, err := verifier.VerifyContext(ctx, request.GetAssertion())
	if err != nil {
		return nil, denied()
	}
	subject, err := base64.RawURLEncoding.Strict().DecodeString(claims.Subject)
	if err != nil || len(subject) != len(credential.SubjectID{}) {
		return nil, denied()
	}
	return &authv1.VerifyAssertionResponse{SubjectId: subject, SessionId: claims.SessionID, ExpiresAtUnix: claims.ExpiresAt.Unix()}, nil
}

func (h *Handler) GetSigningKeys(ctx context.Context, request *authv1.GetSigningKeysRequest) (*authv1.GetSigningKeysResponse, error) {
	if _, err := h.authorize(ctx, method("GetSigningKeys")); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, denied()
	}
	keys, err := h.keys.PublicKeys()
	if err != nil {
		return nil, unavailable()
	}
	payload, err := json.Marshal(struct {
		Keys []sessionkeys.PublicKey `json:"keys"`
	}{Keys: keys})
	if err != nil {
		return nil, unavailable()
	}
	return &authv1.GetSigningKeysResponse{JwksJson: string(payload)}, nil
}

func (h *Handler) authorize(ctx context.Context, fullMethod string) (workloadid.Identity, error) {
	identity, _, err := workloadid.FromContext(ctx)
	if err != nil || !h.policy.Allow(identity, fullMethod) {
		return workloadid.Identity{}, denied()
	}
	return identity, nil
}
func method(name string) string { return "/" + serviceName + "/" + name }
func denied() error             { return status.Error(codes.Unauthenticated, "authentication failed") }
func unavailable() error        { return status.Error(codes.Unavailable, "service unavailable") }
func operationFailure(err error) error {
	if errors.Is(err, domain.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return unavailable()
	}
	return denied()
}

type sessionChecker struct{ service SessionOperations }

func (c sessionChecker) CheckSession(ctx context.Context, claims sessionassert.Claims) error {
	id, err := domain.ParseID(claims.SessionID)
	if err != nil {
		return err
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(claims.Subject)
	if err != nil || len(raw) != len(credential.SubjectID{}) {
		return domain.ErrInvalidSession
	}
	var subject credential.SubjectID
	copy(subject[:], raw)
	return c.service.Check(ctx, id, subject, claims.AuthTime)
}

var _ authv1.AuthInternalServiceServer = (*Handler)(nil)
