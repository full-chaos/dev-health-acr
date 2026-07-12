package sidecar

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRenderEvidenceMarkdownIncludesCoreStructuralFields(t *testing.T) {
	out, _ := RenderEvidenceMarkdown(sampleEvidence(), 8192)
	// attempt_count/duration_ms are structured-field content rendered inside
	// an untrustedBlock fence, not safeInline'd, so they stay unescaped; the
	// underscore-bearing metadata IDs go through safeInline and are expected
	// in their escaped form.
	for _, want := range []string{
		safeInline("ev_1"), "available", "0.91", "ci-log-parser", safeInline("github_actions"), safeInline("workflow_run"),
		"checkout CI run #12345", "attempt_count", "duration_ms",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected output to contain %q; got:\n%s", want, out)
		}
	}
}

func TestRenderEvidenceMarkdownDoesNotRenderClickableLinkForSafeURI(t *testing.T) {
	out, _ := RenderEvidenceMarkdown(sampleEvidence(), 8192)
	if strings.Contains(out, "](https://example.com/runs/12345)") {
		t.Fatal("evidence SafeURI must never be rendered as a markdown hyperlink")
	}
	if !strings.Contains(out, "https://example.com/runs/12345") {
		t.Fatal("evidence SafeURI should still be present as inert text")
	}
}

func TestRenderEvidenceMarkdownLabelsExcerptAsUntrusted(t *testing.T) {
	out, _ := RenderEvidenceMarkdown(sampleEvidence(), 8192)
	if !strings.Contains(out, untrustedDataHeader) {
		t.Fatal("expected UNTRUSTED DATA labeling for the excerpt")
	}
}

func TestRenderEvidenceMarkdownBoundsOutputSize(t *testing.T) {
	evidence := sampleEvidence()
	evidence.Excerpt = strings.Repeat("y", 100_000)
	const budget = 1024
	out, _ := RenderEvidenceMarkdown(evidence, budget)
	if len(out) > budget {
		t.Fatalf("output exceeded budget: %d > %d", len(out), budget)
	}
}

func TestRenderEvidenceMarkdownZeroBudgetIsEmpty(t *testing.T) {
	out, truncated := RenderEvidenceMarkdown(sampleEvidence(), 0)
	if out != "" {
		t.Fatalf("expected empty output for a zero budget, got %q", out)
	}
	if truncated {
		t.Fatal("expected truncated=false for a zero budget: there was no content to cut short")
	}
}

// TestRenderEvidenceMarkdownDoesNotReportTruncationForUntrustedMarkerText
// locks the truncation-provenance fix for evidence rendering: an excerpt
// that happens to contain the renderer's own truncation-notice wording,
// comfortably inside the byte budget, must not flip the returned
// truncated flag.
func TestRenderEvidenceMarkdownDoesNotReportTruncationForUntrustedMarkerText(t *testing.T) {
	evidence := sampleEvidence()
	evidence.Excerpt = "log excerpt: remaining content omitted from the original vendor dashboard"

	out, truncated := RenderEvidenceMarkdown(evidence, 8192)

	if truncated {
		t.Fatal("expected truncated=false: the marker text came from untrusted content, not actual truncation")
	}
	if !strings.Contains(out, "remaining content omitted from the original vendor dashboard") {
		t.Fatal("expected the untrusted excerpt to still be rendered verbatim")
	}
}

// TestRenderEvidenceMarkdownReportsTruncationWhenBudgetExceeded is the
// positive counterpart: an excerpt that genuinely cannot fit the budget
// must still report truncated=true.
func TestRenderEvidenceMarkdownReportsTruncationWhenBudgetExceeded(t *testing.T) {
	evidence := sampleEvidence()
	evidence.Excerpt = strings.Repeat("y", 100_000)
	const budget = 1024

	_, truncated := RenderEvidenceMarkdown(evidence, budget)

	if !truncated {
		t.Fatal("expected truncated=true when content exceeds the byte budget")
	}
}

func TestRenderEvidenceMarkdownEscapesSourceLabelMarkdownSyntax(t *testing.T) {
	evidence := sampleEvidence()
	evidence.Evidence.Source.DisplayLabel = "click [here](http://evil.example) now"
	out, _ := RenderEvidenceMarkdown(evidence, 8192)
	if strings.Contains(out, "](http://evil.example)") {
		t.Fatalf("evidence source label markdown link syntax was not neutralized:\n%s", out)
	}
}

