package logger

import (
	"context"
	"encoding/json"
	stdlog "log"
	"log/slog"
	"slices"

	"github.com/rs/zerolog"
)

var _ slog.Handler = (*slogHandler)(nil)

// Slog создаёт стандартный slog.Logger, направляющий события в тот же zerolog
// конвейер с маскированием и корреляцией трассировки.
func (l *Logger) Slog() *slog.Logger {
	return slog.New(l.SlogHandler())
}

// SlogHandler создаёт slog.Handler поверх Logger.
func (l *Logger) SlogHandler() slog.Handler {
	return &slogHandler{logger: l}
}

// HTTPErrorLog создаёт *log.Logger для http.Server.ErrorLog.
func (l *Logger) HTTPErrorLog() *stdlog.Logger {
	return slog.NewLogLogger(l.SlogHandler(), slog.LevelError)
}

type slogHandler struct {
	logger *Logger
	attrs  []boundSlogAttr
	prefix string
}

type boundSlogAttr struct {
	prefix string
	attr   slog.Attr
}

func (h *slogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return h.logger.enabled(zerologLevelFromSlog(level))
}

func (h *slogHandler) Handle(ctx context.Context, record slog.Record) error {
	event := h.logger.core.WithLevel(zerologLevelFromSlog(record.Level))
	if event == nil {
		return nil
	}

	if record.Time.IsZero() {
		event.Timestamp()
	} else {
		event.Time(zerolog.TimestampFieldName, record.Time)
	}
	h.logger.addTraceContext(ctx, event)

	for _, boundAttr := range h.attrs {
		h.addAttr(event, boundAttr.prefix, boundAttr.attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		h.addAttr(event, h.prefix, attr)
		return true
	})

	event.Msg(record.Message)
	return nil
}

func (h *slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}

	boundAttrs := make([]boundSlogAttr, len(h.attrs), len(h.attrs)+len(attrs))
	copy(boundAttrs, h.attrs)
	for _, attr := range attrs {
		boundAttrs = append(boundAttrs, boundSlogAttr{prefix: h.prefix, attr: attr})
	}

	return &slogHandler{
		logger: h.logger,
		attrs:  boundAttrs,
		prefix: h.prefix,
	}
}

func (h *slogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	return &slogHandler{
		logger: h.logger,
		attrs:  slices.Clone(h.attrs),
		prefix: joinSlogKey(h.prefix, name),
	}
}

func (h *slogHandler) addAttr(event *zerolog.Event, prefix string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	rawKey := joinSlogKey(prefix, attr.Key)
	masked := h.logger.masker.masks(attr.Key)
	if !masked && rawKey != attr.Key {
		masked = h.logger.masker.masksCustom(rawKey)
	}
	if masked {
		event.Str(protectedFieldKey(rawKey), h.logger.masker.value)
		return
	}
	if attr.Value.Kind() == slog.KindGroup {
		h.addGroup(event, prefix, attr.Key, attr.Value.Group())
		return
	}

	key := protectedFieldKey(rawKey)

	switch attr.Value.Kind() {
	case slog.KindString:
		event.Str(key, attr.Value.String())
	case slog.KindBool:
		event.Bool(key, attr.Value.Bool())
	case slog.KindInt64:
		event.Int64(key, attr.Value.Int64())
	case slog.KindUint64:
		event.Uint64(key, attr.Value.Uint64())
	case slog.KindFloat64:
		event.Float64(key, attr.Value.Float64())
	case slog.KindDuration:
		event.Dur(key, attr.Value.Duration())
	case slog.KindTime:
		event.Time(key, attr.Value.Time())
	case slog.KindAny:
		h.addAny(event, key, attr.Value.Any())
	case slog.KindLogValuer:
		// Resolve выше преобразует LogValuer или возвращает KindAny с ошибкой.
	}
}

func (h *slogHandler) addGroup(
	event *zerolog.Event,
	prefix string,
	name string,
	attrs []slog.Attr,
) {
	groupPrefix := prefix
	if name != "" {
		groupPrefix = joinSlogKey(prefix, name)
	}
	for _, attr := range attrs {
		h.addAttr(event, groupPrefix, attr)
	}
}

func (h *slogHandler) addAny(event *zerolog.Event, key string, value any) {
	if err, ok := value.(error); ok {
		event.AnErr(key, err)
		return
	}
	if rawJSON, ok := value.(json.RawMessage); ok {
		if json.Valid(rawJSON) {
			event.RawJSON(key, rawJSON)
		} else {
			event.Str(key, invalidJSONValue)
		}
		return
	}

	event.Interface(key, value)
}

func zerologLevelFromSlog(level slog.Level) zerolog.Level {
	switch {
	case level < slog.LevelDebug:
		return zerolog.TraceLevel
	case level < slog.LevelInfo:
		return zerolog.DebugLevel
	case level < slog.LevelWarn:
		return zerolog.InfoLevel
	case level < slog.LevelError:
		return zerolog.WarnLevel
	default:
		return zerolog.ErrorLevel
	}
}

func joinSlogKey(prefix string, key string) string {
	if prefix == "" {
		return key
	}
	if key == "" {
		return prefix
	}

	return prefix + "." + key
}
