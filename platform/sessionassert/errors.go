package sessionassert

import "errors"

// Различимые ошибки проверки и выпуска утверждений. Ошибки возвращаются
// с обёрткой %w и дополнительным контекстом причины; сырой токен в текст
// ошибки никогда не включается.
var (
	// ErrMalformed — утверждение не соответствует compact-формату
	// header.payload.signature или содержит битые сегменты.
	ErrMalformed = errors.New("sessionassert: malformed assertion")
	// ErrUnknownKeyID — идентификатор ключа отсутствует в доверенном наборе.
	ErrUnknownKeyID = errors.New("sessionassert: unknown key id")
	// ErrBadSignature — подпись Ed25519 не прошла проверку.
	ErrBadSignature = errors.New("sessionassert: invalid signature")
	// ErrBadIssuer — издатель не совпадает с ожидаемым.
	ErrBadIssuer = errors.New("sessionassert: unexpected issuer")
	// ErrBadAudience — утверждение выпущено для другой аудитории.
	ErrBadAudience = errors.New("sessionassert: unexpected audience")
	// ErrBadType — неверный тип утверждения в заголовке или claims.
	ErrBadType = errors.New("sessionassert: unexpected type")
	// ErrExpired — срок действия утверждения истёк.
	ErrExpired = errors.New("sessionassert: assertion expired")
	// ErrNotYetValid — время выпуска ещё не наступило (за пределами leeway).
	ErrNotYetValid = errors.New("sessionassert: assertion not yet valid")
	// ErrMissingScope — отсутствует обязательная область действия.
	ErrMissingScope = errors.New("sessionassert: missing required scope")
	// ErrInvalidParams — недопустимые параметры выпуска или конфигурации.
	ErrInvalidParams = errors.New("sessionassert: invalid parameters")
)
