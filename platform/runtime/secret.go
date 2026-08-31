package runtime

import (
	"encoding/json"
	"log/slog"
)

// RedactedSecretValue — безопасное текстовое представление Secret.
const RedactedSecretValue = "[REDACTED]"

// Secret хранит чувствительное значение и маскирует его при форматировании,
// structured logging и serialization. Значение доступно только через Reveal.
//
// Secret не может гарантированно стереть строку из памяти Go.
type Secret struct {
	value string
}

func newSecret(value string) Secret {
	return Secret{value: value}
}

// Reveal возвращает исходное значение доверенному потребителю. Результат
// нельзя добавлять в ошибки, логи, traces или metrics.
func (secret Secret) Reveal() string {
	return secret.value
}

// Present сообщает, содержит ли Secret непустое значение.
func (secret Secret) Present() bool {
	return secret.value != ""
}

// String маскирует Secret при обычном форматировании.
func (Secret) String() string {
	return RedactedSecretValue
}

// GoString маскирует Secret при форматировании с %#v.
func (Secret) GoString() string {
	return RedactedSecretValue
}

// LogValue маскирует Secret при использовании с log/slog.
func (Secret) LogValue() slog.Value {
	return slog.StringValue(RedactedSecretValue)
}

// MarshalText маскирует Secret при text serialization.
func (Secret) MarshalText() ([]byte, error) {
	return []byte(RedactedSecretValue), nil
}

// MarshalJSON маскирует Secret при JSON serialization.
func (Secret) MarshalJSON() ([]byte, error) {
	return json.Marshal(RedactedSecretValue)
}
