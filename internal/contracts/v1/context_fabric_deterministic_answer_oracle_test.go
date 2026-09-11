package v1

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
)

// The terminal-form predicate's ORACLES, asserted through Validate() and the
// published schema only -- no symbol this change adds -- so the same file runs
// against the parent commit and fails there on the VALIDATOR'S RESULT, never
// on a missing symbol. Oracles 1, 4 and 5 are controls that hold at the
// parent too; 3, 7 and 8 are the widening and are RED at the parent.

const daAnswer = "Ask Dev is not release-ready because required work remains."

// daFixture returns a valid result at status, with n claimed facts (and the
// completeness count stamped to match) and e evidence refs, and the given
// answer sentence. It re-derives the terminal reason last, so every cell
// differs from the canonical document ONLY in what the cell names.
func daFixture(status ContextFabricInvestigationStatus, facts, evidence int, answer string) ContextFabricInvestigationResult {
	r := validContextFabricContractResult()
	r.Status = status
	r.Completeness.TerminalStatus = status
	r.DeterministicAnswer = answer
	switch status {
	case ContextFabricInvestigationComplete, ContextFabricInvestigationPartial:
		r.DirectJudgment = "Ask Dev is not release-ready."
	default:
		r.DirectJudgment = ""
		r.CurrentState = ""
	}
	if status == ContextFabricInvestigationClarificationRequired {
		r.SubjectResolution.ClarificationPrompt = "Which Ask Dev do you mean?"
	}
	r.ClaimedFacts = []ContextFabricClaimedFact{}
	for i := 0; i < facts; i++ {
		text := fmt.Sprintf("value_%d", i)
		r.ClaimedFacts = append(r.ClaimedFacts, ContextFabricClaimedFact{
			ClaimID: fmt.Sprintf("claim_status_%04d", i),
			Kind:    ContextFabricFactStatus,
			Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"},
			Field:   "state",
			Value:   ContextFabricScalarValue{String: &text},
		})
	}
	r.Completeness.ClaimedFactsCount = facts
	r.EvidenceRefIDs = []string{}
	for i := 0; i < evidence; i++ {
		r.EvidenceRefIDs = append(r.EvidenceRefIDs, fmt.Sprintf("evidence_%08d", i))
	}
	daRestamp(&r)
	return r
}

// daRestamp re-derives the completeness fields the validator requires to
// agree with the rest of the document, after a cell has changed it.
func daRestamp(r *ContextFabricInvestigationResult) {
	r.Completeness.TerminalReason = expectedTerminalReason(*r)
}

func daVerdict(err error) string {
	if err == nil {
		return "accepted"
	}
	return "refused"
}

func daSchema(t *testing.T, r ContextFabricInvestigationResult, schema string) string {
	t.Helper()
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return daVerdict(contractcheck.ValidateSerialized("", schema, encoded))
}

func TestDeterministicAnswerOraclesThroughValidate(t *testing.T) {
	t.Parallel()
	degraded := ContextFabricInvestigationDegraded
	both := func(t *testing.T, r ContextFabricInvestigationResult) (string, string) {
		t.Helper()
		return daVerdict(r.Validate()), daSchema(t, r, "context_fabric_investigation_result.v1.schema.json")
	}
	cases := []struct {
		name string
		r    ContextFabricInvestigationResult
		want string
	}{
		{"1 degraded + supported keeps its answer (control)", daFixture(degraded, 1, 1, daAnswer), "accepted"},
		{"3 degraded + unsupported + no prose, answer EMPTY is accepted", daFixture(degraded, 0, 0, ""), "accepted"},
		{"4a clarification_required that read a fact, answer empty is refused", daFixture(ContextFabricInvestigationClarificationRequired, 1, 1, ""), "refused"},
		{"4b clarification_required that read a fact keeps answer and prompt", daFixture(ContextFabricInvestigationClarificationRequired, 1, 1, daAnswer), "accepted"},
		{"5 complete without an answer is refused (control)", daFixture(ContextFabricInvestigationComplete, 0, 0, ""), "refused"},
		{"7 disclosure survives an empty answer", func() ContextFabricInvestigationResult {
			r := daFixture(degraded, 0, 0, "")
			r.Limitations = []string{"The readiness source was unavailable for this window."}
			r.Warnings = []string{"Coverage was partial."}
			r.Coverage.DegradedReasons = []string{"readiness source unavailable"}
			r.RefusalBasis = ContextFabricRefusalBasisUnspecified
			r.Completeness.RefusalBasis = r.RefusalBasis
			daRestamp(&r)
			return r
		}(), "accepted"},
		{"8 a fact with NO citable evidence does not require an answer", daFixture(degraded, 1, 0, ""), "accepted"},
		{"8 control: the same fact WITH evidence requires it", daFixture(degraded, 1, 1, ""), "refused"},
	}
	for _, c := range cases {
		goVerdict, schemaVerdict := both(t, c.r)
		t.Logf("ORACLE %-70s go=%-8s v1=%-8s want=%s", c.name, goVerdict, schemaVerdict, c.want)
		if goVerdict != c.want || schemaVerdict != c.want {
			t.Errorf("%s: go=%s v1 schema=%s, want %s", c.name, goVerdict, schemaVerdict, c.want)
		}
	}
}

// TestThePublishedUnsupportedExampleIsTheWideningsOwnForm pins the golden
// example consumers sync from: it decodes strictly, validates on the write
// path, is NOT supported, carries an EMPTY answer sentence, and still
// discloses why. A consumer that accepts this document accepts the form.
func TestThePublishedUnsupportedExampleIsTheWideningsOwnForm(t *testing.T) {
	t.Parallel()
	r := contextFabricGoldenResult(t, "context_fabric_investigation_result_unsupported.v1.json")
	if r.DeterministicAnswer != "" {
		t.Fatalf("deterministic_answer = %q, want empty: the example exists to publish the empty form", r.DeterministicAnswer)
	}
	if r.Completeness.ClaimedFactsCount != 0 || len(r.EvidenceRefIDs) != 0 {
		t.Fatalf("claimed_facts_count=%d evidence_ref_ids=%d, want 0/0", r.Completeness.ClaimedFactsCount, len(r.EvidenceRefIDs))
	}
	if len(r.Limitations) == 0 || len(r.Coverage.DegradedReasons) == 0 {
		t.Fatal("the unsupported example carries no disclosure; a content-free form must still say why")
	}
	// CONTROL: the same document claiming one fact with evidence must be
	// refused, or the example would prove nothing about the predicate.
	supported := r
	text := "open"
	supported.ClaimedFacts = []ContextFabricClaimedFact{{ClaimID: "claim_status_0000", Kind: ContextFabricFactStatus,
		Subject: r.SubjectResolution.Committed[0], Field: "state", Value: ContextFabricScalarValue{String: &text}}}
	supported.Completeness.ClaimedFactsCount = 1
	supported.EvidenceRefIDs = []string{"evidence_00000000"}
	if err := supported.Validate(); err == nil {
		t.Fatal("the example made SUPPORTED still validated with an empty answer sentence")
	}
}
