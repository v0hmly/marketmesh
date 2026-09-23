package workloadid

import "errors"

// Различимые ошибки пакета. Ошибки возвращаются с обёрткой %w и безопасным
// контекстом причины; сырой сертификат и его DER-представление в текст ошибки
// никогда не включаются.
var (
	// ErrInvalidIdentity — идентичность или её URI не соответствует формату
	// spiffe://<trust-domain>/<env>/<role>[/<instance>].
	ErrInvalidIdentity = errors.New("workloadid: invalid identity")
	// ErrUnauthenticated — машинная идентичность пира не подтверждена:
	// отсутствует TLS-состояние, нарушена цепочка, назначение, срок или URI SAN.
	ErrUnauthenticated = errors.New("workloadid: unauthenticated peer")
	// ErrInvalidPolicy — политика авторизации содержит недопустимые правила.
	ErrInvalidPolicy = errors.New("workloadid: invalid policy")
)
