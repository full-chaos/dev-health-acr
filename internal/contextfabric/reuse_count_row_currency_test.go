package contextfabric

// IS THE REUSED COUNT ROW EVER STALE? THE PREMISE, MEASURED RATHER THAN READ.
//
// The reported defect is this: the reuse degrade drops cohort members and
// sets `Complete=false, Truncated=true`, while `hasCountOutcome` sees the
// STORED document's assembled-result `count` row and returns early -- so the
// backfill never re-states the cardinality and the served answer claims an
// exact census over a population it no longer carries.
//
// The mechanism is real; the FIRST STEP OF IT IS NOT REACHABLE. These tests
// establish that by execution rather than by reading, because an unreachable
// premise and a live defect look identical in a source trace, and a fix for
// dead code is a fix nothing can red.
//
// THE STRUCTURAL REASON, in one sentence: the degrade only ever assigns
// `member.EvidenceRefIDs`, evidence references are OPTIONAL on a cohort
// member under both the write and the legacy bounds, and `keepRefs` returns a
// non-nil empty slice -- so `strippingBrokeIt` is false for every member on
// every input, and the member-drop branch beside it cannot run.
//
// WHAT AN EARLIER VERSION OF THIS FILE GOT WRONG, and why the shape below is
// what it is. Review round 1 found two real gaps, both executed as surviving
// mutants before this was written:
//
//	THE CONTROL PROVED THE WRONG ARM. A `Finding` dropping proves the STRIP
//	ran; it says nothing about whether the COHORT loop ran. Disabling the
//	entire cohort block left every assertion green. So the cohort arm now
//	proves itself: a member's reference is asserted PRESENT before the
//	degrade and ABSENT after, which no other arm can satisfy.
//
//	THE SERVING PATH WAS NEVER DRIVEN. Both tests called `degradeReusedResult`
//	directly, so a reduction applied in `tryReuse` AFTER the degrade -- the
//	real shape of the reported defect -- was invisible. One test now drives
//	`Engine.Investigate` and reads the SERVED document.
//
//	THE FIXTURE COULD NOT SEE A RANKED MEMBER, and asserted only counts, so a
//	mutant that dropped ranked members, and one that corrupted a served
//	member's identity, both survived. The fixture now carries a ranked member
//	and every member's identity is asserted field by field.
//
// EVERY ASSERTION HERE CARRIES A POSITIVE CONTROL IN THE SAME RUN. A test
// that only shows a counter staying at zero cannot tell "the branch is
// unreachable" apart from "the fixture never reached the code at all". The
// control is a `Finding`, whose evidence references are REQUIRED
// (allowEmpty=false) and which therefore does break when stripped -- same
// degrade, same run, same missing set.

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The references the probes remove. DISTINCT per carrier so a per-carrier
// over-count cannot hide, and all legal ref ids (8-256 chars, trimmed, no '|').
const (
	countCurrencyMemberRef  = "evidence_member_currency_a"
	countCurrencyRankedRef  = "evidence_member_currency_ranked_b"
	countCurrencyFindingRef = "evidence_finding_currency_a"
)

// currencyUnrankedMember is the ordinary member shape: no ranking fields.
func currencyUnrankedMember(refs ...string) CohortMember {
	return CohortMember{
		Subject:          SubjectRef{Kind: SubjectTeam, CanonicalID: "team:CURRENCY_A", Label: "Currency A"},
		Rank:             1,
		InclusionReasons: []string{"matched"},
		EvidenceRefIDs:   append([]string(nil), refs...),
	}
}

// currencyRankedMember is the shape round 1 found missing: `RankingComputed`
// true, which a mutant can key on to drop a member the unranked fixture could
// never expose. Score is nil, so the outcome is `insufficient_evidence` and
// the ranking basis and drivers stay empty -- the one legal ranked shape that
// needs no scored driver set.
func currencyRankedMember(refs ...string) CohortMember {
	return CohortMember{
		Subject:          SubjectRef{Kind: SubjectTeam, CanonicalID: "team:CURRENCY_B", Label: "Currency B"},
		Rank:             2,
		InclusionReasons: []string{"matched"},
		EvidenceRefIDs:   append([]string(nil), refs...),
		RankingComputed:  true,
		AttentionRank:    1,
		DataCompleteness: contractsv1.ContextFabricCohortDataPartial,
		Outcome:          contractsv1.ContextFabricCohortOutcomeInsufficientEvidence,
		MissingSignals:   []string{"investment_mix"},
	}
}

