package emitscan_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

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

func TestKeyScopeMethodThatMayWriteALocalKeyMakesItUnprovable(t *testing.T) {
	runKeyCases(t, []keyCase{
		{"pointer receiver method on a local", `type K string
func (k *K) Promote() { *k = "api_token" }
func f(l *slog.Logger) { key := K("safe"); key.Promote(); l.Info("m", string(key), "secret") }`, "R9:unresolvable key"},
		{"pointer receiver method value on a local", `type K string
func (k *K) Promote() { *k = "api_token" }
func f(l *slog.Logger) { key := K("safe"); p := key.Promote; p(); l.Info("m", string(key), "secret") }`, "R9:unresolvable key"},
		{"control: value receiver method does not write", `type K string
func (k K) Same() K { return k }
func f(l *slog.Logger) { key := K("api_token"); _ = key.Same(); l.Info("m", string(key), "x") }`, "R1:api_token"},
		{"control: attribute.Key value receiver method does not write", `func f(v string) { k := attribute.Key("api_token"); _ = k.Defined(); _ = k.String(v) }`, "R1:api_token"},
	})
}

func TestKeyScopeAttributeKeyReceiverForms(t *testing.T) {
	runKeyCases(t, []keyCase{
		{"parenthesised receiver", `func f(v string) { k := attribute.Key("api_token"); _ = (k).String(v) }`, "R1:api_token"},
		{"pointer receiver is refused", `func f(v string) { k := attribute.Key("api_token"); p := &k; _ = p.String(v) }`, "R10:refusal:key-receiver"},
		{"dereferenced receiver is refused", `func f(v string) { k := attribute.Key("api_token"); p := &k; _ = (*p).String(v) }`, "R10:refusal:key-receiver"},
		{"type parameter receiver is refused", `func g[T interface{ String(string) attribute.KeyValue }](k T, v string) { _ = k.String(v) }`, "R10:refusal:key-receiver"},
		{"interface receiver is refused", `func g(k interface{ String(string) attribute.KeyValue }, v string) { _ = k.String(v) }`, "R10:refusal:key-receiver"},
		{"control: a String method of another type", `type s struct{}
func (s) String(v string) attribute.KeyValue { return attribute.String("count", v) }
func f(v string) { _ = s{}.String(v) }`, ""},
		{"control: an Int method that returns another type", `type n struct{}
func (n) Int(v int) int { return v }
func f(v int) { _ = n{}.Int(v) }`, ""},
	})
}

func TestKeyScopeAttributeKeyMethodValues(t *testing.T) {
	runKeyCases(t, []keyCase{
		{"bound to a local and called", `func f(v string) { k := attribute.Key("api_token"); emit := k.String; _ = emit(v) }`, "R1:api_token"},
		{"bound in a var declaration and called", `func f(v string) { k := attribute.Key("api_token"); var emit = k.String; _ = emit(v) }`, "R1:api_token"},
		{"method value on a call receiver", `func f(v string) { emit := attribute.Key("api_token").String; _ = emit(v) }`, "R1:api_token"},
		{"returned", `func g(k attribute.Key) func(string) attribute.KeyValue { return k.String }`, "R10:refusal:key-method-value"},
		{"passed to a function", `func run(f func(string) attribute.KeyValue) {}
func g() { k := attribute.Key("api_token"); run(k.String) }`, "R10:refusal:key-method-value"},
		{"stored in a struct field", `type h struct{ e func(string) attribute.KeyValue }
func g() h { k := attribute.Key("api_token"); return h{e: k.String} }`, "R10:refusal:key-method-value"},
		{"bound to a local that is also passed on", `func run(f func(string) attribute.KeyValue) {}
func g(v string) { k := attribute.Key("api_token"); emit := k.String; run(emit); _ = emit(v) }`, "R10:refusal:key-method-value"},
		{"bound to a local that is reassigned", `func g(v string, o attribute.Key) { k := attribute.Key("api_token"); emit := k.String; emit = o.String; _ = emit(v) }`, "R10:refusal:key-method-value"},
		{"control: direct call", `func f(v string) { k := attribute.Key("api_token"); _ = k.String(v) }`, "R1:api_token"},
	})
}

func TestKeyScopeTypeSwitchBindingShadowsAKnownKey(t *testing.T) {
	runKeyCases(t, []keyCase{
		{"binding hides a protected const", `const keyName = "api_token"
func f(l *slog.Logger, x any) { switch keyName := x.(type) { case string: l.Info("m", keyName, "x") } }`, "R10:refusal:shadowed-key,R9:unresolvable key"},
		{"binding hides a benign const in the default clause", `const passKey = "pass"
func f(l *slog.Logger, x any) { switch passKey := x.(type) { default: l.Info("m", passKey, 3) } }`, "R10:refusal:shadowed-key,R9:unresolvable key"},
		{"control: no outer const", `func f(l *slog.Logger, x any) { switch key := x.(type) { case string: l.Info("m", key, "x") } }`, "R9:unresolvable key"},
		{"control: outer const is not a known key", `const other = "count"
func f(l *slog.Logger, x any) { switch other := x.(type) { case string: l.Info("m", other, "x") } }`, "R9:unresolvable key"},
	})
}

func chainOf(term, op string, n int) string {
	return strings.Repeat(term+" "+op+" ", n-1) + term
}

const refusedAndUnresolved = "R10:refusal:typecheck,R9:unresolvable key"

