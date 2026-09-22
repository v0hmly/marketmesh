package connectrpc

import (
	"context"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	security "github.com/v0hmly/marketmesh/services/auth/internal/application/security"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/security"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
)

func WithSecurity(service *security.Service) Option {
	return func(h *Handler) error {
		if service == nil || h.sessions == nil {
			return errors.New("auth security: sessions must be configured first")
		}
		h.security = service
		return nil
	}
}

// AuthFailureCode is the closed mapping shared with the private browser adapter.
func AuthFailureCode(reason string) (connect.Code, bool) {
	switch domain.Failure(reason) {
	case domain.InvalidInput, domain.CodeMismatch:
		return connect.CodeInvalidArgument, true
	case domain.InvalidCredentials:
		return connect.CodeUnauthenticated, true
	case domain.LoginLocked, domain.CodeReissued, domain.CodeExpired, domain.TokenExpired, domain.TokenUsed, domain.EmailUnverified, domain.CodeRequired, domain.NewDeviceCooldown:
		return connect.CodeFailedPrecondition, true
	case domain.RateLimited:
		return connect.CodeResourceExhausted, true
	case domain.NotFound:
		return connect.CodeNotFound, true
	default:
		return connect.CodeInternal, false
	}
}
func securityFailure(err error) error {
	var failure domain.Failure
	code := connect.CodeUnavailable
	if errors.Is(err, context.Canceled) {
		code = connect.CodeCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		code = connect.CodeDeadlineExceeded
	}
	result := connect.NewError(code, errors.New("account operation unavailable"))
	if errors.As(err, &failure) {
		if code, ok := AuthFailureCode(string(failure)); ok {
			result = connect.NewError(code, errors.New("account operation rejected"))
			detail, e := connect.NewErrorDetail(&errdetails.ErrorInfo{Domain: "marketmesh.auth", Reason: string(failure)})
			if e == nil {
				result.AddDetail(detail)
			}
		}
	}
	result.Meta().Set("Cache-Control", "no-store")
	return result
}
func securityCall[Request, Response any](h *Handler, ctx context.Context, request *connect.Request[Request], run func(*Request) (*Response, error)) (*connect.Response[Response], error) {
	if h.security == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("account security unavailable"))
	}
	if request == nil || request.Msg == nil {
		return nil, securityFailure(domain.InvalidInput)
	}
	if !h.validOrigin(request.Header()) {
		return nil, securityFailure(domain.InvalidCredentials)
	}
	value, err := run(request.Msg)
	if err != nil {
		return nil, securityFailure(err)
	}
	response := connect.NewResponse(value)
	response.Header().Set("Cache-Control", "no-store")
	return response, nil
}
func (h *Handler) securityActor(ctx context.Context, header http.Header) (session.Record, error) {
	token, err := AccessTokenFromHeader(header)
	if err != nil {
		return session.Record{}, domain.InvalidCredentials
	}
	actor, err := h.sessions.Authenticate(ctx, token)
	if errors.Is(err, session.ErrInvalidSession) {
		return session.Record{}, domain.InvalidCredentials
	}
	return actor, err
}

