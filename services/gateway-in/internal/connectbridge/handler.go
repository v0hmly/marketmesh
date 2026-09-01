package connectbridge

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"

	"connectrpc.com/connect"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
	"google.golang.org/protobuf/proto"
)

const idempotencyKeyHeader = "Idempotency-Key"

// Invoker is the smallest tunnel contract consumed by the Connect adapter.
type Invoker interface {
	Invoke(context.Context, tunnel.Call) (tunnel.Response, error)
	RoutePolicy(contractv1.RouteId) (tunnel.RoutePolicy, bool)
}

// Config fixes one public Connect procedure to one finite RouteId. Request
// headers, URLs, and method names are never forwarded through the tunnel.
type Config struct {
	Procedure             string
	Route                 contractv1.RouteId
	RequireIdempotencyKey bool
	Invoker               Invoker
	Options               []connect.HandlerOption
}

// NewUnaryHandler constructs a typed ConnectRPC handler whose route cannot be
// selected by the caller. Request and Response must be generated protobuf
// message value types, as in connect.NewUnaryHandler.
func NewUnaryHandler[Request any, Response any](config Config) (http.Handler, error) {
	if isNilInvoker(config.Invoker) {
		return nil, errors.New("connect bridge: invoker must not be nil")
	}
	if !validProcedure(config.Procedure) {
		return nil, errors.New("connect bridge: procedure is invalid")
	}
	if _, allowed := config.Invoker.RoutePolicy(config.Route); !allowed {
		return nil, errors.New("connect bridge: route is not allowed")
	}
	requestProbe := new(Request)
	if _, valid := any(requestProbe).(proto.Message); !valid {
		return nil, errors.New("connect bridge: request must be a protobuf message")
	}
	responseProbe := new(Response)
	if _, valid := any(responseProbe).(proto.Message); !valid {
		return nil, errors.New("connect bridge: response must be a protobuf message")
	}

	handler := connect.NewUnaryHandler[Request, Response](
		config.Procedure,
		func(
			ctx context.Context,
			request *connect.Request[Request],
		) (*connect.Response[Response], error) {
			idempotencyKey, err := requestIdempotencyKey(
				request.Header(),
				config.RequireIdempotencyKey,
			)
			if err != nil {
				return nil, publicError(connect.CodeInvalidArgument)
			}
			requestMessage, valid := any(request.Msg).(proto.Message)
			if !valid {
				return nil, publicError(connect.CodeInternal)
			}
			payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(requestMessage)
			if err != nil {
				return nil, publicError(connect.CodeInvalidArgument)
			}

			result, err := config.Invoker.Invoke(ctx, tunnel.Call{
				Route:          config.Route,
				Payload:        payload,
				IdempotencyKey: idempotencyKey,
			})
			if err != nil {
				return nil, mapTunnelError(err)
			}
			responseMessage := new(Response)
			protobufResponse, valid := any(responseMessage).(proto.Message)
			if !valid {
				return nil, publicError(connect.CodeInternal)
			}
			if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(
				result.Payload,
				protobufResponse,
			); err != nil {
				return nil, publicError(connect.CodeInternal)
			}

			return connect.NewResponse(responseMessage), nil
		},
		config.Options...,
	)

	return handler, nil
}

func requestIdempotencyKey(header http.Header, required bool) ([]byte, error) {
	values := header.Values(idempotencyKeyHeader)
	if len(values) == 0 {
		if required {
			return nil, errors.New("connect bridge: idempotency key is required")
		}

		return []byte{}, nil
	}
	if !required || len(values) != 1 {
		return nil, errors.New("connect bridge: idempotency key is not allowed")
	}

	value := []byte(values[0])
	if len(value) == 0 || len(value) > protocolv1.MaxIdempotencyKeyBytes {
		return nil, errors.New("connect bridge: idempotency key is outside bounds")
	}
	for _, character := range value {
		if character < '!' || character > '~' {
			return nil, errors.New("connect bridge: idempotency key is invalid")
		}
	}

	return slices.Clone(value), nil
}

func isNilInvoker(invoker Invoker) bool {
	if invoker == nil {
		return true
	}
	value := reflect.ValueOf(invoker)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

func validProcedure(procedure string) bool {
	if len(procedure) < 3 || len(procedure) > 256 || procedure[0] != '/' {
		return false
	}
	if strings.ContainsAny(procedure, "?#\\\r\n\t ") || strings.HasSuffix(procedure, "/") {
		return false
	}

	return strings.Count(procedure, "/") == 2
}

func mapTunnelError(err error) error {
	var resultErr *tunnel.ResultError
	if errors.As(err, &resultErr) {
		return publicError(connectCode(resultErr.Code()))
	}

	switch {
	case errors.Is(err, context.Canceled):
		return publicError(connect.CodeCanceled)
	case errors.Is(err, context.DeadlineExceeded):
		return publicError(connect.CodeDeadlineExceeded)
	case errors.Is(err, tunnel.ErrQueueFull):
		return publicError(connect.CodeResourceExhausted)
	case errors.Is(err, tunnel.ErrRouteNotAllowed):
		return publicError(connect.CodePermissionDenied)
	case errors.Is(err, tunnel.ErrNoTunnel),
		errors.Is(err, tunnel.ErrTunnelClosed),
		errors.Is(err, tunnel.ErrDraining):
		return publicError(connect.CodeUnavailable)
	default:
		return publicError(connect.CodeInternal)
	}
}

func connectCode(code contractv1.ResultCode) connect.Code {
	switch code {
	case contractv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT:
		return connect.CodeInvalidArgument
	case contractv1.ResultCode_RESULT_CODE_UNAUTHENTICATED:
		return connect.CodeUnauthenticated
	case contractv1.ResultCode_RESULT_CODE_PERMISSION_DENIED:
		return connect.CodePermissionDenied
	case contractv1.ResultCode_RESULT_CODE_NOT_FOUND:
		return connect.CodeNotFound
	case contractv1.ResultCode_RESULT_CODE_CONFLICT:
		return connect.CodeAlreadyExists
	case contractv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED:
		return connect.CodeResourceExhausted
	case contractv1.ResultCode_RESULT_CODE_DEADLINE_EXCEEDED:
		return connect.CodeDeadlineExceeded
	case contractv1.ResultCode_RESULT_CODE_CANCELED:
		return connect.CodeCanceled
	case contractv1.ResultCode_RESULT_CODE_UNAVAILABLE:
		return connect.CodeUnavailable
	default:
		return connect.CodeInternal
	}
}

func publicError(code connect.Code) error {
	return connect.NewError(code, errors.New(publicMessage(code)))
}

func publicMessage(code connect.Code) string {
	switch code {
	case connect.CodeCanceled:
		return "request canceled"
	case connect.CodeInvalidArgument:
		return "invalid argument"
	case connect.CodeDeadlineExceeded:
		return "deadline exceeded"
	case connect.CodeNotFound:
		return "not found"
	case connect.CodeAlreadyExists:
		return "conflict"
	case connect.CodePermissionDenied:
		return "permission denied"
	case connect.CodeResourceExhausted:
		return "resource exhausted"
	case connect.CodeUnavailable:
		return "service unavailable"
	case connect.CodeUnauthenticated:
		return "unauthenticated"
	default:
		return "internal error"
	}
}
