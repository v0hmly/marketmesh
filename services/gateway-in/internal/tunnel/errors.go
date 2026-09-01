package tunnel

import (
	"errors"
	"fmt"

	tunnelv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
)

var (
	// ErrNoTunnel means no ready tunnel currently supports the requested route.
	ErrNoTunnel = errors.New("tunnel: no ready route")
	// ErrTunnelClosed means the selected reverse tunnel disconnected.
	ErrTunnelClosed = errors.New("tunnel: connection closed")
	// ErrQueueFull means the bounded queue for a traffic class is saturated.
	ErrQueueFull = errors.New("tunnel: queue is full")
	// ErrDraining means the tunnel or registry no longer accepts new work.
	ErrDraining = errors.New("tunnel: draining")
	// ErrRouteNotAllowed means the route is absent from the local static policy.
	ErrRouteNotAllowed = errors.New("tunnel: route is not allowed")
	// ErrProtocolViolation means a peer violated the negotiated state machine.
	ErrProtocolViolation = errors.New("tunnel: protocol violation")
	// ErrPeerUnauthorized means the transport peer did not match workload policy.
	ErrPeerUnauthorized = errors.New("tunnel: peer is not authorized")
)

// ResultError is the finite, safe error returned by gateway-out for one call.
type ResultError struct {
	code tunnelv1.ResultCode
}

// Error intentionally contains no peer-provided diagnostic text.
func (e *ResultError) Error() string {
	return fmt.Sprintf("tunnel: logical request failed with result %s", resultLabel(e.code))
}

// Code returns the finite protocol result category.
func (e *ResultError) Code() tunnelv1.ResultCode {
	if e == nil {
		return tunnelv1.ResultCode_RESULT_CODE_INTERNAL
	}

	return e.code
}

func newResultError(code tunnelv1.ResultCode) error {
	return &ResultError{code: code}
}

func resultLabel(code tunnelv1.ResultCode) string {
	switch code {
	case tunnelv1.ResultCode_RESULT_CODE_OK:
		return "ok"
	case tunnelv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT:
		return "invalid_argument"
	case tunnelv1.ResultCode_RESULT_CODE_UNAUTHENTICATED:
		return "unauthenticated"
	case tunnelv1.ResultCode_RESULT_CODE_PERMISSION_DENIED:
		return "permission_denied"
	case tunnelv1.ResultCode_RESULT_CODE_NOT_FOUND:
		return "not_found"
	case tunnelv1.ResultCode_RESULT_CODE_CONFLICT:
		return "conflict"
	case tunnelv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED:
		return "resource_exhausted"
	case tunnelv1.ResultCode_RESULT_CODE_DEADLINE_EXCEEDED:
		return "deadline_exceeded"
	case tunnelv1.ResultCode_RESULT_CODE_CANCELED:
		return "canceled"
	case tunnelv1.ResultCode_RESULT_CODE_UNAVAILABLE:
		return "unavailable"
	default:
		return "internal"
	}
}
