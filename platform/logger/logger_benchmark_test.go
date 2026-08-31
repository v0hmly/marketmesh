package logger_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/rs/zerolog"
	"github.com/v0hmly/marketmesh/platform/logger"
)

func BenchmarkLogger(b *testing.B) {
	zerologLogger := zerolog.New(zerolog.SyncWriter(io.Discard)).
		Level(zerolog.InfoLevel).
		With().
		Str("service", "auth").
		Str("version", "1.0.0").
		Str("environment", "benchmark").
		Logger()
	wrappedLogger, err := logger.New(logger.Config{
		Service:     "auth",
		Version:     "1.0.0",
		Environment: "benchmark",
		Output:      io.Discard,
	})
	if err != nil {
		b.Fatalf("создать логгер: %v", err)
	}
	nativeSlog := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	b.Run("zerolog", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			zerologLogger.Info().
				Timestamp().
				Str("operation", "login").
				Int("attempt", 1).
				Msg("готово")
		}
	})

	b.Run("wrapper", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			wrappedLogger.Info(
				"готово",
				logger.String("operation", "login"),
				logger.Int("attempt", 1),
			)
		}
	})

	b.Run("zerolog_disabled", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			zerologLogger.Debug().
				Str("operation", "login").
				Int("attempt", 1).
				Msg("готово")
		}
	})

	b.Run("wrapper_disabled", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			wrappedLogger.Debug(
				"готово",
				logger.String("operation", "login"),
				logger.Int("attempt", 1),
			)
		}
	})

	b.Run("wrapper_context_without_span", func(b *testing.B) {
		ctx := context.Background()
		b.ReportAllocs()
		for b.Loop() {
			wrappedLogger.InfoContext(
				ctx,
				"готово",
				logger.String("operation", "login"),
				logger.Int("attempt", 1),
			)
		}
	})

	b.Run("wrapper_masked", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			wrappedLogger.Info("готово", logger.String("token", "secret"))
		}
	})

	b.Run("wrapper_with_bound_fields", func(b *testing.B) {
		componentLogger := wrappedLogger.With(
			logger.String("operation", "login"),
			logger.Int("attempt", 1),
		)
		b.ReportAllocs()
		for b.Loop() {
			componentLogger.Info("готово")
		}
	})

	b.Run("wrapper_enabled", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = wrappedLogger.Enabled(logger.LevelInfo)
		}
	})

	b.Run("slog_adapter", func(b *testing.B) {
		slogLogger := wrappedLogger.Slog()
		ctx := context.Background()
		b.ReportAllocs()
		for b.Loop() {
			slogLogger.LogAttrs(
				ctx,
				slog.LevelInfo,
				"готово",
				slog.String("operation", "login"),
				slog.Int("attempt", 1),
			)
		}
	})

	b.Run("slog_json_handler", func(b *testing.B) {
		ctx := context.Background()
		b.ReportAllocs()
		for b.Loop() {
			nativeSlog.LogAttrs(
				ctx,
				slog.LevelInfo,
				"готово",
				slog.String("operation", "login"),
				slog.Int("attempt", 1),
			)
		}
	})
}
