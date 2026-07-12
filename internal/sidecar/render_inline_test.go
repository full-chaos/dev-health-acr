package sidecar

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestSafeInlineNeutralizesBidiFormatCharacters locks the CHAOS-2908
// safeInline fix: sanitizeMessage only strips C0/C1 control bytes
// (unicode.IsControl never reports true above U+00FF), so every Unicode
// bidi format character - the same "Trojan Source" class untrustedBlock
// already neutralizes - previously survived safeInline untouched and
// could visually reorder a trusted structural label (branch, repo,
// entity, source, provenance, redaction, or display label) rendered
// outside any fence. This iterates the same dangerousBidiFormatChars
// table untrustedBlock relies on, rather than hardcoding a second list.
func TestSafeInlineNeutralizesBidiFormatCharacters(t *testing.T) {
	for bidi := range dangerousBidiFormatChars {
		value := "SAFE" + string(bidi) + "EVIL"
		got := safeInline(value)
		if strings.ContainsRune(got, bidi) {
			t.Fatalf("raw bidi format character %U survived safeInline: %q", bidi, got)
		}
		if !strings.Contains(got, "SAFE") || !strings.Contains(got, "EVIL") {
			t.Fatalf("expected surrounding text to survive sanitization: %q", got)
		}
	}
}

// TestSafeInlineNeutralizesC0AndC1ControlBytes locks that ESC (ANSI/OSC
// introducer), BEL, and the 8-bit C1 control range survive safeInline
// only as inert escaped text, matching untrustedBlock's terminal-safety
// guarantee for the same class of byte.
func TestSafeInlineNeutralizesC0AndC1ControlBytes(t *testing.T) {
	value := "before\x1b[31mred\x07bell\u009bC1 after"
	got := safeInline(value)
	for _, raw := range []rune{0x1b, 0x07, 0x9b} {
		if strings.ContainsRune(got, raw) {
			t.Fatalf("raw control byte %U survived safeInline: %q", raw, got)
		}
	}
	for _, visible := range []string{"before", "red", "bell", "C1", "after"} {
		if !strings.Contains(got, visible) {
			t.Fatalf("expected surrounding text %q to survive sanitization: %q", visible, got)
		}
	}
}

// TestSafeInlineCollapsesNewlineAndTabToSingleSpace locks the "always
// one-line" invariant: unlike untrustedBlock (which preserves tab/newline
// for fenced-block formatting), a safeInline value renders on a single
// structural line, so an embedded newline or tab can never forge a fake
// heading, list item, or blockquote starting mid-value. It becomes a
// plain visible space rather than being silently deleted.
func TestSafeInlineCollapsesNewlineAndTabToSingleSpace(t *testing.T) {
	value := "main\r\n## Fake Heading\r\tindented\ttrailer"
	got := safeInline(value)
	if strings.ContainsAny(got, "\n\r\t") {
		t.Fatalf("expected no raw newline/CR/tab to survive safeInline: %q", got)
	}
	for _, want := range []string{"main", "Fake Heading", "indented", "trailer"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected surrounding text %q to survive sanitization: %q", want, got)
		}
	}
}

// TestSafeInlineOutputIsAlwaysValidUTF8 locks that safeInline never
// leaves an invalid UTF-8 byte sequence in its output, even when the
// input itself was not valid UTF-8.
func TestSafeInlineOutputIsAlwaysValidUTF8(t *testing.T) {
	invalid := "valid " + string([]byte{0xff, 0xfe, 0x80}) + " text"
	got := safeInline(invalid)
	if !utf8.ValidString(got) {
		t.Fatalf("safeInline output is not valid UTF-8: %q", got)
	}
}

// TestSafeInlineDoesNotExceedTheSanitizedMessageByteBudget locks that
// safeInline's byte budget is unchanged by the CHAOS-2908 fix: it still
// truncates to maxSanitizedMessageLength at the same sanitize-then-cut
// stage sanitizeMessage used, before any markdown/autolink escaping can
// expand the value further.
func TestSafeInlineDoesNotExceedTheSanitizedMessageByteBudget(t *testing.T) {
	raw := strings.Repeat("a", maxSanitizedMessageLength+50)
	got := safeInline(raw)
	if len(got) != maxSanitizedMessageLength {
		t.Fatalf("expected safeInline to truncate plain content to exactly %d bytes, got %d: %q",
			maxSanitizedMessageLength, len(got), got)
	}
}

