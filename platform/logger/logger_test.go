package logger_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/v0hmly/marketmesh/platform/logger"
	"go.opentelemetry.io/otel/trace"
)

func TestNewWritesJSONWithRequiredFields(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	log, err := logger.New(logger.Config{
		Service:     "auth",
		Version:     "1.2.3",
		Environment: "test",
		Output:      &output,
	})
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}

	log.Info("пользователь авторизован")

	event := decodeEvent(t, output.Bytes())
	assertStringField(t, event, "level", "info")
	assertStringField(t, event, "service", "auth")
	assertStringField(t, event, "version", "1.2.3")
	assertStringField(t, event, "environment", "test")
	assertStringField(t, event, "message", "пользователь авторизован")

	timestamp, ok := event["time"].(string)
	if !ok {
		t.Fatalf("поле time должно быть строкой, получено: %#v", event["time"])
	}
	if _, err := time.Parse(time.RFC3339, timestamp); err != nil {
		t.Errorf("поле time должно содержать RFC3339: %v", err)
	}
}

func TestNewValidatesRequiredFields(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		config logger.Config
	}{
		{
			name: "service",
			config: logger.Config{
				Version:     "1.0.0",
				Environment: "test",
			},
		},
		{
			name: "version",
			config: logger.Config{
				Service:     "auth",
				Environment: "test",
			},
		},
		{
			name: "environment",
			config: logger.Config{
				Service: "auth",
				Version: "1.0.0",
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := logger.New(testCase.config)
			if err == nil {
				t.Fatal("ожидалась ошибка валидации")
			}
			if !strings.Contains(err.Error(), testCase.name) {
				t.Errorf("ошибка должна указывать поле %q: %v", testCase.name, err)
			}
		})
	}
}

func TestNewRejectsInvalidLevel(t *testing.T) {
	t.Parallel()

	_, err := logger.New(validConfig(&bytes.Buffer{}, "definitely-not-a-level"))
	if err == nil {
		t.Fatal("ожидалась ошибка уровня логирования")
	}
}

func TestNewAllowsConsoleOnlyLocally(t *testing.T) {
	t.Parallel()

	t.Run("non-local", func(t *testing.T) {
		t.Parallel()

		config := validConfig(&bytes.Buffer{}, "info")
		config.Console = true

		_, err := logger.New(config)
		if err == nil {
			t.Fatal("ожидался запрет ConsoleWriter вне local")
		}
	})

	t.Run("local", func(t *testing.T) {
		t.Parallel()

		var output bytes.Buffer
		config := validConfig(&output, "info")
		config.Environment = "local"
		config.Console = true

		log, err := logger.New(config)
		if err != nil {
			t.Fatalf("создать локальный логгер: %v", err)
		}
		log.Info("готово")

		if json.Valid(output.Bytes()) {
			t.Fatal("локальный ConsoleWriter не должен выдавать JSON")
		}
		if !strings.Contains(output.String(), "готово") {
			t.Errorf("консольная запись не содержит сообщение: %q", output.String())
		}
	})
}

func TestNewKeepsLevelsIndependent(t *testing.T) {
	t.Parallel()

	globalLevel := zerolog.GlobalLevel()
	var infoOutput bytes.Buffer
	var errorOutput bytes.Buffer

	infoLog, err := logger.New(validConfig(&infoOutput, "info"))
	if err != nil {
		t.Fatalf("создать info логгер: %v", err)
	}
	errorLog, err := logger.New(validConfig(&errorOutput, "error"))
	if err != nil {
		t.Fatalf("создать error логгер: %v", err)
	}

	infoLog.Info("info visible")
	errorLog.Info("info hidden")
	errorLog.Error("error visible")

	if !strings.Contains(infoOutput.String(), "info visible") {
		t.Errorf("info логгер отфильтровал разрешённый уровень: %q", infoOutput.String())
	}
	if strings.Contains(errorOutput.String(), "info hidden") {
		t.Errorf("error логгер пропустил info-событие: %q", errorOutput.String())
	}
	if !strings.Contains(errorOutput.String(), "error visible") {
		t.Errorf("error логгер отфильтровал error-событие: %q", errorOutput.String())
	}
	if zerolog.GlobalLevel() != globalLevel {
		t.Errorf("глобальный уровень zerolog изменён: было %s, стало %s", globalLevel, zerolog.GlobalLevel())
	}
}

func TestLoggerEnabled(t *testing.T) {
	t.Parallel()

	log, err := logger.New(validConfig(&bytes.Buffer{}, "warn"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}

	testCases := []struct {
		name    string
		level   logger.Level
		enabled bool
	}{
		{name: "unknown", level: logger.LevelUnknown},
		{name: "trace", level: logger.LevelTrace},
		{name: "debug", level: logger.LevelDebug},
		{name: "info", level: logger.LevelInfo},
		{name: "warn", level: logger.LevelWarn, enabled: true},
		{name: "error", level: logger.LevelError, enabled: true},
		{name: "fatal", level: logger.LevelFatal, enabled: true},
		{name: "panic", level: logger.LevelPanic, enabled: true},
		{name: "invalid", level: logger.Level(255)},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if actual := log.Enabled(testCase.level); actual != testCase.enabled {
				t.Errorf("Enabled(%d): ожидалось %t, получено %t", testCase.level, testCase.enabled, actual)
			}
		})
	}
}

