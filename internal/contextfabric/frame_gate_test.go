package contextfabric

// The ORDERING SEAM pinned WHERE IT ACTUALLY BINDS.
//
// Frame validation shipped as a shadow: it decided, recorded and telemetered,
// and gated nothing. The unit tests for the invariants were all green through
// the entire life of that shadow, because a pure function that returns
// `refused_invalid` is correct whether or not anybody acts on it. So the pins
// here do NOT test DecideFrameGate in isolation and stop -- they drive
// Engine.Investigate for real and read what the GraphReader RECEIVED, the
// same discipline the scope-anchor gate's own engine tests adopted after a
// round found a pure-function pin could not see either call site.
//
// The harm each pin asserts is the one measured on the rig: a subject
// COMMITTED, or a cohort BUILT, for a question whose frame the server had
// already refused. Asserting only "the gate was consulted" would go green on
// a build that consulted it and answered anyway, which is exactly the build
// this change replaces.

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// frameGatePinJudgment is long enough to clear the SERVED answer's minimum
// size bound. It has nothing to do with what these pins assert -- it exists
// because a served document with a two-character judgment is refused by v1
// validation, and the positive control has to produce a SERVED document for
// its "still commits" claim to mean anything. The refusal pins never reach
// synthesis, so the bound never applies to them.
const frameGatePinJudgment = "The team's delivery signals are steady across the requested window, with no " +
	"regression in the observed throughput or review latency series and no open incident attached to any " +
	"of the repositories in scope. This judgment is fixture prose written to clear the served answer's " +
	"minimum size bound and carries no meaning for the assertions in this file, which read the subject " +
	"resolution and the graph reader's own call counts rather than the narrative text. It is deliberately " +
	"free of any subject label, kind or identifier so that no assertion anywhere can accidentally come to " +
	"depend on its contents rather than on the structured fields beside it."

// frameGateCapturingGraph records whether retrieval ran at all. Both counters
// matter and neither alone is enough: refusing resolution while still
// discovering a cohort would leave the I6 row served, which is the rig
// behaviour this change exists to remove.
type frameGateCapturingGraph struct {
	resolution    SubjectResolution
	context       GraphContext
	resolveCalls  int
	discoverCalls int
}

// The basis set is IDENTITY-PROVEN on purpose. With an empty set every commit
// reads as CommitBasisUnknown, the CHAOS-4085 affirmation gate retracts it
// because the fixture's synthesized prose never names the subject, and the
// positive control then reports "committed 0" for a reason that has nothing to
// do with the frame gate -- the exact half-published shape that reads
// identically to the defect under test. A caller-canonical-id basis cannot be
// retracted, so a zero here means the gate refused and nothing else.
func (g *frameGateCapturingGraph) ResolveSubjects(_ context.Context, _ storage.Principal, _ InvestigationRequest, _ InterpretedQuestion, _ ResolvedGraphBinding, _ *ConfirmedExpectedKind, _ *ConfirmedAnchorSelection, _ *QuestionFrame, _ SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.resolveCalls++
	bases := CommitBasisSet{}
	for _, subject := range g.resolution.Committed {
		bases.Record(subject, CommitBasisCallerCanonicalID)
	}
	return g.resolution, StructureOfferMaterial{}, bases, CommitDecisionDigestSet{}, nil
}

func (g *frameGateCapturingGraph) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	g.discoverCalls++
	return g.context, nil
}

func (g *frameGateCapturingGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "frame-gate-key", Epoch: 0}, nil
}

