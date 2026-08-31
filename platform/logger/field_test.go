package logger_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/platform/logger"
)

func TestLoggerWritesTypedFields(t *testing.T) {
	t.Parallel()

	loggedAt := time.Date(2026, time.August, 31, 12, 30, 0, 0, time.UTC)
	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "info"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}

	log.Info(
		"типизированные поля",
		logger.String("string", "value"),
		logger.Bool("bool", true),
		logger.Int("int", -42),
		logger.Int64("int64", math.MinInt64),
		logger.Uint64("uint64", math.MaxUint64),
		logger.Float64("float64", 3.5),
		logger.Duration("duration", 1500*time.Millisecond),
		logger.Time("logged_at", loggedAt),
		logger.Bytes("bytes", []byte("data")),
		logger.Err(errors.New("failure")),
		logger.UnsafeAny("object", struct {
			Name string `json:"name"`
		}{Name: "test"}),
	)

	event := decodeEvent(t, output.Bytes())
	assertStringField(t, event, "string", "value")
	assertStringField(t, event, "bytes", "data")
	assertStringField(t, event, "error", "failure")
	if event["bool"] != true {
		t.Errorf("поле bool: ожидалось true, получено %#v", event["bool"])
	}
	if event["int"] != float64(-42) {
		t.Errorf("поле int: ожидалось -42, получено %#v", event["int"])
	}
	if event["int64"] != float64(math.MinInt64) {
		t.Errorf("поле int64: ожидалось %d, получено %#v", int64(math.MinInt64), event["int64"])
	}
	if event["uint64"] != float64(math.MaxUint64) {
		t.Errorf("поле uint64: ожидалось %d, получено %#v", uint64(math.MaxUint64), event["uint64"])
	}
	if event["float64"] != 3.5 {
		t.Errorf("поле float64: ожидалось 3.5, получено %#v", event["float64"])
	}
	if event["duration"] != float64(1500) {
		t.Errorf("поле duration: ожидалось 1500, получено %#v", event["duration"])
	}
	assertStringField(t, event, "logged_at", loggedAt.Format(time.RFC3339))
	object, ok := event["object"].(map[string]any)
	if !ok {
		t.Fatalf("поле object должно быть объектом, получено %#v", event["object"])
	}
	assertStringField(t, object, "name", "test")
}

func TestLoggerMasksDefaultAndConfiguredFields(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	config := validConfig(&output, "info")
	config.MaskFields = []string{"private_note"}
	config.MaskValue = "***"
	log, err := logger.New(config)
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}

	log.Info(
		"маскирование",
		logger.String("user_id", "user-42"),
		logger.String("password", "plain-password"),
		logger.Int("token", 12345),
		logger.UnsafeAny("private_note", map[string]any{"secret": "hidden"}),
	)

	event := decodeEvent(t, output.Bytes())
	assertStringField(t, event, "user_id", "user-42")
	assertStringField(t, event, "password", "***")
	assertStringField(t, event, "token", "***")
	assertStringField(t, event, "private_note", "***")
	if bytes.Contains(output.Bytes(), []byte("plain-password")) ||
		bytes.Contains(output.Bytes(), []byte("hidden")) {
		t.Fatalf("исходное чувствительное значение попало в лог: %s", output.Bytes())
	}
}

func TestLoggerWritesJSONAsStructuredValue(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "info"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}
	raw := json.RawMessage(`{"request_id":"request-42","items":[1,2]}`)
	field := logger.JSON("payload", raw)

	// JSON хранится в Field собственной копией.
	raw[2] = 'X'
	log.Info("JSON-поле", field)

	event := decodeEvent(t, output.Bytes())
	payload, ok := event["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload должен быть JSON-объектом, получено %#v", event["payload"])
	}
	assertStringField(t, payload, "request_id", "request-42")
	if strings.Contains(output.String(), `\"request_id\"`) {
		t.Fatalf("JSON записан как экранированная строка: %s", output.Bytes())
	}
}

func TestJSONReplacesInvalidValue(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "info"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}

	log.Info("невалидный JSON", logger.JSON("payload", json.RawMessage(`{"broken":`)))

	event := decodeEvent(t, output.Bytes())
	assertStringField(t, event, "payload", "[INVALID JSON]")
}

func TestSensitiveAlwaysMasksValue(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "info"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}

	log.Info("чувствительное поле", logger.Sensitive("credential", "must-not-leak"))

	event := decodeEvent(t, output.Bytes())
	assertStringField(t, event, "credential", logger.DefaultMaskValue)
	if strings.Contains(output.String(), "must-not-leak") {
		t.Fatalf("чувствительное значение попало в лог: %s", output.Bytes())
	}
}

func TestJSONFieldIsMaskedBeforeSerialization(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "info"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}
	field := logger.JSON("token", json.RawMessage(`{"secret":"must-not-leak"}`))

	log.Info("замаскированный JSON", field)

	event := decodeEvent(t, output.Bytes())
	assertStringField(t, event, "token", logger.DefaultMaskValue)
	if strings.Contains(output.String(), "must-not-leak") {
		t.Fatalf("JSON был сериализован до маскирования: %s", output.Bytes())
	}
}

func TestLoggerWithWritesTypedFields(t *testing.T) {
	t.Parallel()

	loggedAt := time.Date(2026, time.August, 31, 12, 30, 0, 0, time.UTC)
	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "info"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}
	log = log.With(
		logger.String("string", "value"),
		logger.Bool("bool", true),
		logger.Int64("int64", -42),
		logger.Uint64("uint64", 42),
		logger.Float64("float64", 3.5),
		logger.Duration("duration", 1500*time.Millisecond),
		logger.Time("logged_at", loggedAt),
		logger.Bytes("bytes", []byte("data")),
		logger.NamedError("cause", errors.New("failure")),
		logger.JSON("payload", json.RawMessage(`{"request_id":"request-42"}`)),
		logger.Sensitive("credential", "must-not-leak"),
		logger.UnsafeAny("object", map[string]string{"key": "value"}),
	)

	log.Info("поля контекста")

	event := decodeEvent(t, output.Bytes())
	assertStringField(t, event, "string", "value")
	assertStringField(t, event, "bytes", "data")
	assertStringField(t, event, "cause", "failure")
	assertStringField(t, event, "credential", logger.DefaultMaskValue)
	if event["bool"] != true || event["int64"] != float64(-42) || event["uint64"] != float64(42) {
		t.Errorf("числовые или логические поля потеряли типы: %#v", event)
	}
	if event["float64"] != 3.5 || event["duration"] != float64(1500) {
		t.Errorf("float64 или duration потеряли типы: %#v", event)
	}
	assertStringField(t, event, "logged_at", loggedAt.Format(time.RFC3339))
	if _, ok := event["payload"].(map[string]any); !ok {
		t.Errorf("payload должен быть JSON-объектом, получено %#v", event["payload"])
	}
	if _, ok := event["object"].(map[string]any); !ok {
		t.Errorf("object должен быть JSON-объектом, получено %#v", event["object"])
	}
}

func TestNewRejectsEmptyMaskField(t *testing.T) {
	t.Parallel()

	config := validConfig(&bytes.Buffer{}, "info")
	config.MaskFields = []string{" "}

	_, err := logger.New(config)
	if err == nil {
		t.Fatal("ожидалась ошибка пустого имени маскируемого поля")
	}
}
