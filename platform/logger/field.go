package logger

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// DefaultMaskValue заменяет значение чувствительного поля по умолчанию.
const DefaultMaskValue = "[REDACTED]"

const invalidJSONValue = "[INVALID JSON]"

type fieldKind uint8

const (
	fieldKindString fieldKind = iota + 1
	fieldKindBool
	fieldKindInt64
	fieldKindUint64
	fieldKindFloat64
	fieldKindDuration
	fieldKindTime
	fieldKindBytes
	fieldKindError
	fieldKindJSON
	fieldKindSensitive
	fieldKindUnsafeAny
)

// Field представляет типизированное структурное поле события.
// Значение Field применяется непосредственно к zerolog.Event без map и reflect,
// кроме явно выбранного UnsafeAny.
type Field struct {
	key     string
	text    string
	value   any
	numeric uint64
	kind    fieldKind
}

// String создаёт строковое поле.
func String(key string, value string) Field {
	return Field{key: key, text: value, kind: fieldKindString}
}

// Bool создаёт логическое поле.
func Bool(key string, value bool) Field {
	var numeric uint64
	if value {
		numeric = 1
	}

	return Field{key: key, numeric: numeric, kind: fieldKindBool}
}

// Int создаёт целочисленное поле.
func Int(key string, value int) Field {
	return Field{key: key, numeric: uint64(value), kind: fieldKindInt64}
}

// Int64 создаёт поле int64.
func Int64(key string, value int64) Field {
	return Field{key: key, numeric: uint64(value), kind: fieldKindInt64}
}

// Uint64 создаёт поле uint64.
func Uint64(key string, value uint64) Field {
	return Field{key: key, numeric: value, kind: fieldKindUint64}
}

// Float64 создаёт поле float64.
func Float64(key string, value float64) Field {
	return Field{key: key, numeric: math.Float64bits(value), kind: fieldKindFloat64}
}

// Duration создаёт поле time.Duration.
func Duration(key string, value time.Duration) Field {
	return Field{key: key, numeric: uint64(value), kind: fieldKindDuration}
}

// Time создаёт поле time.Time.
func Time(key string, value time.Time) Field {
	return Field{key: key, value: value, kind: fieldKindTime}
}

// Bytes создаёт бинарное поле как JSON-строку.
func Bytes(key string, value []byte) Field {
	return Field{key: key, value: value, kind: fieldKindBytes}
}

// Err создаёт поле error с ключом error.
func Err(value error) Field {
	return NamedError("error", value)
}

// NamedError создаёт поле error с заданным ключом.
func NamedError(key string, value error) Field {
	return Field{key: key, value: value, kind: fieldKindError}
}

// JSON создаёт поле из заранее сериализованного JSON. Значение проверяется и
// копируется, поэтому его можно безопасно сохранить в дочернем Logger через With.
// Невалидное значение заменяется безопасной диагностической строкой.
func JSON(key string, value json.RawMessage) Field {
	if !json.Valid(value) {
		return String(key, invalidJSONValue)
	}

	return Field{
		key:   key,
		value: bytes.Clone(value),
		kind:  fieldKindJSON,
	}
}

// Sensitive создаёт поле, значение которого всегда заменяется маской.
// Переданное значение намеренно не сохраняется внутри Field.
func Sensitive(key string, _ any) Field {
	return Field{key: key, kind: fieldKindSensitive}
}

// UnsafeAny создаёт поле произвольного типа. Оно использует рефлексию в
// zerolog и может раскрыть вложенные чувствительные данные.
func UnsafeAny(key string, value any) Field {
	return Field{key: key, value: value, kind: fieldKindUnsafeAny}
}

func (l *Logger) addField(event *zerolog.Event, field *Field) {
	key := protectedFieldKey(field.key)
	if field.kind == fieldKindSensitive || l.masker.masks(field.key) {
		event.Str(key, l.masker.value)
		return
	}

	switch field.kind {
	case fieldKindString:
		event.Str(key, field.text)
	case fieldKindBool:
		event.Bool(key, field.numeric != 0)
	case fieldKindInt64:
		event.Int64(key, int64(field.numeric))
	case fieldKindUint64:
		event.Uint64(key, field.numeric)
	case fieldKindFloat64:
		event.Float64(key, math.Float64frombits(field.numeric))
	case fieldKindDuration:
		event.Dur(key, time.Duration(field.numeric))
	case fieldKindTime:
		event.Time(key, field.value.(time.Time))
	case fieldKindBytes:
		event.Bytes(key, field.value.([]byte))
	case fieldKindError:
		err, _ := field.value.(error)
		event.AnErr(key, err)
	case fieldKindJSON:
		event.RawJSON(key, field.value.([]byte))
	case fieldKindUnsafeAny:
		event.Interface(key, field.value)
	}
}

func (l *Logger) addContextField(context zerolog.Context, field *Field) zerolog.Context {
	key := protectedFieldKey(field.key)
	if field.kind == fieldKindSensitive || l.masker.masks(field.key) {
		return context.Str(key, l.masker.value)
	}

	switch field.kind {
	case fieldKindString:
		return context.Str(key, field.text)
	case fieldKindBool:
		return context.Bool(key, field.numeric != 0)
	case fieldKindInt64:
		return context.Int64(key, int64(field.numeric))
	case fieldKindUint64:
		return context.Uint64(key, field.numeric)
	case fieldKindFloat64:
		return context.Float64(key, math.Float64frombits(field.numeric))
	case fieldKindDuration:
		return context.Dur(key, time.Duration(field.numeric))
	case fieldKindTime:
		return context.Time(key, field.value.(time.Time))
	case fieldKindBytes:
		return context.Bytes(key, field.value.([]byte))
	case fieldKindError:
		err, _ := field.value.(error)
		return context.AnErr(key, err)
	case fieldKindJSON:
		return context.RawJSON(key, field.value.([]byte))
	case fieldKindUnsafeAny:
		return context.Interface(key, field.value)
	default:
		return context
	}
}

func protectedFieldKey(key string) string {
	switch key {
	case "service", "version", "environment", "time", "level", "message", "trace_id", "span_id":
		return "fields." + key
	default:
		return key
	}
}

type fieldMasker struct {
	custom map[string]struct{}
	value  string
}

func newFieldMasker(fields []string, value string) (fieldMasker, error) {
	if value == "" {
		value = DefaultMaskValue
	}

	var custom map[string]struct{}
	if len(fields) > 0 {
		custom = make(map[string]struct{}, len(fields))
	}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			return fieldMasker{}, errors.New("logger: mask field must not be empty")
		}
		custom[field] = struct{}{}
	}

	return fieldMasker{custom: custom, value: value}, nil
}

func (m fieldMasker) masks(field string) bool {
	if isDefaultMaskedField(field) {
		return true
	}

	return m.masksCustom(field)
}

func (m fieldMasker) masksCustom(field string) bool {
	if m.custom == nil {
		return false
	}

	_, masked := m.custom[field]
	return masked
}

func isDefaultMaskedField(field string) bool {
	switch field {
	case "password",
		"password_hash",
		"token",
		"access_token",
		"refresh_token",
		"authorization",
		"proxy_authorization",
		"cookie",
		"set_cookie",
		"api_key",
		"secret",
		"client_secret",
		"card_number",
		"cvv",
		"email",
		"phone",
		"phone_number",
		"address",
		"full_name",
		"Authorization",
		"Proxy-Authorization",
		"Cookie",
		"Set-Cookie",
		"X-Api-Key":
		return true
	default:
		return false
	}
}
