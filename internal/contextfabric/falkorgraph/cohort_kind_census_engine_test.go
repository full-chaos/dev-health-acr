package falkorgraph

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// subjectlessGraphReader is the real adapter with subject resolution returning
// nothing committed, the resolution a population survey with no named member
// reaches. DiscoverContext, the census gate, the kind-scoped census and cohort
// assembly all run through the embedded *Adapter.
type subjectlessGraphReader struct {
	*Adapter
}

func (subjectlessGraphReader) ResolveSubjects(
	_ context.Context, _ storage.Principal, _ contextfabric.InvestigationRequest, _ contextfabric.InterpretedQuestion,
	_ contextfabric.ResolvedGraphBinding, _ *contextfabric.ConfirmedExpectedKind, _ *contextfabric.ConfirmedAnchorSelection,
	_ *contextfabric.QuestionFrame, _ contextfabric.SubjectKind,
) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}},
		contextfabric.StructureOfferMaterial{}, nil, nil, nil
}

// TestInvestigateTermFreeSurveyServesTheDeclaredKindsCohort drives a whole
// investigation for every servable kind the exact-name census does not fetch:
// the frame declares that member kind, no member matches a term, and the served
// document carries the kind's population as its cohort.
func TestInvestigateTermFreeSurveyServesTheDeclaredKindsCohort(t *testing.T) {
	t.Parallel()
	for _, kind := range uncensusedServableKinds(t) {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			store := &kindCensusStore{population: map[string]int{string(kind): 3, "team": 2, "repository": 1}}
			adapter := newFakeAdapterWithTelemetry(t, store.conn(t), &recordingTelemetry{})
			derived := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
				Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
				SubjectExpression: contextfabric.SubjectExpression{
					Kind:       contextfabric.SubjectExpressionDiscoveredKind,
					Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: kind},
				},
				Temporal: contextfabric.TemporalIntentCurrent,
				Version:  contextfabric.QuestionFrameVersion,
			}, nil)
			engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
				Interpreter: framedInterpreter{
					interpreted: contextfabric.InterpretedQuestion{
						Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "attention",
						TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
						FactRequirements: []contextfabric.FactRequirement{},
					},
					frame: &derived,
				},
				Graph:        subjectlessGraphReader{Adapter: adapter},
				Facts:        emptyFactReader{},
				Synthesizer:  countingSynthesizer{},
				Results:      discardingResultStore{},
				Requirements: productionRequirementDeriver{},
			}, contextfabric.EngineOptions{
				ServiceVersion: "acr-test",
				NewResultID:    func() string { return "result_56540001" },
			})
			if err != nil {
				t.Fatalf("NewEngine() error = %v", err)
			}
			request := contextfabric.InvestigationRequest{
				SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_56540001",
				Question: "which ones need a look",
				TimeContext: contextfabric.TimeContext{
					Axis:           contextfabric.TemporalCurrent,
					EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D},
				},
				Options: contextfabric.InvestigationOptions{
					MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 50,
					MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
				},
				Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
			}

			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, request)
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.Cohort == nil {
				t.Fatalf("status %q: the served document carries no cohort for a term-free %s survey", result.Status, kind)
			}
			if result.Cohort.Kind != kind {
				t.Errorf("served cohort kind = %q, want %q", result.Cohort.Kind, kind)
			}
			want := map[string]bool{}
			for i := 0; i < 3; i++ {
				want[kindCensusMemberID(string(kind), i)] = true
			}
			got := memberIDs(result.Cohort)
			if len(got) != len(want) {
				t.Fatalf("served members = %v, want the %d projected %s members", got, len(want), kind)
			}
			for _, id := range got {
				if !want[id] {
					t.Errorf("served member %q is not one of the projected %s members", id, kind)
				}
			}
			if len(store.singleKindQueries(kind)) == 0 {
				t.Error("the served cohort did not come from the kind-scoped census")
			}
		})
	}
}

// ambiguousCandidateGraphReader wraps the real *Adapter (so DiscoverContext,
// the census gate, the kind-scoped census and cohort assembly all run
// production code exactly like subjectlessGraphReader above) but reports TWO
// uncommitted, ambiguous subject candidates from ResolveSubjects -- driving
// engine.Investigate to a clarification_required terminal (resolveTerminalStatus,
// unresolved.go: non-empty Candidates + AllowClarification=true) instead of a
// served cohort answer.
type ambiguousCandidateGraphReader struct {
	*Adapter
	// candidateKind matches the frame's declared cohort member kind: CHAOS-5660
	// requires at least one offered option to carry the declared kind or the
	// turn is unsatisfiable (no_match) before resolveTerminalStatus ever
	// reaches its non-empty-Candidates clarification branch.
	candidateKind contextfabric.SubjectKind
}