// runFrameGateEngine drives the production entry point once and hands back
// both what retrieval saw and what the caller got.
//
// THE GRAPH DOUBLE IS DELIBERATELY EAGER: it commits a subject on every call.
// A double that committed nothing would make every one of these pins pass on
// the unfixed tree for the wrong reason -- "no subject was committed" would be
// true because the fixture never commits one, not because the gate refused.
// The positive control below is what proves the eagerness is real.
func runFrameGateEngine(t *testing.T, outcome QuestionFamilyOutcome) (*frameGateCapturingGraph, InvestigationResult, error) {
	t.Helper()
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_chaos", Label: "Fullchaos"}
	graph := &frameGateCapturingGraph{
		resolution: SubjectResolution{
			Candidates: []SubjectCandidate{{
				ReceiptID: "receipt_frame_gate_pin", Subject: team,
				State: ResolutionCommitted, Confidence: 1,
				MatchedTerms: []string{"Fullchaos"}, MatchReasons: []string{"exact"},
				MatchMechanisms: []MatchMechanism{MatchExact},
			}},
			Committed: []SubjectRef{team},
		},
		context: GraphContext{
			Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{{Kind: FactStatus}},
			EvidenceRefIDs:   []string{"evidence_team_status"},
			Coverage:         Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	interpreted := InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent},
		SubjectTerms: []string{"Fullchaos"}, FactRequirements: []FactRequirement{{Kind: FactStatus}},
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{interpreted: interpreted, outcome: outcome},
		Graph:       graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			// Every slice non-nil and DeterministicAnswer non-empty: v1
			// validation refuses a nil array outright (it cannot tell
			// "genuinely none" from "never populated"), and a served
			// document that fails validation would fail the positive
			// control for a reason that has nothing to do with this gate.
			return InvestigationResult{
				Status:         InvestigationComplete,
				DirectJudgment: frameGatePinJudgment, CurrentState: frameGatePinJudgment,
				DeterministicAnswer: frameGatePinJudgment,
				StrongestPressures:  []string{}, Drivers: []DriverJudgment{},
				RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{}, Warnings: []string{},
				ClaimedFacts: []ClaimedFact{},
				Coverage:     Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results: &resultStoreStub{},
	}, EngineOptions{ServiceVersion: "acr-test", NewResultID: func() string { return "result_12345678" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	result, investigateErr := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"},
		InvestigationRequest{
			SchemaVersion: InvestigationRequestSchemaV1, RequestID: "request_12345678",
			Question: "How is the team doing?", TimeContext: TimeContext{Axis: TemporalCurrent},
			Options:  InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true},
			Consumer: ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
		})
	return graph, result, investigateErr
}

func frameGateOutcome(frame *QuestionFrame, gate FrameGate) QuestionFamilyOutcome {
	return QuestionFamilyOutcome{
		Frame:              frame,
		Gate:               gate,
		Family:             QuestionFamilyUnclassified,
		Source:             QuestionFamilySourceModel,
		WinningSampleIndex: 0,
		WinningSample:      FamilySample{},
	}
}

// selfGroupedFrame is the I6-illegal shape measured on the rig: a
// grouped_members expression whose grouping axis IS its member kind.
// checkI6 refuses it, and before this change nothing acted on the refusal.
func selfGroupedFrame() *QuestionFrame {
	return &QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:    SubjectExpressionGroupedMembers,
			Grouped: &GroupedSetExpression{GroupKind: SubjectTeam, MemberKind: SubjectTeam},
		},
		Temporal:    TemporalIntentCurrent,
		Obligations: []AnswerObligation{ObligationState},
		Version:     QuestionFrameVersion,
	}
}

// unservableMemberKindFrame declares a member kind no discovery arm serves.
// The frame is otherwise VALID -- it passes every invariant -- so this pin
// and the I6 one exercise the two refusing verdicts separately rather than
// through one shape that could satisfy both for one reason.
func unservableMemberKindFrame() *QuestionFrame {
	return &QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:       SubjectExpressionDiscoveredKind,
			Discovered: &DiscoveredSetExpression{MemberKind: SubjectPullRequest},
		},
		Temporal:    TemporalIntentCurrent,
		Obligations: []AnswerObligation{ObligationState},
		Version:     QuestionFrameVersion,
	}
}

