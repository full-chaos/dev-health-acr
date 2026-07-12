package v1

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMCPMalformedInputNeverPanics is the manual malformed-fixture sweep:
// wrong JSON types, truncated documents, and deeply mis-shaped nesting
// must all surface as a decode error, never a panic, through the same
// UnmarshalJSON boundary the null-rejection canaries exercise.
func TestMCPMalformedInputNeverPanics(t *testing.T) {
	cases := []string{
		`{"goal":"g","repository":[]}`,
		`{"goal":"g","scope":{"branch":{"nested":"not-a-string"}}}`,
		`{"goal":"g"`,
		`{"goal":123}`,
		`[]`,
		`"just a string"`,
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("decode boundary must never panic on malformed input, got: %v", r)
				}
			}()
			var decoded MCPContextForTaskRequest
			if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
				t.Fatalf("expected malformed input to be rejected: %s", raw)
			}
		})
	}
}

// TestMCPRequestRejectsUnknownFieldAtDecodeBoundary is the Go-decode-side
// analogue of TestMCPContextForTaskRequestRejectsUnknownField /
// TestMCPSourceEvidenceRequestRejectsUnknownField in mcp_contracts_test.go,
// which only prove the offline JSON Schema (additionalProperties: false)
// rejects an unrecognized field. Before the UnmarshalJSON methods below
// used a strict decoder, the actual runtime tool-call decode path (plain
// encoding/json struct unmarshaling) silently dropped any field the Go
// struct did not declare -- including a caller-sent source_evidence
// "schema_version", which the contract explicitly requires to be
// rejected, not ignored (see mcp_types.go's MCPSourceEvidenceRequest doc).
func TestMCPRequestRejectsUnknownFieldAtDecodeBoundary(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"context_for_task_top_level", `{"goal":"g","unexpected_field":true}`},
		{"context_for_task_schema_version", `{"goal":"g","schema_version":"` + MCPContextForTaskRequestSchema + `"}`},
		{"context_for_task_repository_repo_id", `{"goal":"g","repository":{"slug":"a/b","repo_id":"x"}}`},
		{"context_for_task_scope_unknown", `{"goal":"g","scope":{"branch":"main","bogus":"x"}}`},
		{"context_for_task_budget_unknown", `{"goal":"g","budget":{"max_items":5,"bogus":1}}`},
		{"source_evidence_schema_version", `{"schema_version":"` + MCPSourceEvidenceRequestSchema + `","evidence_ref_id":"ev_01J0ACR001"}`},
		{"source_evidence_unknown", `{"evidence_ref_id":"ev_01J0ACR001","extra":"nope"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if strings.HasPrefix(tc.name, "source_evidence") {
				var decoded MCPSourceEvidenceRequest
				if err := json.Unmarshal([]byte(tc.raw), &decoded); err == nil {
					t.Fatalf("expected unknown field to be rejected at the Go decode boundary: %s", tc.raw)
				}
				return
			}
			var decoded MCPContextForTaskRequest
			if err := json.Unmarshal([]byte(tc.raw), &decoded); err == nil {
				t.Fatalf("expected unknown field to be rejected at the Go decode boundary: %s", tc.raw)
			}
		})
	}
}

// TestMCPRequestRejectsTrailingDataAtDecodeBoundary locks strict decoding's
// second guarantee: a JSON document followed by additional non-whitespace
// data (a smuggled second value, or garbage bytes) must be rejected, not
// silently truncated to the first value.
func TestMCPRequestRejectsTrailingDataAtDecodeBoundary(t *testing.T) {
	validRequest := `{"goal":"g"}`
	cases := []string{
		validRequest + `{"goal":"h"}`,
		validRequest + ` garbage`,
		validRequest + `,`,
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			var decoded MCPContextForTaskRequest
			if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
				t.Fatalf("expected trailing data to be rejected: %s", raw)
			}
		})
	}
	// Trailing whitespace-only data must still decode cleanly: strict
	// decoding rejects extra content, not cosmetic formatting.
	var decoded MCPContextForTaskRequest
	if err := json.Unmarshal([]byte(validRequest+"\n\t "), &decoded); err != nil {
		t.Fatalf("expected trailing whitespace to be tolerated: %v", err)
	}
}
