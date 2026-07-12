package sidecar

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// This file locks the CHAOS-2908 remediation pass over safeInline: the
// four blockers the release reviewers reported against the prior fix
// (bareEmailTrigger under-matching punctuation-ending local parts,
// safeInline's byte budget being enforced before rather than after
// markdown/autolink escaping, the scheme/www triggers over-defanging text
// no real GFM parser ever links, and HTML character references reactivating
// an email/bidi hazard after decoding). Every non-trivial expectation below
// was verified against the real remark-gfm parser (unified+remark-parse+
// remark-gfm, walking the resulting mdast tree for link/text nodes) in an
// out-of-band probe session, not just reasoned about from the GFM spec text.

// TestSafeInlineDefangsEmailLocalPartsEndingInPunctuation locks the fix for
// the confirmed bareEmailTrigger gap: the real remark-gfm parser links
// "user-@example.com", "user_@example.com", "user.@example.com",
// "user+@example.com", and even an all-punctuation local part like
// "-@example.com" or "...@example.com" exactly as readily as
// "user@example.com" - GFM's local-part class has no start/end alphanumeric
// requirement at all, unlike the prior regex's `[a-z0-9](?:...[a-z0-9])?`
// shape, which silently let every one of these survive safeInline unbroken.
func TestSafeInlineDefangsEmailLocalPartsEndingInPunctuation(t *testing.T) {
	for _, raw := range []string{
		"contact user-@example.com for access",
		"contact user_@example.com for access",
		"contact user.@example.com for access",
		"contact user+@example.com for access",
		"leading punctuation -@example.com only",
		"leading punctuation ---@example.com only",
		"all punctuation ...@example.com local part",
	} {
		got := safeInline(raw)
		if found := activeGFMConstructPresent(got); len(found) > 0 {
			t.Fatalf("active GFM construct(s) survived safeInline: raw=%q got=%q found=%v", raw, got, found)
		}
	}
}