// namedSubjectFrame is the POSITIVE CONTROL's frame: well formed, valid,
// and not a cohort variant, so the gate passes it.
func namedSubjectFrame() *QuestionFrame {
	return &QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:  SubjectExpressionNamed,
			Named: &NamedSubjectExpression{Terms: []string{"Fullchaos"}},
		},
		Temporal:    TemporalIntentCurrent,
		Obligations: []AnswerObligation{ObligationState},
		Version:     QuestionFrameVersion,
	}
}

// THE HARM: a frame refused by I6 must not produce a committed subject.
//
// On the unfixed tree the engine carries no verdict at all -- a refused frame
// arrives as `Frame: nil`, indistinguishable from a question that proposed
// none -- so resolution runs, the graph double commits, and the assertion
// below on Committed is what goes red. The call-count assertions are the
// mechanism; this one is the harm, and it is stated first for that reason.
func TestAnI6IllegalFrameCommitsNoSubject(t *testing.T) {
	t.Parallel()
	// The verdict is built by DecideFrameGate from the real validator, never
	// hand-written: a hand-written FrameGate would pin this test to a
	// constant rather than to the invariant that produced it, and would stay
	// green if checkI6 stopped refusing the shape entirely.
	result := ValidateFrame(*selfGroupedFrame(), nil, "")
	gate := DecideFrameGate(result, true)
	if gate.Outcome != FrameGateRejectedInvalid {
		t.Fatalf("fixture defect: the self-grouped frame gates as %q, want %q -- this test proves nothing about I6 unless the frame actually fails it", gate.Outcome, FrameGateRejectedInvalid)
	}
	if gate.FailedInvariant != FrameInvariantI6 {
		t.Fatalf("fixture defect: failed invariant = %q, want %q", gate.FailedInvariant, FrameInvariantI6)
	}

	graph, investigation, err := runFrameGateEngine(t, frameGateOutcome(nil, gate))
	if err != nil {
		t.Fatalf("Investigate() error = %v -- a refused frame is a served refusal, never a stage error", err)
	}
	if len(investigation.SubjectResolution.Committed) != 0 {
		t.Fatalf("committed %d subject(s) on a frame refused by %s, want 0 -- this is the rig's substituted-subject shape", len(investigation.SubjectResolution.Committed), FrameInvariantI6)
	}
	if investigation.Cohort != nil {
		t.Fatalf("built a cohort on a frame refused by %s, want none", FrameInvariantI6)
	}
	if graph.resolveCalls != 0 {
		t.Fatalf("ResolveSubjects called %d time(s) on a refused frame, want 0 -- the gate must bind ABOVE retrieval, not inside it", graph.resolveCalls)
	}
	if graph.discoverCalls != 0 {
		t.Fatalf("DiscoverContext called %d time(s) on a refused frame, want 0", graph.discoverCalls)
	}
}

// The second refusing verdict, on a frame that is otherwise entirely valid:
// the member kind has no discovery arm, so no retrieval can produce the
// population the question asked for, and every subject offered into it is an
// offer toward a question the server has already established it cannot serve.
func TestAnUnservableMemberKindCommitsNoSubject(t *testing.T) {
	t.Parallel()
	result := ValidateFrame(*unservableMemberKindFrame(), nil, "")
	if result.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("fixture defect: the unservable-kind frame is INVALID (%s/%s); this pin must exercise the refuse BASIS, not an invariant failure", result.Outcome, result.Failure.Invariant)
	}
	gate := DecideFrameGate(result, true)
	if gate.Outcome != FrameGateRefusedBasis || gate.RefuseBasis != CohortMemberKindUnservable {
		t.Fatalf("fixture defect: gate = %q/%q, want %q/%q", gate.Outcome, gate.RefuseBasis, FrameGateRefusedBasis, CohortMemberKindUnservable)
	}

	graph, investigation, err := runFrameGateEngine(t, frameGateOutcome(nil, gate))
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(investigation.SubjectResolution.Committed) != 0 {
		t.Fatalf("committed %d subject(s) into a %s frame, want 0", len(investigation.SubjectResolution.Committed), CohortMemberKindUnservable)
	}
	if graph.resolveCalls != 0 || graph.discoverCalls != 0 {
		t.Fatalf("retrieval ran (resolve=%d discover=%d) on a %s frame, want 0/0", graph.resolveCalls, graph.discoverCalls, CohortMemberKindUnservable)
	}
}

