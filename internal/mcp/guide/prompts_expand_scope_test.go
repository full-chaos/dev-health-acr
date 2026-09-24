package guide

import (
	"strings"
	"testing"
)

// A Context Fabric reference names its subject, not its answer: the
// expand_evidence prompt must generate a source_evidence call the handler
// accepts, i.e. one carrying the answer's result_id, and refuse to build the
// unscoped call the handler rejects.
func TestExpandEvidencePromptScopesContextFabricReferences(t *testing.T) {
	v := PromptVocab{}
	ref := "acr:v1:team:team_123"

	if _, err := RenderPrompt(v, PromptExpand, map[string]string{ArgEvidenceRefID: ref}); err == nil {
		t.Fatal("an unscoped Context Fabric reference must not render a call the handler refuses")
	}
	got, err := RenderPrompt(v, PromptExpand, map[string]string{ArgEvidenceRefID: ref, ArgResultID: "result_abcdef123456"})
	if err != nil {
		t.Fatalf("scoped render: %v", err)
	}
	if !strings.Contains(got.Text, `"result_id": "result_abcdef123456"`) || !strings.Contains(got.Text, `"evidence_ref_id": "`+ref+`"`) {
		t.Fatalf("call lacks the reference and its result_id:\n%s", got.Text)
	}
	if _, err := RenderPrompt(v, PromptExpand, map[string]string{ArgEvidenceRefID: ref, ArgResultID: "short"}); err == nil {
		t.Fatal("a result_id below the schema minimum must be refused")
	}
	packet, err := RenderPrompt(v, PromptExpand, map[string]string{ArgEvidenceRefID: "ev_abc12345"})
	if err != nil || strings.Contains(packet.Text, "result_id\": ") {
		t.Fatalf("a packet handle needs no result_id: err=%v\n%s", err, packet.Text)
	}
}