// TestRenderEvidenceMarkdownIncludesCitationAsUntrustedContent locks the
// CHAOS-2908 provenance fix: EvidenceRef.Citation must survive rendering
// into the human-readable markdown, labeled the same UNTRUSTED DATA way as
// the excerpt and structured fields, never silently dropped.
func TestRenderEvidenceMarkdownIncludesCitationAsUntrustedContent(t *testing.T) {
	out, _ := RenderEvidenceMarkdown(sampleEvidence(), 8192)
	if !strings.Contains(out, fmt.Sprintf("## Citation (%s)", untrustedDataHeader)) {
		t.Fatalf("expected the citation to be rendered under an explicit UNTRUSTED DATA heading; got:\n%s", out)
	}
	if !strings.Contains(out, sampleEvidence().Evidence.Citation) {
		t.Fatalf("expected the citation text to be rendered verbatim; got:\n%s", out)
	}
}

// TestRenderEvidenceMarkdownCitationRespectsByteBudgetAndUntrustedLabeling
// locks that an oversized citation is cut to the configured byte budget
// (never silently dropped from the truncation bookkeeping) and, when it
// still fits, stays labeled as untrusted content.
func TestRenderEvidenceMarkdownCitationRespectsByteBudgetAndUntrustedLabeling(t *testing.T) {
	evidence := sampleEvidence()
	evidence.Evidence.Citation = strings.Repeat("c", 2000)
	const budget = 512
	out, truncated := RenderEvidenceMarkdown(evidence, budget)
	if len(out) > budget {
		t.Fatalf("citation content exceeded the configured byte budget: %d > %d", len(out), budget)
	}
	if !truncated {
		t.Fatal("expected truncated=true: citation content exceeds the byte budget")
	}
	if !strings.Contains(out, fmt.Sprintf("## Citation (%s)", untrustedDataHeader)) {
		t.Fatalf("expected the citation section to still be labeled UNTRUSTED DATA before truncation; got:\n%s", out)
	}
}

// TestRenderEvidenceMarkdownTruncatesWithoutSplittingMultibyteRuneAtBoundary
// locks that a byte-budget cut through multi-byte excerpt content never
// splits a rune in half, regardless of where the exact byte boundary falls.
func TestRenderEvidenceMarkdownTruncatesWithoutSplittingMultibyteRuneAtBoundary(t *testing.T) {
	evidence := sampleEvidence()
	evidence.Excerpt = strings.Repeat("\u4e94", 2000) // 3-byte UTF-8 rune, repeated
	// This exact byte budget lands the finish()-time trim boundary (the
	// point where already-buffered content is shortened to make room for
	// the truncation notice) strictly inside one of the excerpt's 3-byte
	// runes rather than on a rune boundary.
	const budget = 6480
	out, truncated := RenderEvidenceMarkdown(evidence, budget)
	if !truncated {
		t.Fatal("expected truncated=true when multibyte excerpt content exceeds the byte budget")
	}
	if !utf8.ValidString(out) {
		t.Fatalf("truncated evidence markdown is not valid UTF-8: %q", out)
	}
}

// bareURLOutsideFencedBlocks returns every rendered line that contains a
// bare, GFM-autolinkable URL trigger (an http(s) scheme or a www. domain
// prefix) outside of a fenced code block (the blockquoted "> ```" fences
// untrustedBlock emits). GFM's autolink extension turns any bare
// https?://, http?://, or www. text into a clickable link unless it sits
// inside a code span or fenced code block, so this is the GFM-aware
// surface a renderer must keep clear of untrusted or reference URLs.
var bareURLPattern = regexp.MustCompile(`(?i)(https?://|www\.)\S+`)

func bareURLOutsideFencedBlocks(markdown string) []string {
	var offending []string
	inFence := false
	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimPrefix(line, "> ")
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if !inFence && bareURLPattern.MatchString(line) {
			offending = append(offending, line)
		}
	}
	return offending
}