// THE POSITIVE CONTROL, and it carries the whole suite's credibility: it
// proves the graph double really does commit, so the two zeros above are the
// gate refusing and not the fixture being inert.
func TestAWellFormedNamedSubjectFrameStillCommits(t *testing.T) {
	t.Parallel()
	result := ValidateFrame(*namedSubjectFrame(), nil, "")
	gate := DecideFrameGate(result, true)
	if gate.Outcome != FrameGatePassed {
		t.Fatalf("fixture defect: a well-formed named_subject frame gates as %q, want %q", gate.Outcome, FrameGatePassed)
	}

	graph, investigation, err := runFrameGateEngine(t, frameGateOutcome(namedSubjectFrame(), gate))
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if graph.resolveCalls == 0 {
		t.Fatal("ResolveSubjects was never called on a PASSING frame -- the gate is refusing what it must admit")
	}
	if len(investigation.SubjectResolution.Committed) != 1 {
		t.Fatalf("committed %d subject(s) on a passing frame, want 1 -- either the gate over-refuses or the double never commits, and the refusal pins above are worthless either way", len(investigation.SubjectResolution.Committed))
	}
}

// A turn that proposed NO frame keeps working. This is the fail-safe every
// seam-7 consumer already takes, and the one state a refusing default would
// break for every question the product answers.
func TestAnAbsentFrameStillCommits(t *testing.T) {
	t.Parallel()
	gate := DecideFrameGate(FrameValidationResult{}, false)
	if gate.Outcome != FrameGateNotProposed {
		t.Fatalf("gate for an absent proposal = %q, want %q", gate.Outcome, FrameGateNotProposed)
	}
	graph, investigation, err := runFrameGateEngine(t, frameGateOutcome(nil, gate))
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if graph.resolveCalls == 0 || len(investigation.SubjectResolution.Committed) != 1 {
		t.Fatalf("an absent frame must behave exactly as before this seam: resolve=%d committed=%d, want 1/1", graph.resolveCalls, len(investigation.SubjectResolution.Committed))
	}
}

// The ZERO VALUE allows, and that is a deliberate decision rather than an
// oversight -- see FrameGate's own doc comment. Pinned so a later "make the
// zero value strict" tidy-up has to argue with a test instead of with a
// comment, and so the difference between `not_evaluated` and `not_proposed`
// stays a difference someone has to preserve on purpose.
func TestAnUnevaluatedGateAllows(t *testing.T) {
	t.Parallel()
	var unset FrameGate
	if unset.Outcome != FrameGateNotEvaluated {
		t.Fatalf("the zero FrameGate outcome = %q, want %q", unset.Outcome, FrameGateNotEvaluated)
	}
	if unset.Refuses() {
		t.Fatal("the zero FrameGate refuses; every caller that never ran interpretation would be refused")
	}
	// Rendered as a WORD even though the value is the empty string: an
	// empty log value cannot be told apart from an absent key, and that
	// distinction is the point of the whole seam.
	if unset.Observable() != "not_evaluated" {
		t.Fatalf("Observable() = %q, want %q", unset.Observable(), "not_evaluated")
	}
	if unset.ObservableRefuseBasis() != "none" {
		t.Fatalf("ObservableRefuseBasis() = %q, want %q", unset.ObservableRefuseBasis(), "none")
	}
}

