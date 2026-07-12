package sidecar

import (
	"html"
	"regexp"
	"strings"
	"unicode/utf8"
)

// This file is the structural, per-node-type scanner the CHAOS-2908
// safeInline autolink tests share: rather than one flat substring check,
// each pattern mirrors a specific mdast node category (link,
// linkReference, image, html, definition) that an actual downstream GFM
// parser (remark-gfm/micromark-gfm) resolves untrusted text into. Every
// pattern here was cross-validated against the real remark-gfm/mdast
// parser (unified+remark-parse+remark-gfm) via an out-of-band probe matrix
// run against the exact vectors render_inline_test.go exercises,
// confirming zero link/linkReference/image/imageReference/html/definition
// nodes survive safeInline's output for any of them.
var (
	// bareSchemeAutolinkRe matches ONLY the literal, case-insensitive
	// "http://" or "https://" text - confirmed against the real remark-gfm
	// parser (unified+remark-parse+remark-gfm) to be the SOLE bare
	// (unbracketed) scheme GFM's own autolink-literal extension resolves to
	// a link node. A bare "ftp://", "xmpp://", "gopher://", or any other
	// scheme - confirmed via the same oracle probe - is never linked without
	// angle brackets; generalizing this pattern to arbitrary RFC 3986
	// schemes (as an earlier revision of both this scanner and
	// render.go's neutralizeAutolinks did) was itself the bug: it flagged
	// harmless bare "ftp://"/custom-scheme text as an active construct that
	// no real GFM parser ever forms, hiding both over-defanging in
	// production and any future regression here from ever being caught.
	// Angle-bracket-wrapped arbitrary schemes remain covered separately by
	// angleAutolinkRe below, which correctly keeps the broader RFC 3986
	// scheme class - that CommonMark base production really does accept any
	// scheme once bracketed.
	bareSchemeAutolinkRe = regexp.MustCompile(`(?i)https?://`)
	// bareWWWAutolinkRe matches an unsplit "www." - the shape GFM's
	// www-autolink extension resolves to a link node with no scheme at all.
	bareWWWAutolinkRe = regexp.MustCompile(`(?i)www\.`)
	// bareEmailAutolinkRe matches an unsplit "local@domain.tld" - the shape
	// GFM's extended email autolink resolves to a link node (target
	// "mailto:...") anywhere in text, with no brackets and no "mailto:"
	// prefix required. The local-part class is deliberately unanchored at
	// both ends - confirmed against the real remark-gfm parser, which links
	// "-@example.com", "...@example.com", and "user-@example.com" exactly
	// as readily as "user@example.com" - rather than requiring the first/
	// last local-part character to be alphanumeric, which under-matched
	// every GFM-valid local part ending in "-", "_", ".", or "+" and let a
	// real trigger survive safeInline unbroken.
	bareEmailAutolinkRe = regexp.MustCompile(`(?i)[a-z0-9._%+-]+@[a-z0-9_-]+(?:\.[a-z0-9_-]+)+`)
	// angleAutolinkRe matches CommonMark's base bracketed autolink
	// production <scheme:target> (any RFC 3986 scheme, not just
	// http/https/ftp - this is what makes tel:/data:/javascript: dangerous
	// even though none of them ever match bareSchemeAutolinkRe) or its
	// bracketed email form <local@domain>.
	angleAutolinkRe = regexp.MustCompile(`(?i)<[a-z][a-z0-9+.-]{1,31}:[^\s<>]*>|<[a-z0-9._%+-]+@[a-z0-9.-]+>`)
	// rawHTMLTagRe matches an unescaped raw HTML open/close/comment tag -
	// the shape a GFM parser resolves to an html node.
	rawHTMLTagRe = regexp.MustCompile(`</?[a-zA-Z][^<>]*>|<!--`)
	// imageOrLinkSyntaxRe matches inline reference/destination link syntax -
	// [text](target) / [text][label], with an optional leading "!" for
	// image/imageReference - the shape a GFM parser resolves to a link,
	// linkReference, image, or imageReference node.
	imageOrLinkSyntaxRe = regexp.MustCompile(`!?\[[^\]\n]*\]\([^)\n]*\)|!?\[[^\]\n]*\]\[[^\]\n]*\]`)
	// definitionSyntaxRe matches a link reference definition
	// "[label]: target" at a true line start - the shape a GFM parser
	// resolves to a definition node. safeInline's newline-collapsing
	// (locked by TestSafeInlineCollapsesNewlineAndTabToSingleSpace) already
	// makes a true line start unreachable from a single-line value, so this
	// exists purely as an explicit, checked invariant rather than an
	// implicit assumption.
	definitionSyntaxRe = regexp.MustCompile(`(?m)^\s{0,3}\[[^\]\n]*\]:\s*\S+`)
	// entityReferenceCandidateRe matches any syntactically-plausible HTML
	// named or numeric character reference for this scanner's own
	// hazard check. Deliberately looser than render.go's own
	// entityReferenceTrigger (no digit-count cap, no separate numeric/hex/
	// named alternation) so this scanner's coverage does not depend on
	// production's exact character-class choices - two independently
	// shaped patterns are far less likely to share the same blind spot than
	// one pattern duplicated in both places.
	entityReferenceCandidateRe = regexp.MustCompile(`&#?[0-9A-Za-z]+;`)
)

