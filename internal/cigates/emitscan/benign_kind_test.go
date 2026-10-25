package emitscan_test

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
	"github.com/full-chaos/dev-health-acr/internal/cigates/sourceload"
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
		{"int call result is typed", `func g() int { return 1 }
func f(l *slog.Logger) { l.Info("m", "pass", g()) }`, ""},
		{"any call result is unprovable", `func g() any { return 1 }
func f(l *slog.Logger) { l.Info("m", "pass", g()) }`, "pass"},
		{"typed local variable", `func f(l *slog.Logger, n int) { x := n; l.Info("m", "pass", x) }`, ""},
		{"interface variable is unprovable", `func f(l *slog.Logger, v any) { l.Info("m", "pass", v) }`, "pass"},
		{"error value is unprovable", `func f(l *slog.Logger, err error) { l.Info("m", "pass", err) }`, "pass"},
		{"field name declared with two kinds resolves by receiver", `type a struct{ N int }
type b struct{ N uint }
func f(l *slog.Logger, e a, g b) { l.Info("m", "pass", e.N); l.Info("m", "pass", g.N) }`, "mismatch:pass"},
		{"field of an anonymous struct", `func f(l *slog.Logger, r struct{ Count int }) { l.Info("m", "pass", r.Count) }`, ""},
		{"arithmetic of ints", `func f(l *slog.Logger, a, b int) { l.Info("m", "pass", a+b) }`, ""},
		{"untyped constant expression", `func f(l *slog.Logger) { l.Info("m", "pass", 1<<3) }`, ""},
		{"rune literal boxes as an int", `func f(l *slog.Logger) { l.Info("m", "pass", 'a') }`, ""},
		{"non-benign key with an unsigned value", `func f(l *slog.Logger) { l.Info("m", "count", uint(1)) }`, ""},
		{"benign word as a value, not a key", `func f() []any { return []any{"note", "pass"} }`, ""},
		{"lone key without a value", `func f() []any { return []any{"pass"} }`, ""},
		{"Attr literal with a string value on an int key", `func f(l *slog.Logger) { l.LogAttrs(context.Background(), slog.LevelInfo, "m", slog.Attr{Key: "authorization_batches", Value: slog.StringValue("x")}) }`, "mismatch:authorization_batches"},
		{"Attr literal with a bool value in WithAttrs on an int key", `func f(h slog.Handler) { _ = h.WithAttrs([]slog.Attr{{Key: "pass", Value: slog.BoolValue(true)}}) }`, "mismatch:pass"},
		{"Attr literal with the right value kind", `func f(l *slog.Logger) { l.LogAttrs(context.Background(), slog.LevelInfo, "m", slog.Attr{Key: "authorization_batches", Value: slog.IntValue(1)}) }`, ""},
		{"Attr literal with an unknown value constructor", `func f(l *slog.Logger) { l.LogAttrs(context.Background(), slog.LevelInfo, "m", slog.Attr{Key: "pass", Value: slog.AnyValue(1)}) }`, "pass"},
		{"shadowed int function is not the builtin", `func int(x int8) bool { return true }
func f(l *slog.Logger) { l.Info("m", "authorization_batches", int(1)) }`, "mismatch:authorization_batches"},
		{"shadowed len function is not the builtin", `func len(x []int) bool { return true }
func f(l *slog.Logger, x []int) { l.Info("m", "authorization_batches", len(x)) }`, "mismatch:authorization_batches"},
		{"package-qualified selector is not a package field", `type ev struct{ DeadlineExceeded int }
var _ ev
func f(l *slog.Logger) { l.Info("m", "pass", context.DeadlineExceeded) }`, "pass"},
		{"control: same field name on a package value is still int", `type ev struct{ DeadlineExceeded int }
func f(l *slog.Logger, e ev) { l.Info("m", "pass", e.DeadlineExceeded) }`, ""},
		{"control: int alias field is an int", `type counter = int
type ev struct{ Batches counter }
func f(l *slog.Logger, e ev) { l.Info("m", "authorization_batches", e.Batches) }`, ""},
		{"chained int alias field is an int", `type base = int
type counter = base
type ev struct{ Batches counter }
func f(l *slog.Logger, e ev) { l.Info("m", "authorization_batches", e.Batches) }`, ""},
		{"uint alias field is a mismatch", `type counter = uint
type ev struct{ Batches counter }
func f(l *slog.Logger, e ev) { l.Info("m", "authorization_batches", e.Batches) }`, "mismatch:authorization_batches"},
		{"field of an undeclared named type is a refusal", `type ev struct{ Batches elsewhere }
func f(l *slog.Logger, e ev) { l.Info("m", "authorization_batches", e.Batches) }`, "refusal:typecheck"},
		{"method value collides with a field name", `type a struct{ Pass int }
type b struct{}
func (b) Pass() {}
var _ a
func f(l *slog.Logger, x b) { l.Info("m", "pass", x.Pass) }`, "mismatch:pass"},
		{"package-qualified error variable is unprovable", `type ev struct{ DeadlineExceeded int }
var _ ev
func f(l *slog.Logger) { l.Info("m", "pass", context.DeadlineExceeded) }`, "pass"},
		{"receiver named int shadows the builtin", `type conv func(int8) uint
func (int conv) use(l *slog.Logger) { l.Info("m", "pass", int(1)) }`, "mismatch:pass"},
		{"type parameter named int shadows the builtin", `func g[int ~uint8](l *slog.Logger, v int) { l.Info("m", "pass", v) }`, "mismatch:pass"},
		{"type parameter that may be an int is unprovable", `func g[T ~int | ~uint8](l *slog.Logger, v T) { l.Info("m", "pass", v) }`, "pass"},
		{"type parameter that is exactly int", `func g[T int](l *slog.Logger, v T) { l.Info("m", "pass", v) }`, ""},
		{"conversion to a defined type", `type count int
func f(l *slog.Logger) { l.Info("m", "pass", count(1)) }`, "mismatch:pass"},
		{"conversion to a uint alias", `type counter = uint
func f(l *slog.Logger) { l.Info("m", "pass", counter(1)) }`, "mismatch:pass"},
		{"conversion to an int alias", `type counter = int
func f(l *slog.Logger) { l.Info("m", "pass", counter(1)) }`, ""},
		{"builtin name redeclared as a uint alias", `type int = uint
func f(l *slog.Logger) { l.Info("m", "pass", int(1)) }`, "mismatch:pass"},
		{"alias declared in another place of the package", `func f(l *slog.Logger, e ev) { l.Info("m", "authorization_batches", e.Batches) }
type ev struct{ Batches counter }
type counter = int`, ""},
		{"parenthesized wrong slog.Value constructor", `func f(l *slog.Logger) { l.LogAttrs(context.Background(), slog.LevelInfo, "m", slog.Attr{Key: "pass", Value: (slog.StringValue("x"))}) }`, "mismatch:pass"},
		{"Attr literal with a group value", `func f(l *slog.Logger) { l.LogAttrs(context.Background(), slog.LevelInfo, "m", slog.Attr{Key: "pass", Value: slog.GroupValue()}) }`, "mismatch:pass"},
		{"Attr literal with a Value variable is unprovable", `func f(l *slog.Logger, v slog.Value) { l.LogAttrs(context.Background(), slog.LevelInfo, "m", slog.Attr{Key: "pass", Value: v}) }`, "pass"},
		{"user-defined append is not the builtin", `func append(a []any, b ...any) []any { return a }
func f(args []any) []any { return append(args, "pass", uint8(1)) }`, ""},
		{"slice that is not []any is not a pair list", `func f() []string { return []string{"pass", "x"} }`, ""},
		{"any redeclared as string makes the slice a []string", `type any = string
func f() []any { return []any{"pass", "x"} }`, ""},
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
		if want := "kind=" + kind; !hasKindToken(reason, want) {
			t.Errorf("pins.tsv: %s %s %s must say %q in its reason: %q", rule, fields[1], detail, want, reason)
		}
	}
	if pinned == 0 {
		t.Error("pins.tsv holds no pin of a benign key: the check reads nothing")
	}
}

