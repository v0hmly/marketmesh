// Package connectrpc exposes Auth application use cases over Connect, gRPC, and gRPC-Web.
package connectrpc

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	"github.com/v0hmly/marketmesh/api/gen/go/auth/v1/authv1connect"
	"github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/login"
	applicationsession "github.com/v0hmly/marketmesh/services/auth/internal/application/session"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	domainsession "github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
)

const (
	invalidInputMessage = "invalid credential input"
	// #nosec G101 -- This is the deliberately generic public authentication failure, not a secret.
	invalidCredentialsMessage = "invalid credentials"
	internalErrorMessage      = "internal error"
	accessCookieName          = "__Host-mm-access"
	refreshCookieName         = "__Host-mm-refresh"
	maxCookieHeaderBytes      = 8192
)

// Registration executes credential registration.
type Registration interface {
	Execute(ctx context.Context, identifier string, password []byte) error
}

// Verification executes credential verification.
type Verification interface {
	Execute(ctx context.Context, identifier string, password []byte) (credential.SubjectID, error)
}

// SessionLifecycle is the narrow session use-case surface required by the public handler.
type SessionLifecycle interface {
	Start(context.Context, credential.SubjectID) (applicationsession.Tokens, error)
	Refresh(context.Context, string) (applicationsession.Tokens, error)
	Authenticate(context.Context, string) (domainsession.Record, error)
	Revoke(context.Context, string) error
	RevokeAll(context.Context, string) error
}

// SessionConfig configures browser-session handling. AllowedOrigins is an exact HTTPS allowlist.
type SessionConfig struct {
	AllowedOrigins []string
	Clock          func() time.Time
}

// Option configures a Handler.
type Option func(*Handler) error

// WithSessions enables secure cookie session endpoints.
func WithSessions(service SessionLifecycle, config SessionConfig) Option {
	return func(handler *Handler) error {
		if service == nil {
			return errors.New("auth connect: session service must not be nil")
		}
		origins, err := normalizeOrigins(config.AllowedOrigins)
		if err != nil {
			return err
		}
		if config.Clock == nil {
			config.Clock = time.Now
		}
		handler.sessions = service
		handler.origins = origins
		handler.clock = config.Clock
		return nil
	}
}

// Handler maps transport DTOs and sanitizes every outward error.
type Handler struct {
	registration Registration
	verification Verification
	log          *logger.Logger
	sessions     SessionLifecycle
	origins      map[string]struct{}
	clock        func() time.Time
}

// New constructs an Auth Connect handler.
func New(registration Registration, verification Verification, log *logger.Logger, options ...Option) (*Handler, error) {
	if registration == nil || verification == nil || log == nil {
		return nil, errors.New("auth connect: dependencies must not be nil")
	}
	handler := &Handler{registration: registration, verification: verification, log: log}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("auth connect: option must not be nil")
		}
		if err := option(handler); err != nil {
			return nil, err
		}
	}
	return handler, nil
}

// RegisterCredentials maps and executes a registration request.
func (handler *Handler) RegisterCredentials(
	ctx context.Context,
	request *connect.Request[authv1.RegisterCredentialsRequest],
) (*connect.Response[authv1.RegisterCredentialsResponse], error) {
	if request == nil || request.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(invalidInputMessage))
	}
	password := request.Msg.GetPassword()
	defer clear(password)
	if err := handler.registration.Execute(ctx, request.Msg.GetIdentifier(), password); err != nil {
		if isDomainInputError(err) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(invalidInputMessage))
		}
		handler.log.ErrorContext(ctx, "регистрация учётных данных завершилась с ошибкой", logger.Err(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New(internalErrorMessage))
	}

	return connect.NewResponse(&authv1.RegisterCredentialsResponse{}), nil
}

// Login maps and executes a credential verification request.
func (handler *Handler) Login(
	ctx context.Context,
	request *connect.Request[authv1.LoginRequest],
) (*connect.Response[authv1.LoginResponse], error) {
	if request == nil || request.Msg == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New(invalidCredentialsMessage))
	}
	password := request.Msg.GetPassword()
	defer clear(password)
	if handler.sessions != nil && !handler.validOrigin(request.Header()) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New(invalidCredentialsMessage))
	}
	subjectID, err := handler.verification.Execute(ctx, request.Msg.GetIdentifier(), password)
	if errors.Is(err, login.ErrInvalidCredentials) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New(invalidCredentialsMessage))
	}
	if err != nil {
		handler.log.ErrorContext(ctx, "проверка учётных данных завершилась с ошибкой", logger.Err(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New(internalErrorMessage))
	}

	response := connect.NewResponse(&authv1.LoginResponse{SubjectId: subjectID.Bytes()})
	if handler.sessions == nil {
		return response, nil
	}
	tokens, err := handler.sessions.Start(ctx, subjectID)
	if err != nil {
		handler.log.ErrorContext(ctx, "создание сессии завершилось с ошибкой", logger.Err(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New(internalErrorMessage))
	}
	handler.setCookies(response.Header(), tokens)
	return response, nil
}

// RefreshSession rotates browser cookies without accepting tokens in the body.
func (handler *Handler) RefreshSession(ctx context.Context, request *connect.Request[authv1.RefreshSessionRequest]) (*connect.Response[authv1.RefreshSessionResponse], error) {
	if handler.sessions == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("sessions are disabled"))
	}
	if request == nil || request.Msg == nil || !handler.validOrigin(request.Header()) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New(invalidCredentialsMessage))
	}
	refresh, err := cookieValue(request.Header(), refreshCookieName)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New(invalidCredentialsMessage))
	}
	tokens, err := handler.sessions.Refresh(ctx, refresh)
	if err != nil {
		return nil, sessionFailure(ctx, handler.log, err)
	}
	response := connect.NewResponse(&authv1.RefreshSessionResponse{})
	handler.setCookies(response.Header(), tokens)
	return response, nil
}

