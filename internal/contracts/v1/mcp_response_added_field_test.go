package v1

import (
	"encoding/json"
	"testing"
)

// A client older than the server must read a response that carries a field it
// does not know, at the root and in a nested object. Requests stay strict.
func TestMCPResponsesDecodeAnAddedField(t *testing.T) {
	cases := []struct {
		name   string
		file   string
		nested string
		into   func() any
	}{
		{"context_for_task", "mcp_context_for_task_response.v1.json", "structured", func() any { return &MCPContextForTaskResponse{} }},
		{"source_evidence", "mcp_source_evidence_response.v1.json", "structured", func() any { return &MCPSourceEvidenceResponse{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			document := loadFixture[map[string]any](t, tc.file)
			document["added_by_a_newer_server"] = "x"
			nested, ok := document[tc.nested].(map[string]any)
			if !ok {
				t.Fatalf("fixture has no %s object", tc.nested)
			}
			nested["added_by_a_newer_server"] = "x"
			mutated, _ := json.Marshal(document)
			if err := json.Unmarshal(mutated, tc.into()); err != nil {
				t.Fatalf("response with an added field must decode: %v", err)
			}
		})
	}
}

func TestMCPRequestsStillRefuseAnUnknownField(t *testing.T) {
	var request MCPContextForTaskRequest
	if err := json.Unmarshal([]byte(`{"goal":"g","unknown_field":1}`), &request); err == nil {
		t.Fatal("a request with an unknown field must stay refused")
	}
}
