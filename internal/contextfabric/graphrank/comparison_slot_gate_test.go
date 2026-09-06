package graphrank

// ONE OPERAND SLOT, IN ISOLATION -- the unit that answers "did the gate refuse,
// or did the wiring never reach it".
//
// WHY THIS FILE EXISTS, stated because the reason is a debugging lesson worth
// keeping. The end-to-end battery reported `committed = []` for a comparison
// whose two operands were each retrieved with confidence 1 and a MatchExact
// mechanism. That single fact is consistent with at least three different
// defects -- retrieval never reaching the slots, the per-slot gate declining a
// lone exact match, or publication holding a pair that had in fact resolved --
// and an end-to-end fixture cannot tell them apart, because it can only see
// what came out of the far end.
//
// This file drives ONE slot with a hand-built dependency stub and a RECORDING
// TRACER, so the gate states which branch it took in its own words rather than
// leaving it to be inferred from the outcome.

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// slotGateTracer captures every ResolutionTraceEvent the resolver emits.
type slotGateTracer struct{ events []ResolutionTraceEvent }

func (r *slotGateTracer) Trace(event ResolutionTraceEvent) { r.events = append(r.events, event) }

// decisions returns the decision-stage events, which are the ones that name
// the commit gate that fired (or that nothing did).
func (r *slotGateTracer) decisions() []ResolutionTraceEvent {
	var out []ResolutionTraceEvent
	for _, event := range r.events {
		if event.Stage == "decision" {
			out = append(out, event)
		}
	}
	return out
}

// exactMatchNode is a node whose label EQUALS the term, which is what makes it
// an exact-label match, and whose authorization attribute admits every
// principal so authorization can never be the reason a slot came back empty.
func exactMatchNode(kind contextfabric.SubjectKind, canonicalID, label string) CandidateNode {
	return CandidateNode{
		UUID: canonicalID,
		Name: label,
		Attributes: map[string]interface{}{
			"subject_kind":               string(kind),
			"canonical_id":               canonicalID,
			"label":                      label,
			"authorization_repositories": "*",
		},
		Mechanism: contextfabric.MatchLexical,
	}
}

// slotGateDeps is the smallest ResolveDeps that can carry one slot's
// retrieval: a Search keyed on the term, and the two callbacks the merge path
// requires. Everything else is left nil, which is the point -- the census,
// the evidence round and the question pass are NOT wired here, because a
// comparison slot must not reach any of them.
func slotGateDeps(byTerm map[string][]CandidateNode, tracer ResolutionTracer) ResolveDeps {
	return ResolveDeps{
		Search: func(_ context.Context, term string, _ int) ([]CandidateNode, bool, bool, error) {
			return byTerm[term], false, false, nil
		},
		Traverse: func(context.Context, string, CandidateNode, bool) (contextfabric.SubjectCandidate, ObservationTraversal) {
			return contextfabric.SubjectCandidate{}, ObservationNoParent
		},
		IsInternal:       func(contextfabric.SubjectRef) bool { return false },
		ResolutionTracer: tracer,
	}
}

func slotGateRequest() contextfabric.InvestigationRequest {
	return contextfabric.InvestigationRequest{
		RequestID: "request_slot_gate",
		Question:  "compare alpha and beta",
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
			MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144, AllowClarification: true,
		},
	}
}

