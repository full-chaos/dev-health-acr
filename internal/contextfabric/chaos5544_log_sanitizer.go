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