func (a ambiguousCandidateGraphReader) ResolveSubjects(
	_ context.Context, _ storage.Principal, _ contextfabric.InvestigationRequest, _ contextfabric.InterpretedQuestion,
	_ contextfabric.ResolvedGraphBinding, _ *contextfabric.ConfirmedExpectedKind, _ *contextfabric.ConfirmedAnchorSelection,
	_ *contextfabric.QuestionFrame, _ contextfabric.SubjectKind,
) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	candidate := func(id, label string) contextfabric.SubjectCandidate {
		return contextfabric.SubjectCandidate{
			ReceiptID: "receipt_" + id,
			Subject:   contextfabric.SubjectRef{Kind: a.candidateKind, CanonicalID: string(a.candidateKind) + ":" + id, Label: label},
			State:     contextfabric.ResolutionProposed, MatchReasons: []string{"Fuzzy name match."},
			Confidence: 0.4, MatchedTerms: []string{label}, EvidenceRefIDs: []string{},
		}
	}
	return contextfabric.SubjectResolution{
			Candidates: []contextfabric.SubjectCandidate{candidate("a", "Candidate A"), candidate("b", "Candidate B")},
			Committed:  []contextfabric.SubjectRef{},
		},
		contextfabric.StructureOfferMaterial{}, nil, nil, nil
}

// TestInvestigateKindCensusTruncatedOnClarificationTerminal drives a whole
// investigation, through engine.Investigate at the production level, that
// ends in clarification_required rather than a served cohort: subject
// resolution is ambiguous (two uncommitted candidates), and every candidate
// of the census's declared kind is denied by authorization so no cohort is
// ever assembled (investigationSubjects returns empty, routing through
// terminalResult, unresolved.go -- the SAME subjectless path a clarification
// takes). CHAOS-5732 (D47): the kind census still ran and was cut, and the
// row must reach the CLARIFICATION terminal's own Coverage.Details -- not
// only a fully-served terminal, and not conditioned on a Cohort existing.
func TestInvestigateKindCensusTruncatedOnClarificationTerminal(t *testing.T) {
	t.Parallel()
	kind := uncensusedServableKinds(t)[0]
	store := &kindCensusStore{
		population:  map[string]int{string(kind): exactNameCandidateQueryLimit + 1, "team": 1},
		deniedKinds: map[string]bool{string(kind): true},
	}
	adapter := newFakeAdapterWithTelemetry(t, store.conn(t), &recordingTelemetry{})
	derived := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:       contextfabric.SubjectExpressionDiscoveredKind,
			Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: kind},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}, nil)
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: framedInterpreter{
			interpreted: contextfabric.InterpretedQuestion{
				Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "attention",
				TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				FactRequirements: []contextfabric.FactRequirement{},
			},
			frame: &derived,
		},
		Graph:        ambiguousCandidateGraphReader{Adapter: adapter, candidateKind: kind},
		Facts:        emptyFactReader{},
		Synthesizer:  countingSynthesizer{},
		Results:      discardingResultStore{},
		Requirements: productionRequirementDeriver{},
	}, contextfabric.EngineOptions{
		ServiceVersion: "acr-test",
		NewResultID:    func() string { return "result_57320002" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_57320002",
		Question: "which team is struggling",
		TimeContext: contextfabric.TimeContext{
			Axis:           contextfabric.TemporalCurrent,
			EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D},
		},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 25, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}

	// A principal with a non-wildcard RepositoryScopes is required for the
	// deniedKinds authorization check to actually deny -- the same principal
	// TestDiscoverContextWhollyDeniedClaimNeedsTheCohortKindsOwnCensus uses.
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"repo-allowed"}}
	result, err := engine.Investigate(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != contextfabric.InvestigationClarificationRequired {
		t.Fatalf("status = %q, want %q (offers: %d candidates)", result.Status, contextfabric.InvestigationClarificationRequired, len(result.SubjectResolution.Candidates))
	}
	if result.Cohort != nil {
		t.Fatalf("Cohort = %+v, want nil -- every %s member is denied", result.Cohort, kind)
	}
	detail, found := kindCensusTruncatedDetail(result.Coverage.Details)
	if !found {
		t.Fatalf("clarification result.Coverage.Details = %+v, want a kind_census_truncated row beside the offered candidates", result.Coverage.Details)
	}
	if detail.Kind != kind {
		t.Errorf("detail kind = %q, want %q", detail.Kind, kind)
	}
	if detail.Declared == nil || *detail.Declared != exactNameCandidateQueryLimit {
		t.Errorf("detail declared = %v, want %d", detail.Declared, exactNameCandidateQueryLimit)
	}
	if detail.Served == nil || *detail.Served != 0 {
		t.Errorf("detail served = %v, want 0 -- no cohort of this kind was served", detail.Served)
	}
	if !result.Coverage.Partial {
		t.Error("clarification result Coverage.Partial = false, want true")
	}
}