// TestRenderEvidenceMarkdownSafeURINeverAppearsAsAutolinkableBareURL is the
// CHAOS-2908 GFM-autolink fix: TestRenderEvidenceMarkdownDoesNotRenderClickableLinkForSafeURI
// above only rejects explicit [text](url) syntax, but GFM's autolink
// extension turns a *bare* https:// URL into a clickable link with no
// brackets at all. SafeURI must render only inside an explicitly inert
// fenced/code block, never as bare text GFM can auto-link.
func TestRenderEvidenceMarkdownSafeURINeverAppearsAsAutolinkableBareURL(t *testing.T) {
	out, _ := RenderEvidenceMarkdown(sampleEvidence(), 8192)
	if offending := bareURLOutsideFencedBlocks(out); len(offending) > 0 {
		t.Fatalf("SafeURI rendered where GFM would auto-link it as a clickable bare URL: %v\nfull output:\n%s", offending, out)
	}
	uri := sampleEvidence().Evidence.Source.SafeURI
	if !strings.Contains(out, uri) {
		t.Fatal("expected SafeURI to still be present as inert text inside a fenced block")
	}
}

// TestRenderEvidenceMarkdownLabelsSafeURIAsUntrustedReference locks that the
// evidence reference URI is rendered under the same explicit UNTRUSTED DATA
// heading as citation/excerpt/structured fields, not folded into trusted
// structural metadata.
func TestRenderEvidenceMarkdownLabelsSafeURIAsUntrustedReference(t *testing.T) {
	out, _ := RenderEvidenceMarkdown(sampleEvidence(), 8192)
	if !strings.Contains(out, fmt.Sprintf("## Reference (%s)", untrustedDataHeader)) {
		t.Fatalf("expected SafeURI to be rendered under an explicit UNTRUSTED DATA reference heading; got:\n%s", out)
	}
}

// TestRenderEvidenceMarkdownNeutralizesBidiOverrideInDisplayLabel locks the
// CHAOS-2908 safeInline fix end-to-end: a bidi override/pop-directional
// pair injected into the evidence source display label - a safeInline'd
// structural field rendered outside any fence - must never survive into
// the full rendered document, where it could visually reorder the trusted
// "- Source: ..." line around it, including this renderer's own labels.
func TestRenderEvidenceMarkdownNeutralizesBidiOverrideInDisplayLabel(t *testing.T) {
	evidence := sampleEvidence()
	evidence.Evidence.Source.DisplayLabel = "safe\u202eevil\u202c label"
	out, _ := RenderEvidenceMarkdown(evidence, 8192)
	for _, bidi := range []rune{0x202e, 0x202c} {
		if strings.ContainsRune(out, bidi) {
			t.Fatalf("raw bidi format character %U survived into the full rendered document:\n%s", bidi, out)
		}
	}
}

// TestRenderEvidenceMarkdownNeutralizesBareURLInDisplayLabel is the
// bracket-free counterpart to TestRenderEvidenceMarkdownEscapesSourceLabelMarkdownSyntax
// above: GFM's autolink extension turns a *bare* https?://\S+ or www.
// trigger into a clickable link with no [text](url) syntax at all, so
// backslash-escaping brackets alone does not close this off.
func TestRenderEvidenceMarkdownNeutralizesBareURLInDisplayLabel(t *testing.T) {
	evidence := sampleEvidence()
	evidence.Evidence.Source.DisplayLabel = "click here https://evil.example now"
	out, _ := RenderEvidenceMarkdown(evidence, 8192)
	if offending := bareURLOutsideFencedBlocks(out); len(offending) > 0 {
		t.Fatalf("bare URL autolink trigger survived into the full rendered document: %v\n%s", offending, out)
	}
}

// TestRenderEvidenceMarkdownNeutralizesBareURLInProvenanceField locks the
// same GFM-autolink defense for Provenance, another safeInline'd freeform
// field rendered outside any fence.
func TestRenderEvidenceMarkdownNeutralizesBareURLInProvenanceField(t *testing.T) {
	evidence := sampleEvidence()
	evidence.Evidence.Provenance = "see www.evil.example for details"
	out, _ := RenderEvidenceMarkdown(evidence, 8192)
	if offending := bareURLOutsideFencedBlocks(out); len(offending) > 0 {
		t.Fatalf("bare URL autolink trigger survived into the full rendered document: %v\n%s", offending, out)
	}
}