// liveText strips every backslash-escaped character down to nothing,
// mirroring CommonMark's own backslash-escape handling: an escaped "<",
// "[", "]", "(", ")", or ">" is inert plain text that can never start a
// raw HTML tag, a bracketed CommonMark autolink, or reference/inline link
// syntax, even though the raw bytes still contain the same underlying
// substring. escapeMarkdownInline is exactly what produces these escaped
// pairs in safeInline's real output, so the node-shaped scanners below
// must see the same de-escaped view a real parser would - checking the
// raw, still-escaped bytes would flag safeInline's own correct escaping
// as a false positive.
func liveText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\\' && i+1 < len(runes) {
			i++ // the escaped character itself is inert; drop the pair.
			continue
		}
		b.WriteRune(runes[i])
	}
	return b.String()
}

// precededByASCIILetter and precededByASCIIAlnum implement the left-
// boundary conditions that block GFM's extended url and www autolink
// extensions from firing, confirmed empirically against the real
// remark-gfm parser: "foohttps://x" and "Ahttps://x" are never linked
// (any immediately preceding ASCII letter blocks the url autolink), while
// "9https://x", "_https://x", ".https://x", and "(https://x" all are; the
// www autolink's boundary is one character wider - "9www.x" is also
// blocked, though "_www.x" and "-www.x" are still linked. Implemented as
// a plain byte/rune lookback rather than a regex capture group, so this
// check does not share its construction with neutralizeAutolinks's regex-
// based boundary handling in render.go: a mistake in one is not
// structurally guaranteed to also be present in the other.
func precededByASCIILetter(s string, idx int) bool {
	if idx == 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:idx])
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func precededByASCIIAlnum(s string, idx int) bool {
	if idx == 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:idx])
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// unboundedBareTriggerPresent reports whether s contains at least one
// match of re whose left boundary is NOT blocked by blocked(s, idx) - an
// occurrence a real GFM parser would still resolve into a live autolink,
// as opposed to one immediately preceded by a boundary-blocking character.
func unboundedBareTriggerPresent(s string, re *regexp.Regexp, blocked func(string, int) bool) bool {
	for _, loc := range re.FindAllStringIndex(s, -1) {
		if !blocked(s, loc[0]) {
			return true
		}
	}
	return false
}

// entityHazardPresent reports whether s still contains an HTML named or
// numeric character reference that decodes (per the same html.5 entity
// table Go's stdlib html package implements, which CommonMark's own
// entity/numeric-character-reference production is defined against) to a
// character capable of reactivating a hazard once a real parser resolves
// it: '@' (email autolink), '<'/'>' (raw HTML/angle-bracket autolink), a
// dangerous control/bidi rune, or a line-break character (which could
// forge fake markdown structure exactly like a raw embedded newline -
// confirmed against the real remark-gfm parser: "user&NewLine;@x" decodes
// to a literal embedded newline). A reference is only counted if Go's
// decoder consumed the ENTIRE candidate substring (no residual ";" left in
// its output): Go's html.UnescapeString implements HTML5's lenient
// legacy-entity rule that decodes some names even without a trailing ";"
// (e.g. "&notarealentity;" partially decodes as "&not" + literal
// "arealentity;"), which CommonMark's own stricter, semicolon-required
// grammar does not; requiring full, residue-free consumption keeps this
// scanner's notion of "decodes" aligned with the real remark-gfm parser
// (confirmed: "x&notarealentity;y" and "x&ampfoo;y" are left completely
// untouched by remark-gfm) rather than Go's more permissive one.
func entityHazardPresent(s string) bool {
	for _, m := range entityReferenceCandidateRe.FindAllString(s, -1) {
		decoded := html.UnescapeString(m)
		if decoded == m || strings.ContainsRune(decoded, ';') {
			continue
		}
		for _, r := range decoded {
			if r == '@' || r == '<' || r == '>' || r == '\n' || r == '\t' || isDangerousControlRune(r) {
				return true
			}
		}
	}
	return false
}

// entityDecodedView is this scanner's own, independently-constructed
// reconstruction of what a real GFM parser's tokenizer would see once it
// resolves every entity reference in s: every entityReferenceCandidateRe
// match that fully decodes (Go's html.UnescapeString consumes the ENTIRE
// candidate, leaving no residual ";", matching the real remark-gfm
// parser's own stricter semicolon-required grammar - the same rule
// entityHazardPresent already applies) is replaced by its decoded value;
// anything else survives as its own literal text, exactly as a real GFM
// parser leaves it. entityReferenceCandidateRe is deliberately a
// differently-shaped pattern from render.go's own entityReferenceTrigger/
// textUnit machinery (see its own doc comment above), so this
// reconstruction is not sharing a construction with the one production
// uses to decide what to defang - a blind spot in one is not structurally
// guaranteed to also be present in the other.
func entityDecodedView(s string) string {
	return entityReferenceCandidateRe.ReplaceAllStringFunc(s, func(match string) string {
		decoded := html.UnescapeString(match)
		if decoded == match || strings.ContainsRune(decoded, ';') {
			return match
		}
		return decoded
	})
}

