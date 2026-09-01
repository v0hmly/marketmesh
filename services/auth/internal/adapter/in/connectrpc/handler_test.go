package connectrpc_test

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	"github.com/v0hmly/marketmesh/api/gen/go/auth/v1/authv1connect"
	"github.com/v0hmly/marketmesh/platform/logger"
	handleradapter "github.com/v0hmly/marketmesh/services/auth/internal/adapter/in/connectrpc"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/login"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
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

type registrationStub struct {
	err error
}

func (stub *registrationStub) Execute(context.Context, string, []byte) error {
	return stub.err
}

type verificationStub struct {
	subjectID credential.SubjectID
	err       error
}

func (stub *verificationStub) Execute(_ context.Context, _ string, _ []byte) (credential.SubjectID, error) {
	if stub.err != nil {
		return credential.SubjectID{}, stub.err
	}
	if stub.subjectID == (credential.SubjectID{}) {
		return credential.SubjectID{}, login.ErrInvalidCredentials
	}
	return stub.subjectID, nil
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
