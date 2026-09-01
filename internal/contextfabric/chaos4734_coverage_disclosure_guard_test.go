package contextfabric

import "testing"

// CHAOS-4734 (independent review finding R4, fable51-independent-review-
// intent-plan-2026-09-01.md): applyCoverageDisclosures rejected a
// disclosure's detail_id only when it was absent from result.Coverage.Details
// -- the CANONICAL set. Detail ids are minted sequentially (cov-01, cov-02,
// ...), so once synthesis is shown only a MATERIAL SUBSET of details (plan
// T7), a model could emit an id it never saw, by pattern, and the guard
// would accept it because the id happens to exist canonically -- writing the
// model's phrasing onto a detail it never read. E2's ruled "unknown
// detail_id = hallucinated reference => discard the whole set" was blind to
// exactly this case.
//
// FIX: the guard's universe is the MODEL-FACING id set, passed explicitly
// into applyCoverageDisclosures/classifyCoverageDisclosures, never derived
// implicitly from result.Coverage.Details. Today (no narrower projection
// exists yet) the model-facing set equals the canonical set, so behaviour is
// a byte-identical no-op -- TestCoverageDisclosureGuard_ModelFacingSetEqualsCanonicalSetToday
// pins that equivalence. TestCoverageDisclosureGuard_CanonicalButUnshownIDIsRejected
// is this ticket's own repro, tightened into a permanent test, for the case
// that becomes reachable the day a narrower projection ships.

// TestCoverageDisclosureGuard_CanonicalButUnshownIDIsRejected is the
// ticket's §repro: a detail_id that is REAL and canonical, but was not in
// the set handed to synthesis, must be rejected as a hallucinated
// reference -- the whole-set-discard E2 rule -- not silently accepted
// because it happens to resolve against the canonical set.
func TestCoverageDisclosureGuard_CanonicalButUnshownIDIsRejected(t *testing.T) {
	t.Parallel()
	result := &InvestigationResult{}
	result.Coverage.Details = []CoverageDetail{
		{DetailID: "cov-01", Source: "canonical_fact:health", Label: "health unavailable"},
		{DetailID: "cov-02", Source: "canonical_fact:readiness", Label: "readiness stale"}, // NOT shown to the model
		{DetailID: "cov-03", Source: "canonical_fact:flow", Label: "flow unavailable"},
	}
	modelFacing := map[string]struct{}{"cov-01": {}, "cov-03": {}} // what a T7-style projection would hand synthesis

	disclosures := []CoverageDisclosure{
		{DetailID: "cov-01", Text: "Health evidence was unavailable for this subject."},
		{DetailID: "cov-02", Text: "Readiness evidence was stale for this subject."}, // hallucinated reference
	}
	if _, shown := modelFacing[disclosures[1].DetailID]; shown {
		t.Fatal("fixture error: cov-02 must be outside the model-facing set")
	}

	outcome, violation := applyCoverageDisclosures(result, disclosures, modelFacing)
	t.Logf("outcome=%q violation=%q phrasing[cov-02]=%q", outcome, violation, result.Coverage.Details[1].Phrasing)

	if outcome != CoverageDisclosureRejectedByGuard || violation != CoverageDisclosureViolationUnknownToModelFacingSet {
		t.Fatalf("DEFECT: guard accepted a detail_id the model never saw: outcome=%q violation=%q; cov-02 phrasing=%q",
			outcome, violation, result.Coverage.Details[1].Phrasing)
	}
	// Whole-set arm: cov-01's otherwise-valid disclosure must NOT survive
	// beside the rejected one -- E2's ruled split, no per-item fallback.
	for _, d := range result.Coverage.Details {
		if d.Phrasing != "" {
			t.Fatalf("detail %q Phrasing = %q, want empty -- the whole set must be discarded, not just cov-02", d.DetailID, d.Phrasing)
		}
	}
}