// TestSafeInlineFinalOutputNeverExceedsByteBudgetAfterEscaping locks the
// fix for the confirmed byte-budget-ordering bug: the pre-fix pipeline
// truncated to maxSanitizedMessageLength BEFORE escapeMarkdownInline and
// neutralizeAutolinks ran, so a value consisting mostly of markdown-active
// punctuation (each byte doubling to a backslash-escaped pair) could still
// leave safeInline's final, actually-rendered output up to 2x over budget -
// exactly the "500 '*' -> 1000 bytes" case release reviewers reported. The
// budget must be the last thing enforced, on the fully escaped/defanged
// string.
func TestSafeInlineFinalOutputNeverExceedsByteBudgetAfterEscaping(t *testing.T) {
	cases := map[string]string{
		"all markdown-active asterisks":   strings.Repeat("*", 600),
		"all markdown-active backslashes": strings.Repeat(`\`, 600),
		"all markdown-active mixed":       strings.Repeat("*_`[]()<>|~", 60),
		"repeated autolink triggers":      strings.Repeat("see https://evil.example and www.evil.example and user@evil.example ", 20),
		"repeated entity references":      strings.Repeat("&#64;&lt;&gt;&commat;", 60),
	}
	for name, raw := range cases {
		got := safeInline(raw)
		if len(got) > maxSanitizedMessageLength {
			t.Fatalf("%s: safeInline output exceeded the %d-byte budget after escaping: got %d bytes: %q",
				name, maxSanitizedMessageLength, len(got), got)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("%s: safeInline output is not valid UTF-8: %q", name, got)
		}
	}
}

// TestSafeInlineByteBudgetExactBoundaries locks the exact 499/500-byte
// boundary behavior explicitly, both for content needing no transformation
// (must survive byte-identical, matching truncateUTF8's own exact-boundary
// contract in api_errors.go) and for content whose escaping pushes it just
// past the boundary from an already-at-the-limit raw value.
func TestSafeInlineByteBudgetExactBoundaries(t *testing.T) {
	t.Run("499 plain bytes survive untouched", func(t *testing.T) {
		raw := strings.Repeat("a", maxSanitizedMessageLength-1)
		got := safeInline(raw)
		if got != raw {
			t.Fatalf("expected 499-byte plain content to survive byte-identical, got %d bytes", len(got))
		}
	})
	t.Run("exactly 500 plain bytes survive untouched", func(t *testing.T) {
		raw := strings.Repeat("a", maxSanitizedMessageLength)
		got := safeInline(raw)
		if got != raw {
			t.Fatalf("expected exactly-500-byte plain content to survive byte-identical, got %d bytes", len(got))
		}
	})
	t.Run("501 plain bytes truncate to exactly 500", func(t *testing.T) {
		raw := strings.Repeat("a", maxSanitizedMessageLength+1)
		got := safeInline(raw)
		if len(got) != maxSanitizedMessageLength {
			t.Fatalf("expected truncation to exactly %d bytes, got %d", maxSanitizedMessageLength, len(got))
		}
	})
	t.Run("escaping a 500-byte raw value never leaves a dangling backslash", func(t *testing.T) {
		// 499 plain bytes followed by one markdown-active byte: escaping
		// expands this to 501 bytes (499 + "\*"), one over budget, with the
		// cut landing immediately after the inserted backslash and before
		// the character it escapes - the exact "dangling escape" shape the
		// final-budget enforcement must never leave behind.
		raw := strings.Repeat("a", maxSanitizedMessageLength-1) + "*"
		got := safeInline(raw)
		if len(got) > maxSanitizedMessageLength {
			t.Fatalf("expected output to respect the %d-byte budget, got %d: %q", maxSanitizedMessageLength, len(got), got)
		}
		if strings.HasSuffix(got, `\`) {
			t.Fatalf("safeInline left a dangling, unpaired trailing backslash escape: %q", got)
		}
		if got != strings.Repeat("a", maxSanitizedMessageLength-1) {
			t.Fatalf("expected the incomplete trailing escape pair to be dropped whole, got %q", got)
		}
	})
}

// TestSafeInlineLeavesEmbeddedNonBoundaryWWWUntouched locks that the www
// trigger's left-boundary rule (blocked only by an immediately preceding
// ASCII letter or digit, confirmed against the real remark-gfm parser) does
// not regress readable identifiers containing "www" as a mid-word
// substring, e.g. a hostname fragment like "mywww.example" or "xwww.example"
// that a real GFM parser never links.
func TestSafeInlineLeavesEmbeddedNonBoundaryWWWUntouched(t *testing.T) {
	for _, raw := range []string{
		"embedded mywww.example here",
		"embedded xwww.example here",
		"embedded 9www.example here",
	} {
		if got := safeInline(raw); got != raw {
			t.Fatalf("expected non-hazardous embedded www text to survive byte-identical: raw=%q got=%q", raw, got)
		}
	}
}

// TestSafeInlineNeutralizesHazardousEntityReferences locks the fix for the
// release reviewers' fourth confirmed finding: an HTML named or numeric/hex
// character reference decodes, under the real remark-gfm parser, into an
// active hazard - "user&#64;example.com" and "contact user&commat;example.com"
// both resolve to a live mailto: link node exactly as if the "@" had been
// written raw, "before&#x202E;after" decodes to a literal embedded U+202E
// RIGHT-TO-LEFT OVERRIDE character, "a &lt;script&gt; b" decodes to literal
// "<"/">" text capable of forming raw HTML or a bracketed autolink once
// re-embedded elsewhere, and "user&NewLine;@example.com" decodes to a
// literal embedded newline capable of forging fake markdown line structure.
// None of sanitizeInlineContent (which only ever sees the raw "&...;"
// bytes, never a decoded character), the fixed "@"/"<"/">"/control/bidi
// hazard-rune scan neutralizeAutolinks's decoded-view trigger detection
// runs alongside (context-free by design, since those specific characters
// are unconditionally dangerous regardless of surrounding text - see
// CHAOS-2908 follow-up below for the context-DEPENDENT ":"/"/"/"." case
// that same decoded-view detection also now covers), or escapeMarkdownInline
// (which never touches "&") defends against this alone without the fixed
// hazard-rune check below.
func TestSafeInlineNeutralizesHazardousEntityReferences(t *testing.T) {
	for _, raw := range []string{
		"user&#64;example.com",
		"user&#064;example.com",
		"user&#x40;example.com",
		"user&#X40;example.com",
		"contact user&commat;example.com",
		"before&#x202E;after",
		"before&#X202E;after",
		"a &lt;script&gt; b",
		"a &LT;script&GT; b",
		"raw html via entity &lt;a href=&quot;https://evil.example&quot;&gt;click&lt;/a&gt;",
		"contact user&NewLine;@example.com",
		"two hazards &#64;example.com and &commat;evil.example",
	} {
		got := safeInline(raw)
		if found := activeGFMConstructPresent(got); len(found) > 0 {
			t.Fatalf("active GFM construct/entity hazard survived safeInline: raw=%q got=%q found=%v", raw, got, found)
		}
		if strings.ContainsAny(got, "\n\t") {
			t.Fatalf("a decoded line-break character survived safeInline: raw=%q got=%q", raw, got)
		}
	}
}

// TestSafeInlinePreservesBenignEntityReferencesByteIdentical locks that the
// entity-neutralization fix is surgical, not a blanket "escape every &":
// an HTML entity that decodes to something with no autolink/control/bidi
// hazard (a copyright sign, an ellipsis, a plain "&"), or text that merely
// contains an "&" with no complete, real entity shape at all, must survive
// completely unaltered - matching real remark-gfm behavior, which also
// never touches "AT&T", "foo & bar", or an unrecognized entity name like
// "&notarealentity;"/"&ampfoo;" (confirmed: Go's html.UnescapeString
// partially decodes the "&not"/"&amp" legacy-no-semicolon prefix of those
// two, but the real remark-gfm parser - which requires the full, semicolon-
// terminated form - leaves them as complete literal text).
func TestSafeInlinePreservesBenignEntityReferencesByteIdentical(t *testing.T) {
	for _, raw := range []string{
		"copyright &copy; 2024",
		"wait&hellip;really",
		"em dash &mdash; here",
		"literal ampersand &amp; stays",
		"AT&T corp",
		"foo & bar",
		"x&notarealentity;y",
		"x&ampfoo;y",
	} {
		if got := safeInline(raw); got != raw {
			t.Fatalf("expected benign entity/ampersand text to survive byte-identical: raw=%q got=%q", raw, got)
		}
	}
}

// gfmOracleMatrixCase is one row of the golden remark-gfm cross-validation
// matrix below: raw is fed to safeInline, and wantByteIdentical records
// whether the real remark-gfm parser (unified+remark-parse+remark-gfm)
// forms no link/text hazard for raw at all, in which case safeInline must
// leave it completely untouched.
type gfmOracleMatrixCase struct {
	name              string
	raw               string
	wantByteIdentical bool
}

// TestSafeInlineGoldenRemarkGFMMatrix is the "actual remark-gfm/GitHub-style
// mdast matrix" this remediation pass is required to include: every case
// below reflects a real parse-tree observation from an out-of-band
// unified+remark-parse+remark-gfm probe session run against these exact
// input strings (walking the resulting mdast tree for link/linkReference/
// image/imageReference/html/definition/text nodes), not an assumption about
// what the GFM spec text implies. It is the independent oracle for this
// remediation - distinct from, and cross-checking, both production's own
// regex triggers and the render_gfm_scanner_test.go node-shaped scanner -
// so a bug shared between production and scanner can still be caught here.
func TestSafeInlineGoldenRemarkGFMMatrix(t *testing.T) {
	cases := []gfmOracleMatrixCase{
		// Positive controls: real remark-gfm forms a link node; safeInline
		// must alter these (defang) and leave them fully inert.
		{"scheme with punctuation boundary", "see .https://evil.example now", false},
		{"scheme with paren boundary", "see (https://evil.example) now", false},
		{"scheme with digit boundary", "9https://evil.example is linked", false},
		{"scheme with underscore boundary", "_https://evil.example is linked", false},
		{"www with dot boundary", "embedded .www.example here", false},
		{"www with underscore boundary", "1_www.example is linked", false},
		{"www with hyphen boundary", "-www.example is linked", false},
		{"email underscore domain label", "a@sub_domain.example is linked", false},
		{"email numeric domain label", "a@111.example is linked", false},
		{"email punycode domain label", "a@xn--tst-qla.example is linked", false},
		{"email leading-hyphen domain label", "a@-example.com is linked", false},
		// Negative controls: real remark-gfm forms NO link/text hazard;
		// safeInline must leave these completely byte-identical.
		{"bare ftp scheme", "download from ftp://files.evil.example/payload.exe", true},
		{"scheme preceded by letter", "see foohttps://evil.example now", true},
		{"scheme preceded by single letter", "see xhttps://evil.example now", true},
		{"www preceded by letter", "embedded mywww.example here", true},
		{"www preceded by digit", "embedded 9www.example here", true},
		{"invalid double-dot domain", "a@example..com is not linked", true},
		{"clean repo slug", "acme/widgets-service:v1.2.3", true},
		{"clean commit sha", "sha256:deadbeefcafe0123", true},
		{"benign named entity", "copyright &copy; 2024", true},
		{"unrecognized entity name", "x&notarealentity;y", true},
		// Positive controls, CHAOS-2908 entity-assisted-autolink follow-up:
		// the exact five vectors three independent release-certification
		// reviewers confirmed resolve to a live remark-gfm link node even
		// though the raw value contains no complete, literal
		// "://"/"www."/"@domain.tld" trigger text at all - only entity/
		// numeric-character references that decode into the missing
		// trigger-completing punctuation (or, for the fourth vector, into
		// part of the scheme word itself). Every one of these, and every
		// other case in this matrix, was additionally verified end-to-end:
		// safeInline's ACTUAL Go output for it was captured and fed to a
		// real unified+remark-parse+remark-gfm process (not just this
		// scanner), confirming zero link/linkReference/image/html/
		// definition nodes survive.
		{"entity-encoded colon completes https scheme", "https&colon;//evil.example", false},
		{"entity-encoded colon and double slash complete https scheme", "https&colon;&sol;&sol;evil.example", false},
		{"entity-encoded period completes www prefix", "www&period;evil.example", false},
		{"entity-encoded letter completes the scheme word itself", "h&#116;tps://evil.example", false},
		{"entity-encoded period completes email domain", "user@example&period;com", false},
		// Same class, additional shapes: uppercase/case-insensitive scheme
		// and www, an entirely entity-spelled "www" (three separate decimal
		// references, no literal "www" text at all), hex/decimal numeric
		// variants of the colon reference, and several triggers mixed in one
		// realistic provenance-shaped value.
		{"uppercase entity-encoded scheme", "HTTPS&colon;//EVIL.EXAMPLE", false},
		{"fully entity-spelled www keyword", "&#119;&#119;&#119;.evil.example", false},
		{"decimal entity colon reference", "https&#58;//evil.example", false},
		{"hex entity colon reference", "https&#x3A;//evil.example", false},
		{"three mixed entity-assisted triggers in one value", "see https&colon;//a.example and www&period;b.example and user@c&period;example for details", false},
		// Negative controls for the same follow-up: an entity decoding to
		// "." that never combines with adjacent trigger text (a decimal
		// number, a dotted version string) and a www-prefix entity decode
		// still blocked by its own left-boundary letter must remain
		// completely untouched, exactly like their non-entity equivalents
		// above - the CHAOS-2908 follow-up fix is surgical, not a blanket
		// "defang every entity near punctuation".
		{"benign entity-decoded decimal number", "3&period;14", true},
		{"benign entity-decoded dotted version string", "v1&period;2&period;3", true},
		{"www boundary letter still blocks after entity decode", "embedded mywww&period;example here", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := safeInline(c.raw)
			if found := activeGFMConstructPresent(got); len(found) > 0 {
				t.Fatalf("active GFM construct/entity hazard survived safeInline: raw=%q got=%q found=%v", c.raw, got, found)
			}
			if c.wantByteIdentical && got != c.raw {
				t.Fatalf("expected oracle-confirmed non-hazardous text to survive byte-identical: raw=%q got=%q", c.raw, got)
			}
		})
	}
}

// TestSafeInlineDefangsExactReviewerVectorsWithReadablePreservation locks
// the CHAOS-2908 entity-assisted-autolink follow-up's exact output shape,
// not just "no hazard survives": every byte of every original vector -
// scheme letters, entity spellings, domain text - must still be present
// somewhere in the output (defanging, per this whole file's established
// convention, only ever ADDS a visible space; it never deletes or
// substitutes a byte), and the fix must not corrupt any entity that did
// not need touching (verified by requiring the untouched entities in
// vectors 2 and 5 to still appear in their original, undefanged "&name;"
// spelling in the output).
func TestSafeInlineDefangsExactReviewerVectorsWithReadablePreservation(t *testing.T) {
	cases := []struct {
		name         string
		raw          string
		wantContains []string
	}{
		{
			"scheme completed by entity colon",
			"https&colon;//evil.example",
			[]string{"https", "&colon;", "evil.example"},
		},
		{
			"scheme completed by chained entity colon and slashes",
			"https&colon;&sol;&sol;evil.example",
			[]string{"https", "&colon;", "&sol;", "evil.example"},
		},
		{
			"www completed by entity period",
			"www&period;evil.example",
			[]string{"www", "&period;", "evil.example"},
		},
		{
			"scheme word itself partially entity-spelled",
			"h&#116;tps://evil.example",
			[]string{"h&#116;tps", "evil.example"},
		},
		{
			"email domain completed by entity period",
			"user@example&period;com",
			[]string{"user", "@example&period;com"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := safeInline(c.raw)
			if found := activeGFMConstructPresent(got); len(found) > 0 {
				t.Fatalf("active GFM construct/entity hazard survived safeInline: raw=%q got=%q found=%v", c.raw, got, found)
			}
			if got == c.raw {
				t.Fatalf("expected the confirmed-hazardous vector to be altered (defanged), got byte-identical: %q", got)
			}
			for _, want := range c.wantContains {
				if !strings.Contains(got, want) {
					t.Fatalf("expected defanged output to still contain readable fragment %q: raw=%q got=%q", want, c.raw, got)
				}
			}
			if !strings.Contains(got, " ") {
				t.Fatalf("expected defanging to insert a plain visible space, found none: raw=%q got=%q", c.raw, got)
			}
			for _, invisible := range []rune{'\u200b', '\u200c', '\u200d', '\u2060', '\ufeff'} {
				if strings.ContainsRune(got, invisible) {
					t.Fatalf("defanging introduced an invisible zero-width character %U: %q", invisible, got)
				}
			}
		})
	}
}

// TestSafeInlineEntityAssistedAutolinkRespectsByteBudgetAndTruncation locks
// that the CHAOS-2908 entity-assisted-autolink fix composes correctly with
// safeInline's existing byte-budget/truncation machinery: neutralizeAutolinks'
// new decoded-view scan must still run on (and its space insertions still
// count toward) the FINAL, maxSanitizedMessageLength-bounded output, and a
// preTransformByteCap cut that lands in the middle of one of the reviewers'
// vectors - severing an entity reference mid-spelling, so it no longer
// matches entityReferenceTrigger at all - must never panic and must still
// produce fully inert, budget-respecting output.
func TestSafeInlineEntityAssistedAutolinkRespectsByteBudgetAndTruncation(t *testing.T) {
	t.Run("repeated entity-assisted triggers stay within the final byte budget", func(t *testing.T) {
		raw := strings.Repeat("see https&colon;//evil.example and www&period;evil.example ", 20)
		got := safeInline(raw)
		if len(got) > maxSanitizedMessageLength {
			t.Fatalf("safeInline output exceeded the %d-byte budget: got %d bytes: %q", maxSanitizedMessageLength, len(got), got)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("safeInline output is not valid UTF-8: %q", got)
		}
		if found := activeGFMConstructPresent(got); len(found) > 0 {
			t.Fatalf("active GFM construct/entity hazard survived safeInline: got=%q found=%v", got, found)
		}
	})
	t.Run("a preTransformByteCap cut severing an entity mid-vector never panics and stays inert", func(t *testing.T) {
		// preTransformByteCap is maxSanitizedMessageLength*4 (2000); pad the
		// reviewer vector with leading filler so the cap's cut point lands
		// inside the trailing "&colon;" entity reference, leaving a residual
		// "&colo" (or similar) with no terminating ";" - exactly the
		// malformed-entity shape entityReferenceTrigger must simply not
		// match, rather than crash on.
		padding := strings.Repeat("a", preTransformByteCap-5)
		raw := padding + "https&colon;//evil.example"
		got := safeInline(raw) // must not panic
		if len(got) > maxSanitizedMessageLength {
			t.Fatalf("safeInline output exceeded the %d-byte budget: got %d bytes", maxSanitizedMessageLength, len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("safeInline output is not valid UTF-8: %q", got)
		}
		if found := activeGFMConstructPresent(got); len(found) > 0 {
			t.Fatalf("active GFM construct/entity hazard survived safeInline: got=%q found=%v", got, found)
		}
	})
	t.Run("entity-assisted defanging never leaves a dangling trailing backslash at the final cut", func(t *testing.T) {
		// 499 bytes of an entity-assisted trigger repeated, then one
		// markdown-active byte: exercises enforceInlineByteBudget's dangling-
		// backslash trim (see TestSafeInlineByteBudgetExactBoundaries) on
		// output that neutralizeAutolinks has already grown with defanging
		// spaces, not just escapeMarkdownInline's own backslashes.
		raw := strings.Repeat("x", maxSanitizedMessageLength-40) + "https&colon;//evil.example*"
		got := safeInline(raw)
		if len(got) > maxSanitizedMessageLength {
			t.Fatalf("expected output to respect the %d-byte budget, got %d: %q", maxSanitizedMessageLength, len(got), got)
		}
		if strings.HasSuffix(got, `\`) {
			t.Fatalf("safeInline left a dangling, unpaired trailing backslash escape: %q", got)
		}
		if found := activeGFMConstructPresent(got); len(found) > 0 {
			t.Fatalf("active GFM construct/entity hazard survived safeInline: got=%q found=%v", got, found)
		}
	})
}
