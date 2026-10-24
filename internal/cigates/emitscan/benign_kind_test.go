package emitscan_test

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
	"github.com/full-chaos/dev-health-acr/internal/telemetryscrub"
)

func benignDetails(findings []emitscan.Finding) string {
	var out []string
	for _, finding := range findings {
		if finding.Rule == emitscan.RuleBenignKind {
			out = append(out, finding.Detail)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestBenignKindFixtures(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"slog.Int constructor", `func f(n int) { slog.Info("m", slog.Int("pass", n)) }`, ""},
		{"slog.Int64 constructor", `func f(n int64) { slog.Info("m", slog.Int64("authorization_batches", n)) }`, ""},
		{"slog.Bool constructor", `func f(b bool) { slog.Info("m", slog.Bool("coverage_bypass", b)) }`, ""},
		{"slog.String constructor on the string key", `func f(s string) { slog.Info("m", slog.String("authorization_reason", s)) }`, ""},
		{"slog.Uint64 constructor", `func f(n uint64) { slog.Info("m", slog.Uint64("pass", n)) }`, "mismatch:pass"},
		{"slog.String constructor on an int key", `func f(s string) { slog.Info("m", slog.String("authorization_batches", s)) }`, "mismatch:authorization_batches"},
		{"slog.Any constructor on a bool key", `func f(v any) { slog.Info("m", slog.Any("coverage_bypass", v)) }`, "mismatch:coverage_bypass"},
		{"int literal", `func f(l *slog.Logger) { l.Info("m", "pass", 3) }`, ""},
		{"len call", `func f(l *slog.Logger, x []int) { l.Info("m", "authorization_batches", len(x)) }`, ""},
		{"int64 conversion", `func f(l *slog.Logger, n uint) { l.Info("m", "authorization_batches", int64(n)) }`, ""},
		{"comparison is a bool", `func f(l *slog.Logger, a, b int) { l.Info("m", "coverage_bypass", a == b) }`, ""},
		{"not is a bool", `func f(l *slog.Logger, a bool) { l.Info("m", "coverage_bypass", !a) }`, ""},
		{"true literal", `func f(l *slog.Logger) { l.Info("m", "coverage_bypass", true) }`, ""},
		{"string conversion on the string key", `type R string
func f(l *slog.Logger, r R) { l.Info("m", "authorization_reason", string(r)) }`, ""},
		{"int field of a package struct", `type ev struct{ Batches int }
func f(l *slog.Logger, e ev) { l.Info("m", "authorization_batches", e.Batches) }`, ""},
		{"bool field of a package struct", `type ev struct{ Bypass bool }
func f(l *slog.Logger, e ev) { l.Info("m", "coverage_bypass", e.Bypass) }`, ""},
		{"int field in an any slice", `type ev struct{ Batches int }
func f(e ev) []any { return []any{"authorization_batches", e.Batches} }`, ""},
		{"int field in an append", `type ev struct{ Pass int }
func f(args []any, e ev) []any { return append(args, "pass", e.Pass) }`, ""},
		{"interface{} slice", `func f() []interface{} { return []interface{}{"pass", 2} }`, ""},
		{"uint conversion", `func f(l *slog.Logger) { l.Info("m", "pass", uint(3)) }`, "mismatch:pass"},
		{"uint field", `type ev struct{ Batches uint }
func f(l *slog.Logger, e ev) { l.Info("m", "authorization_batches", e.Batches) }`, "mismatch:authorization_batches"},
		{"named int field boxes as any", `type count int
type ev struct{ Pass count }
func f(l *slog.Logger, e ev) { l.Info("m", "pass", e.Pass) }`, "mismatch:pass"},
		{"string literal on a bool key", `func f(l *slog.Logger) { l.Info("m", "coverage_bypass", "yes") }`, "mismatch:coverage_bypass"},
		{"float literal on an int key", `func f(l *slog.Logger) { l.Info("m", "pass", 1.5) }`, "mismatch:pass"},
		{"int on the string key", `func f(l *slog.Logger) { l.Info("m", "authorization_reason", 3) }`, "mismatch:authorization_reason"},
		{"uint field in an any slice", `type ev struct{ Batches uint }
func f(e ev) []any { return []any{"authorization_batches", e.Batches} }`, "mismatch:authorization_batches"},
		{"uint in an append", `func f(args []any) []any { return append(args, "pass", uint8(1)) }`, "mismatch:pass"},
		{"unknown call result", `func g() int { return 1 }
func f(l *slog.Logger) { l.Info("m", "pass", g()) }`, "pass"},
		{"unknown variable", `func f(l *slog.Logger, n int) { x := n; l.Info("m", "pass", x) }`, "pass"},
		{"field name declared with two kinds", `type a struct{ N int }
type b struct{ N uint }
func f(l *slog.Logger, e a) { l.Info("m", "pass", e.N) }`, "pass"},
		{"field of a struct the package does not declare", `func f(l *slog.Logger, r struct{ Count int }) { l.Info("m", "pass", r.Other) }`, "pass"},
		{"arithmetic of unknowns", `func f(l *slog.Logger, a, b int) { l.Info("m", "pass", a+b) }`, "pass"},
		{"non-benign key with an unsigned value", `func f(l *slog.Logger) { l.Info("m", "count", uint(1)) }`, ""},
		{"benign word as a value, not a key", `func f() []any { return []any{"note", "pass"} }`, ""},
		{"lone key without a value", `func f() []any { return []any{"pass"} }`, ""},
		{"key in a map is not a log pair", `func f() map[string]int { return map[string]int{"pass": 1} }`, ""},
	}
	for _, c := range cases {
		got := benignDetails(scanSource(t, header+c.body+"\n"))
		if got != c.want {
			t.Errorf("%s: R10 details %q want %q", c.name, got, c.want)
		}
	}
}

func benignRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

func TestNoBenignKeyCarriesAnotherKindInTheTree(t *testing.T) {
	findings, err := emitscan.Scan(benignRepoRoot(t), []string{"internal", "cmd"})
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.Rule == emitscan.RuleBenignKind && strings.HasPrefix(finding.Detail, "mismatch:") {
			t.Errorf("%s: %s is logged with a value of a kind the scrubber does not exempt for that key", finding.Where(), strings.TrimPrefix(finding.Detail, "mismatch:"))
		}
	}
}

func TestPinsOfBenignKeysNameTheirKind(t *testing.T) {
	handle, err := os.Open("pins.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	scanner := bufio.NewScanner(handle)
	scanner.Scan()
	pinned := 0
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) != 6 {
			continue
		}
		rule, detail, reason := fields[0], fields[3], fields[5]
		kind := telemetryscrub.BenignKind(detail)
		if rule == emitscan.RuleBenignKind {
			if kind == "" {
				t.Errorf("pins.tsv: R10 row for %q, which is not a benign key (a mismatch cannot be pinned)", detail)
			}
		} else if rule != emitscan.RuleProtectedKey || kind == "" {
			continue
		}
		pinned++
		if want := "kind=" + kind; !strings.Contains(reason, want) {
			t.Errorf("pins.tsv: %s %s %s must say %q in its reason: %q", rule, fields[1], detail, want, reason)
		}
	}
	if pinned == 0 {
		t.Error("pins.tsv holds no pin of a benign key: the check reads nothing")
	}
}