// An UNRECOGNISED outcome refuses. The permissive default is the failure mode
// this whole seam removes, so a member added without teaching Refuses() about
// it must fail closed rather than silently join the allowing set.
func TestAnUnrecognisedGateOutcomeRefuses(t *testing.T) {
	t.Parallel()
	future := FrameGate{Outcome: FrameGateOutcome("some_future_member")}
	if !future.Refuses() {
		t.Fatal("an outcome outside the closed vocabulary ALLOWS retrieval; a new member must fail closed")
	}
	if ValidFrameGateOutcome(future.Outcome) {
		t.Fatal("the fixture's outcome is a real vocabulary member, so this test is not exercising the unrecognised case")
	}
}

// Every member of the closed vocabulary is reachable from DecideFrameGate or
// is documented as reachable only from the zero value -- the "a tier with no
// fixture can be dead for its whole life" rule applied to this vocabulary.
func TestEveryFrameGateOutcomeIsAccountedFor(t *testing.T) {
	t.Parallel()
	produced := map[FrameGateOutcome]bool{
		// The zero value; DecideFrameGate never returns it, by design.
		FrameGateNotEvaluated: true,
	}
	produced[DecideFrameGate(FrameValidationResult{}, false).Outcome] = true
	produced[DecideFrameGate(ValidateFrame(*selfGroupedFrame(), nil, ""), true).Outcome] = true
	produced[DecideFrameGate(ValidateFrame(*unservableMemberKindFrame(), nil, ""), true).Outcome] = true
	produced[DecideFrameGate(ValidateFrame(*namedSubjectFrame(), nil, ""), true).Outcome] = true
	for _, member := range FrameGateOutcomeVocabulary() {
		if !produced[member] {
			t.Errorf("no fixture in this file produces %q; an unreachable verdict cannot be trusted to behave when it does occur", member)
		}
	}
}

// THE DEPLOYED INTERPRETER ALWAYS DECIDES. FrameGate's zero value allows, and
// that is only safe because the production path never leaves it unset -- so
// `not_evaluated` on a rig line means "this did not come from the deployed
// interpreter" and nothing else. If resolveFrame ever stops stamping a
// verdict, the allowing default silently becomes production behaviour and the
// whole seam reverts to the shadow it replaced, with no other symptom.
//
// Drives resolveFrame, the production call site, rather than DecideFrameGate:
// the pure function has been correct all along, and it was correct throughout
// the entire life of the shadow.
func TestTheDeployedInterpreterAlwaysDecidesTheFrameGate(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		frame *QuestionFrame
		want  FrameGateOutcome
	}{
		{"no proposal at all", nil, FrameGateNotProposed},
		{"a frame refused by I6", selfGroupedFrame(), FrameGateRejectedInvalid},
		{"a frame whose member kind no arm serves", unservableMemberKindFrame(), FrameGateRefusedBasis},
		{"a well-formed named subject", namedSubjectFrame(), FrameGatePassed},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			receipt := ModelExecutionReceipt{QuestionFrame: testCase.frame}
			RuntimeQuestionInterpreter{}.resolveFrame(context.Background(), storage.Principal{OrgID: "org_1"}, &receipt, "")
			if receipt.FrameGateOutcome == FrameGateNotEvaluated {
				t.Fatalf("the deployed interpreter left the gate UNDECIDED; the zero value allows, so this reverts the seam to a shadow with no other symptom")
			}
			if receipt.FrameGateOutcome != testCase.want {
				t.Fatalf("receipt gate outcome = %q, want %q", receipt.FrameGateOutcome, testCase.want)
			}
		})
	}
}

