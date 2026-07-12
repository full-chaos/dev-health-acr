package v1

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
)

// setJSONNull decodes raw into a generic document, sets the field at path
// (dot-walked through nested objects) to the literal JSON value null, and
// re-encodes it. It is the actual-JSON-decode fixture mutator every
// explicit-null canary below uses instead of building a Go struct by hand:
// a manually constructed MCPContextForTaskRequest{Repository: nil} can never
// exercise the decode-boundary bug this file fixes, since that bug only
// exists in the gap between "field omitted" and "field explicitly null" on
// the wire.
func setJSONNull(t *testing.T, raw []byte, path ...string) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode fixture for mutation: %v", err)
	}
	nestJSONNull(t, doc, path)
	mutated, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal mutated fixture: %v", err)
	}
	return mutated
}

func nestJSONNull(t *testing.T, doc map[string]any, path []string) {
	t.Helper()
	if len(path) == 1 {
		doc[path[0]] = nil
		return
	}
	nested, ok := doc[path[0]].(map[string]any)
	if !ok {
		t.Fatalf("fixture missing object at %q for nested null mutation", path[0])
	}
	nestJSONNull(t, nested, path[1:])
}

// requestNullCanaries lists every (schema file, fixture, field path) triple
// the request-side explicit-null tests below drive: top-level optional
// objects, their own nested fields, and the two top-level required scalars.
func requestNullCanaries() []struct {
	name    string
	schema  string
	fixture string
	path    []string
} {
	return []struct {
		name    string
		schema  string
		fixture string
		path    []string
	}{
		{"context_for_task_goal", "mcp_context_for_task_request.v1.schema.json", "mcp_context_for_task_request_full.v1.json", []string{"goal"}},
		{"context_for_task_repository", "mcp_context_for_task_request.v1.schema.json", "mcp_context_for_task_request_full.v1.json", []string{"repository"}},
		{"context_for_task_repository_slug", "mcp_context_for_task_request.v1.schema.json", "mcp_context_for_task_request_full.v1.json", []string{"repository", "slug"}},
		{"context_for_task_scope", "mcp_context_for_task_request.v1.schema.json", "mcp_context_for_task_request_full.v1.json", []string{"scope"}},
		{"context_for_task_scope_branch", "mcp_context_for_task_request.v1.schema.json", "mcp_context_for_task_request_full.v1.json", []string{"scope", "branch"}},
		{"context_for_task_scope_time_window_days", "mcp_context_for_task_request.v1.schema.json", "mcp_context_for_task_request_full.v1.json", []string{"scope", "time_window_days"}},
		{"context_for_task_scope_as_of", "mcp_context_for_task_request.v1.schema.json", "mcp_context_for_task_request_full.v1.json", []string{"scope", "as_of"}},
		{"context_for_task_scope_include_changed_files", "mcp_context_for_task_request.v1.schema.json", "mcp_context_for_task_request_full.v1.json", []string{"scope", "include_changed_files"}},
		{"context_for_task_budget", "mcp_context_for_task_request.v1.schema.json", "mcp_context_for_task_request_full.v1.json", []string{"budget"}},
		{"context_for_task_budget_max_items", "mcp_context_for_task_request.v1.schema.json", "mcp_context_for_task_request_full.v1.json", []string{"budget", "max_items"}},
		{"source_evidence_evidence_ref_id", "mcp_source_evidence_request.v1.schema.json", "mcp_source_evidence_request.v1.json", []string{"evidence_ref_id"}},
	}
}

