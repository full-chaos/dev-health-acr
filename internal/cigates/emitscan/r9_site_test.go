package emitscan_test

import (
	"strings"
	"testing"
)

// r9Sites lists "func|detail" for every R9 finding of one source.
func r9Sites(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	for _, finding := range scanSource(t, header+body+"\n") {
		if finding.Rule == "R9" {
			out = append(out, finding.Func+"|"+finding.Detail)
		}
	}
	return out
}

func TestR9FindingNamesItsFunctionAndKeyText(t *testing.T) {
	got := r9Sites(t, `func first(l *slog.Logger, k string) { l.Info("m", k, 1) }`)
	if len(got) != 1 || got[0] != "first|unresolvable key: k" {
		t.Fatalf("R9 sites = %q, want [first|unresolvable key: k]", got)
	}
}

// Fixing one unresolvable key and adding a different one in the same function keeps the count; the pin must change.
func TestR9SwappedKeyChangesThePin(t *testing.T) {
	before := r9Sites(t, `func f(l *slog.Logger, a, b string) { l.Info("m", a, 1) }`)
	after := r9Sites(t, `func f(l *slog.Logger, a, b string) { l.Info("m", b, 1) }`)
	if len(before) != 1 || len(after) != 1 {
		t.Fatalf("R9 sites before %q after %q, want one each", before, after)
	}
	if before[0] == after[0] {
		t.Fatalf("a different key gives the same pin: %q", before[0])
	}
}

func TestR9SameKeyInTwoFunctionsIsTwoPins(t *testing.T) {
	got := r9Sites(t, `func one(l *slog.Logger, k string) { l.Info("m", k, 1) }
func two(l *slog.Logger, k string) { l.Info("m", k, 1) }`)
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("R9 sites = %q, want two distinct pins", got)
	}
}

func TestR9LongKeyTextIsCutAndDigested(t *testing.T) {
	long := strings.Repeat("a", 120)
	got := r9Sites(t, `func f(l *slog.Logger, `+long+` string) { l.Info("m", `+long+`, 1) }`)
	if len(got) != 1 || len(got[0]) > 140 || !strings.Contains(got[0], "...#") {
		t.Fatalf("R9 sites = %q, want a cut text with a digest", got)
	}
}

func TestR9KeyTextIsOneLine(t *testing.T) {
	got := r9Sites(t, "func f(l *slog.Logger, p, q string) { l.Info(\"m\", p+\n\t\tq, 1) }")
	if len(got) != 1 || strings.ContainsAny(got[0], "\t\n") {
		t.Fatalf("R9 sites = %q, want one line without a tab", got)
	}
}

// Two methods of one name on different receiver types are two sites: moving a finding between them changes the pin.
func TestR9SameNamedMethodsOnTwoReceiversAreTwoPins(t *testing.T) {
	before := r9Sites(t, `type a struct{}
type b struct{}
func (x *a) emit(l *slog.Logger, k string) { l.Info("m", k, 1) }
func (x *b) emit(l *slog.Logger) {}`)
	after := r9Sites(t, `type a struct{}
type b struct{}
func (x *a) emit(l *slog.Logger) {}
func (x *b) emit(l *slog.Logger, k string) { l.Info("m", k, 1) }`)
	if len(before) != 1 || len(after) != 1 || before[0] == after[0] {
		t.Fatalf("R9 sites before %q after %q, want one each and different", before, after)
	}
}

func TestR9MethodSiteNamesItsReceiver(t *testing.T) {
	got := r9Sites(t, `type a struct{}
type g[T any] struct{}
func (x *a) p(l *slog.Logger, k string) { l.Info("m", k, 1) }
func (x a) v(l *slog.Logger, k string) { l.Info("m", k, 1) }
func (x *g[T]) q(l *slog.Logger, k string) { l.Info("m", k, 1) }`)
	want := []string{"(*a).p|unresolvable key: k", "a.v|unresolvable key: k", "(*g).q|unresolvable key: k"}
	if strings.Join(got, ";") != strings.Join(want, ";") {
		t.Fatalf("R9 sites = %q, want %q", got, want)
	}
}

// Whitespace inside a string literal is content: a changed literal changes the pin.
func TestR9WhitespaceInsideStringLiteralChangesThePin(t *testing.T) {
	before := r9Sites(t, `func f(l *slog.Logger, k string) { l.Info("m", "prefix  suffix"+k, 1) }`)
	after := r9Sites(t, `func f(l *slog.Logger, k string) { l.Info("m", "prefix suffix"+k, 1) }`)
	if len(before) != 1 || len(after) != 1 || before[0] == after[0] {
		t.Fatalf("R9 sites before %q after %q, want one each and different", before, after)
	}
}

func TestR9RawLiteralLineBreakStaysOneLine(t *testing.T) {
	got := r9Sites(t, "func f(l *slog.Logger, k string) { l.Info(\"m\", `a\n\tb`+k, 1) }")
	if len(got) != 1 || strings.ContainsAny(got[0], "\t\n") {
		t.Fatalf("R9 sites = %q, want one line without a tab", got)
	}
}