// The gate the RECEIPT carries and the gate the FAMILY OUTCOME carries are
// one decision, not two readings of it. They are written at different points
// in resolveFrame's caller, which is exactly how a pair like this drifts --
// the backfill-versus-requirement-derivation defect recorded in
// resolveFrame's own comments was this shape.
func TestTheCarriedGateAgreesWithTheReceipt(t *testing.T) {
	t.Parallel()
	for _, frame := range []*QuestionFrame{selfGroupedFrame(), unservableMemberKindFrame(), namedSubjectFrame()} {
		receipt := ModelExecutionReceipt{QuestionFrame: frame}
		RuntimeQuestionInterpreter{}.resolveFrame(context.Background(), storage.Principal{OrgID: "org_1"}, &receipt, "")
		fromResult := DecideFrameGate(ValidateFrame(*frame, nil, ""), true)
		if receipt.FrameGateOutcome != fromResult.Outcome {
			t.Errorf("receipt outcome %q disagrees with the validator's own verdict %q", receipt.FrameGateOutcome, fromResult.Outcome)
		}
		if receipt.FrameGateRefuseBasis != fromResult.RefuseBasis {
			t.Errorf("receipt refuse basis %q disagrees with the validator's own %q", receipt.FrameGateRefuseBasis, fromResult.RefuseBasis)
		}
	}
}

// THE REFUSING TURNS NEVER REACH RESOLUTION, so the decision summary -- the
// Info line that carries frame_gate for a turn that DID resolve -- is not
// emitted for them at all. Their verdict lives on the frame-validation line
// instead, and this asserts the VALUE there, at the production log level,
// through the production sink.
//
// Found by a surviving mutation, and it is worth recording why the existing
// guards missed it: the structural test proves the field HAS a log key and
// the leak guard proves the key is ALLOWED, but neither reads what the key
// says -- so replacing the verdict with a hardcoded `passed` satisfied both.
// A key whose value nothing asserts is a key that can lie.
func TestFrameGateReachesTheProductionFrameValidationLine(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name            string
		frame           *QuestionFrame
		wantGate        string
		wantRefuseBasis string
	}{
		{"refused by I6", selfGroupedFrame(), "rejected:i6", "none"},
		{"refused on the basis", unservableMemberKindFrame(), "refused:member_kind_unservable", "member_kind_unservable"},
		{"passed", namedSubjectFrame(), "passed", "none"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			event := FrameValidationEventFrom(*testCase.frame, ValidateFrame(*testCase.frame, nil, ""), "", nil)
			records := captureSlogJSONAtProductionLevel(t, func(logger *slog.Logger) {
				NewSlogEngineTelemetry(logger).RecordFrameValidation(context.Background(),
					storage.Principal{OrgID: "org_frame_gate"}, event)
			})
			if len(records) != 1 {
				t.Fatalf("got %d records at the production log level, want 1 -- the verdict for a turn that never resolves lives on THIS line", len(records))
			}
			if got, _ := records[0]["frame_gate"].(string); got != testCase.wantGate {
				t.Errorf("frame_gate = %q, want %q", got, testCase.wantGate)
			}
			if got, _ := records[0]["refuse_basis"].(string); got != testCase.wantRefuseBasis {
				t.Errorf("refuse_basis = %q, want %q", got, testCase.wantRefuseBasis)
			}
		})
	}
}

// THE TERMINAL, driven through the production entry point.
//
// The graphrank pins prove the resolution CARRIES the signal; this proves the
// engine ACTS on it. They are different claims and the rig arm is why both
// exist: the resolution was already correct there -- ambiguous, three
// vector-only candidates withheld -- and the turn still ended as `no_match`,
// because nothing above read that state as different from an empty graph.
//
// Red at 9c3ed3dc: the withheld-pool arm returns no_match.
func TestAWithheldOfferPoolClarifiesInsteadOfNoMatch(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name       string
		resolution SubjectResolution
		allow      bool
		want       InvestigationStatus
	}{
		{
			// Retrieval found candidates and may offer none of them. The
			// pairing -- zero candidates, a prompt -- is what graphrank
			// emits for a pool emptied by the vector-only exclusion.
			name: "a pool emptied by the exclusion clarifies",
			resolution: SubjectResolution{
				Candidates: []SubjectCandidate{}, Committed: []SubjectRef{},
				ClarificationPrompt: OfferPoolEmptiedClarificationPrompt,
			},
			allow: true, want: InvestigationClarificationRequired,
		},
		{
			// THE CONTROL. A graph that genuinely found nothing keeps its
			// no_match; if this ever flips, the change has stopped
			// discriminating and has turned every empty resolution into a
			// clarification.
			name:       "a genuinely empty pool still reports no_match",
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			allow:      true, want: InvestigationNoMatch,
		},
		{
			// A caller that will not accept a clarification gets the
			// terminal it asked for, not one invented for it.
			name: "clarification refused by the caller stays no_match",
			resolution: SubjectResolution{
				Candidates: []SubjectCandidate{}, Committed: []SubjectRef{},
				ClarificationPrompt: OfferPoolEmptiedClarificationPrompt,
			},
			allow: false, want: InvestigationNoMatch,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			resolution := testCase.resolution
			request := InvestigationRequest{Options: InvestigationOptions{AllowClarification: testCase.allow}}
			got, limitation := resolveTerminalStatus(request, &resolution)
			if got != testCase.want {
				t.Fatalf("status = %q, want %q", got, testCase.want)
			}
			if strings.TrimSpace(limitation) == "" {
				t.Fatal("the terminal carries no limitation; a caller must be told why nothing was resolved")
			}
		})
	}
}

