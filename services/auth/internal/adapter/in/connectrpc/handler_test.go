package connectrpc_test

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	"github.com/v0hmly/marketmesh/api/gen/go/auth/v1/authv1connect"
	"github.com/v0hmly/marketmesh/platform/logger"
	handleradapter "github.com/v0hmly/marketmesh/services/auth/internal/adapter/in/connectrpc"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/login"
	applicationsession "github.com/v0hmly/marketmesh/services/auth/internal/application/session"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	domainsession "github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
)

func TestHandlerServesConnectContractAndSanitizesAuthenticationFailures(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	handler := newHandler(t, &registrationStub{}, &verificationStub{}, &logs)
	path, httpHandler := authv1connect.NewAuthServiceHandler(handler)
	if path == "" {
		t.Fatal("generated handler path is empty")
	}
	server := httptest.NewServer(httpHandler)
	t.Cleanup(server.Close)
	client := authv1connect.NewAuthServiceClient(server.Client(), server.URL)

	_, err := client.RegisterCredentials(context.Background(), connect.NewRequest(&authv1.RegisterCredentialsRequest{
		Identifier: "user@example.com", Password: []byte("correct horse battery staple"),
	}))
	if err != nil {
		t.Fatalf("RegisterCredentials() error = %v", err)
	}

	errorsByIdentifier := make([]*connect.Error, 0, 2)
	for _, identifier := range []string{"unknown@example.com", "user@example.com"} {
		_, err := client.Login(context.Background(), connect.NewRequest(&authv1.LoginRequest{
			Identifier: identifier, Password: []byte("incorrect password value"),
		}))
		var connectErr *connect.Error
		if !errors.As(err, &connectErr) {
			t.Fatalf("Login(%q) error = %v", identifier, err)
		}
		errorsByIdentifier = append(errorsByIdentifier, connectErr)
	}
	if errorsByIdentifier[0].Code() != connect.CodeUnauthenticated ||
		errorsByIdentifier[0].Message() != errorsByIdentifier[1].Message() ||
		errorsByIdentifier[0].Message() != "invalid credentials" {
		t.Fatalf("authentication errors = %#v, %#v", errorsByIdentifier[0], errorsByIdentifier[1])
	}
	if logs.Len() != 0 {
		t.Fatalf("expected authentication rejections not to be error-logged: %s", logs.String())
	}
}

func TestHandlerReturnsSubjectAndSanitizesInternalErrors(t *testing.T) {
	t.Parallel()

	var subjectID credential.SubjectID
	subjectID[0] = 12
	var logs bytes.Buffer
	verification := &verificationStub{subjectID: subjectID}
	handler := newHandler(t, &registrationStub{}, verification, &logs)

	response, err := handler.Login(context.Background(), connect.NewRequest(&authv1.LoginRequest{
		Identifier: "user@example.com", Password: []byte("correct horse battery staple"),
	}))
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if !bytes.Equal(response.Msg.GetSubjectId(), subjectID.Bytes()) {
		t.Fatalf("Login() subject = %v", response.Msg.GetSubjectId())
	}

	verification.err = errors.New("database unavailable")
	_, err = handler.Login(context.Background(), connect.NewRequest(&authv1.LoginRequest{
		Identifier: "private@example.com", Password: []byte("do-not-log-this-password"),
	}))
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInternal || connectErr.Message() != "internal error" {
		t.Fatalf("Login(internal) error = %v", err)
	}
	for _, forbidden := range []string{"private@example.com", "do-not-log-this-password"} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("logs contain secret or PII %q: %s", forbidden, logs.String())
		}
	}
}

func TestHandlerMapsInvalidRegistrationInput(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	handler := newHandler(t, &registrationStub{err: credential.ErrInvalidPassword}, &verificationStub{}, &logs)
	_, err := handler.RegisterCredentials(context.Background(), connect.NewRequest(&authv1.RegisterCredentialsRequest{
		Identifier: "user@example.com", Password: []byte("short"),
	}))
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument || connectErr.Message() != "invalid credential input" {
		t.Fatalf("RegisterCredentials() error = %v", err)
	}
	if logs.Len() != 0 {
		t.Fatalf("invalid input was error-logged: %s", logs.String())
	}
}

