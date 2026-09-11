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

// SanitizeLogAttrs (CHAOS-5544, r2 P1) sanitizes a whole `[]any{key, value,
// key, value, ...}` slog attribute slice before it is spread into a logger
// call (`logger.InfoContext(ctx, msg, SanitizeLogAttrs(attrs)...)`). An r2
// review round found seven sites building this slice INCREMENTALLY across
// several `append` calls rather than one literal, then spreading it with
// `attrs...` -- a shape the whole-tree instrument's static scan explicitly
// skipped (an ellipsis call's argument is a runtime value, not visible at
// the call site), so every incrementally-appended value at those seven
// sites reached the sink completely unsanitized regardless of type. This
// is the one exception to "SanitizeLogAttr(Strings) at the value
// expression": a spread's contents are not statically enumerable the way a
// literal's are, so the barrier moves to the spread's OWN boundary and
// sanitizes every string/[]string value in the already-built slice at
// runtime, keys and non-string values (counts, bools, closed-enum
// conversions) passed through unchanged. Returns a NEW slice; nil in means
// nil out, an odd-length slice (a caller bug -- key with no value) has its
// trailing key passed through unchanged rather than panicking.
func SanitizeLogAttrs(attrs []any) []any {
	if attrs == nil {
		return nil
	}
	out := make([]any, len(attrs))
	for i, v := range attrs {
		if i%2 == 0 {
			out[i] = v // a key: never sanitized, always a literal
			continue
		}
		switch value := v.(type) {
		case string:
			out[i] = SanitizeLogAttr(value)
		case []string:
			out[i] = SanitizeLogStrings(value)
		default:
			out[i] = v
		}
	}
	return out
}