// oversizedShapes are source shapes the checker folds at a cost the scan's own budget does not bound. Each, followed by an
// identifier key, must be refused without type-checking the package.
func oversizedShapes() []keyCase {
	trig := "\nfunc f(l *slog.Logger, v string) { key := \"api_token\"; l.Info(\"m\", key, v) }"
	var implicit, doubling, split, linked strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&implicit, "\tn%d\n", i)
	}
	doubling.WriteString("const c0 = \"a\"\n")
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&doubling, "const c%d = c%d + c%d\n", i, i-1, i-1)
	}
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&split, "const s%d = %s\n", i, chainOf(`"a"`, "+", 1000))
	}
	fmt.Fprintf(&linked, "const l0 = %s\n", chainOf(`"a"`, "+", 2000))
	for i := 1; i < 10; i++ {
		fmt.Fprintf(&linked, "const l%d = l%d + %s\n", i, i-1, chainOf(`"a"`, "+", 2000))
	}
	return []keyCase{
		{"one + chain of 5000", "const big = " + chainOf(`"a"`, "+", 5000) + trig, refusedAndUnresolved},
		{"one + chain of 2048", "const big = " + chainOf(`"a"`, "+", 2048) + trig, refusedAndUnresolved},
		{"ten consts of 1000-term chains", split.String() + trig, refusedAndUnresolved},
		{"ten linked consts of 2000 terms", linked.String() + trig, refusedAndUnresolved},
		{"doubling consts to c40", doubling.String() + trig, refusedAndUnresolved},
		{"doubling consts, one used as the key", doubling.String() + "\nfunc g(l *slog.Logger, v string) { l.Info(\"m\", c25, v) }", refusedAndUnresolved},
		{"* chain of 40000", "const big = " + chainOf("1", "*", 40000) + trig, refusedAndUnresolved},
		{"- chain of 40000", "const big = " + chainOf("1", "-", 40000) + trig, refusedAndUnresolved},
		{"<< chain of 5000", "const big = " + chainOf("1", "<<", 5000) + trig, refusedAndUnresolved},
		{"parentheses 30000 deep", "const big = " + strings.Repeat("(", 30000) + `"a"` + strings.Repeat(")", 30000) + trig, refusedAndUnresolved},
		{"string conversions 20000 deep", "const big = " + strings.Repeat("string(", 20000) + `"a"` + strings.Repeat(")", 20000) + trig, refusedAndUnresolved},
		{"iota + chain repeated by 2000 implicit names", "const (\n\tn_ = " + chainOf("iota", "+", 2000) + "\n" + implicit.String() + ")" + trig, "R1:census incomplete: work budget"},
		{"oversized direct key expression", "func f(l *slog.Logger, v string) { key := \"api_token\"; l.Info(\"m\", key, v); l.Info(\"m\", " + chainOf(`"a"`, "+", 5000) + ", v) }", refusedAndUnresolved},
	}
}

func TestKeyScopeOversizedPackageIsNeverTypeChecked(t *testing.T) {
	big := "const big = " + chainOf(`"a"`, "+", 5000) + "\n"
	cases := append(oversizedShapes(), []keyCase{
		{"a const key beside an oversized const", big + "const k = \"api_token\"\nfunc f(l *slog.Logger, v string) { l.Info(\"m\", k, v); _ = big }", refusedAndUnresolved},
		{"a parameter shadows an unknown const (slog key)", big + "const secret = \"api_token\"\nfunc f(l *slog.Logger, secret, v string) { l.Info(\"m\", secret, v); _ = big }", refusedAndUnresolved},
		{"a parameter shadows an unknown const (attribute key)", big + "const secret = \"api_token\"\nfunc f(secret, v string) { _ = attribute.String(secret, v); _ = big }", "R10:refusal:typecheck,R1:unfoldable const key"},
		{"a local copy of a parameter", big + "func f(l *slog.Logger, p, v string) { k := p; l.Info(\"m\", k, v); _ = big }", refusedAndUnresolved},
		{"benign key kind beside an oversized const", big + "func f(l *slog.Logger) { l.Info(\"m\", \"pass\", 3); _ = big }", "R10:refusal:typecheck,R1:pass"},
		{"literal keys are still read by value", big + "func f(l *slog.Logger, v string) { l.Info(\"m\", \"api_token\", v); _ = big }", "R1:api_token"},
		{"control: nothing oversized, the local key is read", "const small = \"a\" + \"b\"\nfunc f(l *slog.Logger, v string) { key := \"api_token\"; l.Info(\"m\", key, v); _ = small }", "R1:api_token"},
		{"control: a long ordinary const is still typed", "const text = \"" + strings.Repeat("x", 400) + "\"\nfunc f(l *slog.Logger, v string) { key := \"api_token\"; l.Info(\"m\", key, v); _ = text }", "R1:api_token"},
	}...)
	for _, c := range cases {
		start := cpuTime(t)
		got := keyFindings(scanSource(t, header+c.body+"\n"))
		used := cpuTime(t) - start
		t.Logf("%s: %s cpu, findings %q", c.name, used, got)
		if limit := time.Second * raceSlowdown; used > limit {
			t.Errorf("%s: used %s cpu, want < %s", c.name, used, limit)
		}
		if got != c.want {
			t.Errorf("%s: key findings %q want %q", c.name, got, c.want)
		}
	}
}
