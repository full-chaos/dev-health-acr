package guide

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A Context Fabric reference names its subject, not its answer: the
// expand_evidence prompt must generate a source_evidence call the handler
// accepts, i.e. one carrying the answer's result_id, and refuse to build the
// unscoped call the handler rejects.
func TestExpandEvidencePromptScopesContextFabricReferences(t *testing.T) {
	v := PromptVocab{}
	ref := "acr:v1:organization:org_123"

	if contractsv1.RowKeyedEvidenceRef(ref) {
		t.Fatal("test premise: an organization reference is record-only")
	}
	if _, err := RenderPrompt(v, PromptExpand, map[string]string{ArgEvidenceRefID: ref}); err == nil {
		t.Fatal("an unscoped record-only Context Fabric reference must not render a call the handler refuses")
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

func TestExpandEvidencePromptRendersRowKeyedReferencesWithoutResultID(t *testing.T) {
	for _, ref := range []string{"acr:v1:team:team_123", "acr:v1:project:proj_123", "acr:v1:pull-request:pr_123"} {
		if !contractsv1.RowKeyedEvidenceRef(ref) {
			t.Fatalf("test premise: %s must be row-keyed", ref)
		}
		got, err := RenderPrompt(PromptVocab{}, PromptExpand, map[string]string{ArgEvidenceRefID: ref})
		if err != nil {
			t.Fatalf("%s: a row-keyed reference needs no result_id: %v", ref, err)
		}
		if !strings.Contains(got.Text, `"evidence_ref_id": "`+ref+`"`) || strings.Contains(got.Text, `"result_id"`) {
			t.Fatalf("%s: call must carry the reference and no result_id:\n%s", ref, got.Text)
		}
	}
}

func TestExpandEvidencePromptArgTextNamesNoKindList(t *testing.T) {
	for _, p := range PromptDefs(PromptVocab{}) {
		if p.Name != PromptExpand {
			continue
		}
		for _, a := range p.Args {
			if a.Name == ArgResultID && (!strings.Contains(a.Description, "row-keyed") || strings.Contains(a.Description, "pull-request")) {
				t.Fatalf("result_id text must say row-keyed refs need none and keep no kind list: %s", a.Description)
			}
		}
		return
	}
	t.Fatal("expand prompt not found")
}