// TestCoverageDisclosureGuard_UnknownEverywhereIsStillDistinguished pins the
// OTHER half of the split: an id that is not even canonical (never existed
// at all) is a DIFFERENT violation than "canonical but unshown" -- both
// discard the whole set, but an operator needs to tell the two apart
// (ORDERS: "emits the guard outcome class unknown_to_input vs
// unknown_to_canonical").
func TestCoverageDisclosureGuard_UnknownEverywhereIsStillDistinguished(t *testing.T) {
	t.Parallel()
	result := &InvestigationResult{}
	result.Coverage.Details = []CoverageDetail{
		{DetailID: "cov-01", Source: "canonical_fact:health", Label: "health unavailable"},
	}
	modelFacing := map[string]struct{}{"cov-01": {}}
	disclosures := []CoverageDisclosure{{DetailID: "cov-99", Text: "This detail never existed."}}

	outcome, violation := applyCoverageDisclosures(result, disclosures, modelFacing)
	if outcome != CoverageDisclosureRejectedByGuard || violation != CoverageDisclosureViolationUnknownDetailID {
		t.Fatalf("applyCoverageDisclosures() = (%q, %q), want (rejected_by_guard, unknown_detail_id) -- distinct from unknown_to_model_facing_set", outcome, violation)
	}
}

// TestCoverageDisclosureGuard_ModelFacingSetEqualsCanonicalSetToday is
// CHAOS-4734 acceptance criterion 2's explicit no-op assertion: until a
// narrower model-facing projection ships, coverageDetailIDSet(the same
// result.Coverage.Details the caller already merges) IS the model-facing
// set, so this fix changes nothing observable today. A model that names any
// canonical detail_id -- shown or not, because there is no "not" yet -- is
// still accepted exactly as before.
func TestCoverageDisclosureGuard_ModelFacingSetEqualsCanonicalSetToday(t *testing.T) {
	t.Parallel()
	result := twoDetailResult()
	modelFacing := coverageDetailIDSet(result.Coverage.Details)
	if len(modelFacing) != len(result.Coverage.Details) {
		t.Fatalf("coverageDetailIDSet() = %d ids, want %d -- today's model-facing set must equal the canonical set", len(modelFacing), len(result.Coverage.Details))
	}
	disclosures := []CoverageDisclosure{
		{DetailID: "cov-01", Text: "Readiness data could not be reached this time."},
		{DetailID: "cov-02", Text: "Status data was not part of this check."},
	}
	outcome, violation := applyCoverageDisclosures(result, disclosures, modelFacing)
	if outcome != CoverageDisclosurePhrased || violation != "" {
		t.Fatalf("applyCoverageDisclosures() = (%q, %q), want (phrased, \"\") -- every canonical id is still accepted when the model-facing set equals the canonical set", outcome, violation)
	}
}

// TestCoverageDisclosureGuard_MutationProof pins the fix against the exact
// regression this ticket names: reverting the guard universe from
// modelFacingDetailIDs back to indexByDetailID (the canonical set) alone.
// With modelFacing narrower than canonical, the pre-fix guard would have
// resolved cov-02 successfully (it is in indexByDetailID) and accepted it;
// the fix rejects it specifically because it is absent from modelFacing.
// One clause, one mutation site (the `if _, shown := modelFacingDetailIDs[...]`
// check), one assertion.
func TestCoverageDisclosureGuard_MutationProof(t *testing.T) {
	t.Parallel()
	result := &InvestigationResult{}
	result.Coverage.Details = []CoverageDetail{
		{DetailID: "cov-01", Source: "canonical_fact:health", Label: "health unavailable"},
		{DetailID: "cov-02", Source: "canonical_fact:readiness", Label: "readiness stale"},
	}
	modelFacing := map[string]struct{}{"cov-01": {}} // cov-02 deliberately excluded
	disclosures := []CoverageDisclosure{{DetailID: "cov-02", Text: "Readiness evidence was stale for this subject."}}

	outcome, violation := applyCoverageDisclosures(result, disclosures, modelFacing)
	if outcome != CoverageDisclosureRejectedByGuard {
		t.Fatal("MUTATION-PROOF FAILURE: guard accepted a canonical-but-unshown id -- the modelFacingDetailIDs check was removed or bypassed")
	}
	if violation != CoverageDisclosureViolationUnknownToModelFacingSet {
		t.Fatalf("MUTATION-PROOF FAILURE: violation = %q, want unknown_to_model_facing_set", violation)
	}
	if result.Coverage.Details[1].Phrasing != "" {
		t.Fatal("MUTATION-PROOF FAILURE: phrasing was written onto a detail the model never saw")
	}
}
