package sidecar

import (
	"strings"
	"testing"
)

// This file locks safeInline's CHAOS-2908 entity-assisted-autolink
// remediation with checked-in, EXACT expected-output fixtures, rather than
// routing every assertion through activeGFMConstructPresent
// (render_gfm_scanner_test.go). That scanner is a real, independently
// shaped second implementation and stays valuable as a structural,
// per-node-type regression net (see render_inline_remediation_test.go and
// render_gfm_scanner_test.go), but every one of its checks is still this
// codebase's own model of what a GFM parser does - it can share a blind
// spot with render.go's neutralizeAutolinks/neutralizeEntityReferences if
// both were reasoned about the same (mistaken) way, and a scanner-only test
// suite would not notice. The fixtures below break that dependency: every
// "want" string is a fixed byte sequence captured from safeInline's actual
// output and then independently cross-validated - out-of-band, outside this
// repository, with no Node/npm/TypeScript dependency added here - against
// the real remark-gfm@4.0.1 parser (unified@11 + remark-parse@11 +
// remark-gfm@4.0.1), walking the resulting mdast tree for
// link/linkReference/image/imageReference/html/definition nodes. Two
// independent oracle runs back this file:
//  1. every RAW (pre-safeInline) vector below was confirmed to form a live
//     link node in real remark-gfm (proving the entity-decode hazard these
//     fixtures close is real, not hypothetical - e.g. raw
//     "https&colon;//evil.example" parses to a link node with
//     url="https://evil.example", raw "www&period;evil.example" to
//     url="http://www.evil.example", raw "user@example&period;com" to
//     url="mailto:user@example.com");
//  2. every "want" string below (safeInline's actual output for that raw
//     vector) was confirmed to form ZERO such hazard nodes.
//
// A future change to render.go that alters any of these exact bytes -
// whether or not the new output happens to still satisfy
// activeGFMConstructPresent's own model - fails this file immediately, by
// simple string comparison, with no GFM-parsing knowledge required to
// diagnose the regression from the (byte-exact, then-vs-now) diff alone.
func TestSafeInlineExactOutputFixturesForEntityAssistedAutolinkVectors(t *testing.T) {
	type fixture struct {
		name string
		raw  string
		want string
	}
	cases := []fixture{
		// The five exact reviewer-confirmed vectors (CHAOS-2908 follow-up):
		// each decodes, once fed through a real GFM parser's entity
		// resolution, into a complete autolink trigger that no fixed-byte
		// scan of the RAW value alone could ever see, since the
		// trigger-completing punctuation - or, for the fourth vector, part
		// of the scheme word itself - is not literal text in the raw value
		// at all. safeInline defangs each by inserting exactly one visible
		// ASCII space immediately before the trigger's completion point;
		// every original byte, including every entity's own spelling,
		// survives untouched.
		{
			name: "scheme completed by entity colon",
			raw:  "https&colon;//evil.example",
			want: "https &colon;//evil.example",
		},
		{
			name: "scheme completed by chained entity colon and slashes",
			raw:  "https&colon;&sol;&sol;evil.example",
			want: "https &colon;&sol;&sol;evil.example",
		},
		{
			name: "www completed by entity period",
			raw:  "www&period;evil.example",
			want: "www &period;evil.example",
		},
		{
			name: "scheme word itself partially entity-spelled",
			raw:  "h&#116;tps://evil.example",
			want: "h&#116;tps ://evil.example",
		},
		{
			name: "email domain completed by entity period",
			raw:  "user@example&period;com",
			want: "user @example&period;com",
		},
		// Additional shapes of the same class: case-insensitive scheme,
		// an entirely entity-spelled "www" (three separate decimal
		// references - no literal "www" text in the raw value at all),
		// decimal and hex numeric-reference variants of the colon
		// reference, and several entity-assisted triggers mixed into one
		// realistic provenance-shaped value.
		{
			name: "uppercase entity-encoded scheme",
			raw:  "HTTPS&colon;//EVIL.EXAMPLE",
			want: "HTTPS &colon;//EVIL.EXAMPLE",
		},
		{
			name: "fully entity-spelled www keyword",
			raw:  "&#119;&#119;&#119;.evil.example",
			want: "&#119;&#119;&#119; .evil.example",
		},
		{
			name: "decimal entity colon reference",
			raw:  "https&#58;//evil.example",
			want: "https &#58;//evil.example",
		},
		{
			name: "hex entity colon reference",
			raw:  "https&#x3A;//evil.example",
			want: "https &#x3A;//evil.example",
		},
		{
			name: "three mixed entity-assisted triggers in one value",
			raw:  "see https&colon;//a.example and www&period;b.example and user@c&period;example for details",
			want: "see https &colon;//a.example and www &period;b.example and user @c&period;example for details",
		},
		// Benign byte-identical controls: an entity decoding to a
		// character that never combines with adjacent trigger text (a
		// decimal number, a dotted version string), and a www-prefix
		// entity decode still blocked by its own left-boundary letter
		// (the "9www."/"mywww." boundary rule applies identically whether
		// the "." is literal or entity-decoded) - none of these form any
		// hazard under real remark-gfm, so safeInline must leave every one
		// completely untouched, proving this remediation is surgical
		// rather than a blanket "defang every entity near punctuation".
		{
			name: "benign entity-decoded decimal number",
			raw:  "3&period;14",
			want: "3&period;14",
		},
		{
			name: "benign entity-decoded dotted version string",
			raw:  "v1&period;2&period;3",
			want: "v1&period;2&period;3",
		},
		{
			name: "www boundary letter still blocks after entity decode",
			raw:  "embedded mywww&period;example here",
			want: "embedded mywww&period;example here",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := safeInline(c.raw)
			if got != c.want {
				t.Fatalf("safeInline(%q):\n got=%q\nwant=%q", c.raw, got, c.want)
			}
		})
	}
}

