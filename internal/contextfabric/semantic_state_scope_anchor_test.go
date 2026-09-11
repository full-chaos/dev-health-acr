package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// anchorRecordingGraph records the scope anchor subject resolution and
// discovery were asked to run under.
type anchorRecordingGraph struct {
	inner          graphReaderStub
	anchorKind     SubjectKind
	resolveCalls   int
	anchorResolved *bool
}

func (g *anchorRecordingGraph) ResolveInvestigationBinding(ctx context.Context, p storage.Principal) (ResolvedGraphBinding, error) {
	return g.inner.ResolveInvestigationBinding(ctx, p)
}

func (g *anchorRecordingGraph) ResolveSubjects(ctx context.Context, p storage.Principal, r InvestigationRequest, i InterpretedQuestion, b ResolvedGraphBinding, k *ConfirmedExpectedKind, a *ConfirmedAnchorSelection, f *QuestionFrame, anchor SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.resolveCalls++
	g.anchorKind = anchor
	return g.inner.ResolveSubjects(ctx, p, r, i, b, k, a, f, anchor)
}

func (g *anchorRecordingGraph) DiscoverContext(ctx context.Context, p storage.Principal, req GraphDiscoveryRequest) (GraphContext, error) {
	resolved := req.ScopeAnchorResolved
	g.anchorResolved = &resolved
	return g.inner.DiscoverContext(ctx, p, req)
}

// scopedAnchorInterpreter proposes a validated children_of_scope frame and a
// winning sample carrying the given scope anchor.
type scopedAnchorInterpreter struct {
	anchorKind SubjectKind
	anchorTerm string
	// noWinner models a fresh consensus that found no winning sample: the
	// index is -1 and the winning sample is its zero value.
	noWinner bool
}

func scopedAnchorFrame(t testing.TB) QuestionFrame {
	t.Helper()
	v := ValidateFrame(QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:   SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{AnchorTerms: []string{"the platform team"}, MemberKind: SubjectRepository},
		},
		Temporal: TemporalIntentCurrent,
	}, nil, ShapeOpen)
	if v.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("fixture defect: scoped frame invalid (%v)", v.Failure.Invariant)
	}
	return v.Frame
}

func (f scopedAnchorInterpreter) outcome(frame QuestionFrame) QuestionFamilyOutcome {
	if f.noWinner {
		return QuestionFamilyOutcome{
			Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame,
			Gate: FrameGate{Outcome: FrameGatePassed}, WinningSampleIndex: -1, Version: QuestionFamilyTableVersion,
		}
	}
	return QuestionFamilyOutcome{
		Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame,
		Gate:               FrameGate{Outcome: FrameGatePassed},
		WinningSampleIndex: 0,
		WinningSample:      FamilySample{ModelFamily: QuestionFamilyScopedCohortStatus, ScopeAnchorKind: f.anchorKind, ScopeAnchorTerm: f.anchorTerm},
		Version:            QuestionFamilyTableVersion,
	}
}

func (f scopedAnchorInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	v := ValidateFrame(QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:   SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{AnchorTerms: []string{"the platform team"}, MemberKind: SubjectRepository},
		},
		Temporal: TemporalIntentCurrent,
	}, nil, ShapeOpen)
	return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}},
		f.outcome(v.Frame), nil
}

