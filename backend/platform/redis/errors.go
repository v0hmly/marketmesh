package redis

import "errors"

var (
	// ErrInvalidConfig обозначает небезопасную или неполную конфигурацию.
	ErrInvalidConfig = errors.New("redis: invalid configuration")

	// ErrRetryExhausted обозначает исчерпание разрешённых попыток явно
	// идемпотентной операции.
	ErrRetryExhausted = errors.New("redis: idempotent operation retry attempts exhausted")
)

type operationError struct {
	operation string
	role      Role
	err       error
}

func (err *operationError) Error() string {
	if err.role == "" {
		return "redis: " + err.operation + " failed"
	}

	return "redis: " + err.operation + " " + string(err.role) + " client failed"
}

func (err *operationError) Unwrap() error {
	return err.err
}

func wrapOperation(operation string, role Role, err error) error {
	if err == nil {
		return nil
	}

	return &operationError{
		operation: operation,
		role:      role,
		err:       err,
	}
}
