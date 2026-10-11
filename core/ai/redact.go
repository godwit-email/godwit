package ai

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// redactor passes on Error and above only, with every secret value replaced by
// "[redacted]" in the message and attributes (spec 5.5).
type redactor struct {
	inner   slog.Handler
	secrets func() []string
}

func (h *redactor) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= slog.LevelError && h.inner.Enabled(ctx, l)
}

func (h *redactor) Handle(ctx context.Context, r slog.Record) error {
	clean := h.cleaner()
	out := slog.NewRecord(r.Time, r.Level, clean(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool { out.AddAttrs(cleanAttr(a, clean)); return true })
	return h.inner.Handle(ctx, out)
}

// WithAttrs redacts with the secrets known now. ponytail: a key added later is
// not redacted from attributes bound earlier; Genkit binds none of ours.
func (h *redactor) WithAttrs(as []slog.Attr) slog.Handler {
	clean := h.cleaner()
	for i := range as {
		as[i] = cleanAttr(as[i], clean)
	}
	return &redactor{h.inner.WithAttrs(as), h.secrets}
}

func (h *redactor) WithGroup(name string) slog.Handler {
	return &redactor{h.inner.WithGroup(name), h.secrets}
}

func (h *redactor) cleaner() func(string) string {
	secrets := h.secrets()
	return func(s string) string { return Redact(s, secrets) }
}

// Redact replaces each secret in s with "[redacted]". Secrets under 8 characters are skipped:
// replacing them by value would mangle ordinary text, and real keys are longer.
func Redact(s string, secrets []string) string {
	for _, k := range secrets {
		if len(k) >= 8 {
			s = strings.ReplaceAll(s, k, "[redacted]")
		}
	}
	return s
}

func cleanAttr(a slog.Attr, clean func(string) string) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		var as []any
		for _, g := range v.Group() {
			as = append(as, cleanAttr(g, clean))
		}
		return slog.Group(a.Key, as...)
	case slog.KindString:
		return slog.String(a.Key, clean(v.String()))
	case slog.KindAny:
		if s := fmt.Sprint(v.Any()); clean(s) != s {
			return slog.String(a.Key, clean(s))
		}
	}
	return slog.Attr{Key: a.Key, Value: v}
}
