package v1

import (
	"strings"
	"testing"
)

const addedVocabularyValue = "value_added_after_the_client_cached_the_schema"

func TestOpenVocabularyTokenShape(t *testing.T) {
	for _, ok := range []string{addedVocabularyValue, "a", "A1", "x.y:z-w_v", strings.Repeat("a", 128)} {
		if !openVocabularyToken(ok) {
			t.Errorf("openVocabularyToken(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", " a", "a b", "-a", "_a", "a/b", "a\n", strings.Repeat("a", 129), "é"} {
		if openVocabularyToken(bad) {
			t.Errorf("openVocabularyToken(%q) = true, want false", bad)
		}
	}
}

func TestPacketAndEvidenceValidateReadAcceptsAddedVocabularyValues(t *testing.T) {
	cases := []struct {
		name     string
		validate func(value string) error
	}{
		{"evidence_ref.provenance", func(v string) error {
			e := loadFixture[EvidenceRef](t, "evidence_ref.v1.json")
			e.Provenance = v
			return e.ValidateRead()
		}},
		{"evidence_ref.availability", func(v string) error {
			e := loadFixture[EvidenceRef](t, "evidence_ref.v1.json")
			e.Availability = EvidenceAvailability(v)
			return e.ValidateRead()
		}},
		{"expanded_evidence.availability", func(v string) error {
			e := loadFixture[ExpandedEvidence](t, "expanded_evidence.v1.json")
			e.Availability = EvidenceAvailability(v)
			return e.ValidateRead()
		}},
		{"expanded_evidence.evidence.provenance", func(v string) error {
			e := loadFixture[ExpandedEvidence](t, "expanded_evidence.v1.json")
			e.Evidence.Provenance = v
			return e.ValidateRead()
		}},
		{"context_packet.resolved_scope.resolution", func(v string) error {
			p := loadFixture[ContextPacket](t, "context_packet.v1.json")
			p.ResolvedScope.Resolution = ScopeResolution(v)
			return p.ValidateRead()
		}},
		{"context_packet.freshness.watermarks.status", func(v string) error {
			p := loadFixture[ContextPacket](t, "context_packet.v1.json")
			p.Freshness.Watermarks[0].Status = v
			return p.ValidateRead()
		}},
		{"context_packet.items.category", func(v string) error {
			p := loadFixture[ContextPacket](t, "context_packet.v1.json")
			p.Items[0].Category = PacketCategory(v)
			return p.ValidateRead()
		}},
		{"context_packet_item.claim_kind", func(v string) error {
			i := loadFixture[ContextPacketItem](t, "context_packet_item.v1.json")
			i.ClaimKind = ClaimKind(v)
			return i.ValidateRead()
		}},
		{"context_packet_item.severity", func(v string) error {
			i := loadFixture[ContextPacketItem](t, "context_packet_item.v1.json")
			i.Severity = Severity(v)
			return i.ValidateRead()
		}},
		{"agent_episode.redaction_state", func(v string) error {
			e := loadFixture[AgentEpisode](t, "agent_episode.v1.json")
			e.RedactionState = v
			return e.ValidateRead()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.validate(addedVocabularyValue); err != nil {
				t.Fatalf("an added value is rejected: %v", err)
			}
			for _, malformed := range []string{"", "has space", strings.Repeat("v", 129)} {
				if err := tc.validate(malformed); err == nil {
					t.Fatalf("a malformed value %q is accepted", malformed)
				}
			}
		})
	}
}

func TestValidateKeepsFrozenAndLocallyMintedVocabulariesClosed(t *testing.T) {
	cases := []struct {
		name string
		err  func() error
	}{
		{"context_packet.status", func() error {
			p := loadFixture[ContextPacket](t, "context_packet.v1.json")
			p.Status = PacketStatus(addedVocabularyValue)
			return p.ValidateRead()
		}},
		{"agent_episode.outcome", func() error {
			e := loadFixture[AgentEpisode](t, "agent_episode.v1.json")
			e.Outcome = addedVocabularyValue
			return e.ValidateRead()
		}},
		{"record_episode.transcript_disposition", func() error {
			r := loadFixture[MCPRecordEpisodeResponse](t, "mcp_record_episode_response.v1.json")
			r.TranscriptDisposition = addedVocabularyValue
			return r.Validate()
		}},
		{"agent_episode.transcript.mode", func() error {
			e := loadFixture[AgentEpisode](t, "agent_episode.v1.json")
			e.Transcript.Mode = addedVocabularyValue
			return e.ValidateRead()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err() == nil {
				t.Fatal("a frozen or locally minted vocabulary accepts an unknown value")
			}
		})
	}
}