func TestHandlerSanitizesRegistrationInternalErrorAndNilRequests(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	handler := newHandler(t, &registrationStub{err: errors.New("storage unavailable")}, &verificationStub{}, &logs)
	_, err := handler.RegisterCredentials(context.Background(), connect.NewRequest(&authv1.RegisterCredentialsRequest{
		Identifier: "private@example.com", Password: []byte("do-not-log-this-password"),
	}))
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInternal || connectErr.Message() != "internal error" {
		t.Fatalf("RegisterCredentials(internal) error = %v", err)
	}
	for _, forbidden := range []string{"private@example.com", "do-not-log-this-password"} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("logs contain secret or PII %q: %s", forbidden, logs.String())
		}
	}
	if _, err := handler.RegisterCredentials(context.Background(), nil); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("RegisterCredentials(nil) error = %v", err)
	}
	if _, err := handler.Login(context.Background(), nil); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("Login(nil) error = %v", err)
	}
}

func TestNewRejectsNilDependencies(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	log, err := logger.New(logger.Config{Service: "auth", Version: "test", Environment: "test", Output: &logs})
	if err != nil {
		t.Fatalf("logger.New() error = %v", err)
	}
	if _, err := handleradapter.New(nil, &verificationStub{}, log); err == nil {
		t.Fatal("New(nil registration) error = nil")
	}
}

func TestSessionLoginSetsSecureCookiesAndRejectsOriginBeforePassword(t *testing.T) {
	t.Parallel()
	var subject credential.SubjectID
	subject[0] = 1
	verification := &verificationStub{subjectID: subject}
	sessions := newSessionStub(t, subject)
	var output bytes.Buffer
	handler := newSessionHandler(t, &registrationStub{}, verification, sessions, &output)

	denied := connect.NewRequest(&authv1.LoginRequest{Identifier: "user@example.com", Password: []byte("secret")})
	denied.Header().Set("Origin", "https://evil.example")
	if _, err := handler.Login(context.Background(), denied); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("Login(evil origin) error = %v", err)
	}
	if verification.calls != 0 {
		t.Fatalf("verification calls after rejected origin = %d", verification.calls)
	}
	if !bytes.Equal(denied.Msg.Password, make([]byte, len(denied.Msg.Password))) {
		t.Fatal("rejected request retained password")
	}

	request := connect.NewRequest(&authv1.LoginRequest{Identifier: "user@example.com", Password: []byte("secret")})
	request.Header().Set("Origin", "https://app.example")
	response, err := handler.Login(context.Background(), request)
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if got := response.Header().Values("Set-Cookie"); len(got) != 2 || !strings.Contains(got[0], "__Host-mm-access=") || !strings.Contains(got[1], "__Host-mm-refresh=") {
		t.Fatalf("Set-Cookie = %v", got)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("session response permits caching")
	}
	for _, value := range response.Header().Values("Set-Cookie") {
		if !strings.Contains(value, "; Path=/") || !strings.Contains(value, "; HttpOnly") || !strings.Contains(value, "; Secure") || !strings.Contains(value, "SameSite=Strict") || strings.Contains(value, "Domain=") {
			t.Fatalf("unsafe cookie = %q", value)
		}
	}
	if strings.Contains(response.Msg.String(), "mm1.") {
		t.Fatalf("token leaked into response body: %s", response.Msg)
	}
}

func TestSessionRefreshLogoutAndDuplicateCookies(t *testing.T) {
	t.Parallel()
	var subject credential.SubjectID
	subject[0] = 3
	sessions := newSessionStub(t, subject)
	var output bytes.Buffer
	handler := newSessionHandler(t, &registrationStub{}, &verificationStub{subjectID: subject}, sessions, &output)

	refresh := connect.NewRequest(&authv1.RefreshSessionRequest{})
	refresh.Header().Set("Origin", "https://app.example")
	refresh.Header().Set("Cookie", "__Host-mm-refresh=refresh-one")
	response, err := handler.RefreshSession(context.Background(), refresh)
	if err != nil || sessions.refresh != "refresh-one" || len(response.Header().Values("Set-Cookie")) != 2 {
		t.Fatalf("RefreshSession() response=%v err=%v refresh=%q", response, err, sessions.refresh)
	}

	duplicate := connect.NewRequest(&authv1.LogoutRequest{})
	duplicate.Header().Set("Origin", "https://app.example")
	duplicate.Header().Add("Cookie", "__Host-mm-access=one")
	duplicate.Header().Add("Cookie", "__Host-mm-access=two")
	if _, err := handler.Logout(context.Background(), duplicate); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("Logout(duplicate cookie) error = %v", err)
	}
	if sessions.revoke != "" {
		t.Fatalf("Revoke received duplicate token: %q", sessions.revoke)
	}

	logoutAll := connect.NewRequest(&authv1.LogoutAllRequest{})
	logoutAll.Header().Set("Origin", "https://app.example")
	logoutAll.Header().Set("Cookie", "__Host-mm-access=access-one")
	cleared, err := handler.LogoutAll(context.Background(), logoutAll)
	if err != nil || sessions.revokeAll != "access-one" || len(cleared.Header().Values("Set-Cookie")) != 2 {
		t.Fatalf("LogoutAll() response=%v err=%v revokeAll=%q", cleared, err, sessions.revokeAll)
	}
	if response.Header().Get("Cache-Control") != "no-store" || cleared.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("session mutation permits caching")
	}
}