func TestLoggerWithAddsImmutableContext(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "info"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}
	payloadField := logger.JSON("payload", json.RawMessage(`{"source":"server"}`))
	componentLog := log.With(
		logger.String("component", "http"),
		logger.Sensitive("credential", "must-not-leak"),
		payloadField,
	)

	log.Info("родитель")
	componentLog.Info("потомок", logger.String("operation", "request"))

	scanner := bufio.NewScanner(bytes.NewReader(output.Bytes()))
	var events []map[string]any
	for scanner.Scan() {
		events = append(events, decodeEvent(t, scanner.Bytes()))
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("прочитать лог: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("ожидалось два события, получено %d", len(events))
	}
	if _, exists := events[0]["component"]; exists {
		t.Fatalf("With изменил родительский логгер: %#v", events[0])
	}
	assertStringField(t, events[1], "component", "http")
	assertStringField(t, events[1], "credential", logger.DefaultMaskValue)
	assertStringField(t, events[1], "operation", "request")
	payload, ok := events[1]["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload должен быть JSON-объектом, получено %#v", events[1]["payload"])
	}
	assertStringField(t, payload, "source", "server")
	if strings.Contains(output.String(), "must-not-leak") {
		t.Fatalf("чувствительное значение из With попало в лог: %s", output.Bytes())
	}
}

func TestLoggerWithNoFieldsReturnsSameInstance(t *testing.T) {
	t.Parallel()

	log, err := logger.New(validConfig(&bytes.Buffer{}, "info"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}
	if log.With() != log {
		t.Fatal("With без полей должен возвращать исходный экземпляр")
	}
}

func TestLoggerProtectsReservedFields(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "info"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}
	log = log.With(
		logger.String("service", "forged-service"),
		logger.String("level", "forged-level"),
	)

	log.Info(
		"настоящее сообщение",
		logger.String("message", "поддельное сообщение"),
		logger.String("trace_id", "forged-trace"),
	)

	event := decodeEvent(t, output.Bytes())
	assertStringField(t, event, "service", "auth")
	assertStringField(t, event, "level", "info")
	assertStringField(t, event, "message", "настоящее сообщение")
	assertStringField(t, event, "fields.service", "forged-service")
	assertStringField(t, event, "fields.level", "forged-level")
	assertStringField(t, event, "fields.message", "поддельное сообщение")
	assertStringField(t, event, "fields.trace_id", "forged-trace")
}

func TestLoggerWritesNonTerminatingLevels(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		expected string
		write    func(*logger.Logger)
	}{
		{
			name:     "trace",
			expected: "trace",
			write:    func(log *logger.Logger) { log.Trace("event") },
		},
		{
			name:     "debug",
			expected: "debug",
			write:    func(log *logger.Logger) { log.Debug("event") },
		},
		{
			name:     "info",
			expected: "info",
			write:    func(log *logger.Logger) { log.Info("event") },
		},
		{
			name:     "warn",
			expected: "warn",
			write:    func(log *logger.Logger) { log.Warn("event") },
		},
		{
			name:     "error",
			expected: "error",
			write:    func(log *logger.Logger) { log.Error("event") },
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer
			log, err := logger.New(validConfig(&output, "trace"))
			if err != nil {
				t.Fatalf("создать логгер: %v", err)
			}

			testCase.write(log)

			event := decodeEvent(t, output.Bytes())
			assertStringField(t, event, "level", testCase.expected)
		})
	}
}

func TestLoggerWritesContextLevels(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		expected string
		write    func(*logger.Logger, context.Context)
	}{
		{
			name:     "trace",
			expected: "trace",
			write:    func(log *logger.Logger, ctx context.Context) { log.TraceContext(ctx, "event") },
		},
		{
			name:     "debug",
			expected: "debug",
			write:    func(log *logger.Logger, ctx context.Context) { log.DebugContext(ctx, "event") },
		},
		{
			name:     "info",
			expected: "info",
			write:    func(log *logger.Logger, ctx context.Context) { log.InfoContext(ctx, "event") },
		},
		{
			name:     "warn",
			expected: "warn",
			write:    func(log *logger.Logger, ctx context.Context) { log.WarnContext(ctx, "event") },
		},
		{
			name:     "error",
			expected: "error",
			write:    func(log *logger.Logger, ctx context.Context) { log.ErrorContext(ctx, "event") },
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer
			log, err := logger.New(validConfig(&output, "trace"))
			if err != nil {
				t.Fatalf("создать логгер: %v", err)
			}

			testCase.write(log, context.Background())

			event := decodeEvent(t, output.Bytes())
			assertStringField(t, event, "level", testCase.expected)
		})
	}
}