// The terminal REASON names the withheld pool specifically. `empty_pool`
// would send an operator to look for a graph that never had the data, which
// is the opposite of what happened.
func TestTheWithheldPoolHasItsOwnTerminalReason(t *testing.T) {
	t.Parallel()
	withheld := SubjectResolution{
		Candidates: []SubjectCandidate{}, Committed: []SubjectRef{},
		ClarificationPrompt: OfferPoolEmptiedClarificationPrompt,
	}
	if got := subjectlessTerminalReason(withheld, 0); got != "offer_pool_emptied_by_exclusion" {
		t.Errorf("terminal reason = %q, want %q", got, "offer_pool_emptied_by_exclusion")
	}
	empty := SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}
	if got := subjectlessTerminalReason(empty, 0); got != "empty_pool" {
		t.Errorf("terminal reason for a genuinely empty pool = %q, want %q -- the control", got, "empty_pool")
	}
}

// THE OFFERS-ONLY CALL SITE, driven through the regime that actually reaches
// it. This is the pin that would have caught the rig defect, and the first
// version of it did NOT: written against this file's own engine fixture it
// passed while the gate was removed, because that fixture never enters the
// class-default window gate and so never calls gatedOfferMaterial at all.
// A mutation re-injecting the defect SURVIVED, which is what exposed the
// vacuity -- the assertions were green because they never ran on this path.
//
// It reuses the window-gate regime's own fixtures (countingInterpreter,
// chaos4234GatedGraph, buildWindowGateEngine) rather than inventing a
// second way to reach the same code, so a change to that regime moves this
// pin with it instead of leaving it quietly testing nothing.
func TestAFrameRefusalStopsTheOffersOnlyResolution(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		frame *QuestionFrame
	}{
		{"refused by an invariant", selfGroupedFrame()},
		{"refused on the basis", unservableMemberKindFrame()},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			gate := DecideFrameGate(ValidateFrame(*testCase.frame, nil, ""), true)
			if !gate.Refuses() {
				t.Fatalf("fixture defect: the frame gates as %q, which does not refuse", gate.Outcome)
			}
			interpreter := &countingInterpreter{
				interpretation: bootstrapInterpretation(),
				family:         QuestionFamilyOutcome{Gate: gate},
			}
			graph := chaos4234GatedGraph()
			engine := buildWindowGateEngine(t, interpreter, graph, &staticResultStore{results: map[string]InvestigationResult{}})

			if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest()); err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if graph.resolveCalls != 0 {
				t.Fatalf("the offers-only pass ran ResolveSubjects %d time(s) on a refused frame, want 0 -- it is a full retrieval whose result is discarded, and running it hands the caller offers to answer for a question the server has refused", graph.resolveCalls)
			}
		})
	}
}

