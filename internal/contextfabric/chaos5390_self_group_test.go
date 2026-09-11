package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestAFrameThatGroupsAKindByItselfIsRefusedWithItsInvariant is the frame-seam
// half, and it is the probe that established the mechanism, kept as a pin.
//
// The suspicion this ticket started from was that some decode, normalise or
// repair step re-expressed an illegal grouped frame into a legal
// discovered-kind one before validation could see it. It does not: the decode
// is a faithful passthrough, and an illegal frame handed to the validator is
// caught. Pinning that is what stops the mechanism silently acquiring a
// laundering step later -- the failure mode would be invisible, because a
// re-expressed frame looks exactly like a question the model never asked
// illegally.
func TestAFrameThatGroupsAKindByItselfIsRefusedWithItsInvariant(t *testing.T) {
	t.Parallel()

	frame := QuestionFrame{
		Version: QuestionFrameVersion,
		SubjectExpression: SubjectExpression{
			Kind:    SubjectExpressionGroupedMembers,
			Grouped: &GroupedSetExpression{GroupKind: SubjectTeam, MemberKind: SubjectTeam},
		},
		Obligations: []AnswerObligation{ObligationState},
		Goals:       []InvestigationGoal{GoalAssessState},
		Temporal:    TemporalIntentCurrent,
	}

	result := ValidateFrame(frame, nil, ShapeDiscoveredCohort)
	t.Logf("outcome=%q invariant=%q detail=%q", result.Outcome, result.Failure.Invariant, result.Failure.Detail)
	if result.Outcome == FrameValidationOutcomeValid {
		t.Fatalf("a frame grouping %q by %q validated -- grouping a kind by itself is not a grouping, and a frame that permits it produces a plan the contract rejects downstream",
			SubjectTeam, SubjectTeam)
	}
	if result.Failure.Invariant != FrameInvariantI6 {
		t.Errorf("failure invariant = %q, want %q -- the refusal must NAME the invariant it violated, or an operator counting frame failures cannot tell this from any other rejection",
			result.Failure.Invariant, FrameInvariantI6)
	}
	if result.Failure.Detail != FrameFailureGroupEqualsMember {
		t.Errorf("failure detail = %q, want %q", result.Failure.Detail, FrameFailureGroupEqualsMember)
	}

	// AND THE GATE, which is what actually binds retrieval. A validation
	// result nothing gates on is a shadow, and the whole value of catching
	// this early is that the turn stops before it commits a subject.
	gate := DecideFrameGate(result, true)
	t.Logf("gate outcome=%q refuse basis=%q", gate.Outcome, gate.RefusalBasis())
	if !gate.Refuses() {
		t.Fatalf("the gate outcome %q does not refuse -- an invalid frame that does not stop the turn is a frame validation nothing acts on", gate.Outcome)
	}
	if got := gate.RefusalBasis(); got != contractsv1.ContextFabricRefusalBasisFrameInvariantViolated {
		t.Errorf("refusal basis = %q, want %q -- the EXISTING member, never a new one", got, contractsv1.ContextFabricRefusalBasisFrameInvariantViolated)
	}
}

// TestAFrameGroupingTwoDifferentKindsStillValidates is the discriminating
// control for the pin above, and it must pass at the parent as well as at the
// tip.
//
// Without it, a mutant that refuses EVERY grouped frame satisfies every
// assertion there while destroying the entire grouped family -- the pin would
// be measuring "grouped frames are refused", which is the opposite of the
// property.
func TestAFrameGroupingTwoDifferentKindsStillValidates(t *testing.T) {
	t.Parallel()

	frame := QuestionFrame{
		Version: QuestionFrameVersion,
		SubjectExpression: SubjectExpression{
			Kind:    SubjectExpressionGroupedMembers,
			Grouped: &GroupedSetExpression{GroupKind: SubjectTeam, MemberKind: SubjectProject},
		},
		Obligations: []AnswerObligation{ObligationState},
		Goals:       []InvestigationGoal{GoalAssessState},
		Temporal:    TemporalIntentCurrent,
	}
	result := ValidateFrame(frame, nil, ShapeDiscoveredCohort)
	t.Logf("control outcome=%q invariant=%q", result.Outcome, result.Failure.Invariant)
	if result.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("CONTROL BROKEN: a legitimate grouped frame (%q grouped by %q) was refused with %q/%q -- the pin above would then be measuring `grouped frames are refused`, not the self-group invariant",
			SubjectProject, SubjectTeam, result.Outcome, result.Failure.Invariant)
	}
	if gate := DecideFrameGate(result, true); gate.Refuses() {
		t.Fatalf("CONTROL BROKEN: the gate refused a valid grouped frame (%q)", gate.Outcome)
	}
}

