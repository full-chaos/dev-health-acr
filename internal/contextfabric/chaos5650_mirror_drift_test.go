package contextfabric

import (
	"errors"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// boundOf reports the violated_bound ClassifySynthesisRejection attaches to
// a rejection -- the production seam the route reads, so these tests
// exercise the mirror the way it is actually consulted rather than calling
// the unexported traversal directly.
func boundOf(t *testing.T, draft SynthesisDraft, input SynthesisInput) (string, bool) {
	t.Helper()
	err := draft.ValidateAgainst(input)
	if err == nil {
		t.Fatal("ValidateAgainst() = nil, want a rejection")
	}
	var violation *ModelBoundViolation
	if errors.As(ClassifySynthesisRejection(draft, input, err), &violation) {
		return violation.Bound, true
	}
	return "", false
}

// TestMirrorStopsAtTheTimeSeriesRowsAuthorshipStatement is the regression
// for an UNSOUND divergence between ValidateAgainst and its bound-diagnosis
// mirror.
//
// ValidateAgainst rejects a claim that authors time_series_rows itself
// (the additive second table, the same authorship rule as Rows one
// field over). The mirror enumerated the Rows statement and not the
// TimeSeriesRows one, so a draft whose ONLY fault was model-authored
// time_series_rows passed every clause the mirror checked: the traversal
// ran on past the rejecting claim and could name a bound belonging to a
// LATER entry the validator never evaluated.
//
// That is a WRONG name, not a missing one -- it tells an operator to fix a
// field that was not the reason their answer was refused -- and it is
// exactly the failure the mirror's clause-by-clause construction exists to
// make impossible. The doctrine is explicit: an absent bound name is
// acceptable, a wrong one never is.
//
// The overlong driver title below is the bait: it is a genuine registered
// bound, sitting AFTER the rejecting claim in ValidateAgainst's own order,
// so a mirror that fails to stop at the claim reports it.
func TestMirrorStopsAtTheTimeSeriesRowsAuthorshipStatement(t *testing.T) {
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	project := input.Graph.Resolution.Committed[0]

	draft.ClaimedFacts = []ClaimedFact{{
		ClaimID: "claim_12345678", Kind: FactReadiness, Subject: project,
		Field: "release_ready", Value: boolScalar(false),
		TimeSeriesRows: []ClaimedFactRow{{Fields: map[string]ScalarValue{"week": boolScalar(true)}}},
	}}
	draft.Drivers[0].Title = overlongText(contractsv1.ContextFabricDriverTitleMaxLength + 1)

	// The bait must really BE a nameable bound, or this test passes for a
	// broken mirror too: prove it names one on its own, with the rejecting
	// claim removed.
	bait := draft
	bait.ClaimedFacts = nil
	if bound, ok := boundOf(t, bait, input); !ok || bound != "synthesis.driver.title.max_length" {
		t.Fatalf("the bait alone reports (%q, %v), want (%q, true) -- it is not a nameable bound, so this test could not detect the drift", bound, ok, "synthesis.driver.title.max_length")
	}

	if got := SynthesisRejectionReasonOf(draft.ValidateAgainst(input)); got != RejectionReasonClaimTimeSeriesRowsModelAuthored {
		t.Fatalf("rejection reason = %q, want %q -- this fixture no longer rejects where the test assumes", got, RejectionReasonClaimTimeSeriesRowsModelAuthored)
	}
	if bound, ok := boundOf(t, draft, input); ok {
		t.Fatalf("violated_bound = %q, want no bound: the rejecting statement names none, and every later statement belongs to an entry ValidateAgainst never evaluated", bound)
	}
}

// TestMirrorAdmitsDriverCandidateEvidenceLikeTheValidator is the regression
// for the SOUND-but-incomplete half of the same drift.
//
// ValidateAgainst admits the engine's own driver-candidate evidence refs
// (the payload shows the model those candidates, and
// answer_reuse_degrade serves them verbatim, so refusing the model for
// citing them refused ACR's own published evidence). The mirror's allowed
// set was never widened to match, making it a strict SUBSET of the
// validator's -- so a draft citing such a ref passed ValidateAgainst and
// made the mirror bail at the top-level evidence loop, reporting NO bound
// for whatever clause actually rejected later.
//
// A subset can only under-report, never misname, which is why this was
// silent rather than wrong. It is still a divergence, and the whole premise
// of a literal mirror is that neither kind exists.
func TestMirrorAdmitsDriverCandidateEvidenceLikeTheValidator(t *testing.T) {
	const candidateEvidence = "evidence_driver_candidate_1"

	input := validSynthesisInputFixture()
	project := input.Graph.Resolution.Committed[0]
	input.Graph.DriverCandidates = []DriverJudgment{{
		DriverID: "driver_87654321", Standing: DriverPrincipal, Category: "relationship",
		Title: "Candidate", Summary: "Candidate summary", AffectedSubjects: []SubjectRef{project},
		EvidenceRefIDs: []string{candidateEvidence},
		Derivation:     DerivationRuleInferred, EpistemicStatus: EpistemicInferred, Confidence: 0.5,
	}}

	// A control first: the ref really is admitted by the validator, and
	// admitted ONLY because of the driver candidate above. Without this the
	// assertion below could pass for the wrong reason.
	admitted := validSynthesisDraftFixture(input)
	admitted.EvidenceRefIDs = append(admitted.EvidenceRefIDs, candidateEvidence)
	if err := admitted.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() = %v, want the driver-candidate evidence admitted", err)
	}
	withoutCandidates := input
	withoutCandidates.Graph.DriverCandidates = nil
	if err := admitted.ValidateAgainst(withoutCandidates); err == nil {
		t.Fatal("ValidateAgainst() = nil with no driver candidates, want the ref refused -- the ref is reachable from another source, so this fixture proves nothing")
	}

	// Now the real assertion: a nameable bound AFTER the admitted top-level
	// ref must still be named. A mirror that bails on the ref names nothing.
	draft := admitted
	draft.Drivers = append([]DriverJudgment{}, draft.Drivers...)
	draft.Drivers[0].Title = overlongText(contractsv1.ContextFabricDriverTitleMaxLength + 1)

	bound, ok := boundOf(t, draft, input)
	if !ok || bound != "synthesis.driver.title.max_length" {
		t.Fatalf("violated_bound = (%q, %v), want (%q, true)", bound, ok, "synthesis.driver.title.max_length")
	}
}
