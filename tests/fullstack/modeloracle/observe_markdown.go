package main

import (
	"github.com/full-chaos/dev-health-acr/tests/fullstack/sidecarmd"
)

// The sidecar returns every tool result twice: as MCP StructuredContent carrying the JSON
// contract, and as a bounded markdown rendering in the text content. OpenCode 1.18.4 forwards
// only the text content to the model, so a real agent driving acr-mcp through OpenCode never
// sees the JSON — it reasons over the rendering. This reader therefore reads what the agent
// actually receives. The JSON path is kept for clients that do forward structured content.
//
// The parsing itself lives in tests/fullstack/sidecarmd, shared with the assertion tool, which
// has to read these same renderings back out of the OpenCode event stream. This file is only
// the adapter into this package's Observation shape.

func looksLikeSidecarMarkdown(payload string) bool { return sidecarmd.Looks(payload) }

func unescapeMarkdownInline(value string) string { return sidecarmd.UnescapeInline(value) }

func observeSidecarMarkdown(payload string) Observation {
	rendering := sidecarmd.Parse(payload)
	var out Observation
	if rendering.Packet.Present {
		out.PacketStatus = rendering.Packet.PacketStatus
		out.ScopeResolution = rendering.Packet.ScopeResolution
		for _, id := range rendering.Packet.EvidenceRefIDs {
			// A reference the packet names has merely been offered, not expanded.
			out.addSighting(EvidenceSighting{EvidenceRefID: id})
		}
	}
	for _, evidence := range rendering.Evidence {
		// An "# Evidence" section exists only because source_evidence returned it, so the
		// reference it names was genuinely expanded during this run.
		out.addSighting(EvidenceSighting{
			EvidenceRefID: evidence.EvidenceRefID,
			EntityType:    evidence.EntityType,
			EntityID:      evidence.EntityID,
			Label:         evidence.Label,
			Expanded:      true,
		})
	}
	out.sortSightings()
	return out
}
