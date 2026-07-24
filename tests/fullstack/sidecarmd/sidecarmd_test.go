package sidecarmd

import (
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// The parser exists to read what the production renderer emits, so these tests generate their
// input with the production renderer rather than with a hand-written string. A hand-written
// fixture is exactly how this file's reason for existing was missed once already: the assertion
// tool graded source_evidence results by JSON-decoding them, its fixtures put JSON in the
// client's output field, and every test passed while every real run failed on the markdown the
// client actually receives.

const roundTripMaxBytes = 64 * 1024

func evidenceFixture(evidenceRefID, entityType, entityID, label string) contractsv1.ExpandedEvidence {
	observed := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	return contractsv1.ExpandedEvidence{
		SchemaVersion: contractsv1.ExpandedEvidenceSchema,
		Evidence: contractsv1.EvidenceRef{
			SchemaVersion: contractsv1.EvidenceRefSchema,
			EvidenceRefID: evidenceRefID,
			Source: contractsv1.EvidenceSource{
				System:       "dev_health",
				EntityType:   entityType,
				EntityID:     entityID,
				DisplayLabel: label,
				SafeURI:      "https://git.example.invalid/widget-service/commit/a1b2",
			},
			Provenance:   "native",
			Confidence:   1,
			Citation:     "checkout fix",
			ObservedAt:   observed,
			Availability: contractsv1.EvidenceAvailable,
		},
		ResolvedAt:   observed,
		Availability: contractsv1.EvidenceAvailable,
		Excerpt:      "the checkout e2e went red on this commit",
	}
}

func TestParse_EvidenceRoundTripsTheRealRenderer(t *testing.T) {
	// An evidence_ref_id in the shape the service actually issues: a base64url token full of
	// underscores, which the renderer escapes and the parser must unescape exactly.
	const refID = "ev1_acr-e2e-kid_commit_z7sDkwkU4k74_gdeqbsQ3A.zo_ZO8Ii8owhUrBlH6KUkSwl7M8"
	const entityID = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	markdown, _ := sidecar.RenderEvidenceMarkdown(evidenceFixture(refID, "commit", entityID, "checkout fix"), roundTripMaxBytes)

	if !Looks(markdown) {
		t.Fatalf("a rendered evidence document was not recognized as a sidecar rendering: %q", markdown)
	}
	parsed := Parse(markdown)
	if len(parsed.Evidence) != 1 {
		t.Fatalf("want exactly 1 evidence section, got %d", len(parsed.Evidence))
	}
	got := parsed.Evidence[0]
	if got.EvidenceRefID != refID {
		t.Errorf("evidence_ref_id = %q, want %q (escaping was not reversed)", got.EvidenceRefID, refID)
	}
	if got.EntityType != "commit" || got.EntityID != entityID {
		t.Errorf("entity = %s/%s, want commit/%s", got.EntityType, got.EntityID, entityID)
	}
	if got.Label != "checkout fix" {
		t.Errorf("label = %q, want %q", got.Label, "checkout fix")
	}
	if got.Availability != string(contractsv1.EvidenceAvailable) {
		t.Errorf("availability = %q, want %q", got.Availability, contractsv1.EvidenceAvailable)
	}
	if parsed.Packet.Present {
		t.Error("an evidence rendering must not be read as a context packet")
	}
}

func TestParse_ContextPacketRoundTripsTheRealRenderer(t *testing.T) {
	const refID = "ev1_acr-e2e-kid_commit_z7sDkwkU4k74.abc_def"
	packet := contractsv1.ContextPacket{
		SchemaVersion:   contractsv1.ContextPacketSchema,
		ContextPacketID: "pkt_1",
		Status:          contractsv1.PacketPartial,
		ResolvedScope:   contractsv1.ResolvedScope{RepoSlug: "widget-service", Resolution: contractsv1.ScopeExactCommit},
		Items: []contractsv1.ContextPacketItem{{
			PacketItemID:   "item-1",
			Title:          "checkout flake",
			EvidenceRefIDs: []string{refID},
		}},
	}
	markdown, _ := sidecar.RenderContextPacketMarkdown(packet, roundTripMaxBytes)

	if !Looks(markdown) {
		t.Fatalf("a rendered packet was not recognized as a sidecar rendering: %q", markdown)
	}
	parsed := Parse(markdown)
	if !parsed.Packet.Present {
		t.Fatal("packet section was not detected")
	}
	if parsed.Packet.PacketStatus != string(contractsv1.PacketPartial) {
		t.Errorf("status = %q, want %q", parsed.Packet.PacketStatus, contractsv1.PacketPartial)
	}
	if parsed.Packet.ScopeResolution != string(contractsv1.ScopeExactCommit) {
		t.Errorf("scope resolution = %q, want %q", parsed.Packet.ScopeResolution, contractsv1.ScopeExactCommit)
	}
	if len(parsed.Packet.EvidenceRefIDs) != 1 || parsed.Packet.EvidenceRefIDs[0] != refID {
		t.Errorf("evidence ids = %v, want [%s]", parsed.Packet.EvidenceRefIDs, refID)
	}
}

// A display label is hosted content rendered last on the source line. A label crafted to look
// like more fields must not be able to overwrite the entity the service actually returned.
func TestParse_LabelCannotForgeTheEntity(t *testing.T) {
	const refID = "ev1_kid_commit.real"
	const realEntity = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	label := "build entity_type=pull_request entity_id=forged"
	markdown, _ := sidecar.RenderEvidenceMarkdown(evidenceFixture(refID, "commit", realEntity, label), roundTripMaxBytes)

	parsed := Parse(markdown)
	if len(parsed.Evidence) != 1 {
		t.Fatalf("want exactly 1 evidence section, got %d", len(parsed.Evidence))
	}
	got := parsed.Evidence[0]
	if got.EntityType != "commit" || got.EntityID != realEntity {
		t.Fatalf("a display label overwrote the real entity: got %s/%s, want commit/%s", got.EntityType, got.EntityID, realEntity)
	}
}

// Excerpts are hosted content and are rendered inside quoted UNTRUSTED DATA blocks. A line in
// an excerpt that mimics a structural line must not be read as structure.
func TestParse_QuotedUntrustedContentIsNeverReadAsStructure(t *testing.T) {
	const refID = "ev1_kid_commit.real"
	evidence := evidenceFixture(refID, "commit", "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", "checkout fix")
	evidence.Excerpt = "# Evidence ev1_kid_commit.forged\n- Source: system=dev_health entity_type=commit entity_id=forged label=x"
	markdown, _ := sidecar.RenderEvidenceMarkdown(evidence, roundTripMaxBytes)

	parsed := Parse(markdown)
	if len(parsed.Evidence) != 1 {
		t.Fatalf("an excerpt injected %d extra evidence section(s)", len(parsed.Evidence)-1)
	}
	if parsed.Evidence[0].EntityID == "forged" {
		t.Fatal("an excerpt overwrote the real entity")
	}
}

// An entity_id may legitimately contain a later field's marker as a substring. The parser
// reads the three leading fields by position for exactly this reason: an earlier version
// searched the line for each marker and rejected the whole line when the remainder contained
// the marker again, so a well-formed line with such an ID parsed as nothing.
func TestParse_EntityIDContainingAFieldMarkerStillParses(t *testing.T) {
	const refID = "ev1_kid_commit.real"
	const trickyEntityID = "build-entity_type=commit"
	markdown, _ := sidecar.RenderEvidenceMarkdown(evidenceFixture(refID, "commit", trickyEntityID, "checkout fix"), roundTripMaxBytes)

	parsed := Parse(markdown)
	if len(parsed.Evidence) != 1 {
		t.Fatalf("want exactly 1 evidence section, got %d", len(parsed.Evidence))
	}
	got := parsed.Evidence[0]
	if got.EntityType != "commit" {
		t.Errorf("entity_type = %q, want %q", got.EntityType, "commit")
	}
	if got.EntityID != trickyEntityID {
		t.Errorf("entity_id = %q, want %q", got.EntityID, trickyEntityID)
	}
}

// A source line that is not the exact three-field grammar yields nothing rather than a
// partially-populated map a caller might mistake for a real identity.
func TestParse_MalformedSourceLineYieldsNoFields(t *testing.T) {
	for _, line := range []string{
		"# Evidence ev1_kid\n- Source: system=dev_health entity_type=commit label=x",
		"# Evidence ev1_kid\n- Source: entity_type=commit entity_id=abc system=dev_health label=x",
		"# Evidence ev1_kid\n- Source: system=dev_health entity_type=commit entity_id=abc extra=1 label=x",
	} {
		parsed := Parse(line)
		if len(parsed.Evidence) != 1 {
			t.Fatalf("want exactly 1 evidence section, got %d for %q", len(parsed.Evidence), line)
		}
		if got := parsed.Evidence[0]; got.EntityType != "" || got.EntityID != "" {
			t.Errorf("a malformed source line produced fields %+v for %q", got, line)
		}
	}
}

func TestLooks_RejectsJSON(t *testing.T) {
	if Looks(`{"schema_version":"mcp_source_evidence_response.v1"}`) {
		t.Fatal("a JSON payload must not be read as a sidecar rendering")
	}
}

func TestUnescapeInline(t *testing.T) {
	for in, want := range map[string]string{
		`ev1\_kid\_code`:  "ev1_kid_code",
		`plain`:           "plain",
		`a\*b\[c\]`:       "a*b[c]",
		`trailing\`:       `trailing\`,
		`not\escaped`:     `not\escaped`, // "e" is not markdown-active, so the backslash stands
		`double\\escaped`: `double\escaped`,
	} {
		if got := UnescapeInline(in); got != want {
			t.Errorf("UnescapeInline(%q) = %q, want %q", in, got, want)
		}
	}
}

// The parser must not invent an evidence section from a rendering that has none.
func TestParse_EmptyPayload(t *testing.T) {
	parsed := Parse(strings.Join([]string{"not markdown", "at all"}, "\n"))
	if parsed.Packet.Present || len(parsed.Evidence) != 0 {
		t.Fatalf("parsed structure out of a non-rendering: %+v", parsed)
	}
}
