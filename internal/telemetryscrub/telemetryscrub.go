// Package telemetryscrub redacts log records before any handler sees them.
package telemetryscrub

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"regexp"
	"strings"
)

const Redacted = "[REDACTED]"

var protectedFragments = []string{
	"token", "secret", "password", "passwd", "credential", "authorization", "bearer",
	"cookie", "api_key", "apikey", "api-key", "signature", "dsn", "jwt", "session", "private_key",
}

var benignKeys = map[string]bool{
	"max_output_tokens":                    true,
	"output_tokens":                        true,
	"input_tokens":                         true,
	"packet_tokens":                        true,
	"devhealthfacts.blocker_authorization": true,
}

var networkKeys = map[string]bool{"remote_ip": true, "client_ip": true, "client.address": true}

var valueShapes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]*`),
	regexp.MustCompile(`-----BEGIN [A-Z ]+-----`),
	regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s/:@]+:[^\s/@]+@`),
	regexp.MustCompile(`\b(?:sk|ghp|gho|ghs|xox[abp])[-_][A-Za-z0-9_-]{16,}`),
}

// ErrorClass names an error's dynamic type; the message is never returned.
func ErrorClass(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%T", err)
}

func protectedKey(key string) bool {
	k := strings.ToLower(key)
	if benignKeys[k] {
		return false
	}
	for _, f := range protectedFragments {
		if strings.Contains(k, f) {
			return true
		}
	}
	return false
}

func scrubString(s string) string {
	for _, re := range valueShapes {
		s = re.ReplaceAllString(s, Redacted)
	}
	return s
}

func networkPrefix(raw string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return Redacted
	}
	addr = addr.Unmap()
	bits := 24
	if addr.Is6() {
		bits = 48
	}
	p, err := addr.Prefix(bits)
	if err != nil {
		return Redacted
	}
	return p.String()
}

func scrubAttr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		group := a.Value.Group()
		out := make([]slog.Attr, 0, len(group))
		for _, g := range group {
			out = append(out, scrubAttr(g))
		}
		a.Value = slog.GroupValue(out...)
		return a
	}
	switch {
	case protectedKey(a.Key):
		a.Value = slog.StringValue(Redacted)
	case networkKeys[strings.ToLower(a.Key)]:
		a.Value = slog.StringValue(networkPrefix(a.Value.String()))
	default:
		switch a.Value.Kind() {
		case slog.KindString:
			a.Value = slog.StringValue(scrubString(a.Value.String()))
		case slog.KindAny:
			if err, ok := a.Value.Any().(error); ok {
				a.Value = slog.StringValue(ErrorClass(err))
			} else {
				a.Value = slog.StringValue(scrubString(a.Value.String()))
			}
		}
	}
	return a
}

type handler struct{ next slog.Handler }

// Handler returns next behind a scrubber: every attribute, record or bound, is scrubbed first.
func Handler(next slog.Handler) slog.Handler { return handler{next: next} }

func (h handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h handler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, scrubString(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(scrubAttr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		out = append(out, scrubAttr(a))
	}
	return handler{next: h.next.WithAttrs(out)}
}

func (h handler) WithGroup(name string) slog.Handler { return handler{next: h.next.WithGroup(name)} }