// currencyCohort is the two-member population both degrade probes use: one
// unranked, one ranked, each carrying its own distinct reference.
func currencyCohort() *Cohort {
	return &Cohort{
		Kind: SubjectTeam, Rationale: "reuse currency probe", Complete: true,
		Members: []CohortMember{
			currencyUnrankedMember(countCurrencyMemberRef),
			currencyRankedMember(countCurrencyRankedRef),
		},
	}
}

// currencyReuseEngine builds an engine whose recheck can actually SERVE a
// cohort-bearing stored answer.
//
// IT EXISTS BECAUSE THE SHARED `reuseDegradeEngine` CANNOT, and the reason is
// worth stating: `reuseSubjectsToRecheck` (answer_reuse.go) collects every
// COHORT MEMBER's subject into the recheck set, and the recheck REFUSES
// (`AnswerReuseMissAuthorization`) unless every one of them comes back in
// `resolution.Committed`. The shared helper commits one subject, so a
// cohort-bearing candidate misses reuse entirely and the engine falls through
// to a fresh investigation -- which is precisely what the first version of
// this test did, and it failed loudly on the interpreter stub rather than
// passing while proving nothing.
//
// The shared helper is left ALONE rather than widened: other tests depend on
// its exact committed set, and widening a shared fixture to fit one test is
// how a fixture stops meaning what its other callers assume.
func currencyReuseEngine(t *testing.T, stored InvestigationResult, telemetry *recordingTelemetry) *Engine {
	t.Helper()
	committed := []SubjectRef{reuseDegradeSubject()}
	if stored.Cohort != nil {
		for _, member := range stored.Cohort.Members {
			committed = append(committed, member.Subject)
		}
	}
	return mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: committed},
			// The stored answer's own citation stays visible; the candidate's
			// node ref and both member refs do not.
			context: productionShapedGraphContext([]string{reuseCitationRef}, nil),
		},
		Results:   &resultStoreStub{},
		Telemetry: telemetry,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return stored, true, nil
		}),
	})
}

// currencyCountRow is the stored answer's own count row: satisfied over the
// two members it carries. The strongest starting claim, and the one the
// reported defect says a member drop would leave standing.
func currencyCountRow(served int) RequirementOutcomeRow {
	return RequirementOutcomeRow{
		Stage:       contractsv1.ContextFabricOutcomeStageAssembledResult,
		Requirement: string(ObligationCount) + "/" + string(SubjectRoleMember) + "/" + string(SubjectTeam),
		Obligation:  string(ObligationCount),
		Outcome:     contractsv1.ContextFabricRequirementSatisfied,
		Impact:      contractsv1.ContextFabricAnswerImpactNone,
		Served:      served, Declared: served,
	}
}

// assertMemberIdentityPreserved checks every field of a served member EXCEPT
// its evidence references, which the degrade is allowed to change.
//
// Round 1: asserting only counts let a mutant corrupt a served member's
// identity metadata while keeping the population the same size.
func assertMemberIdentityPreserved(t *testing.T, where string, want, got CohortMember) {
	t.Helper()
	if got.Subject.Kind != want.Subject.Kind ||
		got.Subject.CanonicalID != want.Subject.CanonicalID ||
		got.Subject.Label != want.Subject.Label {
		t.Errorf("%s: served member subject = %+v, want %+v -- the degrade may remove a member's "+
			"evidence references and nothing else", where, got.Subject, want.Subject)
	}
	if got.Rank != want.Rank {
		t.Errorf("%s: served member rank = %d, want %d", where, got.Rank, want.Rank)
	}
	if len(got.InclusionReasons) != len(want.InclusionReasons) {
		t.Errorf("%s: served member inclusion reasons = %v, want %v", where, got.InclusionReasons, want.InclusionReasons)
	} else {
		for i := range want.InclusionReasons {
			if got.InclusionReasons[i] != want.InclusionReasons[i] {
				t.Errorf("%s: served member inclusion reason %d = %q, want %q",
					where, i, got.InclusionReasons[i], want.InclusionReasons[i])
			}
		}
	}
	if got.RankingComputed != want.RankingComputed ||
		got.AttentionRank != want.AttentionRank ||
		got.DataCompleteness != want.DataCompleteness ||
		got.Outcome != want.Outcome {
		t.Errorf("%s: served member ranking fields = {computed:%v rank:%d completeness:%q outcome:%q}, "+
			"want {computed:%v rank:%d completeness:%q outcome:%q}",
			where, got.RankingComputed, got.AttentionRank, got.DataCompleteness, got.Outcome,
			want.RankingComputed, want.AttentionRank, want.DataCompleteness, want.Outcome)
	}
}