// TestOneOperandSlotCommitsItsLoneExactMatch is the isolating arm.
//
// One operand, one term, one node whose label equals that term. Nothing else
// competes and nothing else is wired. If this slot does not commit, the defect
// is in the per-slot gate invocation and NOT in the dispatch, the engine, or
// publication -- and the recorded decision event says which branch the gate
// took, so the next question does not have to be guessed either.
func TestOneOperandSlotCommitsItsLoneExactMatch(t *testing.T) {
	t.Parallel()

	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_alpha", Label: "alpha"}
	tracer := &slotGateTracer{}
	deps := slotGateDeps(map[string][]CandidateNode{
		"alpha": {exactMatchNode(contextfabric.SubjectTeam, "team_alpha", "alpha")},
	}, tracer)

	slot := contextfabric.ComparisonOperandSlot{
		Position: 0, Variant: contextfabric.ComparisonOperandNamed,
		Kind: contextfabric.SubjectTeam, Terms: []string{"alpha"},
	}

	run, err := resolveOneOperandSlot(context.Background(), storage.Principal{OrgID: "org-1"}, slotGateRequest(), deps, slot)
	if err != nil {
		t.Fatalf("resolveOneOperandSlot() error = %v", err)
	}

	// FIXTURE CONTROL FIRST. If the candidate is not in the slot's pool, the
	// arm is measuring retrieval, not the gate.
	if len(run.candidates) != 1 {
		t.Fatalf("slot candidates = %d, want 1 -- retrieval did not reach this slot, so nothing below measures the gate", len(run.candidates))
	}
	candidate := run.candidates[0]
	if candidate.Confidence != 1 || !HasMechanism(candidate.MatchMechanisms, contextfabric.MatchExact) {
		t.Fatalf("slot candidate = conf %.2f mechanisms %v, want confidence 1 with an exact mechanism -- the gate's exact tier requires both, so a fixture that produces neither cannot exercise it",
			candidate.Confidence, candidate.MatchMechanisms)
	}

	// THE PROPERTY.
	if len(run.committed) != 1 {
		t.Errorf("slot committed = %v (%d), want exactly the lone exact match %s.\ndecision events = %#v",
			run.committed, len(run.committed), SubjectKey(subject), tracer.decisions())
	}
	if !run.resolved() {
		t.Errorf("slot state = %q, want %q", run.state(), operandSlotResolved)
	}
}

// TestOneOperandSlotSeesOnlyItsOwnTerms is the isolation property at the unit,
// and it is the one an end-to-end fixture proves only circumstantially.
//
// The stub answers a DIFFERENT term with a different subject. A slot that
// searched anything other than its own terms would pick it up.
func TestOneOperandSlotSeesOnlyItsOwnTerms(t *testing.T) {
	t.Parallel()

	tracer := &slotGateTracer{}
	deps := slotGateDeps(map[string][]CandidateNode{
		"alpha": {exactMatchNode(contextfabric.SubjectTeam, "team_alpha", "alpha")},
		"beta":  {exactMatchNode(contextfabric.SubjectTeam, "team_beta", "beta")},
	}, tracer)

	slot := contextfabric.ComparisonOperandSlot{
		Position: 0, Variant: contextfabric.ComparisonOperandNamed,
		Kind: contextfabric.SubjectTeam, Terms: []string{"alpha"},
	}
	run, err := resolveOneOperandSlot(context.Background(), storage.Principal{OrgID: "org-1"}, slotGateRequest(), deps, slot)
	if err != nil {
		t.Fatalf("resolveOneOperandSlot() error = %v", err)
	}

	for _, candidate := range run.candidates {
		if candidate.Subject.CanonicalID == "team_beta" {
			t.Errorf("the slot for term %q retrieved %s, which only the OTHER operand's term reaches -- slot term isolation is the whole mechanism and it is not holding",
				slot.Terms[0], SubjectKey(candidate.Subject))
		}
	}
	if len(run.candidates) == 0 {
		t.Fatal("the slot retrieved nothing at all, so this arm cannot distinguish isolation from a dead fixture")
	}
}

