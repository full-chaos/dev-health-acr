package contextfabric

import "github.com/full-chaos/dev-health-acr/internal/logsanitize"

// SanitizeLogAttr (CHAOS-5544) is THE sanitizer for a string that
// originates from a decoded request before it becomes a structured log
// attribute value anywhere in this repo.
//
// RELOCATED (CHAOS-5558): the actual implementation now lives in
// internal/logsanitize -- see that package's doc comment for why (internal/
// auth needed the SAME barrier and cannot import internal/contextfabric,
// which already transitively imports internal/auth; a real cycle,
// `go list -deps` confirmed). This stays a real `func` declaration, not a
// package-level `var` alias, on purpose: chaos5544_sanitizer_instrument_test.go
// resolves the barrier by identity (info.Uses[ident].(*types.Func), then
// Pkg().Path()+Name()) -- a var of function type would type-check to
// *types.Var at every one of this package's own ~40 existing call sites and
// make the zero-tolerance gate report all of them as unresolved. Every
// existing call site in this package and genkitruntime is UNCHANGED; this
// is the only file that moved.
//
// Every attribute in this package or in genkitruntime's decision-log
// helpers that originates from a decoded request goes through this ONE
// function; nothing else in either package re-implements sanitization of
// such a value (`requestIDLogAttrs` below, and
// genkitruntime.logInterpretDecision/logSynthesizeDecision's `request_id`
// field, are its only callers).
func SanitizeLogAttr(s string) string {
	return logsanitize.SanitizeLogAttr(s)
}

// SanitizeLogStrings (CHAOS-5544, r2 P1) applies SanitizeLogAttr to every
// element of a []string log attribute value -- an r2 review round found
// three sites (graphrank/tracer.go's top_ids/fired_ids/eliminated_ids)
// logging a raw []string directly: the whole-tree instrument classified by
// SCALAR string type only, so a slice of unsanitized strings was invisible
// to it. Returns a NEW slice; the caller's own backing array is never
// mutated (an ID list can be shared with other readers, e.g. a served
// response, that must keep the unsanitized original). RELOCATED (CHAOS-5558)
// the same way SanitizeLogAttr was, for the same reason -- see above.
func SanitizeLogStrings(ss []string) []string {
	return logsanitize.SanitizeLogStrings(ss)
}

// There is deliberately no SanitizeLogAttrs([]any) []any barrier here. An
// r2 review round found seven sites building a `[]any{key, value, ...}`
// slog attribute slice INCREMENTALLY across several `append` calls and
// then spreading it (`attrs...`) -- a shape the whole-tree instrument's
// static scan first skipped entirely, then (a first fix attempt) tried to
// close by sanitizing the whole already-built slice at the spread's own
// boundary. The PR-ref CodeQL go/log-injection gate caught that fix as
// wrong before merge: CodeQL's array-taint model taints the WHOLE
// returned []any whenever ANY input element traces to a request source,
// even one going through this function's `default: out[i] = v` no-op
// passthrough for a request-derived NUMBER -- it cannot see that the
// passthrough is safe specifically because a number cannot carry a forged
// line break. A function shaped func([]any) []any is, to CodeQL, no
// better a barrier than the hand-rolled loop this whole ticket exists to
// replace.
//
// The fix instead sanitizes each qualifying string/[]string value
// individually, at its OWN append call (`append(attrs, "key",
// SanitizeLogAttr(value))`), matching the shape a direct `[]any{"key",
// SanitizeLogAttr(value)}` literal already used and CodeQL already
// recognized as clean at every other site. chaos5544_sanitizer_instrument
// _test.go's Rule D/E enforce this at every append/field-builder call
// site reachable from a logger spread, and separately require the
// spread's own construction to be STATICALLY traceable back to composite
// literals and such appends (never merely trusted because it LOOKS like a
// []any) -- an opaque spread is a finding, never silently accepted.