// TestAPlanWhoseGroupAxisCollapsedOntoItsMembersIsRefusedNotFlattened is the
// PLAN-seam half, and it is the one the frame gate cannot cover.
//
// The plan's group axis comes from the model's flat family hint; its member
// kind is stamped from the cohort THE GRAPH ACTUALLY RETURNED, which is not
// known when the frame is validated. So a frame that is entirely legal --
// projects grouped by team -- can still arrive at the plan seam with both
// kinds equal, because discovery came back with teams. The frame gate could
// not have caught that, and does not.
//
// The parent's response is to silently set the group axis to the empty string
// and answer flat. That is the same laundering the frame prompt performs, one
// seam lower: the question asked for a partition, the server could not
// provide one, and the answer says nothing about either fact. A reader cannot
// tell this answer from one to a question that never asked for grouping, and
// `plan.GroupKind = ""` is indistinguishable in the persisted plan from a plan
// that never had an axis.
//
// RED at the parent: the turn serves, carries no refusal basis, and its plan
// reports no group kind.
func TestAPlanWhoseGroupAxisCollapsedOntoItsMembersIsRefusedNotFlattened(t *testing.T) {
	t.Parallel()

	recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	// A TEAM cohort under a plan whose group axis is also `team`: the axis
	// collapses onto the members at the plan seam, with a legal frame.
	engine, request := groupReadEngineFixtureSelfGroup(t, &recordingTelemetry{}, recorder)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v -- a collapsed group axis is a product outcome the caller must be able to read, not a stage failure", err)
	}

	t.Logf("status=%q refusal_basis=%q plan group_kind=%q member_kind=%q",
		result.Status, result.RefusalBasis, result.AnswerPlan.GroupKind, result.AnswerPlan.MemberKind)

	if result.RefusalBasis != contractsv1.ContextFabricRefusalBasisFrameInvariantViolated {
		t.Errorf("refusal_basis = %q, want %q -- a plan that groups a kind by its own kind partitions a set by itself; flattening it silently answers a different question than the one asked, and the served document says nothing about either fact",
			result.RefusalBasis, contractsv1.ContextFabricRefusalBasisFrameInvariantViolated)
	}
	if len(result.SubjectResolution.Committed) != 0 {
		t.Errorf("committed subjects = %d, want 0 -- a refused turn commits nothing", len(result.SubjectResolution.Committed))
	}
	if len(result.ClaimedFacts) != 0 {
		t.Errorf("claimed facts = %d, want 0 on a refused turn", len(result.ClaimedFacts))
	}
}

// TestAPlanWhoseGroupAxisDiffersFromItsMembersStillGroups is the
// discriminating control for the plan-seam pin, and must pass at the parent
// too: a mutant that refuses every grouped plan would otherwise satisfy the
// pin above while deleting the grouped family outright.
func TestAPlanWhoseGroupAxisDiffersFromItsMembersStillGroups(t *testing.T) {
	t.Parallel()

	recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	engine, request := groupReadEngineFixture(t, &recordingTelemetry{}, recorder)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("CONTROL BROKEN: Investigate() error = %v on an ordinary grouped turn", err)
	}
	if result.RefusalBasis == contractsv1.ContextFabricRefusalBasisFrameInvariantViolated {
		t.Fatalf("CONTROL BROKEN: an ordinary grouped turn (projects grouped by team) was refused with %q -- the plan-seam pin would then be measuring `grouped turns are refused`",
			result.RefusalBasis)
	}
	if result.Cohort == nil || len(result.Cohort.Groups) == 0 {
		t.Fatalf("CONTROL BROKEN: an ordinary grouped turn produced no groups, so the pin's fixture cannot distinguish a refusal from a turn that never grouped")
	}
}
