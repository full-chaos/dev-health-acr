package emitscan_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
)

func keyFindings(findings []emitscan.Finding) string {
	var out []string
	for _, finding := range findings {
		switch finding.Rule {
		case emitscan.RuleProtectedKey, emitscan.RuleLogKey, emitscan.RuleBenignKind:
			out = append(out, finding.Rule+":"+finding.Detail)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

type keyCase struct{ name, body, want string }

func runKeyCases(t *testing.T, cases []keyCase) {
	t.Helper()
	for _, c := range cases {
		if got := keyFindings(scanSource(t, header+c.body+"\n")); got != c.want {
			t.Errorf("%s: key findings %q want %q", c.name, got, c.want)
		}
	}
}

func TestKeyScopeShadowOfKnownKeyIsRefused(t *testing.T) {
	runKeyCases(t, []keyCase{
		{"param shadows a benign const key", `const passKey = "pass"
func f(l *slog.Logger, passKey string, v string) { l.Info("m", passKey, v) }`, "R10:refusal:shadowed-key,R9:unresolvable key"},
		{"param shadows a protected const key", `const tokKey = "api_token"
func f(l *slog.Logger, tokKey, v string) { l.Info("m", tokKey, v) }`, "R10:refusal:shadowed-key,R9:unresolvable key"},
		{"param shadows a protected const key in an attribute", `const tokKey = "api_token"
func f(tokKey, v string) { _ = attribute.String(tokKey, v) }`, "R10:refusal:shadowed-key,R1:unfoldable const key"},
		{"local variable shadows a benign const key", `const passKey = "pass"
func f(l *slog.Logger) { passKey := "other"; l.Info("m", passKey, 1) }`, "R10:refusal:shadowed-key,R9:unresolvable key"},
		{"local variable shadows a function-local protected const", `func f(l *slog.Logger) { const tokKey = "api_token"; { tokKey := "x"; l.Info("m", tokKey, 1) } }`, "R10:refusal:shadowed-key,R9:unresolvable key"},
		{"named result shadows a protected const key", `const tokKey = "api_token"
func f(l *slog.Logger) (tokKey string) { l.Info("m", tokKey, 1); return }`, "R10:refusal:shadowed-key,R9:unresolvable key"},
	})
}

func TestKeyScopeLocalHoldingAConstantIsResolved(t *testing.T) {
	runKeyCases(t, []keyCase{
		{"local string on an int key", `func f(l *slog.Logger) { key := "authorization_batches"; l.Info("m", key, "s") }`, "R10:mismatch:authorization_batches,R1:authorization_batches"},
		{"local of the right kind", `func f(l *slog.Logger) { key := "pass"; l.Info("m", key, 3) }`, "R1:pass"},
		{"local from a package const", `const passKey = "pass"
func f(l *slog.Logger) { k := passKey; l.Info("m", k, "s") }`, "R10:mismatch:pass,R1:pass"},
		{"local from a concatenation", `func f(l *slog.Logger) { k := "authorization_" + "batches"; l.Info("m", k, "s") }`, "R10:mismatch:authorization_batches,R1:authorization_batches"},
		{"var declaration with one value", `func f(l *slog.Logger) { var k = "api_token"; l.Info("m", k, "x") }`, "R1:api_token"},
		{"local chain", `func f(l *slog.Logger) { a := "api_token"; b := a; l.Info("m", b, "x") }`, "R1:api_token"},
		{"local used in an attribute", `func f(v string) { k := "api_token"; _ = attribute.String(k, v) }`, "R1:api_token"},
		{"local attribute.Key with a method call", `func f(v string) { k := attribute.Key("api_token"); _ = k.String(v) }`, "R1:api_token"},
		{"local const with another text shadows a package const", `const passKey = "pass"
func f(l *slog.Logger) { const passKey = "other"; l.Info("m", passKey, "s") }`, ""},
	})
}

func TestKeyScopeUnprovableLocalIsR9(t *testing.T) {
	runKeyCases(t, []keyCase{
		{"parameter", `func f(l *slog.Logger, k string) { l.Info("m", k, 1) }`, "R9:unresolvable key"},
		{"reassigned local", `func f(l *slog.Logger, o string) { key := "authorization_batches"; key += o; l.Info("m", key, 1) }`, "R9:unresolvable key"},
		{"assigned twice", `func f(l *slog.Logger, o string) { key := "authorization_batches"; key = o; l.Info("m", key, 1) }`, "R9:unresolvable key"},
		{"address taken", `func f(l *slog.Logger, o string) { key := "authorization_batches"; p := &key; *p = o; l.Info("m", key, "s") }`, "R9:unresolvable key"},
		{"range assigned", `func f(l *slog.Logger, m map[string]int) { key := "authorization_batches"; for key = range m { _ = key }; l.Info("m", key, "s") }`, "R9:unresolvable key"},
		{"local with no value", `func f(l *slog.Logger) { var key string; l.Info("m", key, 1) }`, "R9:unresolvable key"},
		{"call result", `func g() string { return "pass" }
func f(l *slog.Logger) { key := g(); l.Info("m", key, "s") }`, "R9:unresolvable key"},
		{"package variable", `var pkgKey = "pass"
func f(l *slog.Logger) { l.Info("m", pkgKey, 3) }`, "R9:unresolvable key"},
		{"shadowed string conversion is a call", `const passKey = "pass"
func f(l *slog.Logger) { string := func(s any) any { return "api_token" }; l.Info("m", string(passKey), 3) }`, "R9:unresolvable key"},
	})
}

func TestKeyScopeLoggerMethodValuesAreScanned(t *testing.T) {
	runKeyCases(t, []keyCase{
		{"Info method value with a wrong kind", `func f(l *slog.Logger) { log := l.Info; log("m", "authorization_batches", "s") }`, "R10:mismatch:authorization_batches,R1:authorization_batches"},
		{"Warn method value with an unresolvable key", `func f(l *slog.Logger, k string) { log := l.Warn; log("m", k, 1) }`, "R9:unresolvable key"},
		{"Error method value with a protected key", `func f(l *slog.Logger) { log := l.Error; log("m", "api_token", "x") }`, "R1:api_token"},
		{"InfoContext method value", `func f(l *slog.Logger) { lg := l.InfoContext; lg(context.Background(), "m", "api_token", "x") }`, "R1:api_token"},
		{"Log method value", `func f(l *slog.Logger) { lg := l.Log; lg(context.Background(), slog.LevelInfo, "m", "api_token", "x") }`, "R1:api_token"},
		{"LogAttrs method value with an Attr variable", `func f(l *slog.Logger, a slog.Attr) { lg := l.LogAttrs; lg(context.Background(), slog.LevelInfo, "m", a) }`, "R9:unresolvable key"},
		{"method value held in a var declaration", `func f(l *slog.Logger) { var log = l.Debug; log("m", "api_token", "x") }`, "R1:api_token"},
		{"method value in a struct literal", `type lg struct{ out func(string, ...any) }
func f(l *slog.Logger) { v := lg{out: l.Info}; v.out("m", "api_token", "x") }`, "R1:api_token"},
	})
}

func TestKeyScopePackageThatDoesNotTypeCheckRefusesKeyIdents(t *testing.T) {
	runKeyCases(t, []keyCase{
		{"slog key ident", `const k = "pass"
func f(l *slog.Logger) { l.Info("m", k, 3) }
var _ = undefinedName`, "R10:refusal:typecheck,R9:unresolvable key"},
		{"attribute key ident", `const k = "api_token"
func f(v string) { _ = attribute.String(k, v) }
var _ = undefinedName`, "R10:refusal:typecheck,R1:unfoldable const key"},
		{"control: literal keys need no types", `func f(l *slog.Logger) { l.Info("m", "api_token", 3) }
var _ = undefinedName`, "R1:api_token"},
	})
}

func TestKeyScopeControlsPass(t *testing.T) {
	runKeyCases(t, []keyCase{
		{"package const key with an int value", `const batchesKey = "authorization_batches"
func f(l *slog.Logger) { l.Info("m", batchesKey, 3) }`, "R1:authorization_batches"},
		{"package const key with a wrong kind is still caught", `const batchesKey = "authorization_batches"
func f(l *slog.Logger) { l.Info("m", batchesKey, "s") }`, "R10:mismatch:authorization_batches,R1:authorization_batches"},
		{"param named like a const that is not a key", `const passKey = "pass"
func f(l *slog.Logger, passKey string) { l.Info("m", "count", passKey) }`, ""},
		{"method value never called", `func f(l *slog.Logger) { log := l.Info; _ = log }`, ""},
		{"unrelated func field named like a log method", `type o struct{ Info func() }
func f(v o) { x := v.Info; x() }`, ""},
		{"call with the key held in a const of another file name", `const a = "pass"
const b = a
func f(l *slog.Logger) { l.Info("m", b, 3) }`, "R1:pass"},
		{"typed string const key", `type K string
const k K = "authorization_batches"
func f(l *slog.Logger) { l.Info("m", string(k), 3) }`, "R1:authorization_batches"},
	})
}

func TestKeyScopeLocalChainBeyondTheDepthCapIsUnprovable(t *testing.T) {
	var b strings.Builder
	b.WriteString("func f(l *slog.Logger) {\n\tv0 := \"api_token\"\n")
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&b, "\tv%d := v%d\n", i, i-1)
	}
	b.WriteString("\tl.Info(\"m\", v12, \"x\")\n}")
	runKeyCases(t, []keyCase{{"twelve locals deep", b.String(), "R9:unresolvable key"}})
}