// TestSemanticStateContinuation_TheScopeAnchorMovesWithTheCarriedFrame drives a
// real two-turn continuation whose carried reading is a children_of_scope frame
// resolved under a TEAM anchor. Whatever anchor this turn's sampler proposes,
// retrieval runs under the carried anchor, discovery sees the carried anchor's
// presence, the continued result's own snapshot records the carried anchor,
// and a fresh anchor that differs is DISCLOSED as a scope_anchor conflict --
// never reported as agreement.
func TestSemanticStateContinuation_TheScopeAnchorMovesWithTheCarriedFrame(t *testing.T) {
	t.Parallel()
	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyScopedCohortStatus, "")
	turnOne := scopedAnchorInterpreter{anchorKind: SubjectTeam, anchorTerm: "the platform team"}
	carried := BuildSemanticState(SemanticStateInput{
		Outcome:        turnOne.outcome(scopedAnchorFrame(t)),
		EmittedShape:   ShapeOpen,
		NarrowingBasis: prior.AnswerPlan.Budget.NarrowingBasis,
		FamilyVersion:  QuestionFamilyTableVersion,
	})
	if _, err := EncodeSemanticState(carried); err != nil {
		t.Fatalf("fixture defect: %v", err)
	}
	for _, tc := range []struct {
		name          string
		fresh         scopedAnchorInterpreter
		wantAgreement bool
	}{
		{"the fresh sample names the same anchor", scopedAnchorInterpreter{anchorKind: SubjectTeam, anchorTerm: "the platform team"}, true},
		{"the fresh sample names another anchor kind", scopedAnchorInterpreter{anchorKind: SubjectProject, anchorTerm: "the platform team"}, false},
		{"the fresh sample names no anchor at all", scopedAnchorInterpreter{}, false},
		// The carried anchor's PRESENCE must survive a fresh consensus with no
		// winner: discovery reads it off the carried reading, not off an index
		// this turn never produced.
		{"the fresh consensus found no winning sample", scopedAnchorInterpreter{noWinner: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &staticResultStore{
				results: map[string]InvestigationResult{prior.ResultID: prior},
				states:  map[string]*PersistedSemanticState{prior.ResultID: carried},
			}
			project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
			graph := &anchorRecordingGraph{inner: graphReaderStub{
				resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
				bases:      provenCommitBases(project),
			}}
			telemetry := &recordingTelemetry{}
			fresh := validInvestigationResult()
			engine := mustReuseTestEngine(t, EngineDependencies{
				Graph: graph,
				Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
					return CanonicalFactBundle{}, nil
				}),
				Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
					return fresh, nil
				}),
				Interpreter: tc.fresh,
				Results:     store,
				Telemetry:   telemetry,
			})
			if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
				t.Fatalf("Investigate: %v", err)
			}
			if len(telemetry.windowContinuationDecisions) != 1 {
				t.Fatalf("decisions = %d, want 1", len(telemetry.windowContinuationDecisions))
			}
			d := telemetry.windowContinuationDecisions[0]
			resolved := "not called"
			if graph.anchorResolved != nil {
				resolved = map[bool]string{true: "true", false: "false"}[*graph.anchorResolved]
			}
			saved := SemanticScopeAnchor{Kind: "<none>"}
			if store.savedSemantic != nil && store.savedSemantic.State != nil {
				saved = store.savedSemantic.State.ScopeAnchor
			}
			t.Logf("%s -> disposition=%s conflict_fields=%v agreement=%v | resolve anchor_kind=%q discovery scope_anchor_resolved=%s | saved snapshot anchor kind=%q term_present=%v",
				tc.name, d.Disposition, d.ConflictFieldTokens(), d.Agreement, graph.anchorKind, resolved, saved.Kind, saved.Term != "")
			if d.Disposition != ContinuationApplied {
				t.Fatalf("disposition = %s (%s), want applied", d.Disposition, d.Reason)
			}
			if graph.resolveCalls == 0 || graph.anchorKind != SubjectTeam {
				t.Errorf("subject resolution ran under anchor kind %q (calls=%d), want the CARRIED %q", graph.anchorKind, graph.resolveCalls, SubjectTeam)
			}
			if graph.anchorResolved == nil {
				t.Errorf("discovery was never called, so the carried anchor's presence was never observed")
			} else if !*graph.anchorResolved {
				t.Errorf("discovery saw scope_anchor_resolved=false, want the carried anchor's presence")
			}
			if saved != carried.ScopeAnchor {
				t.Errorf("the continued result saved anchor %+v, want the carried %+v", saved, carried.ScopeAnchor)
			}
			if d.Agreement != tc.wantAgreement {
				t.Errorf("agreement = %v, want %v", d.Agreement, tc.wantAgreement)
			}
			hasAnchor := false
			for _, field := range d.ConflictFields {
				if field == ContinuationConflictFieldScopeAnchor {
					hasAnchor = true
				}
			}
			if hasAnchor == tc.wantAgreement {
				t.Errorf("conflict_fields = %v: scope_anchor named=%v on a turn whose fresh anchor agreement=%v", d.ConflictFieldTokens(), hasAnchor, tc.wantAgreement)
			}
		})
	}
	_ = contractsv1.ContextFabricSubjectTeam
}
