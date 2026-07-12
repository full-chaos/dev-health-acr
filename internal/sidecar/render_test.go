package sidecar

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func samplePacket() contractsv1.ContextPacket {
	return contractsv1.ContextPacket{
		ContextPacketID: "packet_1",
		Status:          contractsv1.PacketComplete,
		Goal:            "investigate flaky checkout tests",
		ResolvedScope: contractsv1.ResolvedScope{
			RepoID: "repo_1", RepoSlug: "acme/widgets", Branch: "main", CommitSHA: "deadbeef",
			Resolution: contractsv1.ScopeExactCommit,
		},
		Warnings: []string{"catalog data is 2 hours stale"},
		Items: []contractsv1.ContextPacketItem{
			{
				Category: contractsv1.CategoryCause, ClaimKind: contractsv1.ClaimInferred,
				Title: "Flaky retry logic", Summary: "Checkout retries too eagerly under load.",
				WhyIncluded: "Matches recent CI failures.", Confidence: 0.82, Severity: contractsv1.SeverityHigh,
				Rank: 1, EvidenceRefIDs: []string{"ev_1", "ev_2"},
			},
		},
	}
}

func sampleEvidence() contractsv1.ExpandedEvidence {
	return contractsv1.ExpandedEvidence{
		Evidence: contractsv1.EvidenceRef{
			EvidenceRefID: "ev_1", Confidence: 0.91, Provenance: "ci-log-parser",
			Citation: "checkout CI run #12345, step \"test\", line 42",
			Source: contractsv1.EvidenceSource{
				System: "github_actions", EntityType: "workflow_run", EntityID: "12345",
				DisplayLabel: "checkout CI run #12345", SafeURI: "https://example.com/runs/12345",
			},
		},
		ResolvedAt:   time.Now().UTC(),
		Availability: contractsv1.EvidenceAvailable,
		Excerpt:      "test flaked 3 times in a row",
		Structured:   map[string]any{"attempt_count": 3, "duration_ms": 450},
	}
}

func TestSafeInlineEscapesMarkdownLinkSyntax(t *testing.T) {
	escaped := safeInline("[click here](javascript:alert(1))")
	if strings.Contains(escaped, "](") {
		t.Fatalf("markdown link syntax was not neutralized: %q", escaped)
	}
}

func TestSafeInlineEscapesEmphasisAndCodeSpanSyntax(t *testing.T) {
	escaped := safeInline("*bold* _em_ `code` <script>")
	for _, active := range []string{"*bold*", "_em_", "`code`", "<script>"} {
		if strings.Contains(escaped, active) {
			t.Fatalf("markdown-active sequence %q was not escaped: %q", active, escaped)
		}
	}
}

func TestSafeInlineDoesNotEscapeOrdinaryIdentifierCharacters(t *testing.T) {
	// Slashes, dots, hyphens, and colons are common in real repo slugs,
	// branch names, and commit SHAs; escaping them would just add noise
	// without closing any injection vector, since block-level markdown
	// syntax (#, -, >) can only activate at a true line start, which
	// safeInline's control-character stripping already makes unreachable.
	value := "acme/widgets-service:v1.2.3"
	if got := safeInline(value); got != value {
		t.Fatalf("ordinary identifier characters were unexpectedly escaped: %q", got)
	}
}

// TestBoundedBuilderFinishTrimsMultibyteContentAtValidRuneBoundary locks the
// CHAOS-2908 fix: finish() must never cut a multi-byte UTF-8 rune in half
// when it trims already-written content to make room for the truncation
// notice. Budget the builder so the already-written buffer plus the notice
// overflows by a margin that lands the raw byte-count trim point strictly
// inside a 3-byte rune, then assert the result is still valid UTF-8.
func TestBoundedBuilderFinishTrimsMultibyteContentAtValidRuneBoundary(t *testing.T) {
	const rune3 = "\u4e94" // 3-byte UTF-8 rune
	maxBytes := len(truncationNotice) + 100
	b := newBoundedBuilder(maxBytes)
	line := strings.Repeat(rune3, 40) // 120 bytes, forcing finish() to trim it
	if !b.writeLine(line) {
		t.Fatal("expected the first line to fit within the budget")
	}
	if b.writeLine(strings.Repeat("x", maxBytes)) {
		t.Fatal("expected the second write to overflow the budget")
	}
	out := b.finish()
	if !utf8.ValidString(out) {
		t.Fatalf("finish() split a multi-byte rune at the trim boundary: %q", out)
	}
	if len(out) > maxBytes {
		t.Fatalf("finish() exceeded the configured byte budget: %d > %d", len(out), maxBytes)
	}
}

