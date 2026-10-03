package contextfabric

import (
	"context"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type anchorKindRun struct {
	discoverCalls int
	refusalBasis  contractsv1.ContextFabricRefusalBasis
	saved         *PersistedSemanticState
}

func runDeploymentCohortWithAnchor(t *testing.T, declared SubjectKind, committed ...SubjectRef) anchorKindRun {
	t.Helper()
	frame := deploymentScopedFrame(GoalAssessState)
	gate := DecideFrameGate(ValidateFrame(frame, nil, ""), true)
	outcome := QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: gate, WinningSample: FamilySample{ScopeAnchorKind: declared, ScopeAnchorTerm: "Anchor"}}
	resolution := workItemTuplePayloadFixture(t).SubjectResolution
	resolution.Committed = append([]SubjectRef{}, committed...)
	template := resolution.Candidates[0]
	resolution.CommitDecisionDigests = identityProvenDigests(committed...)
	resolution.Candidates = []SubjectCandidate{}
	for _, subject := range committed {
		candidate := template
		candidate.Subject = subject
		resolution.Candidates = append(resolution.Candidates, candidate)
	}
	graph := &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: resolution, bases: provenCommitBases(committed...)}}
	store := &staticResultStore{}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "deployments", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactDeployments}}}, outcome: outcome},
		Graph:       graph,
		Results:     store,
		CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
			return true, ""
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return validInvestigationResult(), nil
		}),
	}, EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_anchor_kind_001" }})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, validInvestigationRequestWithConfirmedWindow())
	if runErr != nil {
		t.Fatalf("investigate: %v", runErr)
	}
	run := anchorKindRun{discoverCalls: graph.discoverCalls, refusalBasis: result.RefusalBasis}
	if store.savedSemantic != nil {
		run.saved = store.savedSemantic.State
	}
	return run
}

func TestEmptyDeclaredAnchorKindIsDerivedFromTheOneCommittedSubject(t *testing.T) {
	for _, kind := range []SubjectKind{SubjectRepository, SubjectTeam, SubjectProject} {
		run := runDeploymentCohortWithAnchor(t, "", SubjectRef{Kind: kind, CanonicalID: "anchor-1", Label: "Anchor"})
		if run.discoverCalls != 1 {
			t.Fatalf("%s: discover calls = %d, want 1", kind, run.discoverCalls)
		}
		if run.saved == nil {
			t.Fatalf("%s: no semantic state saved", kind)
		}
		if got := run.saved.ScopeAnchor; got.Kind != kind || got.KindSource != ScopeAnchorKindCommittedHint {
			t.Fatalf("%s: scope anchor = %+v, want kind %s source committed_hint", kind, got, kind)
		}
	}
}

func TestEmptyDeclaredAnchorKindWithSeveralCommittedSubjectsIsRefused(t *testing.T) {
	run := runDeploymentCohortWithAnchor(t, "",
		SubjectRef{Kind: SubjectRepository, CanonicalID: "anchor-1", Label: "A"},
		SubjectRef{Kind: SubjectTeam, CanonicalID: "anchor-2", Label: "B"})
	if run.discoverCalls != 0 || run.refusalBasis != contractsv1.ContextFabricRefusalBasisMemberKindUnservable {
		t.Fatalf("discover=%d refusal=%q, want 0 and member_kind_unservable", run.discoverCalls, run.refusalBasis)
	}
	if run.saved != nil && run.saved.ScopeAnchor.Kind != "" {
		t.Fatalf("refused turn recorded an anchor kind: %+v", run.saved.ScopeAnchor)
	}
}

func TestEmptyDeclaredAnchorKindWithAnUnservableCommittedKindIsRefused(t *testing.T) {
	run := runDeploymentCohortWithAnchor(t, "", SubjectRef{Kind: SubjectIncident, CanonicalID: "anchor-1", Label: "A"})
	if run.discoverCalls != 0 || run.refusalBasis != contractsv1.ContextFabricRefusalBasisMemberKindUnservable {
		t.Fatalf("discover=%d refusal=%q, want 0 and member_kind_unservable", run.discoverCalls, run.refusalBasis)
	}
}

func TestDeclaredAnchorKindPathIsUnchangedByDerivation(t *testing.T) {
	served := runDeploymentCohortWithAnchor(t, SubjectRepository, SubjectRef{Kind: SubjectRepository, CanonicalID: "anchor-1", Label: "A"})
	if served.discoverCalls != 1 || served.saved == nil || served.saved.ScopeAnchor.Kind != SubjectRepository || served.saved.ScopeAnchor.KindSource != "" {
		t.Fatalf("declared match: %+v", served)
	}
	refused := runDeploymentCohortWithAnchor(t, SubjectProject, SubjectRef{Kind: SubjectRepository, CanonicalID: "anchor-1", Label: "A"})
	if refused.discoverCalls != 0 || refused.refusalBasis != contractsv1.ContextFabricRefusalBasisMemberKindUnservable {
		t.Fatalf("declared mismatch: %+v", refused)
	}
}