// Logout revokes the session represented by the browser access cookie.
func (handler *Handler) Logout(ctx context.Context, request *connect.Request[authv1.LogoutRequest]) (*connect.Response[authv1.LogoutResponse], error) {
	if handler.sessions == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("sessions are disabled"))
	}
	if request == nil || request.Msg == nil || !handler.validOrigin(request.Header()) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New(invalidCredentialsMessage))
	}
	access, err := AccessTokenFromHeader(request.Header())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New(invalidCredentialsMessage))
	}
	if err := handler.sessions.Revoke(ctx, access); err != nil {
		return nil, sessionFailure(ctx, handler.log, err)
	}
	response := connect.NewResponse(&authv1.LogoutResponse{})
	clearCookies(response.Header())
	return response, nil
}

// LogoutAll revokes every session for the subject authenticated by the access cookie.
func (handler *Handler) LogoutAll(ctx context.Context, request *connect.Request[authv1.LogoutAllRequest]) (*connect.Response[authv1.LogoutAllResponse], error) {
	if handler.sessions == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("sessions are disabled"))
	}
	if request == nil || request.Msg == nil || !handler.validOrigin(request.Header()) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New(invalidCredentialsMessage))
	}
	access, err := AccessTokenFromHeader(request.Header())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New(invalidCredentialsMessage))
	}
	if err := handler.sessions.RevokeAll(ctx, access); err != nil {
		return nil, sessionFailure(ctx, handler.log, err)
	}
	response := connect.NewResponse(&authv1.LogoutAllResponse{})
	clearCookies(response.Header())
	return response, nil
}

func sessionFailure(ctx context.Context, log *logger.Logger, err error) error {
	if errors.Is(err, domainsession.ErrInvalidSession) || errors.Is(err, domainsession.ErrRefreshReuse) {
		return connect.NewError(connect.CodeUnauthenticated, errors.New(invalidCredentialsMessage))
	}
	log.ErrorContext(ctx, "операция сессии завершилась с ошибкой", logger.Err(err))
	return connect.NewError(connect.CodeInternal, errors.New(internalErrorMessage))
}

func (handler *Handler) setCookies(header http.Header, tokens applicationsession.Tokens) {
	header.Set("Cache-Control", "no-store")
	header.Add("Set-Cookie", secureCookie(accessCookieName, tokens.Access.Reveal(), tokens.Record.AccessExpiresAt).String())
	header.Add("Set-Cookie", secureCookie(refreshCookieName, tokens.Refresh.Reveal(), tokens.Record.RefreshExpiresAt).String())
}
func secureCookie(name, value string, expiry time.Time) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expiry.UTC()}
}
func clearCookies(header http.Header) {
	header.Set("Cache-Control", "no-store")
	for _, name := range []string{accessCookieName, refreshCookieName} {
		cookie := secureCookie(name, "", time.Unix(1, 0))
		cookie.MaxAge = -1
		header.Add("Set-Cookie", cookie.String())
	}
}

// AccessTokenFromHeader reads exactly one bounded access cookie from an HTTP header.
func AccessTokenFromHeader(header http.Header) (string, error) {
	return cookieValue(header, accessCookieName)
}
func cookieValue(header http.Header, name string) (string, error) {
	lines := header.Values("Cookie")
	total := 0
	found := ""
	for _, line := range lines {
		total += len(line)
		if total > maxCookieHeaderBytes {
			return "", errors.New("cookie header too large")
		}
		cookies, err := http.ParseCookie(line)
		if err != nil {
			return "", err
		}
		for _, cookie := range cookies {
			if cookie.Name == name {
				if found != "" || cookie.Value == "" {
					return "", errors.New("duplicate or empty cookie")
				}
				found = cookie.Value
			}
		}
	}
	if found == "" {
		return "", errors.New("missing cookie")
	}
	return found, nil
}
func (handler *Handler) validOrigin(header http.Header) bool {
	if len(header.Values("Origin")) != 1 || strings.EqualFold(strings.TrimSpace(header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}
	origin := strings.TrimSpace(header.Get("Origin"))
	if origin == "" || origin == "null" {
		return false
	}
	_, ok := handler.origins[origin]
	return ok
}
func normalizeOrigins(values []string) (map[string]struct{}, error) {
	if len(values) == 0 {
		return nil, errors.New("auth connect: allowed origins are required")
	}
	result := make(map[string]struct{}, len(values))
	for _, raw := range values {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil || parsed.String() != raw {
			return nil, errors.New("auth connect: allowed origin must be exact HTTPS origin")
		}
		if _, duplicate := result[raw]; duplicate {
			return nil, errors.New("auth connect: duplicate allowed origin")
		}
		result[raw] = struct{}{}
	}
	return result, nil
}

func isDomainInputError(err error) bool {
	return errors.Is(err, credential.ErrInvalidIdentifier) || errors.Is(err, credential.ErrInvalidPassword)
}

var _ authv1connect.AuthServiceHandler = (*Handler)(nil)
