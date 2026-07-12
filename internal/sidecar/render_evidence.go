package sidecar

import (
	"fmt"
	"sort"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// RenderEvidenceMarkdown renders one expanded evidence reference as
// bounded markdown. Source identity, confidence, provenance, and
// availability are structural text; the excerpt and any structured fields
// are wrapped as untrusted content. Any URI on the evidence (SafeURI) is
// rendered inside its own explicitly inert fenced UNTRUSTED DATA block so
// GFM's bare-URL autolink extension can never turn it into a clickable
// link; it is only ever formatted as inert text and never fetched.
func RenderEvidenceMarkdown(evidence contractsv1.ExpandedEvidence, maxBytes int) (markdown string, truncated bool) {
	if maxBytes <= 0 {
		return "", false
	}
	b := newBoundedBuilder(maxBytes)

	b.writeLine(fmt.Sprintf("# Evidence %s", safeInline(evidence.Evidence.EvidenceRefID)))
	b.writeLine(fmt.Sprintf("- Availability: %s", safeInline(string(evidence.Availability))))
	b.writeLine(fmt.Sprintf("- Confidence: %.2f", evidence.Evidence.Confidence))
	b.writeLine(fmt.Sprintf("- Provenance: %s", safeInline(evidence.Evidence.Provenance)))
	b.writeLine(sourceSummaryLine(evidence.Evidence.Source))
	if evidence.RedactionReason != "" {
		b.writeLine("- Redaction reason: " + safeInline(evidence.RedactionReason))
	}
	if evidence.Evidence.SourceVersion != "" {
		b.writeLine("- Source version: " + safeInline(evidence.Evidence.SourceVersion))
	}

	// Citation is a required EvidenceRef field (schema-enforced non-empty),
	// so unlike the optional Excerpt/Structured blocks below it always
	// renders. It is business-composed prose from the hosted API, not a
	// short identifier, so it gets the same UNTRUSTED DATA fenced treatment
	// as excerpt/structured fields rather than safeInline's single-line
	// escaping.
	b.writeLine("")
	if !b.writeLine(fmt.Sprintf("## Citation (%s)", untrustedDataHeader)) {
		return b.finishWithTruncation()
	}
	if !b.writeLines(untrustedBlock("citation", evidence.Evidence.Citation)) {
		return b.finishWithTruncation()
	}

	if evidence.Excerpt != "" {
		b.writeLine("")
		if !b.writeLine(fmt.Sprintf("## Excerpt (%s)", untrustedDataHeader)) {
			return b.finishWithTruncation()
		}
		if !b.writeLines(untrustedBlock("excerpt", evidence.Excerpt)) {
			return b.finishWithTruncation()
		}
	}

	if len(evidence.Structured) > 0 {
		b.writeLine("")
		if !b.writeLine(fmt.Sprintf("## Structured fields (%s)", untrustedDataHeader)) {
			return b.finishWithTruncation()
		}
		if !b.writeLines(untrustedBlock("structured_fields", formatStructuredFields(evidence.Structured))) {
			return b.finishWithTruncation()
		}
	}

	// The reference URI is business-supplied, not a validated enum or a
	// trusted structural identifier, so - like citation/excerpt/structured
	// fields above - it renders as an explicitly labeled untrusted fenced
	// block rather than as plain inline text a GFM renderer could autolink.
	// It renders last: Citation is schema-required and Excerpt/Structured
	// are the primary evidentiary payload, so under byte-budget pressure
	// this supplementary reference link is the first thing truncated away.
	if evidence.Evidence.Source.SafeURI != "" {
		b.writeLine("")
		if !b.writeLine(fmt.Sprintf("## Reference (%s)", untrustedDataHeader)) {
			return b.finishWithTruncation()
		}
		if !b.writeLines(untrustedBlock("reference", evidence.Evidence.Source.SafeURI)) {
			return b.finishWithTruncation()
		}
	}
	return b.finishWithTruncation()
}

// sourceSummaryLine renders the structural (trusted-shape) identity of an
// evidence source. It deliberately excludes SafeURI: that field is
// hosted-supplied freeform text, not a validated enum, and is rendered
// separately as its own explicit UNTRUSTED DATA reference block so it can
// never appear as a bare, GFM-autolinkable URL on this structural line.
func sourceSummaryLine(source contractsv1.EvidenceSource) string {
	return fmt.Sprintf("- Source: system=%s entity_type=%s entity_id=%s label=%s",
		safeInline(source.System), safeInline(source.EntityType), safeInline(source.EntityID), safeInline(source.DisplayLabel))
}

// formatStructuredFields renders an arbitrary evidence-supplied map as
// deterministic "key: value" lines. Values are formatted with %v and then
// pass through the same untrustedBlock treatment as any other freeform
// content, so nested structures cannot be mistaken for sidecar-authored
// markdown.
func formatStructuredFields(fields map[string]any) string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s: %v", key, fields[key]))
	}
	return strings.Join(lines, "\n")
}
