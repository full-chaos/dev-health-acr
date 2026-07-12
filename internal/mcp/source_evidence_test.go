package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHandleSourceEvidenceSuccess(t *testing.T) {
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)

	req := callToolRequest(t, map[string]any{
		"evidence_ref_id": "ev_abc123",
	})
	result, err := handleSourceEvidence(context.Background(), boot, req)
	if err != nil {
		t.Fatalf("expected a normal tool result, got protocol error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %#v", result.Content)
	}
	text, ok := result.Content[0].(*mcpsdk.TextContent)
	if !ok || !strings.Contains(text.Text, "UNTRUSTED DATA") {
		t.Fatalf("expected UNTRUSTED DATA markdown, got: %#v", result.Content[0])
	}
	if result.StructuredContent == nil {
		t.Fatal("expected structured content")
	}
}

// TestHandleSourceEvidenceDoesNotFalselyReportTruncationForMarkerTextInHostedContent
// is the source_evidence analogue of the context_for_task truncation-
// provenance lock: an excerpt that happens to contain the renderer's own
// truncation-notice wording must never flip Truncated to true.
func TestHandleSourceEvidenceDoesNotFalselyReportTruncationForMarkerTextInHostedContent(t *testing.T) {
	fx := newFixtureServer(t)
	fx.EvidenceHandler = func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[len("/api/v1/agent-context/evidence/"):]
		evidence := validExpandedEvidenceFixture(id)
		evidence.Excerpt = "log excerpt: remaining content omitted from the original vendor dashboard"
		writeJSONFixture(t, w, http.StatusOK, evidence)
	}
	boot := newFixtureBootstrap(t, fx)

	req := callToolRequest(t, map[string]any{
		"evidence_ref_id": "ev_abc123",
	})
	result, err := handleSourceEvidence(context.Background(), boot, req)
	if err != nil {
		t.Fatalf("expected a normal tool result, got protocol error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %#v", result.Content)
	}
	var response contractsv1.MCPSourceEvidenceResponse
	if err := json.Unmarshal(result.StructuredContent.(json.RawMessage), &response); err != nil {
		t.Fatalf("decode structured content: %v", err)
	}
	if response.RenderedMarkdown.Truncated {
		t.Fatal("expected Truncated=false: the marker text came from untrusted hosted content, not actual truncation")
	}
}

func TestHandleSourceEvidenceRejectsMissingID(t *testing.T) {
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)

	req := callToolRequest(t, map[string]any{})
	result, err := handleSourceEvidence(context.Background(), boot, req)
	if err != nil {
		t.Fatalf("expected a tool error, not a protocol error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError for a missing evidence_ref_id")
	}
}

func TestHandleSourceEvidenceRejectsExplicitNullID(t *testing.T) {
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)

	req := &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{
		Arguments: []byte(`{"evidence_ref_id":null}`),
	}}
	result, err := handleSourceEvidence(context.Background(), boot, req)
	if err != nil {
		t.Fatalf("expected a tool error, not a protocol error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError for an explicit JSON null evidence_ref_id")
	}
}

func TestHandleSourceEvidenceMapsNotFound(t *testing.T) {
	fx := newFixtureServer(t)
	fx.EvidenceHandler = func(w http.ResponseWriter, r *http.Request) {
		writeErrorFixture(t, w, http.StatusNotFound, "not_found", false)
	}
	boot := newFixtureBootstrap(t, fx)

	req := callToolRequest(t, map[string]any{
		"evidence_ref_id": "ev_missing",
	})
	result, err := handleSourceEvidence(context.Background(), boot, req)
	if err != nil {
		t.Fatalf("hosted API failures must be tool errors, not protocol errors: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError for a not_found evidence lookup")
	}
	text, ok := result.Content[0].(*mcpsdk.TextContent)
	if !ok || !strings.Contains(text.Text, "no_data") {
		t.Fatalf("expected no_data category in result, got: %#v", result.Content)
	}
}

func TestHandleSourceEvidenceHonorsTimeout(t *testing.T) {
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)

	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()

	req := callToolRequest(t, map[string]any{
		"evidence_ref_id": "ev_abc123",
	})
	result, err := handleSourceEvidence(ctx, boot, req)
	if err != nil {
		t.Fatalf("timeout must be a tool error, not a protocol error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError for an already-expired context")
	}
}

// TestHandleSourceEvidenceRejectsSchemaVersionField locks the handler-level
// end of the contract-owner's Finding 3 (see .omo/evidence/CHAOS-2908-contract.md):
// source_evidence's request accepts exactly evidence_ref_id. A caller that
// still sends schema_version -- even the correct constant value -- must be
// rejected as an unrecognized field by the actual runtime tool-call decode
// path, not merely by the offline JSON Schema contractcheck exercises.
func TestHandleSourceEvidenceRejectsSchemaVersionField(t *testing.T) {
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)

	req := callToolRequest(t, map[string]any{
		"schema_version":  "mcp_source_evidence_request.v1",
		"evidence_ref_id": "ev_abc123",
	})
	result, err := handleSourceEvidence(context.Background(), boot, req)
	if err != nil {
		t.Fatalf("expected a tool error, not a protocol error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError for a source_evidence request carrying schema_version")
	}
}
