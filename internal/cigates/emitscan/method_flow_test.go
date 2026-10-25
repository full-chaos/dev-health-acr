package emitscan_test

import (
	"strings"
	"testing"
)

const traceImport = "\t\"go.opentelemetry.io/otel/metric\"\n"

var traceHeader = strings.Replace(header, traceImport, traceImport+"\t\"go.opentelemetry.io/otel/trace\"\n", 1)

const aliasedAttributeHeader = "package sample\n\nimport otelattr \"go.opentelemetry.io/otel/attribute\"\n\n"

func TestAttributeKeyMethodExpressionsAreRead(t *testing.T) {
	runKeyCases(t, []keyCase{
		{"method expression on a conversion", `func f(v string) { _ = attribute.Key.String(attribute.Key("api_token"), v) }`, "R1:api_token"},
		{"method expression on a local key", `func f(v string) { k := attribute.Key("api_token"); _ = attribute.Key.String(k, v) }`, "R1:api_token"},
		{"Int method expression", `func f(n int) { _ = attribute.Key.Int(attribute.Key("api_token"), n) }`, "R1:api_token"},
		{"parenthesised method expression", `func f(v string) { _ = (attribute.Key.String)(attribute.Key("api_token"), v) }`, "R1:api_token"},
		{"bound to a local and called", `func f(v string) { emit := attribute.Key.String; _ = emit(attribute.Key("api_token"), v) }`, "R1:api_token"},
		{"bound in a var declaration and called", `func f(v string) { var emit = attribute.Key.String; _ = emit(attribute.Key("api_token"), v) }`, "R1:api_token"},
		{"type alias method expression", `type K = attribute.Key
func f(v string) { _ = K.String(K("api_token"), v) }`, "R1:api_token"},
		{"type alias method expression bound to a local", `type K = attribute.Key
func f(v string) { emit := K.String; _ = emit(K("api_token"), v) }`, "R1:api_token"},
		{"pointer method expression is refused", `func f(v string) { k := attribute.Key("api_token"); _ = (*attribute.Key).String(&k, v) }`, "R10:refusal:key-receiver"},
		{"method expression passed on is refused", `func run(f func(attribute.Key, string) attribute.KeyValue) {}
func g() { run(attribute.Key.String) }`, "R10:refusal:key-method-value"},
		{"method expression returned is refused", `func g() func(attribute.Key, string) attribute.KeyValue { return attribute.Key.String }`, "R10:refusal:key-method-value"},
		{"bound method expression reassigned is refused", `func g(v string) { emit := attribute.Key.String; emit = attribute.Key.String; _ = emit(attribute.Key("api_token"), v) }`, "R10:refusal:key-method-value"},
		{"control: package that does not type-check is refused", `func f(v string) { _ = attribute.Key.String(attribute.Key("api_token"), v) }
var _ = undefinedName`, "R10:refusal:typecheck"},
		{"control: benign key", `func f(v string) { _ = attribute.Key.String(attribute.Key("count"), v) }`, ""},
		{"control: Defined method expression", `func f() { _ = attribute.Key.Defined(attribute.Key("api_token")) }`, ""},
		{"control: method expression of another type", `type s struct{}
func (s) String(v string) attribute.KeyValue { return attribute.String("count", v) }
func f(v string) { _ = s.String(s{}, v) }`, ""},
	})
}

func TestAttributeKeyMethodExpressionReachesASpan(t *testing.T) {
	body := `func f(span trace.Span, v string) { span.SetAttributes(attribute.Key.String(attribute.Key("api_token"), v)) }`
	if got := keyFindings(scanSource(t, traceHeader+body+"\n")); got != "R1:api_token" {
		t.Errorf("key findings %q want %q", got, "R1:api_token")
	}
}

func TestAttributeKeyMethodExpressionUnderAnImportAlias(t *testing.T) {
	for _, c := range []keyCase{
		{"direct", `func f(v string) otelattr.KeyValue { return otelattr.Key.String(otelattr.Key("api_token"), v) }`, "R1:api_token"},
		{"bound", `func f(v string) otelattr.KeyValue { emit := otelattr.Key.String; return emit(otelattr.Key("api_token"), v) }`, "R1:api_token"},
	} {
		if got := keyFindings(scanSource(t, aliasedAttributeHeader+c.body+"\n")); got != c.want {
			t.Errorf("%s: key findings %q want %q", c.name, got, c.want)
		}
	}
}

