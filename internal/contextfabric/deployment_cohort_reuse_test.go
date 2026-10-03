package contextfabric

import (
	"context"
	"errors"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// reuseAnchorGuardEngine builds an engine whose stored row committed repo-1
// (a servable deployment cohort anchor) and whose fresh interpretation
// commits the request's project hint under a project anchor, which the
// deployment cohort anchor guard refuses.
func reuseAnchorGuardEngine(t *testing.T, withStoredRow bool, telemetry *recordingTelemetry) *Engine {
	t.Helper()
	frame := deploymentScopedFrame(GoalAssessState)
	gate := DecideFrameGate(ValidateFrame(frame, nil, ""), true)
	outcome := QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: gate, WinningSample: FamilySample{ScopeAnchorKind: SubjectProject, ScopeAnchorTerm: "Anchor"}}
	hinted := SubjectRef{Kind: SubjectProject, CanonicalID: "project-2", Label: "Project"}
	payload := workItemTuplePayloadFixture(t)
	resolution := payload.SubjectResolution
	resolution.Committed = []SubjectRef{hinted}
	resolution.Candidates = []SubjectCandidate{{Subject: hinted}}
	stop := errors.New("stop")

	repo := SubjectRef{Kind: SubjectRepository, CanonicalID: "repo-1", Label: "Repo"}
	_, candidate := reusableCandidate()
	candidate.SubjectResolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{repo}, CommitDecisionDigests: identityProvenDigests(repo)}
	candidate.EffectiveEvidenceWindow = &EffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing90D, Provenance: WindowQuestionStated}

	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "deployments", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactDeployments}}}, outcome: outcome},
		Telemetry:   telemetry,
		Graph:       &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: resolution, bases: provenCommitBases(resolution.Committed...)}},
		CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
			return true, ""
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, stop
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{}, stop
		}),
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, withStoredRow, nil
		}),
	}, EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_fresh_00001" }})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func reuseAnchorGuardRequest() InvestigationRequest {
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestedScope.SubjectHints = []SubjectHint{{Kind: SubjectProject, ID: "project-2", Label: "Project", Source: "caller"}}
	return request
}

// A stored answer about repo-1 must not be served to a turn whose hint names
// another subject: fresh interpretation refuses that hint under the
// deployment cohort anchor guard, and the reuse path never reaches the guard.
func TestReuseDoesNotServeAStoredAnswerPastTheDeploymentAnchorGuard(t *testing.T) {
	for _, tc := range []struct {
		name          string
		withStoredRow bool
	}{
		{"fresh control refuses the hint", false},
		{"stored row for another subject is not served", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			telemetry := &recordingTelemetry{}
			engine := reuseAnchorGuardEngine(t, tc.withStoredRow, telemetry)
			result, err := engine.Investigate(context.Background(), reusePrincipal(), reuseAnchorGuardRequest())
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.Reused {
				t.Fatalf("stored answer %s served to a turn the anchor guard refuses fresh", result.ResultID)
			}
			if result.RefusalBasis != contractsv1.ContextFabricRefusalBasisMemberKindUnservable {
				t.Fatalf("refusal basis = %q, want member_kind_unservable", result.RefusalBasis)
			}
			if tc.withStoredRow && !reuseOutcomeSlicesEqual(telemetry.answerReuseOutcomes, []AnswerReuseOutcome{AnswerReuseMissHintNotCommitted}) {
				t.Fatalf("reuse outcomes = %v, want [%s]", telemetry.answerReuseOutcomes, AnswerReuseMissHintNotCommitted)
			}
		})
	}
}

func TestAnUncoveredHintMissesReuse(t *testing.T) {
	t.Parallel()
	repo := SubjectRef{Kind: SubjectRepository, CanonicalID: "repo-1"}
	for _, tc := range []struct {
		name  string
		hints []SubjectHint
		want  bool
	}{
		{"no hint", nil, true},
		{"hint naming the committed subject", []SubjectHint{{Kind: SubjectRepository, ID: "repo-1"}}, true},
		{"another identity of the committed kind", []SubjectHint{{Kind: SubjectRepository, ID: "repo-2"}}, false},
		{"another kind with the committed id", []SubjectHint{{Kind: SubjectProject, ID: "repo-1"}}, false},
		{"one covered and one uncovered", []SubjectHint{{Kind: SubjectRepository, ID: "repo-1"}, {Kind: SubjectTeam, ID: "team-x"}}, false},
	} {
		if got := requestHintsCommittedBy(tc.hints, []SubjectRef{repo}); got != tc.want {
			t.Errorf("%s: covered = %v, want %v", tc.name, got, tc.want)
		}
	}
	if requestHintsCommittedBy([]SubjectHint{{Kind: SubjectRepository, ID: "repo-1"}}, nil) {
		t.Error("a hint is never covered by a row that committed nothing")
	}
}
