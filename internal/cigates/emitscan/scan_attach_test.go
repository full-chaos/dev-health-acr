package emitscan_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
)

func hasRule(got, rule string) bool {
	for _, r := range strings.Split(got, ",") {
		if r == rule {
			return true
		}
	}
	return false
}

// A logger or a handler carries keys and attributes through With, WithGroup and WithAttrs as well as through log calls.
func TestSlogAttachmentMethodsAreRead(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"WithGroup protected key", "func f(l *slog.Logger) { _ = l.WithGroup(\"api_key\") }", "R1"},
		{"WithGroup unresolvable key", "func f(l *slog.Logger, g string) { _ = l.WithGroup(g) }", "R9"},
		{"handler WithAttrs variable", "func f(h slog.Handler, a slog.Attr) { _ = h.WithAttrs([]slog.Attr{a}) }", "R9"},
		{"handler WithAttrs variable slice", "func f(h slog.Handler, a []slog.Attr) { _ = h.WithAttrs(a) }", "R9"},
		{"handler WithAttrs protected constructor", "func f(h slog.Handler, v string) { _ = h.WithAttrs([]slog.Attr{slog.String(\"api_key\", v)}) }", "R1"},
		{"handler WithGroup protected key", "func f(h slog.Handler) { _ = h.WithGroup(\"password\") }", "R1"},
	}
	for _, tc := range cases {
		body := strings.ReplaceAll(tc.body, "\\\\\"", "\"")
		t.Run(tc.name, func(t *testing.T) {
			if got := rules(scanSource(t, header+body+"\n")); !hasRule(got, tc.want) {
				t.Fatalf("rules = %q, want %s", got, tc.want)
			}
		})
	}
	clean := header + "func f(l *slog.Logger, h slog.Handler) { _ = l.WithGroup(\"request\"); _ = h.WithAttrs([]slog.Attr{slog.String(\"route\", \"x\")}) }\n"
	if got := rules(scanSource(t, clean)); got != "" {
		t.Fatalf("a constant group and a constant attribute: rules = %q, want none", got)
	}
}

// An attribute set reaches a metric only through metric.WithAttributeSet; one built in a helper or through an alias of
// attribute.NewSet cannot be read at the call and is refused like any label set built outside it.
func TestAttributeSetsBehindHelpersAreRefused(t *testing.T) {
	cases := map[string]string{
		"aliased NewSet in a helper": "var newSet = attribute.NewSet\n" +
			"func helper(v string) attribute.Set { return newSet(attribute.String(\"status\", v)) }\n" +
			"func f(c metric.Int64Counter, v string) { c.Add(context.Background(), 1, metric.WithAttributeSet(helper(v))) }\n",
		"direct NewSet with a variable value": "func f(c metric.Int64Counter, v string) { c.Add(context.Background(), 1, metric.WithAttributeSet(attribute.NewSet(attribute.String(\"status\", v)))) }\n",
		"aliased NewSet at the call": "var newSet = attribute.NewSet\n" +
			"func f(c metric.Int64Counter, v string) { c.Add(context.Background(), 1, metric.WithAttributeSet(newSet(attribute.String(\"status\", v)))) }\n",
		"a set held in a variable": "func f(c metric.Int64Counter, s attribute.Set) { c.Add(context.Background(), 1, metric.WithAttributeSet(s)) }\n",
	}
	for name, body := range cases {
		if got := rules(scanSource(t, header+body)); !hasRule(got, "R2") {
			t.Errorf("%s: rules = %q, want R2", name, got)
		}
	}
	clean := "func f(c metric.Int64Counter) { c.Add(context.Background(), 1, metric.WithAttributeSet(attribute.NewSet(attribute.String(\"outcome\", \"ok\")))) }\n"
	if got := rules(scanSource(t, header+clean)); got != "" {
		t.Errorf("a constant set built at the call: rules = %q, want none", got)
	}
}

// flipCtx reports no error for its first calls to Err and context.Canceled afterwards.
type flipCtx struct {
	context.Context
	calls, flipAt int
}

func (c *flipCtx) Err() error {
	c.calls++
	if c.calls >= c.flipAt {
		return context.Canceled
	}
	return nil
}

// A scan during which the context is cancelled never returns success: a partial census is never a pass.
func TestScanNeverSucceedsAfterCancellation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "sample")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sample.go"), []byte(header+"func f(l *slog.Logger) { l.Info(\"x\", \"a\", 1) }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for flipAt := 1; flipAt <= 40; flipAt++ {
		ctx := &flipCtx{Context: context.Background(), flipAt: flipAt}
		_, _, err := emitscan.ScanContext(ctx, root, []string{"internal"})
		if err == nil && ctx.calls >= flipAt {
			t.Fatalf("flipAt=%d: the context was cancelled during the scan (%d Err calls) and the scan returned success", flipAt, ctx.calls)
		}
	}
}