// TestAnAdmittedPairCommitsBothOperandsAtTheResolverUnit is the SECOND step of
// a deliberate bisection.
//
// The slot arm above proves one operand commits its lone exact match. This one
// proves the PAIR does, at the same unit, with the same stub. Together they
// partition the search space for the end-to-end `committed = []`: if this
// passes, the defect is not in slot resolution, not in the publication
// decision, and not in the clarification -- it is in the engine or adapter
// wiring above this call, and the next question is about that layer rather
// than this one.
//
// A bisection arm is worth keeping after it has served its debugging purpose:
// it is the tightest possible statement of "the resolver, given two clean
// operands, publishes both".
func TestAnAdmittedPairCommitsBothOperandsAtTheResolverUnit(t *testing.T) {
	t.Parallel()

	tracer := &slotGateTracer{}
	deps := slotGateDeps(map[string][]CandidateNode{
		"alpha": {exactMatchNode(contextfabric.SubjectTeam, "team_alpha", "alpha")},
		"beta":  {exactMatchNode(contextfabric.SubjectTeam, "team_beta", "beta")},
	}, tracer)

	comparison := contextfabric.ComparisonOperands{
		Admission: contextfabric.ComparisonAdmittedNamedPair,
		Slots: []contextfabric.ComparisonOperandSlot{
			{Position: 0, Variant: contextfabric.ComparisonOperandNamed, Kind: contextfabric.SubjectTeam, Terms: []string{"alpha"}},
			{Position: 1, Variant: contextfabric.ComparisonOperandNamed, Kind: contextfabric.SubjectTeam, Terms: []string{"beta"}},
		},
	}

	resolution, bases, _, err := resolveNamedComparison(context.Background(), storage.Principal{OrgID: "org-1"}, slotGateRequest(), deps, comparison)
	if err != nil {
		t.Fatalf("resolveNamedComparison() error = %v", err)
	}

	if len(resolution.Candidates) != 2 {
		t.Fatalf("published candidates = %d, want 2 -- retrieval did not reach both slots, so nothing below measures publication", len(resolution.Candidates))
	}
	if len(resolution.Committed) != 2 {
		t.Fatalf("published committed = %v (%d), want both operands.\nprompt = %q\ndecisions = %#v",
			resolution.Committed, len(resolution.Committed), resolution.ClarificationPrompt, tracer.decisions())
	}
	if resolution.Committed[0].CanonicalID != "team_alpha" {
		t.Errorf("published order = %v, want the frame's operand order (alpha first)", resolution.Committed)
	}
	if strings.TrimSpace(resolution.ClarificationPrompt) != "" {
		t.Errorf("a fully resolved pair published a clarification prompt %q", resolution.ClarificationPrompt)
	}
	if len(bases) != 2 {
		t.Errorf("published commit bases = %d, want one per committed subject", len(bases))
	}
}

// ---------------------------------------------------------------------------
// STEP 4 -- THE EMPTINESS GATE IS INVOKED INDEPENDENTLY PER SLOT
// ---------------------------------------------------------------------------
//
// §4.5's requirement is met by INVOCATION rather than by modification:
// resolution.go is untouched, its `len(committedIndex) == 0` guard is intact,
// and its six singleton commit assignments are still six singletons and one
// accumulating append. What makes that sufficient is that the existing gate is
// called ONCE PER OPERAND over that operand's OWN pool.
//
// "resolution.go is untouched" is a fact about a diff, and a diff is not a
// property. These two arms are the property.

// TestOneSlotsAmbiguityDoesNotSuppressTheOthersCommit is independence, stated
// as the thing that would break if the invocation were shared.
//
// Operand A has TWO exact claimants and must refuse. Operand B has one and must
// commit anyway. Under a single shared pool -- which is exactly what the
// pre-fix resolver had -- A's two claimants and B's one would land together,
// the pool-wide exact-uniqueness test would see three, and B would be refused
// for A's ambiguity. That is the defect in miniature, and this arm fails if it
// ever returns.
func TestOneSlotsAmbiguityDoesNotSuppressTheOthersCommit(t *testing.T) {
	t.Parallel()

	tracer := &slotGateTracer{}
	deps := slotGateDeps(map[string][]CandidateNode{
		// TWO subjects whose labels both equal operand A's term.
		"alpha": {
			exactMatchNode(contextfabric.SubjectTeam, "team_alpha_one", "alpha"),
			exactMatchNode(contextfabric.SubjectTeam, "team_alpha_two", "alpha"),
		},
		"beta": {exactMatchNode(contextfabric.SubjectTeam, "team_beta", "beta")},
	}, tracer)

	comparison := contextfabric.ComparisonOperands{
		Admission: contextfabric.ComparisonAdmittedNamedPair,
		Slots: []contextfabric.ComparisonOperandSlot{
			{Position: 0, Variant: contextfabric.ComparisonOperandNamed, Kind: contextfabric.SubjectTeam, Terms: []string{"alpha"}},
			{Position: 1, Variant: contextfabric.ComparisonOperandNamed, Kind: contextfabric.SubjectTeam, Terms: []string{"beta"}},
		},
	}

	runA, err := resolveOneOperandSlot(context.Background(), storage.Principal{OrgID: "org-1"}, slotGateRequest(), deps, comparison.Slots[0])
	if err != nil {
		t.Fatalf("slot A: %v", err)
	}
	runB, err := resolveOneOperandSlot(context.Background(), storage.Principal{OrgID: "org-1"}, slotGateRequest(), deps, comparison.Slots[1])
	if err != nil {
		t.Fatalf("slot B: %v", err)
	}

	// FIXTURE CONTROL: A must genuinely be ambiguous, or there is no
	// suppression to be absent.
	if len(runA.candidates) != 2 {
		t.Fatalf("slot A holds %d candidates, want 2 rivals -- without a genuinely ambiguous slot this arm cannot show that its ambiguity did not travel", len(runA.candidates))
	}
	if len(runA.committed) != 0 {
		t.Errorf("slot A committed %v despite two exact claimants on its own term -- its own uniqueness check is gone", runA.committed)
	}

	// THE PROPERTY.
	if len(runB.committed) != 1 {
		t.Errorf("slot B committed %v (%d), want its lone exact match -- one operand's ambiguity suppressed the other's commit, which is the pooled-resolution defect this design removes.\ndecisions = %#v",
			runB.committed, len(runB.committed), tracer.decisions())
	}
}

