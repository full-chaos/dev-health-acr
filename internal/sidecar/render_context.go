package sidecar

import (
	"fmt"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// RenderContextPacketMarkdown renders a hosted context packet as bounded,
// self-describing markdown: schema/status metadata, resolved scope,
// warnings, and every item's category/claim kind/confidence/evidence IDs
// are structural text; every freeform field (goal, item title/summary/why
// included) is wrapped in an explicitly labeled untrusted block. Output
// never exceeds maxBytes.
func RenderContextPacketMarkdown(packet contractsv1.ContextPacket, maxBytes int) (markdown string, truncated bool) {
	if maxBytes <= 0 {
		return "", false
	}
	b := newBoundedBuilder(maxBytes)

	b.writeLine(fmt.Sprintf("# Context Packet %s", safeInline(packet.ContextPacketID)))
	b.writeLine(fmt.Sprintf("- Status: %s", safeInline(string(packet.Status))))
	b.writeLine(scopeSummaryLine(packet.ResolvedScope))
	b.writeLine(fmt.Sprintf("- Coverage: partial=%t sources_considered=%d sources_available=%d sources_unavailable=%d",
		packet.Coverage.Partial, len(packet.Coverage.SourcesConsidered), len(packet.Coverage.SourcesAvailable), len(packet.Coverage.SourcesUnavailable)))
	b.writeLine(fmt.Sprintf("- Budget: items=%d/%d tokens=%d/%d bytes=%d/%d truncated=%t",
		packet.Budget.ItemsUsed, packet.Budget.MaxItems, packet.Budget.EstimatedTokens, packet.Budget.MaxOutputTokens,
		packet.Budget.SerializedBytes, packet.Budget.MaxSerializedBytes, packet.Budget.Truncated))

	if !renderTextListSection(b, "Warnings", "warnings", packet.Warnings) {
		return b.finishWithTruncation()
	}
	if !renderTextListSection(b, "Fallback Reasons", "fallback_reasons", packet.ResolvedScope.FallbackReasons) {
		return b.finishWithTruncation()
	}
	if !renderTextListSection(b, "Coverage Notes", "coverage_degraded_reasons", packet.Coverage.DegradedReasons) {
		return b.finishWithTruncation()
	}

	b.writeLine("")
	if !b.writeLine(fmt.Sprintf("## Goal (%s)", untrustedDataHeader)) {
		return b.finishWithTruncation()
	}
	if !b.writeLines(untrustedBlock("goal", packet.Goal)) {
		return b.finishWithTruncation()
	}

	b.writeLine("")
	if !b.writeLine(fmt.Sprintf("## Items (%d)", len(packet.Items))) {
		return b.finishWithTruncation()
	}
	for _, item := range packet.Items {
		if !renderPacketItem(b, item) {
			return b.finishWithTruncation()
		}
	}
	return b.finishWithTruncation()
}

// renderTextListSection renders a heading-labeled, fenced UNTRUSTED DATA
// block for a list of hosted-supplied freeform strings (warnings,
// fallback reasons, coverage degraded reasons). These are prose-like
// explanatory text from hosted business logic, not simple identifiers, so
// they get the same fenced treatment as goal/title/summary rather than
// safeInline's lighter single-line escaping. A nil or empty list renders
// nothing and is not an error.
func renderTextListSection(b *boundedBuilder, heading, label string, items []string) bool {
	if len(items) == 0 {
		return true
	}
	if !b.writeLine("") {
		return false
	}
	if !b.writeLine(fmt.Sprintf("## %s (%s)", heading, untrustedDataHeader)) {
		return false
	}
	return b.writeLines(untrustedBlock(label, strings.Join(items, "\n")))
}

func scopeSummaryLine(scope contractsv1.ResolvedScope) string {
	line := fmt.Sprintf("- Resolved scope: repo=%s resolution=%s", safeInline(scope.RepoSlug), safeInline(string(scope.Resolution)))
	if scope.Branch != "" {
		line += " branch=" + safeInline(scope.Branch)
	}
	if scope.CommitSHA != "" {
		line += " commit=" + safeInline(scope.CommitSHA)
	}
	return line
}

func renderPacketItem(b *boundedBuilder, item contractsv1.ContextPacketItem) bool {
	header := fmt.Sprintf("### [%s / %s] rank=%d confidence=%.2f severity=%s",
		safeInline(string(item.Category)), safeInline(string(item.ClaimKind)), item.Rank, item.Confidence, safeInline(string(item.Severity)))
	if !b.writeLine(header) {
		return false
	}
	if len(item.EvidenceRefIDs) > 0 {
		if !b.writeLine("- Evidence IDs: " + strings.Join(sanitizeList(item.EvidenceRefIDs), ", ")) {
			return false
		}
	}
	if item.Flags.Stale || item.Flags.Uncertain || item.Flags.Conflicting || item.Flags.UntrustedContent {
		flagsLine := fmt.Sprintf("- Flags: stale=%t uncertain=%t conflicting=%t untrusted_content=%t",
			item.Flags.Stale, item.Flags.Uncertain, item.Flags.Conflicting, item.Flags.UntrustedContent)
		if !b.writeLine(flagsLine) {
			return false
		}
	}
	content := strings.Join([]string{
		"Title: " + item.Title,
		"Summary: " + item.Summary,
		"Why included: " + item.WhyIncluded,
	}, "\n")
	return b.writeLines(untrustedBlock("item content", content))
}