func TestLoggerPanicWritesEvent(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "trace"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}

	defer func() {
		recovered := recover()
		if recovered != "panic event" {
			t.Errorf("ожидался panic с сообщением события, получено %#v", recovered)
		}

		event := decodeEvent(t, output.Bytes())
		assertStringField(t, event, "level", "panic")
		assertStringField(t, event, "reason", "test")
	}()

	log.Panic("panic event", logger.String("reason", "test"))
}

func TestLoggerPanicContextWritesEvent(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "trace"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}

	defer func() {
		recovered := recover()
		if recovered != "panic context event" {
			t.Errorf("ожидался panic с сообщением события, получено %#v", recovered)
		}

		event := decodeEvent(t, output.Bytes())
		assertStringField(t, event, "level", "panic")
	}()

	log.PanicContext(context.Background(), "panic context event")
}

func TestLoggerSkipsDisabledLevelsBeforeProcessingFields(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "error"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}
	ctx := context.Background()
	field := logger.String("token", "must-not-be-processed")

	log.Trace("trace", field)
	log.TraceContext(ctx, "trace context", field)
	log.Debug("debug", field)
	log.DebugContext(ctx, "debug context", field)
	log.Info("info", field)
	log.InfoContext(ctx, "info context", field)
	log.Warn("warn", field)
	log.WarnContext(ctx, "warn context", field)

	if output.Len() != 0 {
		t.Fatalf("отключённые уровни записали события: %s", output.Bytes())
	}
}

func TestLoggerAddsTraceCorrelationFromContext(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "info"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}

	traceID := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8}
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), spanContext)

	log.InfoContext(ctx, "коррелированное событие")

	event := decodeEvent(t, output.Bytes())
	assertStringField(t, event, "trace_id", traceID.String())
	assertStringField(t, event, "span_id", spanID.String())
}

func TestLoggerOmitsInvalidTraceContext(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "info"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}

	log.InfoContext(context.Background(), "событие без трассировки")

	event := decodeEvent(t, output.Bytes())
	if _, exists := event["trace_id"]; exists {
		t.Errorf("невалидный trace_id не должен попадать в событие: %#v", event)
	}
	if _, exists := event["span_id"]; exists {
		t.Errorf("невалидный span_id не должен попадать в событие: %#v", event)
	}
}

func TestLoggerDoesNotCopyArbitraryContextValues(t *testing.T) {
	t.Parallel()

	type secretKey struct{}

	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "info"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}
	ctx := context.WithValue(context.Background(), secretKey{}, "secret-access-token")

	log.InfoContext(ctx, "безопасное событие")

	if strings.Contains(output.String(), "secret-access-token") {
		t.Fatal("произвольное значение context попало в лог")
	}
}

func TestLoggerSerializesConcurrentWrites(t *testing.T) {
	t.Parallel()

	const eventCount = 100

	var output bytes.Buffer
	log, err := logger.New(validConfig(&output, "info"))
	if err != nil {
		t.Fatalf("создать логгер: %v", err)
	}

	var group sync.WaitGroup
	group.Add(eventCount)
	for eventNumber := range eventCount {
		go func() {
			defer group.Done()
			log.Info("параллельное событие", logger.Int("event_number", eventNumber))
		}()
	}
	group.Wait()

	scanner := bufio.NewScanner(bytes.NewReader(output.Bytes()))
	writtenEvents := 0
	for scanner.Scan() {
		if !json.Valid(scanner.Bytes()) {
			t.Fatalf("повреждённая JSON-запись: %q", scanner.Text())
		}
		writtenEvents++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("прочитать лог: %v", err)
	}
	if writtenEvents != eventCount {
		t.Errorf("записано событий: ожидалось %d, получено %d", eventCount, writtenEvents)
	}
}

func ExampleNew() {
	var output bytes.Buffer
	log, err := logger.New(logger.Config{
		Service:     "auth",
		Version:     "1.0.0",
		Environment: "test",
		Output:      &output,
	})
	if err != nil {
		fmt.Println("ошибка создания логгера")
		return
	}

	log.Info("готово", logger.String("operation", "login"))

	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		fmt.Println("ошибка разбора события")
		return
	}
	fmt.Println(event["level"], event["service"], event["operation"], event["message"])
	// Output: info auth login готово
}

func validConfig(output *bytes.Buffer, level string) logger.Config {
	return logger.Config{
		Service:     "auth",
		Version:     "1.0.0",
		Environment: "test",
		Level:       level,
		Output:      output,
	}
}

func decodeEvent(t *testing.T, data []byte) map[string]any {
	t.Helper()

	var event map[string]any
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("разобрать JSON-событие %q: %v", data, err)
	}

	return event
}

func assertStringField(t *testing.T, event map[string]any, field string, expected string) {
	t.Helper()

	actual, ok := event[field].(string)
	if !ok {
		t.Fatalf("поле %s должно быть строкой, получено: %#v", field, event[field])
	}
	if actual != expected {
		t.Errorf("поле %s: ожидалось %q, получено %q", field, expected, actual)
	}
}
