package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateJSON_FormatAssertion is Codex finding 11: jsonschema-go treats "format" as an
// annotation only, so a malformed date-time/uri that is otherwise structurally valid JSON
// would pass validateJSON without the second pass added in schema.go. This exercises the
// exact example the finding gave -- a safe_uri of "not a uri" -- plus a malformed date-time,
// using the real contracts/jsonschema/v1/expanded_evidence.v1.schema.json.
func TestValidateJSON_FormatAssertion(t *testing.T) {
	root := repoRoot(t)
	loader := newSchemaLoader(filepath.Join(root, "contracts/jsonschema/v1"))

	valid := []byte(`{
	  "schema_version":"expanded_evidence.v1",
	  "evidence":{
	    "schema_version":"evidence_ref.v1","evidence_ref_id":"` + testEvidenceRefID + `",
	    "source":{"system":"dev_health","entity_type":"commit","entity_id":"a1b2","display_label":"x","safe_uri":"https://git.example.invalid/commit/a1b2"},
	    "provenance":"native","confidence":1.0,"citation":"x","observed_at":"2026-01-14T12:00:00Z",
	    "availability":"available"
	  },
	  "resolved_at":"2026-01-14T12:00:00Z","availability":"available","structured_fields":{}
	}`)
	if err := loader.validateJSON("expanded_evidence.v1.schema.json", valid); err != nil {
		t.Fatalf("expected a well-formed document to pass: %v", err)
	}

	badURI := []byte(`{
	  "schema_version":"expanded_evidence.v1",
	  "evidence":{
	    "schema_version":"evidence_ref.v1","evidence_ref_id":"` + testEvidenceRefID + `",
	    "source":{"system":"dev_health","entity_type":"commit","entity_id":"a1b2","display_label":"x","safe_uri":"not a uri"},
	    "provenance":"native","confidence":1.0,"citation":"x","observed_at":"2026-01-14T12:00:00Z",
	    "availability":"available"
	  },
	  "resolved_at":"2026-01-14T12:00:00Z","availability":"available","structured_fields":{}
	}`)
	err := loader.validateJSON("expanded_evidence.v1.schema.json", badURI)
	if err == nil {
		t.Fatal("expected a malformed safe_uri (\"not a uri\") to fail format assertion")
	}
	if !strings.Contains(err.Error(), "safe_uri") {
		t.Fatalf("error should name the offending field (safe_uri), got: %v", err)
	}

	badDateTime := []byte(`{
	  "schema_version":"expanded_evidence.v1",
	  "evidence":{
	    "schema_version":"evidence_ref.v1","evidence_ref_id":"` + testEvidenceRefID + `",
	    "source":{"system":"dev_health","entity_type":"commit","entity_id":"a1b2","display_label":"x"},
	    "provenance":"native","confidence":1.0,"citation":"x","observed_at":"not a timestamp",
	    "availability":"available"
	  },
	  "resolved_at":"2026-01-14T12:00:00Z","availability":"available","structured_fields":{}
	}`)
	err = loader.validateJSON("expanded_evidence.v1.schema.json", badDateTime)
	if err == nil {
		t.Fatal("expected a malformed observed_at date-time to fail format assertion")
	}
	if !strings.Contains(err.Error(), "observed_at") {
		t.Fatalf("error should name the offending field (observed_at), got: %v", err)
	}
}

// TestValidateJSON_FormatAssertionAcrossCrossFileRef confirms format assertion follows $ref
// into a sibling schema file (context_packet.v1 -> context_packet_item.v1), not just the top
// level of the document being validated.
func TestValidateJSON_FormatAssertionAcrossCrossFileRef(t *testing.T) {
	root := repoRoot(t)
	loader := newSchemaLoader(filepath.Join(root, "contracts/jsonschema/v1"))

	packet := `{
	  "schema_version":"context_packet.v1","context_packet_id":"cp_0000000000000001",
	  "request_id":"req_0000000000000001","generated_at":"2026-01-14T12:00:00Z",
	  "status":"complete","goal":"investigate",
	  "repository":{"slug":"example-org/widget-service"},
	  "requested_scope":{"commit_sha":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"},
	  "resolved_scope":{"repo_id":"r","repo_slug":"example-org/widget-service","resolution":"exact_commit","fallback_reasons":[]},
	  "query_version":"v1","ranking_version":"v1","summary":"s",
	  "items":[{
	    "schema_version":"context_packet_item.v1","packet_item_id":"item_00000001","category":"evidence",
	    "claim_kind":"observed","title":"t","summary":"s","why_included":"w","rule_id":"r",
	    "confidence":1.0,"severity":"info","rank":1,
	    "validity_scope":{},"flags":{"stale":false,"uncertain":false,"conflicting":false,"untrusted_content":false},
	    "related_entities":[{"type":"commit","id":"a1b2","label":"x","url":"REPLACE_URL"}],
	    "evidence_ref_ids":["` + testEvidenceRefID + `"]
	  }],
	  "required_checks":[],"recommended_next_steps":[],
	  "freshness":{"as_of":"2026-01-14T12:00:00Z","stale_after_seconds":0,"watermarks":[]},
	  "coverage":{"sources_considered":[],"sources_available":[],"sources_unavailable":[],"partial":false,"degraded_reasons":[]},
	  "budget":{"max_items":10,"items_used":1,"max_output_tokens":100,"estimated_tokens":10,"max_serialized_bytes":1000,"serialized_bytes":10,"truncated":false},
	  "warnings":[],
	  "compatibility":{"service_version":"1.0.0","minimum_sidecar_version":"1.0.0","supported_schema_versions":["context_packet.v1"]}
	}`

	good := strings.Replace(packet, "REPLACE_URL", "https://git.example.invalid/commit/a1b2", 1)
	if err := loader.validateJSON("context_packet.v1.schema.json", []byte(good)); err != nil {
		t.Fatalf("expected a well-formed packet to pass: %v", err)
	}

	bad := strings.Replace(packet, "REPLACE_URL", "not a uri either", 1)
	err := loader.validateJSON("context_packet.v1.schema.json", []byte(bad))
	if err == nil {
		t.Fatal("expected the malformed related_entities[].url (reached via a cross-file $ref into context_packet_item.v1) to fail format assertion")
	}
}
