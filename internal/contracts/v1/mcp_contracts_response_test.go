package v1

import (
	"strings"
	"testing"
)

func TestMCPContextForTaskResponseFixture(t *testing.T) {
	response := loadFixture[MCPContextForTaskResponse](t, "mcp_context_for_task_response.v1.json")
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
	if !response.RenderedMarkdown.Untrusted {
		t.Fatal("rendered_markdown.untrusted must be true: retrieved content is untrusted data")
	}
	assertSchemaParity(t, "mcp_context_for_task_response.v1.schema.json", response)
}

func TestMCPSourceEvidenceResponseFixture(t *testing.T) {
	response := loadFixture[MCPSourceEvidenceResponse](t, "mcp_source_evidence_response.v1.json")
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
	if !response.RenderedMarkdown.Untrusted {
		t.Fatal("rendered_markdown.untrusted must be true: retrieved content is untrusted data")
	}
	assertSchemaParity(t, "mcp_source_evidence_response.v1.schema.json", response)
}

func TestMCPRenderedMarkdownValidate_matches_v1_boundaries(t *testing.T) {
	oversized := strings.Repeat("m", mcpRenderedMarkdownMaxLength+1)
	value := MCPRenderedMarkdown{Markdown: oversized, Untrusted: true}
	if err := value.Validate(); err == nil {
		t.Fatal("validator accepted oversized markdown rendering")
	}
	untrustedFalse := MCPRenderedMarkdown{Markdown: "hello", Untrusted: false}
	if err := untrustedFalse.Validate(); err == nil {
		t.Fatal("validator accepted rendered_markdown.untrusted=false")
	}
}

// TestMCPContextForTaskResponseValidateRejectsOtherwiseEmptyStructured
// locks the Oracle gate finding: the response validator used to only
// inspect structured.schema_version, so an otherwise-empty ContextPacket
// (missing every other required field) wrongly passed. It must now be
// rejected because Validate() delegates to ContextPacket.Validate().
func TestMCPContextForTaskResponseValidateRejectsOtherwiseEmptyStructured(t *testing.T) {
	response := MCPContextForTaskResponse{
		SchemaVersion:    MCPContextForTaskResponseSchema,
		Structured:       ContextPacket{SchemaVersion: ContextPacketSchema},
		RenderedMarkdown: MCPRenderedMarkdown{Markdown: "placeholder", Untrusted: true},
	}
	if err := response.Validate(); err == nil {
		t.Fatal("validator accepted a structurally-empty structured ContextPacket")
	}
}

// TestMCPSourceEvidenceResponseValidateRejectsOtherwiseEmptyStructured is
// the source_evidence analogue: the response validator used to only
// inspect structured.schema_version, so an otherwise-empty
// ExpandedEvidence wrongly passed. It must now be rejected because
// Validate() delegates to ExpandedEvidence.Validate().
func TestMCPSourceEvidenceResponseValidateRejectsOtherwiseEmptyStructured(t *testing.T) {
	response := MCPSourceEvidenceResponse{
		SchemaVersion:    MCPSourceEvidenceResponseSchema,
		Structured:       ExpandedEvidence{SchemaVersion: ExpandedEvidenceSchema},
		RenderedMarkdown: MCPRenderedMarkdown{Markdown: "placeholder", Untrusted: true},
	}
	if err := response.Validate(); err == nil {
		t.Fatal("validator accepted a structurally-empty structured ExpandedEvidence")
	}
}