// TestSafeInlineExactByteBudgetAndUTF8BoundariesForEntityAssistedVectors
// locks the exact output shape at the byte-budget and UTF-8 rune boundaries
// specifically where an entity-assisted autolink trigger interacts with
// enforceInlineByteBudget/truncateToValidUTF8, extending the existing
// exact-boundary pattern already established for plain content by
// TestSafeInlineByteBudgetExactBoundaries. Unlike the GFM-oracle matrix
// above, these expectations are pure byte/rune-counting arithmetic over
// safeInline's own documented truncation contract (maxSanitizedMessageLength
// = 500, UTF-8-safe cut, no dangling backslash) - not something a GFM parser
// has any opinion on - so they are deterministically computable and verified
// here directly, without an external oracle.
func TestSafeInlineExactByteBudgetAndUTF8BoundariesForEntityAssistedVectors(t *testing.T) {
	t.Run("entity-assisted trigger defanged then truncated mid-suffix, no dangling escape", func(t *testing.T) {
		// 494 boundary-respecting digits ("9", which - unlike "a" - never
		// blocks bareHTTPAutolinkTrigger's left-boundary check, see
		// TestSafeInlineGoldenRemarkGFMMatrix's "scheme with digit
		// boundary" row) + the first reviewer vector: neutralizeAutolinks
		// still inserts its one defanging space after "https", producing a
		// 521-byte fully-transformed value, which enforceInlineByteBudget
		// then cuts to exactly the 500-byte budget - landing exactly on
		// that inserted space, one byte before the entity reference
		// begins, so the whole entity and domain suffix are truncated away
		// entirely.
		raw := strings.Repeat("9", 494) + "https&colon;//evil.example"
		want := strings.Repeat("9", 494) + "https "
		got := safeInline(raw)
		if got != want {
			t.Fatalf("safeInline(494x'9'+reviewer-vector-1):\n got=%q (len %d)\nwant=%q (len %d)", got, len(got), want, len(want))
		}
		if len(got) != maxSanitizedMessageLength {
			t.Fatalf("expected exactly the %d-byte budget, got %d", maxSanitizedMessageLength, len(got))
		}
	})
	t.Run("entity-assisted trigger fits exactly at the 500-byte budget with no truncation", func(t *testing.T) {
		// 473 digits + the defanged first reviewer vector (473 + 5 + 1 +
		// 7 + 14 = 500) lands EXACTLY on the budget: the fully transformed
		// value must survive completely untruncated, matching
		// TestSafeInlineByteBudgetExactBoundaries's own
		// "exactly 500 plain bytes survive untouched" boundary contract
		// extended to entity-assisted-trigger content.
		raw := strings.Repeat("9", 473) + "https&colon;//evil.example"
		want := strings.Repeat("9", 473) + "https &colon;//evil.example"
		got := safeInline(raw)
		if got != want {
			t.Fatalf("safeInline(473x'9'+reviewer-vector-1):\n got=%q (len %d)\nwant=%q (len %d)", got, len(got), want, len(want))
		}
		if len(got) != maxSanitizedMessageLength {
			t.Fatalf("expected exactly the %d-byte budget with no truncation, got %d", maxSanitizedMessageLength, len(got))
		}
	})
	t.Run("entity-assisted trigger one byte over budget truncates exactly one trailing byte", func(t *testing.T) {
		// One more leading digit (474) than the exact-fit case above pushes
		// the fully transformed value to 501 bytes; enforceInlineByteBudget
		// must cut exactly the final byte ("e" of "example"), leaving the
		// defanging space and the entire entity reference intact.
		raw := strings.Repeat("9", 474) + "https&colon;//evil.example"
		want := strings.Repeat("9", 474) + "https &colon;//evil.exampl"
		got := safeInline(raw)
		if got != want {
			t.Fatalf("safeInline(474x'9'+reviewer-vector-1):\n got=%q (len %d)\nwant=%q (len %d)", got, len(got), want, len(want))
		}
		if len(got) != maxSanitizedMessageLength {
			t.Fatalf("expected exactly the %d-byte budget, got %d", maxSanitizedMessageLength, len(got))
		}
	})
	t.Run("UTF-8 multi-byte rune at the truncation cut backs off to the last complete rune", func(t *testing.T) {
		// 249 copies of U+00E9 ("é", a 2-byte UTF-8 rune: 498 bytes) + one
		// ASCII "h" (499 bytes) + one more "é" (501 bytes total) places the
		// 500-byte cut exactly on the trailing é's lead byte, splitting it:
		// truncateToValidUTF8 must back off one further byte rather than
		// emit an invalid partial rune, dropping the incomplete é entirely
		// and leaving exactly 499 valid UTF-8 bytes.
		raw := strings.Repeat("\u00e9", 249) + "h" + "\u00e9"
		want := strings.Repeat("\u00e9", 249) + "h"
		got := safeInline(raw)
		if got != want {
			t.Fatalf("safeInline(249x'é'+h+é):\n got=%q (len %d)\nwant=%q (len %d)", got, len(got), want, len(want))
		}
		if len(got) != 499 {
			t.Fatalf("expected the split trailing rune to be dropped whole, leaving 499 bytes, got %d", len(got))
		}
	})
}