// TestSafeInlineDefangsBareHTTPSAndHTTPAndWWWAutolinkTriggers locks the
// CHAOS-2908 GFM-autolink fix for safeInline: escapeMarkdownInline only
// neutralizes bracket/backtick/asterisk-style active markdown, but GFM's
// autolink-literal extension turns bare "https://", "http://", or "www."
// text into a clickable link with no brackets at all. A hosted display
// label, branch name, or provenance string containing one of these
// triggers must never survive as a contiguous, autolinkable trigger.
func TestSafeInlineDefangsBareHTTPSAndHTTPAndWWWAutolinkTriggers(t *testing.T) {
	for _, raw := range []string{
		"click here https://evil.example/x now",
		"see http://evil.example for details",
		"see www.evil.example for details",
		"see WWW.EVIL.example for details",
		"multi http://a.example and https://b.example and www.c.example",
	} {
		got := safeInline(raw)
		if bareURLTriggerPresent(got) {
			t.Fatalf("bare URL autolink trigger survived safeInline unbroken: raw=%q got=%q", raw, got)
		}
	}
}

// TestSafeInlineDefangingIsVisibleNotZeroWidth locks that the autolink
// defanging strategy inserts only a plain ASCII space - never an
// invisible zero-width character, which would itself be exactly the kind
// of silent visual-spoofing primitive this fix is closing off elsewhere.
func TestSafeInlineDefangingIsVisibleNotZeroWidth(t *testing.T) {
	got := safeInline("see https://evil.example now")
	for _, invisible := range []rune{'\u200b', '\u200c', '\u200d', '\u2060', '\ufeff'} {
		if strings.ContainsRune(got, invisible) {
			t.Fatalf("safeInline introduced an invisible zero-width character %U: %q", invisible, got)
		}
	}
	if !strings.Contains(got, " ") {
		t.Fatalf("expected the defanged output to still contain plain visible spaces: %q", got)
	}
}

// TestSafeInlinePreservesCompactReadableIdentifiers locks that the
// CHAOS-2908 fix does not regress ordinary, non-URL-shaped identifiers:
// slashes, dots, hyphens, and colons common in real repo slugs, branch
// names, and commit SHAs must survive completely unescaped and unbroken.
func TestSafeInlinePreservesCompactReadableIdentifiers(t *testing.T) {
	for _, value := range []string{
		"acme/widgets-service:v1.2.3",
		"feature/CHAOS-2908-safe-inline",
		"deadbeefcafe0123",
		"ci-log-parser",
		// A single colon with no doubled slash is not a scheme trigger.
		"sha256:deadbeefcafe0123",
		// No "@domain.tld" shape follows, so this is not an email trigger.
		"@scope/pkg-name",
		// A decimal-shaped run of digits and dots is not a www trigger.
		"v1.2.3",
	} {
		if got := safeInline(value); got != value {
			t.Fatalf("ordinary identifier characters were unexpectedly altered: raw=%q got=%q", value, got)
		}
	}
}