// TestUntrustedBlockEscapesActiveC0ControlBytes locks the CHAOS-2908
// terminal-safety fix: ESC (start of ANSI/OSC escape sequences), BEL (OSC
// terminator; also an audible/visual alert), backspace, and form feed are
// the C0 control bytes most able to alter how a terminal renders the
// UNTRUSTED DATA block that follows the header. They must never survive
// into rendered output as raw bytes, but the surrounding structured
// payload must remain visible.
func TestUntrustedBlockEscapesActiveC0ControlBytes(t *testing.T) {
	content := "before\x1b[31mred\x1b[0m\x07bell\x08bs\x0cff after"
	out := strings.Join(untrustedBlock("goal", content), "\n")

	for _, raw := range []rune{0x1b, 0x07, 0x08, 0x0c} {
		if strings.ContainsRune(out, raw) {
			t.Fatalf("raw control byte %U survived untrustedBlock sanitization:\n%s", raw, out)
		}
	}
	for _, visible := range []string{"before", "red", "bell", "bs", "ff", "after"} {
		if !strings.Contains(out, visible) {
			t.Fatalf("expected surrounding text %q to survive sanitization:\n%s", visible, out)
		}
	}
}

// TestUntrustedBlockEscapesC1ControlBytes covers the 8-bit C1 control
// range (U+0080-U+009F): U+009B is the single-codepoint equivalent of the
// two-byte ESC "[" sequence introducer, so a terminal that recognizes
// 8-bit C1 controls would treat it exactly like an ANSI CSI escape.
func TestUntrustedBlockEscapesC1ControlBytes(t *testing.T) {
	content := "before\u009b31mred\u009b0m after"
	out := strings.Join(untrustedBlock("goal", content), "\n")
	if strings.ContainsRune(out, '\u009b') {
		t.Fatalf("raw C1 CSI byte survived untrustedBlock sanitization:\n%s", out)
	}
}

// TestUntrustedBlockEscapesBidiFormatCharacters locks that Unicode bidi
// format controls (here, RIGHT-TO-LEFT OVERRIDE / POP DIRECTIONAL
// FORMATTING) are neutralized. Left unsanitized, RLO can visually reorder
// the characters that follow it - a "Trojan Source"-style attack that
// could make attacker content masquerade as something else, including a
// forged safety label, when displayed.
func TestUntrustedBlockEscapesBidiFormatCharacters(t *testing.T) {
	content := "SAFE\u202eEVIL\u202c label"
	out := strings.Join(untrustedBlock("goal", content), "\n")
	for _, bidi := range []rune{0x202e, 0x202c} {
		if strings.ContainsRune(out, bidi) {
			t.Fatalf("raw bidi format character %U survived untrustedBlock sanitization:\n%s", bidi, out)
		}
	}
}

// TestUntrustedBlockPreservesTabAndNewlineForFencedFormatting locks that
// the sanitizer does not over-strip: tab and newline are needed to
// preserve the shape of fenced-block content and must survive verbatim.
func TestUntrustedBlockPreservesTabAndNewlineForFencedFormatting(t *testing.T) {
	content := "line one\n\tindented line two"
	out := strings.Join(untrustedBlock("goal", content), "\n")
	if !strings.Contains(out, "\tindented line two") {
		t.Fatalf("expected literal tab to survive sanitization for fenced-block formatting:\n%s", out)
	}
}

// TestUntrustedBlockOutputIsAlwaysValidUTF8 locks that sanitization never
// leaves an invalid UTF-8 byte sequence in the rendered block, even when
// the hosted content itself was not valid UTF-8 to begin with.
func TestUntrustedBlockOutputIsAlwaysValidUTF8(t *testing.T) {
	invalid := "valid text " + string([]byte{0xff, 0xfe, 0x80}) + " more text"
	out := strings.Join(untrustedBlock("goal", invalid), "\n")
	if !utf8.ValidString(out) {
		t.Fatalf("untrustedBlock output is not valid UTF-8: %q", out)
	}
}
