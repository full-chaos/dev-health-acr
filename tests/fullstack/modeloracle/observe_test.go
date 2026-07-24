package main

import (
	"encoding/json"
	"testing"
)

const packetResponse = `{
  "schema_version": "mcp_context_for_task_response.v1",
  "structured": {
    "schema_version": "context_packet.v1",
    "context_packet_id": "pkt_0123456789abcdef01234567",
    "status": "complete",
    "resolved_scope": {"repo_slug": "example-org/widget-service", "resolution": "exact_commit", "fallback_reasons": []},
    "warnings": [],
    "coverage": {"sources_unavailable": []},
    "items": [
      {
        "packet_item_id": "itm_1",
        "category": "cause",
        "related_entities": [{"type": "commit", "id": "a1b2", "label": "checkout: retry-safe wait"}],
        "evidence_ref_ids": ["ev1_kid_commit_OPAQUE-A.MAC"]
      },
      {
        "packet_item_id": "itm_2",
        "category": "evidence",
        "related_entities": [{"type": "ci_pipeline_run", "id": "checkout-e2e-run-4821", "label": "CI"}],
        "evidence_ref_ids": ["ev1_kid_ci_OPAQUE-B.MAC"]
      }
    ]
  },
  "rendered_markdown": {"markdown": "status: degraded", "untrusted": true, "truncated": false}
}`

const evidenceResponse = `{
  "schema_version": "mcp_source_evidence_response.v1",
  "structured": {
    "schema_version": "expanded_evidence.v1",
    "evidence": {
      "schema_version": "evidence_ref.v1",
      "evidence_ref_id": "ev1_kid_ci_OPAQUE-B.MAC",
      "source": {"system": "dev_health", "entity_type": "ci_pipeline_run", "entity_id": "checkout-e2e-run-4821", "display_label": "CI"},
      "availability": "available",
      "content_digest": "sha256:abc"
    },
    "availability": "available",
    "structured_fields": {}
  }
}`

func TestObserveToolResultReadsPacketSemantics(t *testing.T) {
	observed := observeToolResult(packetResponse)
	if observed.PacketStatus != "complete" {
		t.Fatalf("packet status = %q, want complete", observed.PacketStatus)
	}
	if observed.ScopeResolution != "exact_commit" {
		t.Fatalf("scope resolution = %q, want exact_commit", observed.ScopeResolution)
	}
	if len(observed.Sightings) != 2 {
		t.Fatalf("sightings = %d, want 2", len(observed.Sightings))
	}
}

// The rendered Markdown in the fixture says "status: degraded" on purpose: only the packet
// node carries context_packet_id, so untrusted rendered text must not move the status.
func TestObserveIgnoresStatusOutsideThePacketNode(t *testing.T) {
	if got := observeToolResult(packetResponse).PacketStatus; got != "complete" {
		t.Fatalf("packet status = %q, want complete", got)
	}
}

func TestObservePairsReferencesWithEntities(t *testing.T) {
	observed := observeToolResult(packetResponse)
	found := map[string]EvidenceSighting{}
	for _, sighting := range observed.Sightings {
		found[sighting.EntityType] = sighting
	}
	commit, ok := found["commit"]
	if !ok || commit.EvidenceRefID != "ev1_kid_commit_OPAQUE-A.MAC" || commit.EntityID != "a1b2" {
		t.Fatalf("commit sighting = %+v", commit)
	}
	if commit.Expanded {
		t.Fatal("a packet-only sighting must not be marked expanded")
	}
}

func TestObserveMarksExpandedEvidence(t *testing.T) {
	var observation Observation
	observation.merge(observeToolResult(packetResponse))
	observation.merge(observeToolResult(evidenceResponse))
	for _, sighting := range observation.Sightings {
		if sighting.EvidenceRefID != "ev1_kid_ci_OPAQUE-B.MAC" {
			continue
		}
		if !sighting.Expanded {
			t.Fatal("expanded evidence was not recorded as expanded")
		}
		if sighting.EntityID != "checkout-e2e-run-4821" {
			t.Fatalf("entity id = %q", sighting.EntityID)
		}
		if len(observation.Sightings) != 2 {
			t.Fatalf("expansion duplicated a sighting: %d", len(observation.Sightings))
		}
		return
	}
	t.Fatal("expanded evidence reference was not observed")
}

func TestObserveSkipsAmbiguousItemPairings(t *testing.T) {
	ambiguous := `{"context_packet_id":"pkt_x","status":"partial","items":[{"related_entities":[{"type":"commit","id":"a"}],"evidence_ref_ids":["ref-1","ref-2"]}]}`
	observed := observeToolResult(ambiguous)
	if len(observed.Sightings) != 2 {
		t.Fatalf("sightings = %d, want 2", len(observed.Sightings))
	}
	for _, sighting := range observed.Sightings {
		if sighting.EntityType != "" || sighting.EntityID != "" {
			t.Fatalf("ambiguous item guessed an entity: %+v", sighting)
		}
	}
}

func TestSelectEvidenceAddressesEntitiesNotTokens(t *testing.T) {
	var observation Observation
	observation.merge(observeToolResult(packetResponse))
	observation.merge(observeToolResult(evidenceResponse))

	cases := []struct {
		selector string
		want     int
	}{
		{"all", 2},
		{"", 2},
		{"expanded", 1},
		{"entity_type:commit", 1},
		{"entity_type:deployment", 0},
		{"entity:ci_pipeline_run/checkout-e2e-run-4821", 1},
		{"entity:ci_pipeline_run/other", 0},
		{"acr:v1:commit:", 0},
	}
	for _, testCase := range cases {
		if got := len(selectEvidence(testCase.selector, observation)); got != testCase.want {
			t.Errorf("selector %q matched %d, want %d", testCase.selector, got, testCase.want)
		}
	}
}

func TestMatchToolNameHandlesNamespacing(t *testing.T) {
	tools := []toolDefinition{}
	for _, name := range []string{"read", "acr_context_for_task", "acr_source_evidence"} {
		tool := toolDefinition{Type: "function"}
		tool.Function.Name = name
		tools = append(tools, tool)
	}
	if got, ok := matchToolName(tools, "context_for_task"); !ok || got != "acr_context_for_task" {
		t.Fatalf("context_for_task resolved to %q (%v)", got, ok)
	}
	if _, ok := matchToolName(tools, "record_episode"); ok {
		t.Fatal("record_episode must not resolve when the sidecar does not offer it")
	}
}

func TestTextContentAcceptsBothContentShapes(t *testing.T) {
	if got := textContent(json.RawMessage(`"plain"`)); got != "plain" {
		t.Fatalf("string content = %q", got)
	}
	if got := textContent(json.RawMessage(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`)); got != "ab" {
		t.Fatalf("part content = %q", got)
	}
}
