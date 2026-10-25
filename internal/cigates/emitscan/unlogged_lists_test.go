package emitscan_test

import "testing"

func TestPairListsAreReadOnlyWhenTheyCanBeLogged(t *testing.T) {
	const mismatch = "mismatch:pass"
	cases := []struct{ name, body, want string }{
		{"local literal only measured", `func f() { args := []any{"pass", uint8(1)}; _ = len(args) }`, ""},
		{"local literal only capped", `func f() { args := []any{"pass", uint8(1)}; _ = cap(args) }`, ""},
		{"var declared literal only measured", `func f() { var args = []any{"pass", uint8(1)}; _ = len(args) }`, ""},
		{"empty literal grown by append then measured", `func f() { args := []any{}; args = append(args, "pass", uint8(1)); _ = len(args) }`, ""},
		{"dead list in a package level function literal", `var g = func() { args := []any{"pass", uint8(1)}; _ = len(args) }`, ""},
		{"dead list inside a closure of the same function", `func f() { go func() { args := []any{"pass", uint8(1)}; _ = len(args) }() }`, ""},

		{"literal spread into slog", `func f() { slog.Info("m", []any{"pass", uint8(1)}...) }`, mismatch},
		{"local spread into slog", `func f() { args := []any{"pass", uint8(1)}; slog.Info("m", args...) }`, mismatch},
		{"local spread into a logger method", `func f(l *slog.Logger) { args := []any{"pass", uint8(1)}; l.Info("m", args...) }`, mismatch},
		{"appended local spread into slog", `func f() { args := []any{}; args = append(args, "pass", uint8(1)); slog.Info("m", args...) }`, mismatch},
		{"literal as a plain argument", `func f(g func(...any)) { g([]any{"pass", uint8(1)}...) }`, mismatch},
		{"literal returned", `func f() []any { return []any{"pass", uint8(1)} }`, mismatch},
		{"local returned", `func f() []any { args := []any{"pass", uint8(1)}; return args }`, mismatch},
		{"local passed to a helper", `func h(a []any) {}
func f() { args := []any{"pass", uint8(1)}; h(args) }`, mismatch},
		{"local captured by a closure", `func f() { args := []any{"pass", uint8(1)}; go func() { slog.Info("m", args...) }() }`, mismatch},
		{"local element read", `func f() { args := []any{"pass", uint8(1)}; slog.Info("m", "k", args[1]) }`, mismatch},
		{"local ranged", `func f() { args := []any{"pass", uint8(1)}; for _, a := range args { slog.Info("m", "k", a) } }`, mismatch},
		{"local stored in a field", `type s struct{ a []any }
func f() s { args := []any{"pass", uint8(1)}; return s{a: args} }`, mismatch},
		{"local address taken", `func f() *[]any { args := []any{"pass", uint8(1)}; return &args }`, mismatch},
		{"package level list", `var args = []any{"pass", uint8(1)}`, mismatch},
		{"parameter list grown by append", `func f(args []any) { args = append(args, "pass", uint8(1)); _ = len(args) }`, mismatch},
		{"append result not assigned back", `func f(args []any) { _ = append(args, "pass", uint8(1)) }`, mismatch},
		{"append of a spread list is not an assign-back of one item list", `func f(o []any) { args := []any{"pass", uint8(1)}; args = append(args, o...); _ = len(args) }`, mismatch},
		{"short declaration append is a new list", `func f() {
	args := []any{"pass", uint8(1)}
	{
		args := append(args, "pass", uint8(2))
		_ = len(args)
	}
}`, mismatch + "," + mismatch},
		{"multi value declaration is not a dead list", `func f() { n, args := 1, []any{"pass", uint8(1)}; _, _ = n, len(args) }`, mismatch},
		{"user-defined append that logs its list", `func append(a []any, b ...any) []any { slog.Info("m", a...); return a }
func f() { args := []any{"pass", uint8(1)}; args = append(args, "pass", uint8(2)); _ = len(args) }`, mismatch},
		{"user-defined len that logs its list", `func len(a []any) int { slog.Info("m", a...); return 0 }
func f() { args := []any{"pass", uint8(1)}; _ = len(args) }`, mismatch},
		{"user-defined cap that logs its list", `func cap(a []any) int { slog.Info("m", a...); return 0 }
func f() { args := []any{"pass", uint8(1)}; _ = cap(args) }`, mismatch},
		{"local variable named len", `func f() { len := func(a []any) int { slog.Info("m", a...); return 0 }; args := []any{"pass", uint8(1)}; _ = len(args) }`, mismatch},
		{"dead list in a package that does not type-check", `func f() { undefinedCall(); args := []any{"pass", uint8(1)}; _ = len(args) }`, "refusal:typecheck"},
		{"dead outer list shadowed by a logged inner list", `func f() {
	args := []any{"pass", uint8(1)}
	_ = len(args)
	{
		args := []any{"pass", uint8(2)}
		slog.Info("m", args...)
	}
}`, mismatch},
		{"logged outer list shadowed by a dead inner list", `func f() {
	args := []any{"pass", uint8(1)}
	slog.Info("m", args...)
	{
		args := []any{"pass", uint8(2)}
		_ = len(args)
	}
}`, mismatch},
		{"package variable assigned a literal here and logged elsewhere", `var shared []any
func f() { shared = []any{"pass", uint8(1)}; _ = len(shared) }
func g() { slog.Info("m", shared...) }`, mismatch},
		{"package variable grown by append here and logged elsewhere", `var shared []any
func f() { shared = append(shared, "pass", uint8(1)); _ = len(shared) }
func g() { slog.Info("m", shared...) }`, mismatch},
		{"package variable grown by append beside a dead local list", `var shared []any
func f() { dead := []any{"note", 1}; _ = len(dead); shared = append(shared, "pass", uint8(1)) }
func g() { slog.Info("m", shared...) }`, mismatch},
		{"receiver grown by append", `type L []any
func (r L) f() { r = append(r, "pass", uint8(1)); _ = len(r) }`, mismatch},
		{"assign-back from another list", `func f(o []any) {
	args := []any{"pass", uint8(1)}
	args = append(o, "pass", uint8(2))
	_ = len(args)
}`, mismatch + "," + mismatch},
		{"blank assigned literal stays checked", `func f() { _ = []any{"pass", uint8(1)} }`, mismatch},
		{"list defined by append is not a literal list", `func f(o []any) { args := append(o, "pass", uint8(1)); _ = len(args) }`, mismatch},
	}
	for _, c := range cases {
		got := benignDetails(scanSource(t, header+c.body+"\n"))
		if got != c.want {
			t.Errorf("%s: R10 details %q want %q", c.name, got, c.want)
		}
	}
}
