package nebula

import (
	"context"
	"log/slog"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// zapHandler routes Nebula's log/slog output into the agent's zap logger.
//
// Nebula v1.11 logs through log/slog, and nebula.Main requires a *slog.Logger.
// Handing it a handler of its own would put Nebula's output somewhere other than
// the agent's log file, outside the rotation configured in internal/agent — so
// everything Nebula says arrives as an ordinary agent log line instead, tagged
// with the "nebula" component.
//
// This exists rather than a dependency on go.uber.org/zap/exp/zapslog because it
// is forty lines and that is a separate, experimental module.
type zapHandler struct {
	logger *zap.Logger
	attrs  []zapcore.Field
	groups []string
}

func newSlogLogger(logger *zap.Logger) *slog.Logger {
	return slog.New(&zapHandler{logger: logger.With(zap.String("component", "nebula"))})
}

func (h *zapHandler) Enabled(_ context.Context, level slog.Level) bool {
	return h.logger.Core().Enabled(zapLevel(level))
}

func (h *zapHandler) Handle(_ context.Context, r slog.Record) error {
	fields := make([]zapcore.Field, 0, len(h.attrs)+r.NumAttrs())
	fields = append(fields, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		fields = append(fields, zap.Any(h.qualify(a.Key), a.Value.Any()))
		return true
	})

	if ce := h.logger.Check(zapLevel(r.Level), r.Message); ce != nil {
		ce.Write(fields...)
	}
	return nil
}

func (h *zapHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := h.clone()
	for _, a := range attrs {
		next.attrs = append(next.attrs, zap.Any(h.qualify(a.Key), a.Value.Any()))
	}
	return next
}

func (h *zapHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := h.clone()
	next.groups = append(next.groups, name)
	return next
}

func (h *zapHandler) clone() *zapHandler {
	return &zapHandler{
		logger: h.logger,
		attrs:  append([]zapcore.Field(nil), h.attrs...),
		groups: append([]string(nil), h.groups...),
	}
}

// qualify prefixes a key with any open slog groups, so grouped attributes do not
// collide with top-level ones of the same name.
func (h *zapHandler) qualify(key string) string {
	for i := len(h.groups) - 1; i >= 0; i-- {
		key = h.groups[i] + "." + key
	}
	return key
}

func zapLevel(l slog.Level) zapcore.Level {
	switch {
	case l >= slog.LevelError:
		return zapcore.ErrorLevel
	case l >= slog.LevelWarn:
		return zapcore.WarnLevel
	case l >= slog.LevelInfo:
		return zapcore.InfoLevel
	default:
		return zapcore.DebugLevel
	}
}
