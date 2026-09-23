package logger

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/trace"
)

const localEnvironment = "local"

// Config описывает отдельный экземпляр логгера.
type Config struct {
	Service     string
	Version     string
	Environment string
	Level       string
	Output      io.Writer
	Console     bool
	MaskFields  []string
	MaskValue   string
}

// Logger — типизированная обёртка над zerolog.
//
// Logger не раскрывает низкоуровневый zerolog.Logger, чтобы все структурные
// поля проходили через единый конвейер маскирования.
type Logger struct {
	core   zerolog.Logger
	masker fieldMasker
}

// New создаёт независимый Logger.
//
// По умолчанию события записываются в JSON в os.Stdout с уровнем info.
// Человекочитаемый ConsoleWriter разрешён только для окружения local.
func New(config Config) (*Logger, error) {
	if strings.TrimSpace(config.Service) == "" {
		return nil, errors.New("logger: service must not be empty")
	}
	if strings.TrimSpace(config.Version) == "" {
		return nil, errors.New("logger: version must not be empty")
	}
	if strings.TrimSpace(config.Environment) == "" {
		return nil, errors.New("logger: environment must not be empty")
	}
	if config.Console && config.Environment != localEnvironment {
		return nil, errors.New("logger: console output is allowed only in local environment")
	}

	level, err := parseLevel(config.Level)
	if err != nil {
		return nil, err
	}
	masker, err := newFieldMasker(config.MaskFields, config.MaskValue)
	if err != nil {
		return nil, err
	}

	output := config.Output
	if output == nil {
		output = os.Stdout
	}
	if config.Console {
		output = zerolog.ConsoleWriter{
			Out:        output,
			NoColor:    true,
			TimeFormat: time.RFC3339,
		}
	}

	core := zerolog.New(zerolog.SyncWriter(output)).
		Level(level).
		With().
		Str("service", config.Service).
		Str("version", config.Version).
		Str("environment", config.Environment).
		Logger()

	return &Logger{
		core:   core,
		masker: masker,
	}, nil
}

// With создаёт дочерний Logger с полями, добавляемыми к каждому событию.
// Исходный Logger не изменяется.
func (l *Logger) With(fields ...Field) *Logger {
	if len(fields) == 0 {
		return l
	}

	context := l.core.With()
	for index := range fields {
		context = l.addContextField(context, &fields[index])
	}

	return &Logger{
		core:   context.Logger(),
		masker: l.masker,
	}
}

// Enabled сообщает, будет ли событие указанного уровня записано.
// Неизвестный уровень всегда отключён.
func (l *Logger) Enabled(level Level) bool {
	zerologLevel, valid := level.zerologLevel()
	return valid && l.enabled(zerologLevel)
}

// Trace записывает событие уровня trace.
func (l *Logger) Trace(message string, fields ...Field) {
	event := l.core.Trace()
	if event == nil {
		return
	}

	l.write(event, message, fields)
}

// TraceContext записывает событие уровня trace и добавляет идентификаторы
// активного OpenTelemetry span из ctx.
func (l *Logger) TraceContext(ctx context.Context, message string, fields ...Field) {
	event := l.core.Trace()
	if event == nil {
		return
	}

	l.writeContext(ctx, event, message, fields)
}

// Debug записывает событие уровня debug.
func (l *Logger) Debug(message string, fields ...Field) {
	event := l.core.Debug()
	if event == nil {
		return
	}

	l.write(event, message, fields)
}

// DebugContext записывает событие уровня debug и добавляет идентификаторы
// активного OpenTelemetry span из ctx.
func (l *Logger) DebugContext(ctx context.Context, message string, fields ...Field) {
	event := l.core.Debug()
	if event == nil {
		return
	}

	l.writeContext(ctx, event, message, fields)
}

// Info записывает событие уровня info.
func (l *Logger) Info(message string, fields ...Field) {
	event := l.core.Info()
	if event == nil {
		return
	}

	l.write(event, message, fields)
}

