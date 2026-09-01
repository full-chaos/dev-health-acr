package contextfabric

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestChaos4690RecomposedCohortNarrativeWireFieldsCarryNoScoringArithmetic is
// CHAOS-4690 Commit E's dedicated red-first proof (design §5, sol r1 F1):
// on the wire, a recomposed cohort narrative's `deterministic_answer` and
// `direct_judgment` fields must carry no `(weight ` substring and no
// `attention points` substring -- the CHAOS-4580 principal-driver-clause
// splice this ticket rips out, not rewords -- and `deterministic_answer`
// must equal the status sentence exactly, same as `direct_judgment`.
//
// RED on parent f4d31f22: recomposeCohortAnswerNarrative there takes a
// third `drivers []DriverJudgment` argument and, when a DriverPrincipal
// entry is present, splices its narrated Summary (scoring arithmetic
// inline) onto deterministic_answer. Verified in a detached worktree
// (`git worktree add --detach /tmp/acr-parent-4690e f4d31f22`) against an
// equivalent probe using the parent's 3-arg signature; removed after
// confirming red. This committed version calls the CHAOS-4690 2-arg
// signature and is green at tip.
//
// The two substring checks below are validation IN A TEST -- sanctioned
// (see the delegation brief) -- never a pattern shipped in production code;
// production composes the status sentence only, via statusSentence, with
// no regex or substring inspection anywhere in the call path.
func TestChaos4690RecomposedCohortNarrativeWireFieldsCarryNoScoringArithmetic(t *testing.T) {
	t.Parallel()
	wantSentence := "This investigation is complete."

	directJudgment, deterministicAnswer := recomposeCohortAnswerNarrative(InvestigationComplete, SubjectResolution{})

	result := InvestigationResult{
		Status:              InvestigationComplete,
		DirectJudgment:      directJudgment,
		DeterministicAnswer: deterministicAnswer,
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal(result) error = %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("json.Unmarshal(raw) error = %v", err)
	}

	var wireDirectJudgment, wireDeterministicAnswer string
	if err := json.Unmarshal(wire["direct_judgment"], &wireDirectJudgment); err != nil {
		t.Fatalf("decode direct_judgment: %v", err)
	}
	if err := json.Unmarshal(wire["deterministic_answer"], &wireDeterministicAnswer); err != nil {
		t.Fatalf("decode deterministic_answer: %v", err)
	}

	if wireDeterministicAnswer != wantSentence {
		t.Fatalf("deterministic_answer = %q, want %q (status sentence alone)", wireDeterministicAnswer, wantSentence)
	}
	if wireDirectJudgment != wantSentence {
		t.Fatalf("direct_judgment = %q, want %q (status sentence alone)", wireDirectJudgment, wantSentence)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"deterministic_answer", wireDeterministicAnswer},
		{"direct_judgment", wireDirectJudgment},
	} {
		if strings.Contains(field.value, "(weight ") {
			t.Fatalf("%s = %q, must not contain %q (CHAOS-4580 driver-clause splice)", field.name, field.value, "(weight ")
		}
		if strings.Contains(field.value, "attention points") {
			t.Fatalf("%s = %q, must not contain %q (CHAOS-4580 driver-clause splice)", field.name, field.value, "attention points")
		}
	}
}