// TestAnOutOfCutOperandCountIsLeftToTheExistingPath is the other half of
// "narrow": the shapes OUTSIDE the cut must be untouched, not refused.
//
// Three named operands, each exactly resolvable. The classifier reports the
// count as out of cut, nothing dispatches, and the existing pooled path
// behaves exactly as it always has -- three exact claimants in one pool, so
// pool-wide uniqueness fails and nothing commits. The contrast with the
// two-operand fixture directly above is the whole point: the same retrieval,
// the same nodes, a different operand count, and a deliberately different
// answer.
func TestAnOutOfCutOperandCountIsLeftToTheExistingPath(t *testing.T) {
	t.Parallel()

	expected := contextfabric.SubjectTeam
	operand := func(term string) contextfabric.SubjectOperand {
		kind := expected
		return contextfabric.SubjectOperand{
			Kind:  contextfabric.SubjectOperandNamed,
			Named: &contextfabric.NamedSubjectExpression{Terms: []string{term}, ExpectedKind: &kind},
		}
	}
	frame := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCompare},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionExplicitSet,
			Explicit: &contextfabric.ExplicitSetExpression{Operands: []contextfabric.SubjectOperand{
				operand("alpha"), operand("beta"), operand("gamma"),
			}},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}, nil)

	// THE CLASSIFIER'S OWN VERDICT, asserted so this arm cannot silently
	// become a test of something else if the cut ever widens.
	if got := contextfabric.ClassifyComparisonOperands(&frame); got.Admission != contextfabric.ComparisonOutOfCutOperandCount {
		t.Fatalf("three operands classified %q, want %q -- if the cut widened, this arm is measuring the wrong thing and must be rewritten deliberately rather than left to pass",
			got.Admission, contextfabric.ComparisonOutOfCutOperandCount)
	}

	tracer := &slotGateTracer{}
	deps := slotGateDeps(map[string][]CandidateNode{
		"alpha": {exactMatchNode(contextfabric.SubjectTeam, "team_alpha", "alpha")},
		"beta":  {exactMatchNode(contextfabric.SubjectTeam, "team_beta", "beta")},
		"gamma": {exactMatchNode(contextfabric.SubjectTeam, "team_gamma", "gamma")},
	}, tracer)

	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeExplicitCohort, SubjectTerms: []string{"alpha", "beta", "gamma"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{},
	}
	bases := contextfabric.CommitBasisSet{}
	digests := contextfabric.CommitDecisionDigestSet{}
	resolution, _, err := resolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, slotGateRequest(), interpreted, deps, nil, nil, bases, digests, &frame, "")
	if err != nil {
		t.Fatalf("resolveSubjects() error = %v", err)
	}

	// FIXTURE CONTROL: all three must have reached one pool, or the arm is
	// measuring a retrieval failure rather than pooled behaviour.
	if len(resolution.Candidates) != 3 {
		t.Fatalf("pooled candidates = %d, want 3 -- the existing path did not retrieve all three operands, so nothing below measures its behaviour", len(resolution.Candidates))
	}
	if len(resolution.Committed) != 0 {
		t.Errorf("an out-of-cut three-operand frame committed %v -- the existing pooled path refuses three exact claimants, and a shape outside the cut must keep the behaviour it has today rather than gaining the comparison path's",
			resolution.Committed)
	}
}
