package postgres

import "errors"

var (
	// ErrInvalidConfig обозначает небезопасную или неполную конфигурацию.
	ErrInvalidConfig = errors.New("postgres: invalid configuration")

	// ErrNestedTransaction обозначает запрещённую вложенную транзакцию.
	ErrNestedTransaction = errors.New("postgres: nested transactions are not supported")

	// ErrRetryExhausted обозначает исчерпание разрешённых попыток транзакции.
	ErrRetryExhausted = errors.New("postgres: transaction retry attempts exhausted")
)

type operationError struct {
	operation string
	pool      poolRole
	err       error
}

func (err *operationError) Error() string {
	if err.pool == "" {
		return "postgres: " + err.operation + " failed"
	}

	return "postgres: " + err.operation + " " + string(err.pool) + " pool failed"
}

func (err *operationError) Unwrap() error {
	return err.err
}

func wrapOperation(operation string, pool poolRole, err error) error {
	if err == nil {
		return nil
	}

	return &operationError{
		operation: operation,
		pool:      pool,
		err:       err,
	}
}
