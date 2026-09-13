package contextfabric

// THE COUNT'S THREE SURFACES, HELD TO ONE RULE EACH.
//
// The count reaches a reader three ways -- an outcome row, a claimed fact and
// a sentence in the answer -- and every defect this file pins was a case of
// those three disagreeing, or of one of them being reachable where the others
// were not. They are pinned together, in one file, because that is the shape
// of the class: a fix to any one surface that leaves another behind is the
// same defect again.

import (
	"context"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// cardinalityClaimOf returns the served cardinality claim, or nil.
func cardinalityClaimOf(result InvestigationResult) *ClaimedFact {
	for i := range result.ClaimedFacts {
		if result.ClaimedFacts[i].Kind == contractsv1.ContextFabricFactCardinality {
			return &result.ClaimedFacts[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// ONE PRECONDITION
// ---------------------------------------------------------------------------

// TestTheCountSurfacesShareOnePrecondition is the design rule, and the defect
// it pins is a document disagreeing with itself.
//
// The row was stated only when the question carried a count obligation, while
// the claim and the sentence were minted for every resolved cohort. A cohort
// answer that owed no count therefore served the number on two surfaces with
// no row to reconcile them against — and a reader had no way to tell whether
// the count was asked for or volunteered.
//
// BOTH DIRECTIONS, because either alone is satisfiable by a mistake. Asserting
// only the counting case passes for an implementation that always emits;
// asserting only the non-counting case passes for one that never does.
func TestTheCountSurfacesShareOnePrecondition(t *testing.T) {
	t.Parallel()

	for _, cell := range []struct {
		name      string
		frame     *QuestionFrame
		wantCount bool
		why       string
	}{
		{
			name:  "a counting question carries all three surfaces",
			frame: countingFrame(SubjectTeam), wantCount: true,
			why: "the question asked for a count, so the row, the claim and the sentence are all owed",
		},
		{
			name:  "a question with no count obligation carries none of them",
			frame: nonCountingFrame(SubjectTeam), wantCount: false,
			why: "a resolved member set is not on its own a reason to assert a count nobody asked for",
		},
	} {
		cell := cell
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			engine := newCountingEngineWithPopulation(t, countingCohort(SubjectTeam, 3), 3, cell.frame, &recordingTelemetry{})
			result := runCountingRequest(t, engine, 3)

			claim := cardinalityClaimOf(result)
			rows := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult)
			sentence := strings.Contains(result.DeterministicAnswer, "Counted ")

			if got := claim != nil; got != cell.wantCount {
				t.Errorf("cardinality claim present = %t, want %t -- %s", got, cell.wantCount, cell.why)
			}
			if got := len(rows) > 0; got != cell.wantCount {
				t.Errorf("count outcome row present = %t, want %t -- %s", got, cell.wantCount, cell.why)
			}
			if sentence != cell.wantCount {
				t.Errorf("count sentence present = %t, want %t -- %s (answer=%q)", sentence, cell.wantCount, cell.why, result.DeterministicAnswer)
			}
			// THE PROPERTY, stated as agreement rather than as three
			// independent expectations: whatever the precondition decided,
			// all three surfaces decided it the same way.
			if (claim != nil) != (len(rows) > 0) || (claim != nil) != sentence {
				t.Errorf("the three count surfaces disagree -- claim=%t row=%t sentence=%t; they share one precondition and must appear or be absent together",
					claim != nil, len(rows) > 0, sentence)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// THE RESERVED MINT NAMESPACE
// ---------------------------------------------------------------------------

// TestTheServerClaimNamespaceIsRefusedForModelAuthoredClaims pins the
// reservation at the boundary where model output is admitted.
//
// Without it a model emitting the server's own claim id produces two claims
// with one id, and whole-result validation rejects the assembled answer for
// duplicate ids -- taking the server's claim, and the answer, down with it.
// Refusing the draft instead means the collision cannot form.
//
// BY PREFIX, so a kind added later inherits the reservation. The arm below
// uses a kind that does not exist to prove the refusal is not a list of the
// ids that happen to be minted today.
func TestTheServerClaimNamespaceIsRefusedForModelAuthoredClaims(t *testing.T) {
	t.Parallel()

	for _, cell := range []struct {
		name    string
		claimID string
		refused bool
		why     string
	}{
		{"an id the server mints today", cardinalityClaimIDPrefix + "team", true,
			"this is exactly the id the server mints, so a model using it collides by construction"},
		{"a kind the server does not mint yet", cardinalityClaimIDPrefix + "not_a_kind_yet", true,
			"the reservation is the NAMESPACE, not the list of ids currently in use -- a kind added later must inherit it without anyone editing a list"},
		{"an ordinary model id", "claim_open_pull_requests", false,
			"the reservation must not swallow the ids models are supposed to author"},
	} {
		cell := cell
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			// THE REAL VALIDATOR, not a restatement of the prefix rule. An
			// assertion over the prefix literal would hold whether or not any
			// validator refuses anything -- it tests the standard library.
			// Driving ValidateAgainst is what makes a missing refusal fail.
			input, draft := closureFixture()
			draft.ClaimedFacts[0].ClaimID = cell.claimID
			for i := range draft.Drivers {
				for j := range draft.Drivers[i].ClaimedFactIDs {
					draft.Drivers[i].ClaimedFactIDs[j] = cell.claimID
				}
			}

			err := draft.ValidateAgainst(input)
			gotRefused := err != nil && strings.Contains(err.Error(), "reserved server claim namespace")
			if gotRefused != cell.refused {
				t.Errorf("draft with claim id %q refused for the reserved namespace = %t, want %t -- %s (err=%v)",
					cell.claimID, gotRefused, cell.refused, cell.why, err)
			}
			if cell.refused {
				if got := SynthesisRejectionReasonOf(err); got != RejectionReasonClaimIDReservedNamespace {
					t.Errorf("rejection reason = %q, want %q -- a reserved-namespace collision must read differently from a model repeating its own id, or the decision line cannot tell the two apart", got, RejectionReasonClaimIDReservedNamespace)
				}
			}
		})
	}

	// THE OTHER REASON STAYS WHERE IT BELONGS. A model genuinely reusing an id
	// of its own is still claim_id_duplicate. Without this arm, a change that
	// routed every id rejection through the new reason would pass the arms
	// above while erasing the distinction the new reason exists to draw.
	t.Run("a genuinely reused model id is still a duplicate", func(t *testing.T) {
		t.Parallel()
		input, draft := closureFixture()
		if len(draft.ClaimedFacts) == 0 {
			t.Fatal("fixture drift: the closure fixture carries no claim to duplicate")
		}
		reused := draft.ClaimedFacts[0]
		draft.ClaimedFacts = append(draft.ClaimedFacts, reused)

		err := draft.ValidateAgainst(input)
		if err == nil {
			t.Fatal("a draft carrying the same model-authored claim id twice was admitted")
		}
		if got := SynthesisRejectionReasonOf(err); got != RejectionReasonClaimIDDuplicate {
			t.Errorf("rejection reason = %q, want %q -- a model repeating its own id is not a reserved-namespace collision", got, RejectionReasonClaimIDDuplicate)
		}
	})

	// The namespace must be visibly the server's. A prefix a model could reach
	// for by accident would make the reservation a trap rather than a rule.
	if !strings.Contains(cardinalityClaimIDPrefix, ":") {
		t.Errorf("the reserved prefix %q carries no namespace separator", cardinalityClaimIDPrefix)
	}
}

// ---------------------------------------------------------------------------
// THE SENTENCE STANDS BEFORE THE BOUND
// ---------------------------------------------------------------------------

// TestTheCountSentenceStandsWithinTheContractBound pins the arithmetic that
// turned a valid answer into a rejected one.
//
// The composer truncates to the contract length, so a sentence appended after
// it pushes the answer over and whole-result validation refuses the document.
// Refusing to append instead would drop the count from an answer that owes
// one. The count is the part the claim must agree with, so the earlier prose
// gives and the sentence stands.
func TestTheCountSentenceStandsWithinTheContractBound(t *testing.T) {
	t.Parallel()

	const sentence = "Counted 14 teams of 36 found."

	for _, cell := range []struct {
		name   string
		answer string
		why    string
	}{
		{"room to spare", "Short answer.",
			"the ordinary case must not be disturbed by the bound logic"},
		{"exactly at the bound", strings.Repeat("a", deterministicAnswerMaxLength),
			"an answer already at the limit is where a blind append breaks the contract"},
		{"one under the bound", strings.Repeat("a", deterministicAnswerMaxLength-1),
			"boundary minus one, where an off-by-one hides"},
		{"far over once appended", strings.Repeat("a", deterministicAnswerMaxLength-5),
			"the measured case: a valid answer that appending alone would invalidate"},
	} {
		cell := cell
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			got := appendCardinalitySentence(cell.answer, sentence)
			if n := len([]rune(got)); n > deterministicAnswerMaxLength {
				t.Errorf("composed answer is %d runes, over the %d-rune contract bound -- %s", n, deterministicAnswerMaxLength, cell.why)
			}
			if !strings.Contains(got, sentence) {
				t.Errorf("the count sentence was dropped to fit -- the count is the part that must survive, not the part that gives: %q", got)
			}
		})
	}

	// An empty sentence is not a reason to touch the answer at all.
	if got := appendCardinalitySentence("Untouched.", ""); got != "Untouched." {
		t.Errorf("appendCardinalitySentence with no sentence returned %q, want the answer unchanged", got)
	}
}

// ---------------------------------------------------------------------------
// REUSE RE-DERIVES
// ---------------------------------------------------------------------------

// TestReuseRederivesTheCountRatherThanCarryingIt pins the path that served
// strictly less than the fresh one.
//
// Reuse backfilled the outcome row and left the claim and the sentence behind,
// so the same question answered from cache carried one surface where a fresh
// answer carried three -- and nothing in the document said which the reader
// had. Re-derived rather than carried because a stored document may predate
// the count entirely, so there is nothing to carry.
func TestReuseRederivesTheCountRatherThanCarryingIt(t *testing.T) {
	t.Parallel()

	// A STORED DOCUMENT THAT OWES A COUNT AND CARRIES NONE OF ITS SURFACES:
	// its planning rows carry the obligation and it carries a member set, but
	// no row, claim or sentence. That is the shape of any document stored by a
	// build that did not mint the count, and reuse must restore all three.
	stored := storedResultWithCandidateEvidence()
	// A cohort of the fixture's OWN subject, so the reuse recheck can still
	// see every member. A cohort of unrelated members makes the recheck refuse
	// the candidate and fall through to a fresh investigation, which would
	// measure the fresh path while claiming to measure reuse.
	stored.Cohort = &Cohort{
		Kind:      SubjectProject,
		Rationale: "scope census match",
		Complete:  true,
		Members: []CohortMember{{
			Subject:          reuseDegradeSubject(),
			Rank:             1,
			InclusionReasons: []string{"matched"},
		}},
	}
	stored.ClaimedFacts = []ClaimedFact{}
	stored.DeterministicAnswer = "Ask Dev is release-ready."
	stored.Completeness.Outcomes = SeedRequirementOutcomes(
		deriveTurnRequirements(countingFrame(SubjectProject), registryDeriver{}))

	// FIXTURE CONTROLS. Without both, the assertions below could pass for a
	// document that never owed a count, or one that already carried it.
	if requirement, _ := countRequirement(stored.Completeness.Outcomes); requirement == "" {
		t.Fatal("the stored fixture carries no count obligation -- this arm would then be asserting an absence it is meant to disprove")
	}
	if cardinalityClaimOf(stored) != nil || strings.Contains(stored.DeterministicAnswer, "Counted ") {
		t.Fatal("the stored fixture already carries a count surface -- a re-derivation cannot be observed against it")
	}

	// THROUGH THE ENGINE'S OWN REUSE PATH. Calling the cardinality helpers
	// directly would prove the helpers work, not that reuse uses them, and it
	// would hold with the engine wiring absent. Serving a real hit is what
	// makes missing wiring fail here.
	engine := reuseDegradeEngine(t, stored,
		productionShapedGraphContext([]string{reuseCitationRef}, []string{reuseNodeRef}), &recordingTelemetry{})
	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !result.Reused {
		t.Fatal("the fixture did not serve from the store, so nothing below measures the reuse path")
	}

	claim := cardinalityClaimOf(result)
	if claim == nil {
		t.Error("the reused answer carries no cardinality claim -- a stored document that owes a count was served with strictly less than a fresh one, and nothing in the document said which the reader had")
	}
	if !strings.Contains(result.DeterministicAnswer, "Counted ") {
		t.Errorf("the reused answer states no count in its prose: %q", result.DeterministicAnswer)
	}
	// AND THE THREE STILL AGREE on this path, which is the property the row
	// backfill alone could not give.
	if claim != nil && claim.Value.Integer != nil {
		if got, want := *claim.Value.Integer, int64(len(stored.Cohort.Members)); got != want {
			t.Errorf("the re-derived claim asserts %d but the reused document carries %d members", got, want)
		}
	}
	if rows := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult); len(rows) == 0 {
		t.Error("the reused answer carries no count outcome row to reconcile the claim against")
	}
}

// nonCountingFrame is the discriminating fixture: the SAME scoped member-kind
// expression countingFrame builds, asking a different question.
//
// Varying only the goal is what makes the pair a control. A fixture that also
// changed the subject expression would leave "no count" explicable by the
// cohort never resolving -- a different reason for the same silence, and one a
// broken precondition would hide behind.
func nonCountingFrame(memberKind SubjectKind) *QuestionFrame {
	frame := frameWith(
		[]InvestigationGoal{GoalAssessState},
		scopedExpression(memberKind),
		TemporalIntentCurrent,
		nil,
	)
	return &frame
}

// ---------------------------------------------------------------------------
// THE CAP DECISION AT THE MINT, ON A QUESTION THAT OWES THE COUNT
// ---------------------------------------------------------------------------

// countingGroupReadDeriver is the group-read derivation plus one count
// obligation at the member role, taken from the production derivation.
//
// THE MINT'S CAP DECISION IS REACHABLE ONLY ON A QUESTION THAT OWES THE COUNT.
// The count claim is minted only under a count obligation, and the claimed-
// facts cap is reached only on a document large enough to fill it. The plain
// group-read fixture fills the document -- 250 groups -- but derives state
// obligations alone, so it owes no count and never reaches the mint's cap
// check. This deriver keeps that document and adds the obligation, so the cap
// decision at the call site is the thing under test. The 249/250/251 pin tests
// the helper beside it, which a call site that stopped consulting the helper
// would never disturb.
type countingGroupReadDeriver struct{}

func (countingGroupReadDeriver) DeriveRequirements(frame QuestionFrame) []DerivedRequirement {
	// The count row comes from the PRODUCTION derivation of a counting
	// question, never hand-built. A hand-built row is a second authority over
	// scope and quantifier, and plan validation refuses an incomplete one --
	// which would measure the fixture instead of the cap.
	rows := groupReadRequirementDeriver{}.DeriveRequirements(frame)
	for _, derived := range (registryDeriver{}).DeriveRequirements(*countingFrame(SubjectProject)) {
		if derived.Obligation == ObligationCount {
			rows = append(rows, derived)
		}
	}
	return rows
}

// TestTheCardinalityClaimYieldsToTheContractCapWhenTheCountIsOwed pins the cap
// decision at the mint site itself, not the helper beside it.
//
// A 251st claim fails the claimed-facts bound and invalidates the WHOLE answer.
// So on a document already at the cap, an owed count claim must be dropped --
// the answer serves without the addressable field rather than serving nothing.
// The helper's boundary is pinned separately at 249/250/251; this is the arm
// that fails if the mint stops consulting it, which that unit test cannot see.
//
// THE CONTROLS ARE WHAT MAKE THE ABSENCE MEAN SOMETHING. A missing claim on its
// own is exactly what a question that owes no count produces, so the arm first
// proves the count WAS owed and the document WAS full -- only then is the
// absence the cap's decision rather than the precondition's.
func TestTheCardinalityClaimYieldsToTheContractCapWhenTheCountIsOwed(t *testing.T) {
	t.Parallel()
	const groups = 250

	logs := captureEngineLogger(t)
	recorder := servingGroupCohort(groups)
	engine, request := groupReadEngineFixtureConfigured(t, logs.telemetry, recorder, groupReadCohortMembers(groups), nil, SubjectProject, nil, nil,
		func(config *groupReadFixtureConfig) { config.deriver = countingGroupReadDeriver{} })

	result, err := engine.Investigate(canonicalRequestContext(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v -- an owed count claim pushed past the contract cap invalidates the whole answer, which is exactly the failure the cap decision exists to prevent", err)
	}

	// CONTROL 1: the count was owed. Without it, an absent claim is the
	// precondition speaking, not the cap.
	if rows := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult); len(rows) == 0 {
		t.Fatal("the fixture produced no count outcome row, so the count was never owed and this arm cannot distinguish the cap from the precondition")
	}
	// CONTROL 2: the document is actually at the cap, so the decision was live.
	if got := len(result.ClaimedFacts); got < contractsv1.ContextFabricClaimedFactsMaxCount {
		t.Fatalf("the served document carries %d claims, below the %d cap -- the cap decision was never reached, so nothing here measures it", got, contractsv1.ContextFabricClaimedFactsMaxCount)
	}

	// THE PROPERTY.
	if got := len(result.ClaimedFacts); got > contractsv1.ContextFabricClaimedFactsMaxCount {
		t.Errorf("the served document carries %d claims, over the %d contract cap", got, contractsv1.ContextFabricClaimedFactsMaxCount)
	}
	if claim := cardinalityClaimOf(result); claim != nil {
		t.Error("the count claim was minted into a document already at the claim cap -- the mint is not consulting the cap decision")
	}
}