func hasKindToken(reason, token string) bool {
	for from := 0; ; {
		at := strings.Index(reason[from:], token)
		if at < 0 {
			return false
		}
		start, end := from+at, from+at+len(token)
		if (start == 0 || !isTokenChar(reason[start-1])) && (end == len(reason) || !isTokenChar(reason[end])) {
			return true
		}
		from = start + 1
	}
}

func isTokenChar(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func TestKindTokenIsExact(t *testing.T) {
	for reason, want := range map[string]bool{
		"x kind=string": true, "kind=string: y": true, "kind=string y": true, "(kind=string)": true, "notkind=string": false, "x_kind=string": false, "kind=stringbogus": false, "kind=strings": false, "no token": false,
	} {
		if got := hasKindToken(reason, "kind=string"); got != want {
			t.Errorf("hasKindToken(%q) = %v want %v", reason, got, want)
		}
	}
}

func scanBenignFiles(t *testing.T, files map[string]string) []emitscan.Finding {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "sample")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, source := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	findings, err := emitscan.Scan(root, []string{"internal"})
	if err != nil {
		t.Fatal(err)
	}
	return findings
}

func TestBenignKindRefusesWhatItCannotType(t *testing.T) {
	pair := `func f(l *slog.Logger) { l.Info("m", "pass", 3) }` + "\n"
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"unresolvable import", map[string]string{"a.go": "package sample\n\nimport (\n\t\"log/slog\"\n\t\"example.invalid/nothing\"\n)\n\nvar _ = nothing.X\n" + pair}, "refusal:typecheck"},
		{"undefined name", map[string]string{"a.go": header + pair + "var _ = undefinedName\n"}, "refusal:typecheck"},
		{"unused variable", map[string]string{"a.go": header + pair + "func g() { x := 1 }\n"}, "refusal:typecheck"},
		{"sibling file does not type-check", map[string]string{"a.go": header + pair, "b.go": "package sample\n\nvar _ = missing\n"}, "refusal:typecheck"},
		{"mixed package names", map[string]string{"a.go": header + pair, "b.go": "package other\n"}, "refusal:typecheck"},
		{"file the build context drops holds the pair", map[string]string{"a.go": header, "b.go": "//go:build ignore\n\npackage sample\n\nimport \"log/slog\"\n\n" + pair}, "refusal:typecheck"},
		{"Attr literal in a file the build context drops", map[string]string{"a.go": header, "b.go": "//go:build ignore\n\npackage sample\n\nimport (\n\t\"context\"\n\t\"log/slog\"\n)\n\nfunc f(l *slog.Logger) {\n\tl.LogAttrs(context.Background(), slog.LevelInfo, \"m\", slog.Attr{Key: \"authorization_batches\", Value: slog.StringValue(\"x\")})\n}\n"}, "refusal:typecheck"},
		{"control: the same package type-checks", map[string]string{"a.go": header + pair, "b.go": "package sample\n\nvar _ = 1\n"}, ""},
		{"control: a broken package with no benign key is not loaded", map[string]string{"a.go": header + `func f(l *slog.Logger) { l.Info("m", "count", 3) }` + "\nvar _ = undefinedName\n"}, ""},
	}
	for _, c := range cases {
		if got := benignDetails(scanBenignFiles(t, c.files)); got != c.want {
			t.Errorf("%s: R10 details %q want %q", c.name, got, c.want)
		}
	}
}

