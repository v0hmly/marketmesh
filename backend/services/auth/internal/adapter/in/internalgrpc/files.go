package internalgrpc

import (
	"context"
	"errors"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	"github.com/v0hmly/marketmesh/platform/workloadid"
)

// ScopedFiles exposes only verification and public keys, never browser sessions.
type ScopedFiles struct {
	authv1.UnimplementedAuthInternalServiceServer
	handler  *Handler
	expected workloadid.Scope
}

func NewScopedFiles(handler *Handler, expected workloadid.Scope) (*ScopedFiles, error) {
	if handler == nil || expected.Validate() != nil || expected.ServiceAccount != "files" || expected.TrustDomain != handler.config.TrustDomain || expected.Environment != handler.config.Environment || len(handler.config.Audiences["files"]) == 0 {
		return nil, errors.New("auth files: invalid workload scope or audience")
	}
	return &ScopedFiles{handler: handler, expected: expected}, nil
}
func (h *ScopedFiles) allowed(ctx context.Context) bool {
	got, _, err := workloadid.ScopedFromContext(ctx)
	return err == nil && got == h.expected
}
func (h *ScopedFiles) VerifyAssertion(ctx context.Context, request *authv1.VerifyAssertionRequest) (*authv1.VerifyAssertionResponse, error) {
	if !h.allowed(ctx) {
		return nil, denied()
	}
	return h.handler.verifyAssertion(ctx, request, "files")
}
func (h *ScopedFiles) GetSigningKeys(ctx context.Context, request *authv1.GetSigningKeysRequest) (*authv1.GetSigningKeysResponse, error) {
	if !h.allowed(ctx) || request == nil {
		return nil, denied()
	}
	return h.handler.signingKeys()
}