func TestDerivedAnchorKindSourceIsInTheSemanticStateTelemetryGroup(t *testing.T) {
	state := &PersistedSemanticState{ScopeAnchor: SemanticScopeAnchor{Kind: SubjectRepository, KindSource: ScopeAnchorKindCommittedHint}}
	if got := semanticStateLogGroup("state", state).String(); !strings.Contains(got, "scope_anchor_kind_source=committed_hint") {
		t.Fatalf("telemetry group lacks the source: %s", got)
	}
	if got := semanticStateLogGroup("state", &PersistedSemanticState{}).String(); !strings.Contains(got, "scope_anchor_kind_source=none") {
		t.Fatalf("telemetry group lacks the none source: %s", got)
	}
}

func TestSemanticStateRejectsAnUnknownScopeAnchorKindSource(t *testing.T) {
	state := sizedSemanticState(t, 4000)
	state.ScopeAnchor = SemanticScopeAnchor{Kind: SubjectRepository, KindSource: "model"}
	if _, err := EncodeSemanticState(state); err == nil {
		t.Fatal("an unknown kind source must be rejected")
	}
	state.ScopeAnchor.KindSource = ScopeAnchorKindCommittedHint
	if _, err := EncodeSemanticState(state); err != nil {
		t.Fatalf("committed_hint must encode: %v", err)
	}
}

func TestContinuationCarriesTheDerivedAnchorKindSource(t *testing.T) {
	state := semanticFixture(t)
	state.ScopeAnchor = SemanticScopeAnchor{Kind: SubjectRepository, Term: "Anchor", KindSource: ScopeAnchorKindCommittedHint}
	decision := windowContinuationDecision{Observed: true, WindowOnlyShape: true, Disposition: ContinuationApplied,
		Accepted: &continuationCarriedContext{Family: state.Family, GroupKind: state.GroupKind, State: state}}
	before := QuestionFamilyOutcome{Family: QuestionFamilyDiscoveredCohortRanking, Source: QuestionFamilySourceModel, WinningSample: FamilySample{ScopeAnchorKind: SubjectTeam, ScopeAnchorKindSource: "other"}}
	after, applied := applyWindowContinuation(before, decision, composeAcceptedContext(compositionInput{Carried: state}))
	if !applied || after.WinningSample.ScopeAnchorKind != SubjectRepository || after.WinningSample.ScopeAnchorKindSource != ScopeAnchorKindCommittedHint {
		t.Fatalf("applied=%v sample anchor kind=%q source=%q, want repository/committed_hint", applied, after.WinningSample.ScopeAnchorKind, after.WinningSample.ScopeAnchorKindSource)
	}
	state.ScopeAnchor = SemanticScopeAnchor{Kind: SubjectRepository, Term: "Anchor"}
	after, _ = applyWindowContinuation(before, decision, composeAcceptedContext(compositionInput{Carried: state}))
	if after.WinningSample.ScopeAnchorKindSource != "" {
		t.Fatalf("a declared carried kind must not gain a source, got %q", after.WinningSample.ScopeAnchorKindSource)
	}
}

func TestScopeAnchorComparisonIgnoresTheKindSourceAndMatchesADerivedKind(t *testing.T) {
	derived := SemanticScopeAnchor{Kind: SubjectRepository, Term: "Anchor", KindSource: ScopeAnchorKindCommittedHint}
	for _, tc := range []struct {
		name         string
		carried, now SemanticScopeAnchor
		same         bool
	}{
		{"same declared kind, source differs", derived, SemanticScopeAnchor{Kind: SubjectRepository, Term: "Anchor"}, true},
		{"derived carried kind vs fresh proposal with none", derived, SemanticScopeAnchor{Term: "Anchor"}, true},
		{"declared carried kind vs fresh proposal with none", SemanticScopeAnchor{Kind: SubjectRepository, Term: "Anchor"}, SemanticScopeAnchor{Term: "Anchor"}, false},
		{"derived carried kind vs a different fresh kind", derived, SemanticScopeAnchor{Kind: SubjectTeam, Term: "Anchor"}, false},
		{"term differs", derived, SemanticScopeAnchor{Kind: SubjectRepository, Term: "Other"}, false},
	} {
		if got := sameScopeAnchor(tc.carried, tc.now); got != tc.same {
			t.Errorf("%s: same = %v, want %v", tc.name, got, tc.same)
		}
	}
}

func TestEmptyDeclaredAnchorKindWithNoCommittedSubjectIsNotServed(t *testing.T) {
	run := runDeploymentCohortWithAnchor(t, "")
	if run.discoverCalls != 0 {
		t.Fatalf("discover calls = %d, want 0", run.discoverCalls)
	}
	if run.saved != nil && run.saved.ScopeAnchor.KindSource != "" {
		t.Fatalf("no committed subject must derive nothing: %+v", run.saved.ScopeAnchor)
	}
}