func TestReturnedLoggerMethodValuesAreScanned(t *testing.T) {
	runKeyCases(t, []keyCase{
		{"helper returns a method value", `func debugLogger(l *slog.Logger) func(string, ...any) { return l.Debug }
func f(l *slog.Logger) { emit := debugLogger(l); emit("m", "api_token", "secret") }`, "R1:api_token"},
		{"helper result called at once", `func debugLogger(l *slog.Logger) func(string, ...any) { return l.Debug }
func f(l *slog.Logger) { debugLogger(l)("m", "api_token", "secret") }`, "R1:api_token"},
		{"method returns a method value", `type h struct{ l *slog.Logger }
func (x h) logger() func(string, ...any) { return x.l.Info }
func f(x h) { emit := x.logger(); emit("m", "api_token", "secret") }`, "R1:api_token"},
		{"helper through a second helper", `func a(l *slog.Logger) func(string, ...any) { return l.Info }
func b(l *slog.Logger) func(string, ...any) { return a(l) }
func f(l *slog.Logger) { emit := b(l); emit("m", "api_token", "secret") }`, "R1:api_token"},
		{"function literal bound to a local", `func f(l *slog.Logger) { get := func() func(string, ...any) { return l.Warn }; emit := get(); emit("m", "api_token", "secret") }`, "R1:api_token"},
		{"function literal called at once", `func f(l *slog.Logger) { emit := func() func(string, ...any) { return l.Warn }(); emit("m", "api_token", "secret") }`, "R1:api_token"},
		{"named result and a bare return", `func lg(l *slog.Logger) (out func(string, ...any)) { out = l.Error; return }
func f(l *slog.Logger) { emit := lg(l); emit("m", "api_token", "secret") }`, "R1:api_token"},
		{"one of two results", `func lg(l *slog.Logger) (func(string, ...any), error) { return l.Info, nil }
func f(l *slog.Logger) { emit, _ := lg(l); emit("m", "api_token", "secret") }`, "R1:api_token"},
		{"helper declared after its use", `func f(l *slog.Logger) { emit := b(l); emit("m", "api_token", "secret") }
func b(l *slog.Logger) func(string, ...any) { return a(l) }
func a(l *slog.Logger) func(string, ...any) { return l.Info }`, "R1:api_token"},
		{"two results forwarded from a call", `func lg(l *slog.Logger) (func(string, ...any), error) { return l.Info, nil }
func fwd(l *slog.Logger) (func(string, ...any), error) { return lg(l) }
func f(l *slog.Logger) { emit, _ := fwd(l); emit("m", "api_token", "secret") }`, "R1:api_token"},
		{"second of two results in a var declaration", `func lg(l *slog.Logger) (error, func(string, ...any)) { return nil, l.Info }
func f(l *slog.Logger) { var err, emit = lg(l); _ = err; emit("m", "api_token", "secret") }`, "R1:api_token"},
		{"result of a declared function type", `type logf func(string, ...any)
func lg(l *slog.Logger) logf { return l.Info }
func f(l *slog.Logger) { emit := lg(l); emit("m", "api_token", "secret") }`, "R1:api_token"},
		{"generic helper", `func lg[T any](l *slog.Logger, _ T) func(string, ...any) { return l.Info }
func f(l *slog.Logger) { emit := lg[int](l, 1); emit("m", "api_token", "secret") }`, "R1:api_token"},
		{"returned context method value", `func lg(l *slog.Logger) func(context.Context, string, ...any) { return l.InfoContext }
func f(l *slog.Logger) { lg(l)(context.Background(), "m", "api_token", "secret") }`, "R1:api_token"},
		{"returned With method value", `func lg(l *slog.Logger) func(...any) *slog.Logger { return l.With }
func f(l *slog.Logger) { _ = lg(l)("api_token", "secret") }`, "R1:api_token"},
		{"control: helper returns another func", `func lg() func(string, ...any) { return func(string, ...any) {} }
func f() { emit := lg(); emit("m", "api_token", "secret") }`, ""},
		{"control: method value returned and never called", `func debugLogger(l *slog.Logger) func(string, ...any) { return l.Debug }
func f(l *slog.Logger) { _ = debugLogger(l) }`, ""},
	})
}

func TestLoggerMethodExpressionsAreScanned(t *testing.T) {
	runKeyCases(t, []keyCase{
		{"Info method expression", `func f(l *slog.Logger) { (*slog.Logger).Info(l, "m", "api_token", "x") }`, "R1:api_token"},
		{"Info method expression with a wrong kind", `func f(l *slog.Logger) { (*slog.Logger).Info(l, "m", "authorization_batches", "s") }`, "R10:mismatch:authorization_batches,R1:authorization_batches"},
		{"control: Info method expression with a computed key", `func f(l *slog.Logger, k string) { (*slog.Logger).Info(l, "m", k, 1) }`, "R9:unresolvable key"},
		{"InfoContext method expression", `func f(l *slog.Logger) { (*slog.Logger).InfoContext(l, context.Background(), "m", "api_token", "x") }`, "R1:api_token"},
		{"Log method expression", `func f(l *slog.Logger) { (*slog.Logger).Log(l, context.Background(), slog.LevelInfo, "m", "api_token", "x") }`, "R1:api_token"},
		{"With method expression", `func f(l *slog.Logger) { _ = (*slog.Logger).With(l, "api_token", "x") }`, "R1:api_token"},
		{"bound to a local", `func f(l *slog.Logger) { info := (*slog.Logger).Info; info(l, "m", "api_token", "x") }`, "R1:api_token"},
		{"type alias method expression", `type L = slog.Logger
func f(l *slog.Logger) { (*L).Info(l, "m", "api_token", "x") }`, "R1:api_token"},
		{"returned from a helper", `func lg() func(*slog.Logger, string, ...any) { return (*slog.Logger).Warn }
func f(l *slog.Logger) { lg()(l, "m", "api_token", "x") }`, "R1:api_token"},
		{"typed method expression is read one way", `func f(l *slog.Logger, k string) { (*slog.Logger).Info(l, "api_token", k, "x") }`, "R9:unresolvable key"},
		{"control: method value on a dereferenced pointer", `func f(p **slog.Logger, k string) { (*p).Info("m", k, "api_token", "x") }`, "R9:unresolvable key"},
		{"control: dereferenced method value bound to a local", `func f(p **slog.Logger, k string) { info := (*p).Info; info("m", k, "api_token", "x") }`, "R9:unresolvable key"},
	})
}