// TestSafeInlineHandlesCombinedBidiControlAndAutolinkVector locks that a
// single adversarial value combining a bidi override, an ANSI escape, and
// a bare autolink trigger comes out fully inert on every axis at once,
// not just whichever one a narrower test happened to isolate.
func TestSafeInlineHandlesCombinedBidiControlAndAutolinkVector(t *testing.T) {
	value := "safe\u202eevil\x1b[31m see https://evil.example now\u2066\u2069"
	got := safeInline(value)
	if strings.ContainsAny(got, "\x1b") {
		t.Fatalf("raw ESC survived combined vector: %q", got)
	}
	for _, bidi := range []rune{0x202e, 0x2066, 0x2069} {
		if strings.ContainsRune(got, bidi) {
			t.Fatalf("raw bidi format character %U survived combined vector: %q", bidi, got)
		}
	}
	if bareURLTriggerPresent(got) {
		t.Fatalf("bare URL autolink trigger survived combined vector: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("combined vector output is not valid UTF-8: %q", got)
	}
}

// TestSafeInlineLeavesBareNonHTTPSchemesAndPreLetterSchemesUntouched locks
// the CHAOS-2908 remediation for the release reviewers' reported readable-
// metadata regression: confirmed against the real remark-gfm parser
// (unified+remark-parse+remark-gfm), GFM's own autolink-literal extension
// NEVER fires for a bare (unbracketed) scheme other than http/https - not
// ftp, not xmpp, not an attacker's own custom scheme - and it never fires
// at all when the scheme word is immediately preceded by an ASCII letter
// ("foohttps://" is never linked). Defanging these anyway, as an earlier
// revision of neutralizeAutolinks did with a generalized RFC-3986-scheme
// regex, alters harmless identifiers/log lines a real GFM parser would
// have rendered as inert plain text - a regression this test locks against
// by requiring byte-identical output. Angle-bracket-wrapped forms remain a
// real hazard for ANY scheme under CommonMark's own base autolink
// production, independent of GFM, and are still fully neutralized below by
// escapeMarkdownInline's backslash-escaping of every raw "<"/">".
func TestSafeInlineLeavesBareNonHTTPSchemesAndPreLetterSchemesUntouched(t *testing.T) {
	for _, raw := range []string{
		"download from ftp://files.evil.example/payload.exe",
		"FTP://EVIL.EXAMPLE/PAYLOAD.EXE",
		"xmpp scheme xmpp://evil.example/room",
		"irc scheme irc://irc.evil.example/channel",
		"gopher scheme gopher://evil.example",
		"embedded prefix foohttps://evil.example not linked",
		"embedded prefix Ahttps://evil.example not linked",
	} {
		if got := safeInline(raw); got != raw {
			t.Fatalf("expected non-hazardous bare scheme text to survive byte-identical: raw=%q got=%q", raw, got)
		}
	}
}

// TestSafeInlineDefangsAngleBracketWrappedArbitrarySchemes locks that the
// bracket-wrapped form of any scheme - which CommonMark's base autolink
// production really does resolve to a link node regardless of GFM at all -
// still comes out fully inert, via escapeMarkdownInline's "<"/">"
// escaping rather than any bare-scheme defanging.
func TestSafeInlineDefangsAngleBracketWrappedArbitrarySchemes(t *testing.T) {
	for _, raw := range []string{
		"angle bracket ftp <ftp://evil.example>",
		"mixed case angle bracket ftp <FtP://evil.example>",
		"custom scheme <custom-scheme+v1://evil.example>",
	} {
		got := safeInline(raw)
		if found := activeGFMConstructPresent(got); len(found) > 0 {
			t.Fatalf("active GFM construct(s) survived safeInline: raw=%q got=%q found=%v", raw, got, found)
		}
	}
}

// TestSafeInlineDefangsBareEmailAutolinkTrigger locks the CHAOS-2908 fix
// for the release reviewers' confirmed finding: GFM's extended email
// autolink turns a bare "user@example.com" into a live mailto: link with
// no brackets, no "mailto:" prefix, and no markdown-active punctuation at
// all - none of which escapeMarkdownInline or the pre-fix bareURLTrigger
// ever touched. Every shape GFM's own matcher accepts (plus signs, dots,
// hyphenated/underscored/leading-hyphen domain labels, single-character
// TLDs, a literal "mailto:" prefix, multiple addresses in one value) must
// come out fully inert.
func TestSafeInlineDefangsBareEmailAutolinkTrigger(t *testing.T) {
	for _, raw := range []string{
		"contact user@example.com for access",
		"USER@EXAMPLE.COM",
		"plus-addressed user+tag@example.com",
		"dotted local first.last@sub.example.co.uk",
		"underscored local user_name@example.com",
		"hyphenated domain user@my-domain.example",
		"leading-hyphen domain label user@-example.com",
		"single-character TLD user@example.c",
		"underscored domain label user@example_.com",
		"bare mailto prefix mailto:user@evil.example",
		"angle bracket email <user@example.com>",
		"two emails a@example.com and b@example.org",
	} {
		got := safeInline(raw)
		if found := activeGFMConstructPresent(got); len(found) > 0 {
			t.Fatalf("active GFM construct(s) survived safeInline: raw=%q got=%q found=%v", raw, got, found)
		}
	}
}

// TestSafeInlineDefangsAngleBracketAutolinksForArbitrarySchemes locks that
// CommonMark's base <scheme:target> autolink production - which accepts
// ANY 2-32 character URI scheme, not just http/https/ftp - can never
// survive safeInline even for schemes with no "//" at all (tel:, data:,
// javascript:), which the "scheme://" trigger alone would never catch.
// escapeMarkdownInline's backslash-escaping of every raw "<" is the layer
// that closes this specific gap, and this test locks it explicitly rather
// than leaving it as an implicit side effect of a test aimed at something
// else.
func TestSafeInlineDefangsAngleBracketAutolinksForArbitrarySchemes(t *testing.T) {
	for _, raw := range []string{
		"call <tel:+15555555555>",
		"embed <data:text/html,evil>",
		"run <javascript:alert(1)>",
		"mailto with email <mailto:user@evil.example>",
		"raw html anchor <a href=\"https://evil.example\">click</a>",
	} {
		got := safeInline(raw)
		if found := activeGFMConstructPresent(got); len(found) > 0 {
			t.Fatalf("active GFM construct(s) survived safeInline: raw=%q got=%q found=%v", raw, got, found)
		}
	}
}

// TestSafeInlineHandlesMultipleMixedAutolinkTriggersInOneValue locks that
// every trigger class defangs independently within a single value: a real
// hosted field could plausibly contain several of these at once (a
// provenance string pasted from an incident report, say), and fixing one
// class must never regress coverage of another.
func TestSafeInlineHandlesMultipleMixedAutolinkTriggersInOneValue(t *testing.T) {
	raw := "see https://a.example and http://b.example and www.c.example " +
		"and ftp://d.example and contact user@e.example or <tel:+1555> " +
		"or <javascript:alert(1)> for details"
	got := safeInline(raw)
	if found := activeGFMConstructPresent(got); len(found) > 0 {
		t.Fatalf("active GFM construct(s) survived combined-trigger safeInline: got=%q found=%v", got, found)
	}
	for _, want := range []string{"a.example", "b.example", "c.example", "d.example", "e.example", "tel:", "javascript:", "details"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected surrounding text %q to survive sanitization: %q", want, got)
		}
	}
}