// TestStrippingEveryEvidenceRefLeavesACohortMemberValid isolates the bound
// that makes the member-drop branch unreachable.
//
// It drives the CONTRACT's own validators, never a local restatement of the
// rule, and it takes the stripped value from `keepRefs` ITSELF rather than
// hand-building an empty slice -- round 1's point that a hand-built value
// proves nothing about what the production helper actually returns.
//
// Both bound sets are exercised: `Validate()` is the WRITE path, and
// `ValidateStoredResult` reaches the LEGACY path the reuse degrade actually
// re-validates against.
func TestStrippingEveryEvidenceRefLeavesACohortMemberValid(t *testing.T) {
	t.Parallel()

	missing := map[string]struct{}{
		countCurrencyMemberRef: {},
		countCurrencyRankedRef: {},
	}

	for _, member := range []CohortMember{
		currencyUnrankedMember(countCurrencyMemberRef),
		currencyRankedMember(countCurrencyRankedRef),
	} {
		name := member.Subject.CanonicalID
		// NON-VACUITY: valid BEFORE the strip, or `strippingBrokeIt` would be
		// false for a reason this probe is not about.
		if err := member.Validate(); err != nil {
			t.Fatalf("%s: fixture member does not validate before stripping: %v -- this probe measures a "+
				"valid-then-invalid transition and there is no valid state to start from", name, err)
		}

		// THE PRODUCTION HELPER'S OWN RETURN VALUE, not a hand-built empty slice.
		kept, removed := keepRefs(member.EvidenceRefIDs, missing)
		if len(removed) != 1 {
			t.Fatalf("%s: keepRefs removed %d refs, want 1 -- the fixture is not exercising the strip", name, len(removed))
		}
		if kept == nil {
			t.Fatalf("%s: keepRefs returned a NIL slice; the unreachability argument rests on it returning "+
				"a non-nil empty slice, so this is the argument's second step failing", name)
		}
		if len(kept) != 0 {
			t.Fatalf("%s: keepRefs kept %d refs, want 0", name, len(kept))
		}

		stripped := member
		stripped.EvidenceRefIDs = kept
		if err := stripped.Validate(); err != nil {
			t.Fatalf("%s: a cohort member stripped of every evidence reference no longer validates under the "+
				"WRITE bounds: %v -- if this is now true, members ARE droppable on the reuse degrade and the "+
				"count row goes stale silently", name, err)
		}

		// And nil, the other shape a stored row can legitimately carry.
		nilled := member
		nilled.EvidenceRefIDs = nil
		if err := nilled.Validate(); err != nil {
			t.Fatalf("%s: a cohort member with nil evidence refs no longer validates: %v", name, err)
		}
	}

	// THE LEGACY BOUNDS, reached through the validator the degrade itself
	// calls. Round 1: the write-path `Validate()` alone does not establish the
	// "under BOTH bound sets" half of the claim.
	stored := storedResultWithCandidateEvidence()
	stored.Cohort = &Cohort{
		Kind: SubjectTeam, Rationale: "legacy bound probe", Complete: true,
		// Both carry NO evidence references at all -- the shape the legacy
		// bounds must accept for the "under both bound sets" claim to hold.
		Members: []CohortMember{
			currencyUnrankedMember(),
			currencyRankedMember(),
		},
	}
	stored.Completeness = ComputeAnswerCompleteness(stored)
	if err := ValidateStoredResult(stored); err != nil {
		t.Fatalf("a stored result whose cohort members carry NO evidence references is rejected under the "+
			"LEGACY bounds: %v -- the unreachability argument claims refs are optional under both bound "+
			"sets, and this is the legacy half", err)
	}

	// POSITIVE CONTROL: a Finding's evidence references are REQUIRED, so the
	// identical strip DOES break it. Without this the assertions above are
	// indistinguishable from a validator that accepts everything.
	finding := Finding{
		FindingID:      "finding_currency_01",
		Kind:           string(contractsv1.ContextFabricDriverCategoryRelationship),
		Summary:        "Acceptance work remains open.",
		Subjects:       []SubjectRef{{Kind: SubjectTeam, CanonicalID: "team:CURRENCY_A", Label: "Currency A"}},
		EvidenceRefIDs: []string{countCurrencyFindingRef},
	}
	if err := finding.Validate(); err != nil {
		t.Fatalf("the control finding does not validate before stripping: %v", err)
	}
	strippedFinding := finding
	strippedFinding.EvidenceRefIDs = make([]string, 0, 1)
	if strippedFinding.Validate() == nil {
		t.Fatal("the CONTROL did not fire: a finding stripped of every evidence reference still validates, " +
			"so this file cannot tell an unreachable branch apart from a validator that accepts anything")
	}
}