func TestLoggerMethodExpressionWithoutTypesIsReadBothWays(t *testing.T) {
	runKeyCases(t, []keyCase{
		{"local type name: both readings", `type L = slog.Logger
func f(l *slog.Logger, k string) { (*L).Info(l, "api_token", k, "x") }
var _ = undefinedName`, "R10:refusal:typecheck,R1:api_token,R9:unresolvable key"},
		{"imported type name: a method expression", `func f(l *slog.Logger, k string) { (*slog.Logger).Info(l, "api_token", k, "x") }
var _ = undefinedName`, "R10:refusal:typecheck,R9:unresolvable key"},
	})
}

func TestLoggerMethodValuesPassedAsArgumentsAreScanned(t *testing.T) {
	runKeyCases(t, []keyCase{
		{"passed to a function", `func run(log func(string, ...any)) { log("m", "api_token", "x") }
func f(l *slog.Logger) { run(l.Info) }`, "R1:api_token"},
		{"passed to a method", `type r struct{}
func (r) run(log func(string, ...any)) { log("m", "api_token", "x") }
func f(l *slog.Logger) { r{}.run(l.Info) }`, "R1:api_token"},
		{"passed to a function literal bound to a local", `func f(l *slog.Logger) { run := func(log func(string, ...any)) { log("m", "api_token", "x") }; run(l.Error) }`, "R1:api_token"},
		{"passed as a variadic argument", `func run(p string, logs ...func(string, ...any)) { for _, lg := range logs { lg(p, "api_token", "x") } }
func f(l *slog.Logger) { run("m", func(string, ...any) {}, l.Warn) }`, "R1:api_token"},
		{"passed in a second position", `func run(n int, log func(string, ...any)) { log("m", "api_token", n) }
func f(l *slog.Logger) { run(1, l.Debug) }`, "R1:api_token"},
		{"method expression passed on", `func run(log func(*slog.Logger, string, ...any), l *slog.Logger) { log(l, "m", "api_token", "x") }
func f(l *slog.Logger) { run((*slog.Logger).Info, l) }`, "R1:api_token"},
		{"method value held in a local then passed", `func run(log func(string, ...any)) { log("m", "api_token", "x") }
func f(l *slog.Logger) { info := l.Info; run(info) }`, "R1:api_token"},
		{"passed as a declared function type", `type logf func(string, ...any)
func run(log logf) { log("m", "api_token", "x") }
func f(l *slog.Logger) { run(l.Info) }`, "R1:api_token"},
		{"passed to a generic function", `func run[T any](log func(string, ...any), _ T) { log("m", "api_token", "x") }
func f(l *slog.Logger) { run[int](l.Info, 1) }`, "R1:api_token"},
		{"passed to a generic function of two type parameters", `func run[T, U any](log func(string, ...any), _ T, _ U) { log("m", "api_token", "x") }
func f(l *slog.Logger) { run[int, string](l.Info, 1, "") }`, "R1:api_token"},
		{"passed to a function literal in a var declaration", `func f(l *slog.Logger) { var run = func(log func(string, ...any)) { log("m", "api_token", "x") }; run(l.Error) }`, "R1:api_token"},
		{"control: argument to a function of another package with the same name", `func Println(log func(string, ...any)) { log("m", "api_token", "x") }
func f(l *slog.Logger) { fmt.Println(l.Info) }`, ""},
		{"control: argument to a parameter that is not a function type", `func describe(emit any) string { return fmt.Sprint(emit) }
func f(l *slog.Logger, emit func(string, ...any)) { _ = describe(l.Info); emit("m", "api_token", "x") }`, ""},
		{"control: a plain function passed", `func run(log func(string, ...any)) { log("m", "api_token", "x") }
func f() { run(func(string, ...any) {}) }`, ""},
	})
}