func (h *Handler) StartLogin(ctx context.Context, r *connect.Request[authv1.StartLoginRequest]) (*connect.Response[authv1.StartLoginResponse], error) {
	var login security.LoginResult
	response, err := securityCall(h, ctx, r, func(m *authv1.StartLoginRequest) (*authv1.StartLoginResponse, error) {
		defer clear(m.Password)
		var err error
		login, err = h.security.Login(ctx, m.Identifier, m.Password, true)
		if err != nil {
			return nil, err
		}
		if login.Tokens.Record.ID != (session.ID{}) {
			return &authv1.StartLoginResponse{SubjectId: login.Tokens.Record.SubjectID.Bytes()}, nil
		}
		return &authv1.StartLoginResponse{LoginChallengeId: login.ChallengeID[:], CodeExpiresInSeconds: int64(login.ExpiresIn / time.Second)}, nil
	})
	if err == nil && login.Tokens.Record.ID != (session.ID{}) {
		h.setCookies(response.Header(), login.Tokens)
	}
	return response, err
}
func (h *Handler) CompleteLogin(ctx context.Context, r *connect.Request[authv1.CompleteLoginRequest]) (*connect.Response[authv1.CompleteLoginResponse], error) {
	var header http.Header
	response, err := securityCall(h, ctx, r, func(m *authv1.CompleteLoginRequest) (*authv1.CompleteLoginResponse, error) {
		if len(m.LoginChallengeId) != 16 {
			return nil, domain.CodeExpired
		}
		var id domain.ID
		copy(id[:], m.LoginChallengeId)
		tokens, err := h.security.CompleteLogin(ctx, id, m.Code)
		if err != nil {
			return nil, err
		}
		header = make(http.Header)
		h.setCookies(header, tokens)
		return &authv1.CompleteLoginResponse{SubjectId: tokens.Record.SubjectID.Bytes()}, nil
	})
	if err == nil {
		for _, cookie := range header.Values("Set-Cookie") {
			response.Header().Add("Set-Cookie", cookie)
		}
	}
	return response, err
}
func (h *Handler) ResendLoginCode(ctx context.Context, r *connect.Request[authv1.ResendLoginCodeRequest]) (*connect.Response[authv1.ResendLoginCodeResponse], error) {
	return securityCall(h, ctx, r, func(m *authv1.ResendLoginCodeRequest) (*authv1.ResendLoginCodeResponse, error) {
		if len(m.LoginChallengeId) != 16 {
			return nil, domain.CodeExpired
		}
		var id domain.ID
		copy(id[:], m.LoginChallengeId)
		ttl, err := h.security.ResendCode(ctx, id)
		return &authv1.ResendLoginCodeResponse{CodeExpiresInSeconds: int64(ttl / time.Second)}, err
	})
}
func (h *Handler) RequestEmailVerification(ctx context.Context, r *connect.Request[authv1.RequestEmailVerificationRequest]) (*connect.Response[authv1.RequestEmailVerificationResponse], error) {
	return securityCall(h, ctx, r, func(m *authv1.RequestEmailVerificationRequest) (*authv1.RequestEmailVerificationResponse, error) {
		return &authv1.RequestEmailVerificationResponse{}, h.security.RequestToken(ctx, m.Email, domain.VerifyEmail)
	})
}
func (h *Handler) ConfirmEmail(ctx context.Context, r *connect.Request[authv1.ConfirmEmailRequest]) (*connect.Response[authv1.ConfirmEmailResponse], error) {
	return securityCall(h, ctx, r, func(m *authv1.ConfirmEmailRequest) (*authv1.ConfirmEmailResponse, error) {
		return &authv1.ConfirmEmailResponse{}, h.security.ConfirmEmail(ctx, m.Token)
	})
}
func (h *Handler) RequestPasswordReset(ctx context.Context, r *connect.Request[authv1.RequestPasswordResetRequest]) (*connect.Response[authv1.RequestPasswordResetResponse], error) {
	return securityCall(h, ctx, r, func(m *authv1.RequestPasswordResetRequest) (*authv1.RequestPasswordResetResponse, error) {
		return &authv1.RequestPasswordResetResponse{}, h.security.RequestToken(ctx, m.Email, domain.ResetPassword)
	})
}
func (h *Handler) ConfirmPasswordReset(ctx context.Context, r *connect.Request[authv1.ConfirmPasswordResetRequest]) (*connect.Response[authv1.ConfirmPasswordResetResponse], error) {
	response, err := securityCall(h, ctx, r, func(m *authv1.ConfirmPasswordResetRequest) (*authv1.ConfirmPasswordResetResponse, error) {
		defer clear(m.NewPassword)
		return &authv1.ConfirmPasswordResetResponse{}, h.security.ResetPassword(ctx, m.Token, m.NewPassword)
	})
	if err == nil {
		clearCookies(response.Header())
	}
	return response, err
}
func (h *Handler) GetCredentials(ctx context.Context, r *connect.Request[authv1.GetCredentialsRequest]) (*connect.Response[authv1.GetCredentialsResponse], error) {
	return securityCall(h, ctx, r, func(*authv1.GetCredentialsRequest) (*authv1.GetCredentialsResponse, error) {
		actor, err := h.securityActor(ctx, r.Header())
		if err != nil {
			return nil, err
		}
		account, err := h.security.Credentials(ctx, actor)
		if err != nil {
			return nil, err
		}
		var cooldown int64
		if until := actor.CreatedAt.Add(24 * time.Hour); h.clock().Before(until) {
			cooldown = until.Unix()
		}
		return &authv1.GetCredentialsResponse{Email: account.Email, EmailVerified: account.Verified, LoginCodeEnabled: account.CodeEnabled, NewDeviceCooldownUntilUnix: cooldown}, nil
	})
}
func (h *Handler) StartEmailChange(ctx context.Context, r *connect.Request[authv1.StartEmailChangeRequest]) (*connect.Response[authv1.StartEmailChangeResponse], error) {
	return securityCall(h, ctx, r, func(m *authv1.StartEmailChangeRequest) (*authv1.StartEmailChangeResponse, error) {
		actor, err := h.securityActor(ctx, r.Header())
		if err != nil {
			return nil, err
		}
		defer clear(m.Password)
		return &authv1.StartEmailChangeResponse{}, h.security.StartEmailChange(ctx, actor, m.NewEmail, m.Password)
	})
}
func (h *Handler) ConfirmEmailChange(ctx context.Context, r *connect.Request[authv1.ConfirmEmailChangeRequest]) (*connect.Response[authv1.ConfirmEmailChangeResponse], error) {
	response, err := securityCall(h, ctx, r, func(m *authv1.ConfirmEmailChangeRequest) (*authv1.ConfirmEmailChangeResponse, error) {
		return &authv1.ConfirmEmailChangeResponse{}, h.security.ConfirmEmailChange(ctx, m.Token)
	})
	if err == nil {
		clearCookies(response.Header())
	}
	return response, err
}
func (h *Handler) CancelEmailChange(ctx context.Context, r *connect.Request[authv1.CancelEmailChangeRequest]) (*connect.Response[authv1.CancelEmailChangeResponse], error) {
	return securityCall(h, ctx, r, func(m *authv1.CancelEmailChangeRequest) (*authv1.CancelEmailChangeResponse, error) {
		return &authv1.CancelEmailChangeResponse{}, h.security.CancelEmailChange(ctx, m.Token)
	})
}
func (h *Handler) ListSessions(ctx context.Context, r *connect.Request[authv1.ListSessionsRequest]) (*connect.Response[authv1.ListSessionsResponse], error) {
	return securityCall(h, ctx, r, func(*authv1.ListSessionsRequest) (*authv1.ListSessionsResponse, error) {
		actor, err := h.securityActor(ctx, r.Header())
		if err != nil {
			return nil, err
		}
		records, err := h.security.ListSessions(ctx, actor)
		if err != nil {
			return nil, err
		}
		result := &authv1.ListSessionsResponse{}
		for _, record := range records {
			info := &authv1.SessionInfo{SessionId: record.ID.Bytes(), Device: "Браузерная сессия", CreatedAtUnix: record.CreatedAt.Unix(), Current: record.ID == actor.ID}
			if info.Current {
				result.Sessions = append([]*authv1.SessionInfo{info}, result.Sessions...)
			} else {
				result.Sessions = append(result.Sessions, info)
			}
		}
		return result, nil
	})
}
func (h *Handler) RevokeSession(ctx context.Context, r *connect.Request[authv1.RevokeSessionRequest]) (*connect.Response[authv1.RevokeSessionResponse], error) {
	current := false
	response, err := securityCall(h, ctx, r, func(m *authv1.RevokeSessionRequest) (*authv1.RevokeSessionResponse, error) {
		actor, err := h.securityActor(ctx, r.Header())
		if err != nil {
			return nil, err
		}
		if len(m.SessionId) != 16 {
			return nil, domain.InvalidInput
		}
		var id session.ID
		copy(id[:], m.SessionId)
		current = id == actor.ID
		return &authv1.RevokeSessionResponse{}, h.security.RevokeSession(ctx, actor, id)
	})
	if err == nil && current {
		clearCookies(response.Header())
	}
	return response, err
}
func (h *Handler) ChangePassword(ctx context.Context, r *connect.Request[authv1.ChangePasswordRequest]) (*connect.Response[authv1.ChangePasswordResponse], error) {
	response, err := securityCall(h, ctx, r, func(m *authv1.ChangePasswordRequest) (*authv1.ChangePasswordResponse, error) {
		defer clear(m.CurrentPassword)
		defer clear(m.NewPassword)
		actor, err := h.securityActor(ctx, r.Header())
		if err != nil {
			return nil, err
		}
		return &authv1.ChangePasswordResponse{}, h.security.ChangePassword(ctx, actor, m.CurrentPassword, m.NewPassword)
	})
	if err == nil {
		clearCookies(response.Header())
	}
	return response, err
}
func (h *Handler) StartLoginCodeChange(ctx context.Context, r *connect.Request[authv1.StartLoginCodeChangeRequest]) (*connect.Response[authv1.StartLoginCodeChangeResponse], error) {
	return securityCall(h, ctx, r, func(m *authv1.StartLoginCodeChangeRequest) (*authv1.StartLoginCodeChangeResponse, error) {
		defer clear(m.Password)
		actor, err := h.securityActor(ctx, r.Header())
		if err != nil {
			return nil, err
		}
		pending, err := h.security.StartCodeChange(ctx, actor, m.Password, m.Enabled)
		return &authv1.StartLoginCodeChangeResponse{ChallengeId: pending.ChallengeID[:], CodeExpiresInSeconds: int64(pending.ExpiresIn / time.Second)}, err
	})
}
func (h *Handler) CompleteLoginCodeChange(ctx context.Context, r *connect.Request[authv1.CompleteLoginCodeChangeRequest]) (*connect.Response[authv1.CompleteLoginCodeChangeResponse], error) {
	response, err := securityCall(h, ctx, r, func(m *authv1.CompleteLoginCodeChangeRequest) (*authv1.CompleteLoginCodeChangeResponse, error) {
		actor, err := h.securityActor(ctx, r.Header())
		if err != nil {
			return nil, err
		}
		if len(m.ChallengeId) != 16 {
			return nil, domain.CodeExpired
		}
		var id domain.ID
		copy(id[:], m.ChallengeId)
		return &authv1.CompleteLoginCodeChangeResponse{}, h.security.CompleteCodeChange(ctx, actor, id, m.Code, m.Enabled)
	})
	if err == nil {
		clearCookies(response.Header())
	}
	return response, err
}
