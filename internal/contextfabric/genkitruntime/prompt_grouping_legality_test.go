package genkitruntime

import (
	"strings"
	"testing"
)

// TestTheFramePromptLeavesGroupingLegalityToTheServer pins the frame
// section's grouped_members sentence.
//
// The sentence it replaced told the model that group_kind and member_kind
// "must be DIFFERENT kinds". That made the MODEL the judge of invariant I6: a
// question that grouped a kind by itself could not be expressed as asked, so
// the model re-expressed it as a flat discovered_kind frame, which validates
// -- and the server's own I6 check, which exists to refuse exactly that
// question, never saw it. The question was answered flat, and nothing on the
// answer said a grouping had been asked for.
//
// Three properties, each asserted on the rendered prompt the model reads:
//   - the old legality instruction is gone;
//   - the new sentence tells the model to express the grouping as asked,
//     with a self-group example, and names the server as the judge;
//   - the sentence lives in the FRAME section, after the subject-expression
//     paragraph, and not in the group_kind hint paragraph the family
//     classifier reads (that paragraph is unchanged by design).
func TestTheFramePromptLeavesGroupingLegalityToTheServer(t *testing.T) {
	const retired = "which must be DIFFERENT kinds"
	if strings.Contains(interpretationSystemPrompt, retired) {
		t.Errorf("the interpretation prompt still says grouped_members kinds %q -- the model would go on deciding I6 itself and re-expressing a self-group as a flat frame the server cannot judge", retired)
	}
	for _, required := range []string{
		`grouped_members with group_kind "team" and member_kind "team"`,
		"Never re-express a grouped question as discovered_kind or any other variant",
		"whether a grouping is legal is decided by the server after you answer, never by you",
	} {
		if !strings.Contains(interpretationSystemPrompt, required) {
			t.Errorf("the interpretation prompt does not say %q", required)
		}
	}

	frameSection := strings.Index(interpretationSystemPrompt, "question_frame.subject_expression describes WHAT the question is about")
	sentence := strings.Index(interpretationSystemPrompt, "whether a grouping is legal is decided by the server")
	hint := strings.Index(interpretationSystemPrompt, "group_kind: emit ONLY when the question asks for results PARTITIONED INTO GROUPS")
	if frameSection < 0 || sentence < 0 || hint < 0 {
		t.Fatalf("anchors missing: frame section=%d sentence=%d hint paragraph=%d", frameSection, sentence, hint)
	}
	if sentence < frameSection {
		t.Errorf("the legality sentence sits at %d, before the frame section at %d -- it belongs to the frame's grouped_members instruction", sentence, frameSection)
	}
	if hint > frameSection {
		t.Errorf("the group_kind hint paragraph moved into or after the frame section (%d > %d)", hint, frameSection)
	}
}
