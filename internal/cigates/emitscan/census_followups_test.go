package emitscan_test

import "testing"

func ruleList(t *testing.T, body string) string {
	t.Helper()
	return rules(scanSource(t, header+body+"\n"))
}

// A span attribute whose key is a variable: the key text is computed at run time, so it needs a pin like an slog key.
func TestSpanAttributeVariableKeyIsR9(t *testing.T) {
	for name, body := range map[string]string{
		"parameter key": `func f(k, v string) { _ = attribute.String(k, v) }`,
		"attribute.Int": `func f(k string) { _ = attribute.Int(k, 1) }`,
	} {
		if got := ruleList(t, body); got != "R9" {
			t.Errorf("%s: rules %q, want an R9", name, got)
		}
	}
}

func TestSpanAttributeVariableKeyNamesItsSite(t *testing.T) {
	got := r9Sites(t, `func f(k, v string) { _ = attribute.String(k, v) }`)
	if len(got) != 1 || got[0] != "f|unresolvable key: k" {
		t.Fatalf("R9 sites = %q, want [f|unresolvable key: k]", got)
	}
}

// A call in key position is already an R2 dynamic key: one finding, no second R9 for the same key.
func TestSpanAttributeCallKeyStaysOneR2(t *testing.T) {
	if got := ruleList(t, "func f(v string) { _ = attribute.String(name(), v) }\nfunc name() string { return \"x\" }"); got != "R2" {
		t.Errorf("rules %q, want R2", got)
	}
}

func TestSpanAttributeConstantKeyIsNotR9(t *testing.T) {
	for name, body := range map[string]string{
		"literal":  `func f(v string) { _ = attribute.String("stage", v) }`,
		"constant": "const k = \"stage\"\nfunc f(v string) { _ = attribute.String(k, v) }",
	} {
		if got := ruleList(t, body); got != "" {
			t.Errorf("%s: rules %q, want none", name, got)
		}
	}
}

// fmt formatting reads error text only through a verb that prints an error: %v %s %w, and %q %x %X (fmt calls Error()
// for the string verbs).
func TestFormattedValueIsErrorTextOnlyWithAnErrorVerb(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"Sprintf %d", `func f(n int) { _ = attribute.String("count", fmt.Sprintf("%d", n)) }`, ""},
		{"Sprintf %05.2f and %%", `func f(x float64) { _ = attribute.String("ratio", fmt.Sprintf("%05.2f%%", x)) }`, ""},
		{"Sprintf %T", `func f(x any) { _ = attribute.String("type", fmt.Sprintf("%T", x)) }`, ""},
		{"log value Sprintf %d", `func f(n int, l *slog.Logger) { l.Info("x", "count", fmt.Sprintf("%d", n)) }`, ""},
		{"Errorf without a verb", `func f() { _ = attribute.String("k", fmt.Errorf("fixed").Error()) }`, "R3"},
		{"Sprintf %v", `func f(err error) { _ = attribute.String("reason", fmt.Sprintf("%v", err)) }`, "R3"},
		{"Sprintf %s", `func f(err error) { _ = attribute.String("reason", fmt.Sprintf("x %s", err)) }`, "R3"},
		{"Sprintf %+v flag", `func f(err error) { _ = attribute.String("reason", fmt.Sprintf("%+v", err)) }`, "R3"},
		{"Sprintf %q", `func f(err error) { _ = attribute.String("reason", fmt.Sprintf("%q", err)) }`, "R3"},
		{"Sprintf %x", `func f(err error) { _ = attribute.String("reason", fmt.Sprintf("%x", err)) }`, "R3"},
		{"Sprintf %d then %v", `func f(n int, err error) { _ = attribute.String("reason", fmt.Sprintf("%d %v", n, err)) }`, "R3"},
		{"Sprintf indexed %[1]v", `func f(err error) { _ = attribute.String("reason", fmt.Sprintf("%[1]v", err)) }`, "R3"},
		{"Sprintf non-constant format", `func f(format string, n int) { _ = attribute.String("reason", fmt.Sprintf(format, n)) }`, "R3"},
		{"Errorf %w", `func f(err error) { _ = attribute.String("reason", fmt.Errorf("x: %w", err).Error()) }`, "R3"},
		{"Sprintf %d around an Error() call", `func f(err error) { _ = attribute.String("reason", fmt.Sprintf("%d", len(err.Error()))) }`, "R3"},
		{"Sprint", `func f(err error) { _ = attribute.String("reason", fmt.Sprint(err)) }`, "R3"},
	}
	for _, c := range cases {
		if got := ruleList(t, c.body); got != c.want {
			t.Errorf("%s: rules %q, want %q", c.name, got, c.want)
		}
	}
}
