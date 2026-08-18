package identity

import "strings"

// EncodeSegment applies the registry's canonical segment encoding used for
// every variable segment of a changed-kind canonical id (design brief
// §1.1): '%' is escaped to "%25" FIRST, then ':' is escaped to "%3A".
//
// The order is load-bearing, not stylistic. Escaping '%' first guarantees
// every '%' byte that survives into the final output is the first
// character of a "%25" or "%3A" triple this function inserted -- never a
// '%' carried over unescaped from the input. That is what makes
// DecodeSegment's own two-step, opposite-order unescape exactly invert
// this function for arbitrary input, including input that already
// contains the literal substrings "%25" or "%3A" (codec_test.go's
// adversarial-input cases). Reversing the escape order would let an
// input's literal "%3A" collide with an encoded ':', breaking injectivity.
func EncodeSegment(raw string) string {
	escaped := strings.ReplaceAll(raw, "%", "%25")
	escaped = strings.ReplaceAll(escaped, ":", "%3A")
	return escaped
}

// DecodeSegment inverts EncodeSegment: ':' is unescaped first ("%3A" ->
// ':'), then '%' is unescaped second ("%25" -> '%') -- the exact mirror
// ordering EncodeSegment's doc comment proves is required. It never
// errors; every string EncodeSegment can produce is decodable by
// construction, so the error return exists only to keep the call sites
// that plumb it through (SQL-side decode would be fallible) symmetric.
func DecodeSegment(encoded string) (string, error) {
	unescaped := strings.ReplaceAll(encoded, "%3A", ":")
	unescaped = strings.ReplaceAll(unescaped, "%25", "%")
	return unescaped, nil
}

// SQLSegmentEncodeFragment is the registry-pinned SQL parity fragment
// (design brief §1.1): the ClickHouse expression S2's SQL-side producers
// are required to emit, and which must produce byte-identical output to
// EncodeSegment for the same input column. This package does not execute
// SQL -- the fragment is pinned here as a string so a drift between the Go
// codec and the SQL expression shows up as a single changed constant under
// review, and codec_test.go cross-tests EncodeSegment against a literal
// Go transcription of this exact fragment's semantics over pinned and
// randomized vectors.
const SQLSegmentEncodeFragment = `replaceAll(replaceAll(col, '%', '%25'), ':', '%3A')`

// JoinSegments encodes every segment with EncodeSegment and joins them
// with ':'. This uniform closure -- applied to EVERY segment, not just
// ones observed to contain ':' or '%' -- is what closes the
// encoding-ambiguity class named in design brief §1.1: without it,
// (a, "b:c") and ("a:b", c) would join to the same string.
func JoinSegments(segments ...string) string {
	encoded := make([]string, len(segments))
	for i, s := range segments {
		encoded[i] = EncodeSegment(s)
	}
	return strings.Join(encoded, ":")
}
