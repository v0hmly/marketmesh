package grpc

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrorCodeMapper преобразует прикладную или доменную ошибку в gRPC code.
// Возвращаемый bool сообщает, что ошибка распознана. Текст ошибки никогда не
// передаётся клиенту: пакет использует стабильное публичное сообщение code.
type ErrorCodeMapper func(err error) (codes.Code, bool)

func sanitizedStatusError(err error, mapper ErrorCodeMapper) error {
	if err == nil {
		return nil
	}

	code := errorCode(err, mapper)
	return status.Error(code, publicStatusMessage(code))
}

func errorCode(err error, mapper ErrorCodeMapper) codes.Code {
	switch {
	case errors.Is(err, context.Canceled):
		return codes.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return codes.DeadlineExceeded
	}

	if mapper != nil {
		if code, found := mapper(err); found && code != codes.OK {
			return sanitizeCode(code)
		}
	}

	if grpcStatus, found := status.FromError(err); found {
		return sanitizeCode(grpcStatus.Code())
	}

	return codes.Internal
}

func sanitizeCode(code codes.Code) codes.Code {
	if code == codes.OK || code == codes.Unknown {
		return codes.Internal
	}

	return code
}

func publicStatusMessage(code codes.Code) string {
	switch code {
	case codes.Canceled:
		return "request canceled"
	case codes.InvalidArgument:
		return "invalid argument"
	case codes.DeadlineExceeded:
		return "deadline exceeded"
	case codes.NotFound:
		return "not found"
	case codes.AlreadyExists:
		return "already exists"
	case codes.PermissionDenied:
		return "permission denied"
	case codes.ResourceExhausted:
		return "resource exhausted"
	case codes.FailedPrecondition:
		return "failed precondition"
	case codes.Aborted:
		return "operation aborted"
	case codes.OutOfRange:
		return "out of range"
	case codes.Unimplemented:
		return "not implemented"
	case codes.Unavailable:
		return "service unavailable"
	case codes.Unauthenticated:
		return "unauthenticated"
	default:
		return "internal error"
	}
}