// TestTheReuseDegradeNeverDropsACohortMember drives the whole degrade and
// reads the counter the reported defect depends on.
//
// Three things make the zero meaningful, and round 1 showed that the first
// alone is not enough:
//
//	the FINDING control fires   -- the missing set reached the strip;
//	the COHORT ARM proves itself -- a member's reference is present before and
//	                                absent after, which no other arm can do;
//	member IDENTITY is asserted  -- so a mutant cannot corrupt a served member
//	                                while keeping the population the same size.
func TestTheReuseDegradeNeverDropsACohortMember(t *testing.T) {
	t.Parallel()

	stored := storedResultWithCandidateEvidence()
	stored.Cohort = currencyCohort()
	stored.RemainingWork = []Finding{{
		FindingID: "finding_currency_01",
		Kind:      string(contractsv1.ContextFabricDriverCategoryRelationship),
		Summary:   "Acceptance work remains open.",
		Subjects:  []SubjectRef{{Kind: SubjectTeam, CanonicalID: "team:CURRENCY_A", Label: "Currency A"}},
		// The CONTROL carrier: required refs, so this one breaks.
		EvidenceRefIDs: []string{countCurrencyFindingRef},
	}}
	stored.Completeness.Outcomes = append(stored.Completeness.Outcomes, currencyCountRow(2))
	stored.Completeness = ComputeAnswerCompleteness(stored)

	wantMembers := append([]CohortMember(nil), stored.Cohort.Members...)

	// PRE-STATE for the cohort-arm control: both member refs are present now.
	for _, member := range stored.Cohort.Members {
		if len(member.EvidenceRefIDs) != 1 {
			t.Fatalf("fixture member %s carries %d refs, want 1 -- the cohort-arm control needs a reference "+
				"to watch disappear", member.Subject.CanonicalID, len(member.EvidenceRefIDs))
		}
	}

	missing := map[string]struct{}{
		countCurrencyMemberRef:  {},
		countCurrencyRankedRef:  {},
		countCurrencyFindingRef: {},
	}
	degraded, counts, _, ok := degradeReusedResult(stored, missing)
	if !ok {
		t.Fatal("degradeReusedResult() refused; this fixture is meant to degrade")
	}

	// CONTROL 1, read FIRST: the missing set really did reach the strip and
	// really did drop an object.
	if counts.DroppedFindings == 0 {
		t.Fatalf("the CONTROL did not fire: the degrade dropped no finding even though its only evidence "+
			"reference was in the missing set. counts = %+v", counts)
	}
	if counts.Refs() == 0 {
		t.Fatalf("the degrade removed no references at all; counts = %+v", counts)
	}
	if degraded.Cohort == nil {
		t.Fatal("the degraded answer carries no cohort")
	}

	// CONTROL 2 -- THE COHORT ARM PROVES ITSELF. Round 1: the finding control
	// above is satisfied with the entire cohort block disabled, so it cannot
	// stand for the cohort loop having run. This can: both members' references
	// were present before and must be gone now, and only the cohort arm
	// removes them.
	for _, member := range degraded.Cohort.Members {
		for _, ref := range member.EvidenceRefIDs {
			if _, gone := missing[ref]; gone {
				t.Fatalf("the COHORT ARM did not run: served member %s still carries %q, a reference the "+
					"recheck could not prove visible -- so a zero member drop below says nothing about "+
					"the cohort loop", member.Subject.CanonicalID, ref)
			}
		}
	}

	// THE PREMISE OF THE REPORTED DEFECT, measured.
	if counts.DroppedMembers != 0 {
		t.Fatalf("the reuse degrade dropped %d cohort member(s). The reported stale-count defect becomes "+
			"REACHABLE the moment this is non-zero: `hasCountOutcome` would then return early over a stored "+
			"row describing a population the served answer no longer carries", counts.DroppedMembers)
	}
	if got, want := len(degraded.Cohort.Members), len(wantMembers); got != want {
		t.Fatalf("the degraded cohort carries %d members, want %d -- the population did not change", got, want)
	}
	for i := range wantMembers {
		assertMemberIdentityPreserved(t, "degrade", wantMembers[i], degraded.Cohort.Members[i])
	}

	// The cohort's coverage flags are untouched, which is the OTHER half of
	// the same fact: the branch that flips them is the member-drop branch.
	if !degraded.Cohort.Complete || degraded.Cohort.Truncated {
		t.Fatalf("the degraded cohort reports complete=%v truncated=%v; both move only inside the "+
			"member-drop branch", degraded.Cohort.Complete, degraded.Cohort.Truncated)
	}

	// And therefore the served count still describes the members served.
	rows := countOutcomeRows(degraded, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(rows) != 1 {
		t.Fatalf("assembled-result `count` rows after degrade = %d, want exactly 1", len(rows))
	}
	if got, want := rows[0].Served, len(degraded.Cohort.Members); got != want {
		t.Fatalf("the served count says %d and the served answer carries %d members", got, want)
	}
}

// TestAServedReusedAnswersCountDescribesTheMembersItServes drives the PUBLIC
// entry point and reads the SERVED document.
//
// Round 1's sharpest finding: both tests above stop at `degradeReusedResult`,
// while the real serving path continues through `tryReuse` and the engine's
// reuse arm. A reduction applied anywhere after the degrade -- which is the
// exact shape of the reported defect -- is invisible to them. This test cannot
// be satisfied by a document the caller never receives.
func TestAServedReusedAnswersCountDescribesTheMembersItServes(t *testing.T) {
	t.Parallel()

	stored := storedResultWithCandidateEvidence()
	stored.Cohort = currencyCohort()
	stored.Completeness.Outcomes = append(stored.Completeness.Outcomes, currencyCountRow(2))
	stored.Completeness = ComputeAnswerCompleteness(stored)
	wantMembers := append([]CohortMember(nil), stored.Cohort.Members...)

	// A missing top-level citation would REFUSE, which is a different test --
	// the refs this drops are all auxiliary, so this degrades.
	telemetry := &recordingTelemetry{}
	engine := currencyReuseEngine(t, stored, telemetry)

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	// REACH GUARD, before any assertion about the served document. A reuse
	// MISS falls through to a fresh investigation, and a fresh result would
	// satisfy several assertions below for reasons that have nothing to do
	// with reuse. This test is only meaningful on the reuse path.
	if !result.Reused {
		t.Fatal("result.Reused = false: the engine did not serve from the store, so nothing below is " +
			"about the reuse path -- check that every cohort member's subject is in the recheck's " +
			"committed set (reuseSubjectsToRecheck refuses on any that is not)")
	}
	if got := lastReuseOutcome(t, telemetry); got != AnswerReuseHitDegraded {
		t.Fatalf("reuse outcome = %q, want %q -- this test needs the DEGRADED arm", got, AnswerReuseHitDegraded)
	}
	if result.Cohort == nil {
		t.Fatal("the served answer carries no cohort")
	}

	// THE HARM ASSERTION: the served count row describes the served member set.
	// A reduction applied after the count row was written -- in the degrade, in
	// tryReuse, or anywhere else on this path -- fails here.
	rows := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(rows) != 1 {
		t.Fatalf("served assembled-result `count` rows = %d, want exactly 1", len(rows))
	}
	if got, want := rows[0].Served, len(result.Cohort.Members); got != want {
		t.Fatalf("the SERVED answer states a count of %d over a cohort of %d members: the caller is told a "+
			"cardinality for a population they did not receive", got, want)
	}

	// The cohort arm ran on this path too -- no member may serve a reference
	// the recheck could not prove.
	for _, member := range result.Cohort.Members {
		if containsRef(member.EvidenceRefIDs, countCurrencyMemberRef) ||
			containsRef(member.EvidenceRefIDs, countCurrencyRankedRef) {
			t.Fatalf("served member %s still carries a reference the recheck could not prove visible; "+
				"the cohort arm did not run on the serving path", member.Subject.CanonicalID)
		}
	}
	if got, want := len(result.Cohort.Members), len(wantMembers); got != want {
		t.Fatalf("the SERVED cohort carries %d members, want %d", got, want)
	}
	for i := range wantMembers {
		assertMemberIdentityPreserved(t, "served", wantMembers[i], result.Cohort.Members[i])
	}
}
