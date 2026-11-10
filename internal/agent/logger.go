package agent

import (
	"context"
	"log/slog"
)

// maskHandler wraps a slog.Handler and masks secret values in the message
// and in string and error attributes. Plugins receive a logger built on it,
// so a plugin that logs a secret by mistake still does not leak it.
type maskHandler struct {
	next slog.Handler
	m    *masker
}

func newMaskedLogger(l *slog.Logger, m *masker) *slog.Logger {
	return slog.New(&maskHandler{next: l.Handler(), m: m})
}

func (h *maskHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *maskHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, h.m.Mask(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h *maskHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	masked := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		masked[i] = h.attr(a)
	}
	return &maskHandler{next: h.next.WithAttrs(masked), m: h.m}
}

func (h *maskHandler) WithGroup(name string) slog.Handler {
	return &maskHandler{next: h.next.WithGroup(name), m: h.m}
}

func (h *maskHandler) attr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.m.Mask(a.Value.String()))
	case slog.KindGroup:
		group := a.Value.Group()
		masked := make([]any, len(group))
		for i, g := range group {
			masked[i] = h.attr(g)
		}
		return slog.Group(a.Key, masked...)
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok {
			return slog.String(a.Key, h.m.Mask(err.Error()))
		}
		return slog.String(a.Key, h.m.Mask(a.Value.String()))
	}
	return a
}