// TestMCPRequestExplicitNullRejectedAtDecodeBoundary is the core Oracle-gate
// regression: every field these two MCP request contracts declare, at every
// depth this package owns, must reject an explicit JSON null both by the
// static JSON Schema (the schema-vs-Go canary: if this ever stops failing,
// the schema quietly grew a nullable type and this test needs updating
// alongside it) and by the Go decode boundary this file's UnmarshalJSON
// methods now enforce.
func TestMCPRequestExplicitNullRejectedAtDecodeBoundary(t *testing.T) {
	for _, tc := range requestNullCanaries() {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile(fixturePath(t, tc.fixture))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			mutated := setJSONNull(t, raw, tc.path...)

			if err := contractcheck.ValidateSerialized("", tc.schema, mutated); err == nil {
				t.Fatalf("schema canary: expected JSON Schema to reject explicit null at %s", strings.Join(tc.path, "."))
			}

			target := decodeTargetFor(tc.schema)
			if err := json.Unmarshal(mutated, target); err == nil {
				t.Fatalf("Go decode boundary: expected explicit null at %s to be rejected", strings.Join(tc.path, "."))
			}
		})
	}
}

// decodeTargetFor returns a fresh pointer to the request type tc.schema
// describes, so the table above can drive both request contracts through
// one loop.
func decodeTargetFor(schema string) any {
	switch schema {
	case "mcp_context_for_task_request.v1.schema.json":
		return &MCPContextForTaskRequest{}
	case "mcp_source_evidence_request.v1.schema.json":
		return &MCPSourceEvidenceRequest{}
	default:
		panic("decodeTargetFor: unknown schema " + schema)
	}
}

// TestMCPContextForTaskRequestOmittedOptionalFieldsAccepted locks the
// ergonomic side of the fix: omitting repository/scope/budget entirely
// must still decode to nil pointers and validate, exactly as before this
// file's UnmarshalJSON methods were added.
func TestMCPContextForTaskRequestOmittedOptionalFieldsAccepted(t *testing.T) {
	minimal := []byte(`{"goal":"Add repository-scoped ACR credentials"}`)
	var decoded MCPContextForTaskRequest
	if err := json.Unmarshal(minimal, &decoded); err != nil {
		t.Fatalf("omitted optional fields must decode: %v", err)
	}
	if decoded.Repository != nil || decoded.Scope != nil || decoded.Budget != nil {
		t.Fatal("omitted fields must decode to nil, not zero-value objects")
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("goal-only decode must validate: %v", err)
	}
}

