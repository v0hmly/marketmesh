package logger

import "github.com/rs/zerolog"

// Level задаёт уровень события для быстрой проверки через Logger.Enabled.
type Level uint8

const (
	// LevelUnknown обозначает неизвестный уровень и всегда отключён.
	LevelUnknown Level = iota
	// LevelTrace обозначает уровень trace.
	LevelTrace
	// LevelDebug обозначает уровень debug.
	LevelDebug
	// LevelInfo обозначает уровень info.
	LevelInfo
	// LevelWarn обозначает уровень warn.
	LevelWarn
	// LevelError обозначает уровень error.
	LevelError
	// LevelFatal обозначает уровень fatal.
	LevelFatal
	// LevelPanic обозначает уровень panic.
	LevelPanic
)

func (level Level) zerologLevel() (zerolog.Level, bool) {
	switch level {
	case LevelTrace:
		return zerolog.TraceLevel, true
	case LevelDebug:
		return zerolog.DebugLevel, true
	case LevelInfo:
		return zerolog.InfoLevel, true
	case LevelWarn:
		return zerolog.WarnLevel, true
	case LevelError:
		return zerolog.ErrorLevel, true
	case LevelFatal:
		return zerolog.FatalLevel, true
	case LevelPanic:
		return zerolog.PanicLevel, true
	default:
		return zerolog.NoLevel, false
	}
}