// InfoContext записывает событие уровня info и добавляет идентификаторы
// активного OpenTelemetry span из ctx.
func (l *Logger) InfoContext(ctx context.Context, message string, fields ...Field) {
	event := l.core.Info()
	if event == nil {
		return
	}

	l.writeContext(ctx, event, message, fields)
}

// Warn записывает событие уровня warn.
func (l *Logger) Warn(message string, fields ...Field) {
	event := l.core.Warn()
	if event == nil {
		return
	}

	l.write(event, message, fields)
}

// WarnContext записывает событие уровня warn и добавляет идентификаторы
// активного OpenTelemetry span из ctx.
func (l *Logger) WarnContext(ctx context.Context, message string, fields ...Field) {
	event := l.core.Warn()
	if event == nil {
		return
	}

	l.writeContext(ctx, event, message, fields)
}

// Error записывает событие уровня error.
func (l *Logger) Error(message string, fields ...Field) {
	event := l.core.Error()
	if event == nil {
		return
	}

	l.write(event, message, fields)
}

// ErrorContext записывает событие уровня error и добавляет идентификаторы
// активного OpenTelemetry span из ctx.
func (l *Logger) ErrorContext(ctx context.Context, message string, fields ...Field) {
	event := l.core.Error()
	if event == nil {
		return
	}

	l.writeContext(ctx, event, message, fields)
}

// Fatal записывает событие уровня fatal и завершает процесс с кодом 1.
func (l *Logger) Fatal(message string, fields ...Field) {
	event := l.core.Fatal()
	l.write(event, message, fields)
}

// FatalContext записывает событие уровня fatal, добавляет идентификаторы
// активного OpenTelemetry span из ctx и завершает процесс с кодом 1.
func (l *Logger) FatalContext(ctx context.Context, message string, fields ...Field) {
	event := l.core.Fatal()
	l.writeContext(ctx, event, message, fields)
}

// Panic записывает событие уровня panic и вызывает panic с message.
func (l *Logger) Panic(message string, fields ...Field) {
	event := l.core.Panic()
	l.write(event, message, fields)
}

// PanicContext записывает событие уровня panic, добавляет идентификаторы
// активного OpenTelemetry span из ctx и вызывает panic с message.
func (l *Logger) PanicContext(ctx context.Context, message string, fields ...Field) {
	event := l.core.Panic()
	l.writeContext(ctx, event, message, fields)
}

func (l *Logger) write(event *zerolog.Event, message string, fields []Field) {
	event.Timestamp()
	l.addFields(event, fields)
	event.Msg(message)
}

func (l *Logger) writeContext(
	ctx context.Context,
	event *zerolog.Event,
	message string,
	fields []Field,
) {
	event.Timestamp()
	l.addTraceContext(ctx, event)
	l.addFields(event, fields)
	event.Msg(message)
}

func (l *Logger) addFields(event *zerolog.Event, fields []Field) {
	for index := range fields {
		l.addField(event, &fields[index])
	}
}

func (l *Logger) addTraceContext(ctx context.Context, event *zerolog.Event) {
	if ctx == nil {
		return
	}

	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return
	}

	event.
		Str("trace_id", spanContext.TraceID().String()).
		Str("span_id", spanContext.SpanID().String())
}

func (l *Logger) enabled(level zerolog.Level) bool {
	configuredLevel := l.core.GetLevel()
	return configuredLevel != zerolog.Disabled &&
		level >= configuredLevel &&
		level >= zerolog.GlobalLevel()
}

func parseLevel(value string) (zerolog.Level, error) {
	if strings.TrimSpace(value) == "" {
		return zerolog.InfoLevel, nil
	}

	level, err := zerolog.ParseLevel(strings.TrimSpace(value))
	if err != nil {
		return zerolog.NoLevel, fmt.Errorf("logger: parse level %q: %w", value, err)
	}

	return level, nil
}