// TestMCPContextForTaskRequestPopulatedObjectsAccepted locks the other
// side: a populated repository/scope/budget object must still decode to
// non-nil pointers with their values intact.
func TestMCPContextForTaskRequestPopulatedObjectsAccepted(t *testing.T) {
	raw, err := os.ReadFile(fixturePath(t, "mcp_context_for_task_request_full.v1.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var decoded MCPContextForTaskRequest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("populated repository/scope/budget must decode: %v", err)
	}
	if decoded.Repository == nil || decoded.Scope == nil || decoded.Budget == nil {
		t.Fatal("populated fixture must decode to non-nil pointers")
	}
	if decoded.Repository.Slug != "full-chaos/dev-health-acr" {
		t.Fatalf("unexpected repository.slug: %q", decoded.Repository.Slug)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("populated decode must validate: %v", err)
	}
}

// TestMCPResponseExplicitNullRejectedAtDecodeBoundary is the response-side
// analogue: schema_version, structured, and rendered_markdown (plus
// rendered_markdown's own nested fields) must reject explicit null at the
// Go decode boundary, matching the JSON Schema.
func TestMCPResponseExplicitNullRejectedAtDecodeBoundary(t *testing.T) {
	cases := []struct {
		name    string
		schema  string
		fixture string
		path    []string
	}{
		{"context_for_task_structured", "mcp_context_for_task_response.v1.schema.json", "mcp_context_for_task_response.v1.json", []string{"structured"}},
		{"context_for_task_rendered_markdown", "mcp_context_for_task_response.v1.schema.json", "mcp_context_for_task_response.v1.json", []string{"rendered_markdown"}},
		{"context_for_task_rendered_markdown_truncated", "mcp_context_for_task_response.v1.schema.json", "mcp_context_for_task_response.v1.json", []string{"rendered_markdown", "truncated"}},
		{"source_evidence_structured", "mcp_source_evidence_response.v1.schema.json", "mcp_source_evidence_response.v1.json", []string{"structured"}},
		{"source_evidence_rendered_markdown", "mcp_source_evidence_response.v1.schema.json", "mcp_source_evidence_response.v1.json", []string{"rendered_markdown"}},
		{"source_evidence_rendered_markdown_untrusted", "mcp_source_evidence_response.v1.schema.json", "mcp_source_evidence_response.v1.json", []string{"rendered_markdown", "untrusted"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile(fixturePath(t, tc.fixture))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			mutated := setJSONNull(t, raw, tc.path...)

			if err := contractcheck.ValidateSerialized("", tc.schema, mutated); err == nil {
				t.Fatalf("schema canary: expected JSON Schema to reject explicit null at %s", strings.Join(tc.path, "."))
			}

			var target any = &MCPContextForTaskResponse{}
			if tc.schema == "mcp_source_evidence_response.v1.schema.json" {
				target = &MCPSourceEvidenceResponse{}
			}
			if err := json.Unmarshal(mutated, target); err == nil {
				t.Fatalf("Go decode boundary: expected explicit null at %s to be rejected", strings.Join(tc.path, "."))
			}
		})
	}
}

// TestMCPGoldenFixturesDecodeCleanly is the golden-fixtures-accepted
// canary: every checked-in MCP example must still decode through the new
// UnmarshalJSON methods with zero errors, proving the null-rejection added
// above never rejects a legitimate, null-free payload.
func TestMCPGoldenFixturesDecodeCleanly(t *testing.T) {
	if _, err := os.ReadFile(fixturePath(t, "mcp_context_for_task_request.v1.json")); err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	_ = loadFixture[MCPContextForTaskRequest](t, "mcp_context_for_task_request.v1.json")
	_ = loadFixture[MCPContextForTaskRequest](t, "mcp_context_for_task_request_full.v1.json")
	_ = loadFixture[MCPSourceEvidenceRequest](t, "mcp_source_evidence_request.v1.json")
	_ = loadFixture[MCPContextForTaskResponse](t, "mcp_context_for_task_response.v1.json")
	_ = loadFixture[MCPSourceEvidenceResponse](t, "mcp_source_evidence_response.v1.json")
}

// TestMCPTopLevelExplicitNullRejected covers the whole-document edge case
// alongside the field-level ones above: a bare top-level `null` payload
// carries no keys at all, so the field-level walk alone would never see
// it, yet the same no-nullable-type rule applies to the document a decode
// call represents. mcpNullCheck.apply checks raw itself before walking
// its keys specifically so this case is rejected too.
func TestMCPTopLevelExplicitNullRejected(t *testing.T) {
	var contextForTaskRequest MCPContextForTaskRequest
	if err := json.Unmarshal([]byte(`null`), &contextForTaskRequest); err == nil {
		t.Fatal("expected top-level null to be rejected for MCPContextForTaskRequest")
	}
	var sourceEvidenceRequest MCPSourceEvidenceRequest
	if err := json.Unmarshal([]byte(`null`), &sourceEvidenceRequest); err == nil {
		t.Fatal("expected top-level null to be rejected for MCPSourceEvidenceRequest")
	}
	var contextForTaskResponse MCPContextForTaskResponse
	if err := json.Unmarshal([]byte(`null`), &contextForTaskResponse); err == nil {
		t.Fatal("expected top-level null to be rejected for MCPContextForTaskResponse")
	}
	var sourceEvidenceResponse MCPSourceEvidenceResponse
	if err := json.Unmarshal([]byte(`null`), &sourceEvidenceResponse); err == nil {
		t.Fatal("expected top-level null to be rejected for MCPSourceEvidenceResponse")
	}
}