// TestSafeInlineDefangingRemainsLinearTimeOnAdversarialInput guards
// against a future regex change reintroducing catastrophic backtracking.
// Go's regexp package is RE2-based and therefore already immune to
// exponential blowup regardless of pattern complexity, but this test pins
// the *empirical* linear-time property so a future contributor who swaps
// in a backtracking-capable matcher (e.g. a third-party regexp2
// dependency) fails this test immediately rather than shipping a ReDoS.
// It exercises neutralizeAutolinks directly (bypassing safeInline's own
// maxSanitizedMessageLength truncation, which already bounds every
// production call site to 500 bytes) with an adversarially-shaped input:
// a long run of scheme/domain-class characters that never resolves into
// an actual "://" or "@domain." match, maximizing partial-trigger
// scanning.
func TestSafeInlineDefangingRemainsLinearTimeOnAdversarialInput(t *testing.T) {
	small := strings.Repeat("a", 2_000)
	large := strings.Repeat("a", 20_000) // 10x larger input
	timeIt := func(s string) time.Duration {
		start := time.Now()
		neutralizeAutolinks(s)
		return time.Since(start)
	}
	timeIt(small) // warm up regexp internals before measuring.
	smallElapsed := timeIt(small)
	largeElapsed := timeIt(large)
	// A quadratic-or-worse implementation would take >=100x as long for a
	// 10x-larger input; a linear one takes roughly 10x. Allow generous
	// headroom for scheduler/GC noise on a shared CI runner while still
	// failing hard on genuine polynomial blowup.
	const maxLinearSlopFactor = 50
	if largeElapsed > smallElapsed*maxLinearSlopFactor {
		t.Fatalf("safeInline scaling looks super-linear: %d bytes took %v, %d bytes took %v",
			len(small), smallElapsed, len(large), largeElapsed)
	}
}