// entityAssistedBareTriggerPresent reports whether the ENTITY-DECODED VIEW
// of s - entityDecodedView(s), not s's own literal bytes - still contains
// an unbounded bare scheme/www/email autolink trigger. This is the
// independent oracle for the correlated, multi-token completion class
// three release-certification reviewers confirmed against the real
// remark-gfm parser: an entity decoding to ":"/"/"/"." that only becomes
// hazardous once combined with adjacent LITERAL trigger text
// ("https&colon;//evil.example"), several such entities chained together
// ("https&colon;&sol;&sol;evil.example"), or a scheme word that is itself
// partially entity-spelled ("h&#116;tps://evil.example") - none of which
// unboundedBareTriggerPresent/bareEmailAutolinkRe can ever detect on their
// own, since they scan s's raw bytes and neither the completing
// punctuation nor every letter of the scheme word is necessarily literal
// text there at all.
func entityAssistedBareTriggerPresent(s string) bool {
	decoded := entityDecodedView(s)
	return unboundedBareTriggerPresent(decoded, bareSchemeAutolinkRe, precededByASCIILetter) ||
		unboundedBareTriggerPresent(decoded, bareWWWAutolinkRe, precededByASCIIAlnum) ||
		bareEmailAutolinkRe.MatchString(decoded)
}

// activeGFMConstructPresent reports every mdast node category (per the
// regexes above) that s still contains an unbroken trigger for, i.e.
// every GFM/CommonMark construct an actual downstream parser would still
// resolve into a live link/linkReference/image/imageReference/html/
// definition node. An empty result means s is fully inert on every axis.
// The bare-trigger checks (scheme/www/email) run against the raw text,
// since backslash-escaping never defangs any of them (confirmed against
// the actual remark-gfm parser: escaping the "h" in "https" or the "@" in
// an email address does not stop GFM's literal/email autolink extensions
// from firing), and the scheme/www checks additionally respect the same
// left-boundary rule the real parser enforces (see precededByASCIILetter/
// precededByASCIIAlnum) so a harmless "foohttps://"/"mywww." substring is
// not misreported as an active construct; the bracket/angle-bracket/html
// checks run against liveText(s), since those specific constructs - and
// only those - are the ones escapeMarkdownInline's backslash-escaping
// separately, since an HTML entity/numeric-character reference is a
// distinct token class no amount of backslash-escaping ever touches;
// entityAssistedBareTriggerPresent runs its own bare-trigger checks again,
// this time against entityDecodedView(s) rather than s's raw bytes, so a
// trigger that only completes once a remaining entity reference decodes -
// including one where the trigger word itself is partially entity-spelled
// - is caught even though entityHazardPresent's fixed hazard-rune check
// has no reason to flag it (":", "/", and "." are not inherently
// dangerous decoded characters in isolation).
func activeGFMConstructPresent(s string) []string {
	var found []string
	if unboundedBareTriggerPresent(s, bareSchemeAutolinkRe, precededByASCIILetter) {
		found = append(found, "link(bare-scheme)")
	}
	if unboundedBareTriggerPresent(s, bareWWWAutolinkRe, precededByASCIIAlnum) {
		found = append(found, "link(bare-www)")
	}
	if bareEmailAutolinkRe.MatchString(s) {
		found = append(found, "link(bare-email)")
	}
	live := liveText(s)
	if angleAutolinkRe.MatchString(live) {
		found = append(found, "link(angle-bracket-autolink)")
	}
	if rawHTMLTagRe.MatchString(live) {
		found = append(found, "html(raw-tag)")
	}
	if imageOrLinkSyntaxRe.MatchString(live) {
		found = append(found, "link-or-image(bracket-syntax)")
	}
	if definitionSyntaxRe.MatchString(live) {
		found = append(found, "definition")
	}
	if entityHazardPresent(s) {
		found = append(found, "entity(hazard)")
	}
	if entityAssistedBareTriggerPresent(s) {
		found = append(found, "link(entity-assisted-bare-trigger)")
	}
	return found
}

// bareURLTriggerPresent reports whether s contains a GFM-autolinkable bare
// URL trigger (an explicit http(s) URI, respecting the same left-boundary
// rule as activeGFMConstructPresent, or the www. domain prefix) that a
// real GFM parser would still resolve into a link node. Kept as a narrow,
// explicit alias of activeGFMConstructPresent's link(bare-*) categories
// for the existing tests that only care about bare-URL-shaped triggers.
func bareURLTriggerPresent(s string) bool {
	return unboundedBareTriggerPresent(s, bareSchemeAutolinkRe, precededByASCIILetter) ||
		unboundedBareTriggerPresent(s, bareWWWAutolinkRe, precededByASCIIAlnum) ||
		entityAssistedBareTriggerPresent(s)
}
