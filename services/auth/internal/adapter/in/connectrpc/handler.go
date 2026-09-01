// Package connectrpc exposes Auth application use cases over Connect, gRPC, and gRPC-Web.
package connectrpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	"github.com/v0hmly/marketmesh/api/gen/go/auth/v1/authv1connect"
	"github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/login"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
)

const (
	invalidInputMessage = "invalid credential input"
	// #nosec G101 -- This is the deliberately generic public authentication failure, not a secret.
	invalidCredentialsMessage = "invalid credentials"
	internalErrorMessage      = "internal error"
)

// Registration executes credential registration.
type Registration interface {
	Execute(ctx context.Context, identifier string, password []byte) error
}

// Verification executes credential verification.
type Verification interface {
	Execute(ctx context.Context, identifier string, password []byte) (credential.SubjectID, error)
}

// Handler maps transport DTOs and sanitizes every outward error.
type Handler struct {
	registration Registration
	verification Verification
	log          *logger.Logger
}

// New constructs an Auth Connect handler.
func New(registration Registration, verification Verification, log *logger.Logger) (*Handler, error) {
	if registration == nil || verification == nil || log == nil {
		return nil, errors.New("auth connect: dependencies must not be nil")
	}
	return &Handler{registration: registration, verification: verification, log: log}, nil
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
	subjectID, err := handler.verification.Execute(ctx, request.Msg.GetIdentifier(), password)
	if errors.Is(err, login.ErrInvalidCredentials) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New(invalidCredentialsMessage))
	}
	if err != nil {
		handler.log.ErrorContext(ctx, "проверка учётных данных завершилась с ошибкой", logger.Err(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New(internalErrorMessage))
	}

	return connect.NewResponse(&authv1.LoginResponse{SubjectId: subjectID.Bytes()}), nil
}

func isDomainInputError(err error) bool {
	return errors.Is(err, credential.ErrInvalidIdentifier) || errors.Is(err, credential.ErrInvalidPassword)
}

var _ authv1connect.AuthServiceHandler = (*Handler)(nil)
