package logger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/platform/logger"
	"go.opentelemetry.io/otel/trace"
)

type testLogValuer string

func (value testLogValuer) LogValue() slog.Value {
	return slog.StringValue(string(value))
}

func TestSlogWritesThroughLoggerPipeline(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "debug"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}
	slogLogger := log.Slog().With("component", "http").WithGroup("request")

	traceID := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8}
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), spanContext)

	slogLogger.InfoContext(
		ctx,
		"запрос обработан",
		slog.String("method", "GET"),
		slog.String("authorization", "Bearer secret"),
		slog.Group("peer", slog.String("address", "private address")),
		slog.Any("error", errors.New("test error")),
	)

	event := decodeEvent(t, output.Bytes())
	assertStringField(t, event, "level", "info")
	assertStringField(t, event, "component", "http")
	assertStringField(t, event, "request.method", "GET")
	assertStringField(t, event, "request.authorization", logger.DefaultMaskValue)
	assertStringField(t, event, "request.peer.address", logger.DefaultMaskValue)
	assertStringField(t, event, "request.error", "test error")
	assertStringField(t, event, "trace_id", traceID.String())
	assertStringField(t, event, "span_id", spanID.String())
	if bytes.Contains(output.Bytes(), []byte("Bearer secret")) ||
		bytes.Contains(output.Bytes(), []byte("private address")) {
		t.Fatalf("slog пропустил чувствительное значение: %s", output.Bytes())
	}
}

func TestSlogRespectsConfiguredLevel(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "warn"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}
	slogLogger := log.Slog()

	if slogLogger.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("slog не должен разрешать info при уровне warn")
	}
	if !slogLogger.Enabled(context.Background(), slog.LevelWarn) {
		t.Fatal("slog должен разрешать warn при уровне warn")
	}
	slogLogger.Info("скрыто")
	slogLogger.Warn("видимо")

	if bytes.Contains(output.Bytes(), []byte("скрыто")) {
		t.Fatalf("slog записал отключённый уровень: %s", output.Bytes())
	}
	event := decodeEvent(t, output.Bytes())
	assertStringField(t, event, "level", "warn")
	assertStringField(t, event, "message", "видимо")
}

func TestHTTPErrorLogWritesErrorEvent(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "info"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}

	log.HTTPErrorLog().Print("TLS handshake failed")

	event := decodeEvent(t, output.Bytes())
	assertStringField(t, event, "level", "error")
	assertStringField(t, event, "message", "TLS handshake failed")
}

func TestSlogWritesTypedAttrs(t *testing.T) {
	t.Parallel()

	loggedAt := time.Date(2026, time.August, 31, 12, 30, 0, 0, time.UTC)
	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "info"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}

	log.Slog().LogAttrs(
		context.Background(),
		slog.LevelInfo,
		"атрибуты slog",
		slog.Bool("bool", true),
		slog.Int64("int64", -42),
		slog.Uint64("uint64", 42),
		slog.Float64("float64", 3.5),
		slog.Duration("duration", 1500*time.Millisecond),
		slog.Time("logged_at", loggedAt),
		slog.Any("valuer", testLogValuer("resolved")),
		slog.Any("object", map[string]string{"key": "value"}),
		slog.Any("payload", json.RawMessage(`{"request_id":"request-42"}`)),
	)

	event := decodeEvent(t, output.Bytes())
	if event["bool"] != true {
		t.Errorf("поле bool: ожидалось true, получено %#v", event["bool"])
	}
	if event["int64"] != float64(-42) {
		t.Errorf("поле int64: ожидалось -42, получено %#v", event["int64"])
	}
	if event["uint64"] != float64(42) {
		t.Errorf("поле uint64: ожидалось 42, получено %#v", event["uint64"])
	}
	if event["float64"] != 3.5 {
		t.Errorf("поле float64: ожидалось 3.5, получено %#v", event["float64"])
	}
	if event["duration"] != float64(1500) {
		t.Errorf("поле duration: ожидалось 1500, получено %#v", event["duration"])
	}
	assertStringField(t, event, "logged_at", loggedAt.Format(time.RFC3339))
	assertStringField(t, event, "valuer", "resolved")
	object, ok := event["object"].(map[string]any)
	if !ok {
		t.Fatalf("поле object должно быть объектом, получено %#v", event["object"])
	}
	assertStringField(t, object, "key", "value")
	payload, ok := event["payload"].(map[string]any)
	if !ok {
		t.Fatalf("поле payload должно быть объектом, получено %#v", event["payload"])
	}
	assertStringField(t, payload, "request_id", "request-42")
}

func TestSlogProtectsReservedFields(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "info"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}

	log.Slog().Info(
		"настоящее сообщение",
		slog.String("service", "forged-service"),
		slog.String("message", "поддельное сообщение"),
	)

	event := decodeEvent(t, output.Bytes())
	assertStringField(t, event, "service", "auth")
	assertStringField(t, event, "message", "настоящее сообщение")
	assertStringField(t, event, "fields.service", "forged-service")
	assertStringField(t, event, "fields.message", "поддельное сообщение")
}

func TestSlogReplacesInvalidRawJSON(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "info"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}

	log.Slog().Info("невалидный JSON", slog.Any("payload", json.RawMessage(`{"broken":`)))

	event := decodeEvent(t, output.Bytes())
	assertStringField(t, event, "payload", "[INVALID JSON]")
}
