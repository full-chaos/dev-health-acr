package contextfabric

import "strings"

// logAttrForgeryReplacer strips the two characters CWE-117 exists for -- a
// newline splits one log record into two, a carriage return can overwrite a
// line in a naive terminal renderer -- via strings.NewReplacer.Replace: the
// go/log-injection query's own documented recognized barrier shape ("line
// breaks should be removed from user input, using strings.Replace or
// similar"). CHAOS-5544: the PRE-EXISTING rune-remap-loop-only sanitizer at
// genkitruntime's decision-log sink did not clear the equivalent CodeQL
// alert -- the taint flowed straight through it -- because a hand-rolled
// loop is not a shape CodeQL's dataflow model recognizes as a barrier; this
// replacer is additive to (not a replacement for) that same rune allowlist
// below, so both the RECOGNIZED shape and the actually-complete coverage
// are present at once.
var logAttrForgeryReplacer = strings.NewReplacer("\n", "?", "\r", "?")

// SanitizeLogAttr (CHAOS-5544) is THE sanitizer for a string that
// originates from a decoded request before it becomes a structured log
// attribute value anywhere in this repo. `\n`/`\r` are replaced first via
// the recognized-shape replacer above; the SAME rune allowlist the
// PRE-EXISTING sink-side guard used (every byte outside the printable
// ASCII range 0x20-0x7e, so every control byte AND every non-ASCII byte)
// is then applied -- kept exactly, not narrowed, as the actual
// completeness guarantee (ESC, NUL, and everything else the two-character
// replacer does not enumerate). The result is bounded to 256 RUNES, never
// a raw byte slice (which could split a multi-byte UTF-8 sequence
// mid-rune and corrupt the value into invalid UTF-8 rather than merely
// truncate it) -- a unicode request id is replaced whole-rune by whole-rune,
// never left as a dangling partial byte sequence.
//
// Every attribute in this package or in genkitruntime's decision-log
// helpers that originates from a decoded request goes through this ONE
// function; nothing else in either package re-implements sanitization of
// such a value (`requestIDLogAttrs` below, and
// genkitruntime.logInterpretDecision/logSynthesizeDecision's `request_id`
// field, are its only callers).
func SanitizeLogAttr(s string) string {
	s = logAttrForgeryReplacer.Replace(s)
	runes := []rune(s)
	if len(runes) > 256 {
		runes = runes[:256]
	}
	for i, r := range runes {
		if r < 0x20 || r > 0x7e {
			runes[i] = '?'
		}
	}
	return string(runes)
}

// SanitizeLogStrings (CHAOS-5544, r2 P1) applies SanitizeLogAttr to every
// element of a []string log attribute value -- an r2 review round found
// three sites (graphrank/tracer.go's top_ids/fired_ids/eliminated_ids)
// logging a raw []string directly: the whole-tree instrument classified by
// SCALAR string type only, so a slice of unsanitized strings was invisible
// to it. Returns a NEW slice; the caller's own backing array is never
// mutated (an ID list can be shared with other readers, e.g. a served
// response, that must keep the unsanitized original).
func SanitizeLogStrings(ss []string) []string {
	if ss == nil {
		return nil
	}
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = SanitizeLogAttr(s)
	}
	return out
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