// THE CONTROL, and it is what proves the pin above is not green by accident:
// with a PASSING gate the very same fixture DOES reach the offers-only
// resolution. Without it, "zero resolutions" could be satisfied by a fixture
// that never enters this regime -- which is exactly how the first version of
// the pin above fooled itself.
func TestAPassingFrameStillReachesTheOffersOnlyResolution(t *testing.T) {
	t.Parallel()
	gate := DecideFrameGate(ValidateFrame(*namedSubjectFrame(), nil, ""), true)
	if gate.Refuses() {
		t.Fatalf("fixture defect: a well-formed named_subject frame gates as %q", gate.Outcome)
	}
	interpreter := &countingInterpreter{
		interpretation: bootstrapInterpretation(),
		family:         QuestionFamilyOutcome{Gate: gate},
	}
	graph := chaos4234GatedGraph()
	engine := buildWindowGateEngine(t, interpreter, graph, &staticResultStore{results: map[string]InvestigationResult{}})

	if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest()); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if graph.resolveCalls == 0 {
		t.Fatal("the offers-only pass did NOT run on a passing frame -- this fixture no longer reaches the call site, so the refusal pin beside it proves nothing")
	}
}

// THE CARRY ITSELF, which nothing pinned until a battery said so.
//
// A hosted mutation battery replaced the carry with `outcome.Gate =
// FrameGate{}` and the entire suite stayed green. That is the single link
// between "decided at interpretation" and "enforced in the engine": every
// other pin in this file BUILDS the outcome by hand through frameGateOutcome,
// and TestTheCarriedGateAgreesWithTheReceipt asserts the RECEIPT's fields, so
// the one hop between them was untested. If it regresses, the gate silently
// stops binding and every existing pin still passes -- the exact shape of the
// shadow this seam replaces.
//
// Drives recordFamilyResolution, the production function that performs the
// carry, rather than asserting on a hand-built outcome.
func TestTheGateIsCarriedFromTheReceiptOntoTheFamilyOutcome(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		frame *QuestionFrame
	}{
		{"refused by an invariant", selfGroupedFrame()},
		{"refused on the basis", unservableMemberKindFrame()},
		{"passing", namedSubjectFrame()},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			receipt := ModelExecutionReceipt{QuestionFrame: testCase.frame}
			interpreter := RuntimeQuestionInterpreter{}
			interpreter.resolveFrame(context.Background(), storage.Principal{OrgID: "org_1"}, &receipt, "")

			outcome := interpreter.recordFamilyResolution(context.Background(), storage.Principal{OrgID: "org_1"}, InterpretedQuestion{}, receipt)

			if outcome.Gate.Outcome != receipt.FrameGateOutcome {
				t.Fatalf("the carried gate outcome is %q while the receipt recorded %q -- the verdict does not survive the hop the engine reads it from", outcome.Gate.Outcome, receipt.FrameGateOutcome)
			}
			if outcome.Gate.RefuseBasis != receipt.FrameGateRefuseBasis {
				t.Errorf("the carried refuse basis is %q while the receipt recorded %q", outcome.Gate.RefuseBasis, receipt.FrameGateRefuseBasis)
			}
			// The zero value ALLOWS, so a dropped carry reads as "let it
			// through". Asserting the refusal survives is what makes the
			// dropped-carry mutant fail rather than pass quietly.
			if receipt.FrameGateOutcome == FrameGateRejectedInvalid || receipt.FrameGateOutcome == FrameGateRefusedBasis {
				if !outcome.Gate.Refuses() {
					t.Fatalf("the receipt recorded %q but the carried gate does not refuse; the engine would run retrieval on a refused frame", receipt.FrameGateOutcome)
				}
			}
			if receipt.FrameGateOutcome == FrameGateRejectedInvalid && outcome.Gate.FailedInvariant != receipt.FrameFailedInvariant {
				t.Errorf("the carried failed invariant is %q while the receipt recorded %q -- the log line would name the wrong invariant", outcome.Gate.FailedInvariant, receipt.FrameFailedInvariant)
			}
		})
	}
}
