package v1

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
)

func TestMCPContextForTaskRequestFixtures(t *testing.T) {
	minimal := loadFixture[MCPContextForTaskRequest](t, "mcp_context_for_task_request.v1.json")
	if err := minimal.Validate(); err != nil {
		t.Fatalf("goal-only fixture: %v", err)
	}
	if minimal.Repository != nil || minimal.Scope != nil || minimal.Budget != nil {
		t.Fatal("minimal fixture must exercise the goal-only ergonomic shape")
	}
	assertSchemaParity(t, "mcp_context_for_task_request.v1.schema.json", minimal)

	full := loadFixture[MCPContextForTaskRequest](t, "mcp_context_for_task_request_full.v1.json")
	if err := full.Validate(); err != nil {
		t.Fatalf("full fixture: %v", err)
	}
	if full.Repository == nil || full.Scope == nil || full.Budget == nil {
		t.Fatal("full fixture must exercise repository, scope, and budget")
	}
	assertSchemaParity(t, "mcp_context_for_task_request.v1.schema.json", full)
}

func TestMCPContextForTaskRequestRejectsRepoIDAndRemoteURL(t *testing.T) {
	base := loadFixture[MCPContextForTaskRequest](t, "mcp_context_for_task_request_full.v1.json")
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	repository, ok := document["repository"].(map[string]any)
	if !ok {
		t.Fatal("fixture must contain a repository object")
	}
	for _, field := range []string{"repo_id", "remote_url"} {
		mutated := map[string]any{}
		maps.Copy(mutated, document)
		mutatedRepository := map[string]any{}
		maps.Copy(mutatedRepository, repository)
		mutatedRepository[field] = "disallowed"
		mutated["repository"] = mutatedRepository
		encoded, err := json.Marshal(mutated)
		if err != nil {
			t.Fatalf("marshal mutated fixture: %v", err)
		}
		if err := contractcheck.ValidateSerialized("", "mcp_context_for_task_request.v1.schema.json", encoded); err == nil {
			t.Fatalf("expected repository.%s to be rejected; only an explicit slug is allowed", field)
		}
	}
}

func TestMCPContextForTaskRequestRejectsUnknownField(t *testing.T) {
	payload := []byte(`{"goal":"g","unexpected_field":true}`)
	if err := contractcheck.ValidateSerialized("", "mcp_context_for_task_request.v1.schema.json", payload); err == nil {
		t.Fatal("expected unknown top-level field to be rejected")
	}
}

func TestMCPContextForTaskRequestValidate_matches_v1_boundaries(t *testing.T) {
	base := MCPContextForTaskRequest{Goal: strings.Repeat("g", 4000)}
	if err := base.Validate(); err != nil {
		t.Fatalf("maximal valid goal-only request: %v", err)
	}
	assertSchemaParity(t, "mcp_context_for_task_request.v1.schema.json", base)

	cases := []struct {
		name   string
		mutate func(*MCPContextForTaskRequest)
	}{
		{name: "goal_empty", mutate: func(v *MCPContextForTaskRequest) { v.Goal = "" }},
		{name: "goal_too_long", mutate: func(v *MCPContextForTaskRequest) { v.Goal = strings.Repeat("g", 4001) }},
		{name: "repo_slug_invalid", mutate: func(v *MCPContextForTaskRequest) {
			v.Repository = &MCPRepositoryRef{Slug: "invalid-slug"}
		}},
		{name: "scope_commit_invalid", mutate: func(v *MCPContextForTaskRequest) {
			v.Scope = &MCPRequestedScope{CommitSHA: "bad"}
		}},
		{name: "scope_duplicate_file", mutate: func(v *MCPContextForTaskRequest) {
			v.Scope = &MCPRequestedScope{Files: []string{"a.go", "a.go"}}
		}},
		{name: "budget_max_items_out_of_range", mutate: func(v *MCPContextForTaskRequest) {
			v.Budget = &MCPBudget{MaxItems: 51}
		}},
		{name: "budget_max_output_tokens_out_of_range", mutate: func(v *MCPContextForTaskRequest) {
			v.Budget = &MCPBudget{MaxOutputTokens: 100}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := MCPContextForTaskRequest{Goal: "goal"}
			tc.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("validator accepted schema-invalid request")
			}
		})
	}
}

// TestMCPContextForTaskRequestScopeRejectsMalformedAsOf locks the new
// scope.as_of field's format at both boundaries: the JSON Schema's
// "format": "date-time" and the Go decode boundary's *time.Time parse.
func TestMCPContextForTaskRequestScopeRejectsMalformedAsOf(t *testing.T) {
	payload := []byte(`{"goal":"g","scope":{"as_of":"not-a-timestamp"}}`)
	if err := contractcheck.ValidateSerialized("", "mcp_context_for_task_request.v1.schema.json", payload); err == nil {
		t.Fatal("expected malformed scope.as_of to be rejected by the JSON Schema format check")
	}
	var decoded MCPContextForTaskRequest
	if err := json.Unmarshal(payload, &decoded); err == nil {
		t.Fatal("expected malformed scope.as_of to be rejected at the Go decode boundary")
	}
}

// TestMCPContextForTaskRequestScopeRejectsNonBooleanIncludeChangedFiles
// locks the new scope.include_changed_files field's type at both
// boundaries: the JSON Schema's "type": "boolean" and the Go decode
// boundary's *bool unmarshal.
func TestMCPContextForTaskRequestScopeRejectsNonBooleanIncludeChangedFiles(t *testing.T) {
	payload := []byte(`{"goal":"g","scope":{"include_changed_files":"yes"}}`)
	if err := contractcheck.ValidateSerialized("", "mcp_context_for_task_request.v1.schema.json", payload); err == nil {
		t.Fatal("expected non-boolean scope.include_changed_files to be rejected by the JSON Schema type check")
	}
	var decoded MCPContextForTaskRequest
	if err := json.Unmarshal(payload, &decoded); err == nil {
		t.Fatal("expected non-boolean scope.include_changed_files to be rejected at the Go decode boundary")
	}
}

// TestMCPContextForTaskRequestScopeAcceptsIncludeChangedFilesTriState locks
// the exact tri-state semantics: omitted decodes to nil (sidecar default),
// and both explicit true and explicit false decode to a non-nil pointer
// carrying that exact value.
func TestMCPContextForTaskRequestScopeAcceptsIncludeChangedFilesTriState(t *testing.T) {
	cases := []struct {
		name string
		json string
		want *bool
	}{
		{"omitted", `{}`, nil},
		{"true", `{"include_changed_files":true}`, boolPtr(true)},
		{"false", `{"include_changed_files":false}`, boolPtr(false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(`{"goal":"g","scope":` + tc.json + `}`)
			var decoded MCPContextForTaskRequest
			if err := json.Unmarshal(payload, &decoded); err != nil {
				t.Fatalf("decode: %v", err)
			}
			got := decoded.Scope.IncludeChangedFiles
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Fatalf("include_changed_files = %v, want %v", ptrString(got), ptrString(tc.want))
			}
		})
	}
}

func boolPtr(v bool) *bool { return &v }

func ptrString(v *bool) string {
	if v == nil {
		return "nil"
	}
	if *v {
		return "true"
	}
	return "false"
}