func TestSessionEndpointsRemainUnimplementedWithoutSessionOption(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	handler := newHandler(t, &registrationStub{}, &verificationStub{}, &output)
	if _, err := handler.RefreshSession(context.Background(), connect.NewRequest(&authv1.RefreshSessionRequest{})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("RefreshSession() error = %v", err)
	}
}

type registrationStub struct {
	err error
}

func (stub *registrationStub) Execute(context.Context, string, []byte) error {
	return stub.err
}

type verificationStub struct {
	subjectID credential.SubjectID
	err       error
	calls     int
}

func (stub *verificationStub) Execute(_ context.Context, _ string, _ []byte) (credential.SubjectID, error) {
	stub.calls++
	if stub.err != nil {
		return credential.SubjectID{}, stub.err
	}
	if stub.subjectID == (credential.SubjectID{}) {
		return credential.SubjectID{}, login.ErrInvalidCredentials
	}
	return stub.subjectID, nil
}

type sessionStub struct {
	tokens                     applicationsession.Tokens
	refresh, revoke, revokeAll string
	err                        error
}

func newSessionStub(t *testing.T, subject credential.SubjectID) *sessionStub {
	t.Helper()
	var id domainsession.ID
	id[0] = 7
	access, err := domainsession.NewToken(id, bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := domainsession.NewToken(id, bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return &sessionStub{tokens: applicationsession.Tokens{Record: domainsession.Record{ID: id, SubjectID: subject, Version: 1, CreatedAt: now, AccessExpiresAt: now.Add(time.Minute), RefreshExpiresAt: now.Add(2 * time.Minute), ExpiresAt: now.Add(time.Hour)}, Access: access, Refresh: refresh}}
}
func (stub *sessionStub) Start(context.Context, credential.SubjectID) (applicationsession.Tokens, error) {
	return stub.tokens, stub.err
}
func (stub *sessionStub) Refresh(_ context.Context, value string) (applicationsession.Tokens, error) {
	stub.refresh = value
	return stub.tokens, stub.err
}
func (stub *sessionStub) Authenticate(context.Context, string) (domainsession.Record, error) {
	return stub.tokens.Record, stub.err
}
func (stub *sessionStub) Revoke(_ context.Context, value string) error {
	stub.revoke = value
	return stub.err
}
func (stub *sessionStub) RevokeAll(_ context.Context, value string) error {
	stub.revokeAll = value
	return stub.err
}

func newHandler(t *testing.T, registration handleradapter.Registration, verification handleradapter.Verification, output *bytes.Buffer) *handleradapter.Handler {
	t.Helper()
	log, err := logger.New(logger.Config{Service: "auth", Version: "test", Environment: "test", Output: output})
	if err != nil {
		t.Fatalf("logger.New() error = %v", err)
	}
	handler, err := handleradapter.New(registration, verification, log)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler
}

func newSessionHandler(t *testing.T, registration handleradapter.Registration, verification handleradapter.Verification, sessions handleradapter.SessionLifecycle, output *bytes.Buffer) *handleradapter.Handler {
	t.Helper()
	log, err := logger.New(logger.Config{Service: "auth", Version: "test", Environment: "test", Output: output})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := handleradapter.New(registration, verification, log, handleradapter.WithSessions(sessions, handleradapter.SessionConfig{AllowedOrigins: []string{"https://app.example"}}))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}