func TestWrongKindIsNeverPinnable(t *testing.T) {
	findings := scanSource(t, header+`func f(l *slog.Logger, v uint) { l.Info("m", "pass", v) }`+"\n")
	if got := benignDetails(findings); got != "mismatch:pass" {
		t.Fatalf("a uint value of an int key: R10 details %q want mismatch:pass", got)
	}
	pinnable := map[string]bool{}
	for _, key := range []string{"pass", "coverage_bypass", "authorization_batches"} {
		pinnable[key] = telemetryscrub.BenignKind(key) != ""
	}
	for _, finding := range findings {
		if finding.Rule == emitscan.RuleBenignKind && pinnable[finding.Detail] {
			t.Errorf("finding %q could be silenced by a kind=int pin of the key", finding.Detail)
		}
	}
}

func TestRealTreeTypeChecks(t *testing.T) {
	loader, err := sourceload.Load(benignRepoRoot(t), []string{"./internal/...", "./cmd/..."}, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("sourceload.Load: %v", err)
	}
	loaded := loader.Packages()
	errs := 0
	packages.Visit(loaded, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			errs++
			t.Errorf("type-check error in %s: %v", p.PkgPath, e)
		}
	})
	t.Logf("real tree type-check: packages=%d errors=%d", len(loaded), errs)
	if len(loaded) == 0 {
		t.Error("no package loaded")
	}
}
