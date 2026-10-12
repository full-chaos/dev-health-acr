package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A flat cohort listing whose answer, with the caller's member budget, holds
// more items than the plan allows, on a deployment where the cohort retry is
// not available. Members are the only thing that can be cut without a model;
// the answer is served partial with the cut named, not refused with a 413.

const (
	memberCutPopulation = 60
	memberCutCallerCap  = 50
	memberCutGaps       = 21
)

type memberCutInterpreter struct {
	outcome contextfabric.QuestionFamilyOutcome
}

func (i memberCutInterpreter) Interpret(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.QuestionFamilyOutcome, error) {
	return contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "status",
		TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}, i.outcome, nil
}

type memberCutDeriver struct{}

func (memberCutDeriver) DeriveRequirements(frame contextfabric.QuestionFrame) []contextfabric.DerivedRequirement {
	return contextfabric.DeriveRequirements(frame, contextfabric.GenerateObligationSeed(nil), nil)
}

type memberCutGraph struct{}

func (memberCutGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "member-cut", Epoch: 0}, nil
}

func (memberCutGraph) ResolveSubjects(context.Context, storage.Principal, contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion, contextfabric.ResolvedGraphBinding, *contextfabric.ConfirmedExpectedKind, *contextfabric.ConfirmedAnchorSelection, *contextfabric.QuestionFrame, contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}}, contextfabric.StructureOfferMaterial{}, nil, nil, nil
}

type memberCutFacts struct{}

func (memberCutFacts) ReadFacts(context.Context, storage.Principal, contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	return contextfabric.CanonicalFactBundle{
		Facts: []contextfabric.CanonicalFact{}, Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
		Version: "ops-v1", Versions: map[contextfabric.FactKind]string{}, Watermarks: map[contextfabric.FactKind]string{},
	}, nil
}

func (g memberCutGraph) DiscoverContext(_ context.Context, _ storage.Principal, request contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	members := make([]contextfabric.CohortMember, 0, memberCutPopulation)
	for i := 0; i < memberCutPopulation; i++ {
		id := fmt.Sprintf("project_%03d", i)
		members = append(members, contextfabric.CohortMember{
			Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: id, Label: id}, Rank: i + 1,
			InclusionReasons: []string{"Project the named team owns, reached from the team in the authorized Context Fabric graph."},
			EvidenceRefIDs:   []string{fmt.Sprintf("ref_member_%02d", i)},
		})
	}
	cohort := &contextfabric.Cohort{Kind: contextfabric.SubjectProject, Members: members, Rationale: "owned projects", Complete: true}
	if limit := request.Request.Options.MaxCohortMembers; limit > 0 && limit < len(members) {
		cohort.Members = members[:limit]
		cohort.Complete, cohort.Truncated, cohort.Population = false, true, memberCutPopulation
	}
	return contextfabric.GraphContext{
		Cohort: cohort, CohortPopulation: memberCutPopulation,
		Paths: []contextfabric.RelationshipPath{}, DriverCandidates: []contextfabric.DriverJudgment{},
		FactRequirements: []contextfabric.FactRequirement{}, EvidenceRefIDs: []string{},
		Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
	}, nil
}

type memberCutSynthesizer struct{}

func (memberCutSynthesizer) Synthesize(_ context.Context, _ storage.Principal, input contextfabric.SynthesisInput) (contextfabric.InvestigationResult, error) {
	gaps := make([]contextfabric.Finding, 0, memberCutGaps)
	for i := 0; i < memberCutGaps; i++ {
		gaps = append(gaps, contextfabric.Finding{FindingID: fmt.Sprintf("finding_gap_%02d", i), Kind: "relationship", Summary: "A readiness gap appears open.", EvidenceRefIDs: []string{"evidence_gap_01"}})
	}
	return contextfabric.InvestigationResult{
		Status: contextfabric.InvestigationComplete, DirectJudgment: "Fine.", CurrentState: "Nominal.",
		StrongestPressures: []string{}, Drivers: []contextfabric.DriverJudgment{}, RemainingWork: []contextfabric.Finding{},
		ReadinessGaps: gaps, Paths: []contextfabric.RelationshipPath{}, Conflicts: []contextfabric.Finding{},
		Limitations: []string{}, EvidenceRefIDs: []string{"evidence_gap_01"}, ClaimedFacts: []contextfabric.ClaimedFact{},
		Coverage: input.Facts.Coverage, DeterministicAnswer: "Fine, based on available context.", Warnings: []string{},
		Versions: contextfabric.VersionSet{
			Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
			InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
		},
	}, nil
}

func memberCutApp(t *testing.T) (*App, string) {
	t.Helper()
	frame := contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:   contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Platform"}, MemberKind: contextfabric.SubjectProject},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
	validated := contextfabric.ValidateFrame(frame, nil, contextfabric.ShapeDiscoveredCohort)
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: memberCutInterpreter{outcome: contextfabric.QuestionFamilyOutcome{
			Frame: &validated.Frame, Family: contextfabric.QuestionFamilyScopedCohortStatus, Source: contextfabric.QuestionFamilySourceModel,
		}},
		Graph: memberCutGraph{}, Facts: memberCutFacts{}, Synthesizer: memberCutSynthesizer{},
		Requirements: memberCutDeriver{},
	}, contextfabric.EngineOptions{
		ServiceVersion: "acr-test", MaxItems: 30,
		Now:         func() time.Time { return time.Unix(200, 0).UTC() },
		NewResultID: func() string { return "result_99999999" },
	})
	if err != nil {
		t.Fatal(err)
	}
	return newContextFabricTestApp(t, engine)
}

func TestAFlatCohortOverTheItemBudgetIsServedPartialNotRefusedWithA413(t *testing.T) {
	app, token := memberCutApp(t)
	body := investigationRequestBody()
	body.Question = "Which projects does the Platform team own?"
	body.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowTrailing90D}
	body.Options.MaxCohortMembers = memberCutCallerCap
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200: the members nothing cites can be cut", response.Code, response.Body.String())
	}
	var served contextfabric.InvestigationResult
	if err := json.Unmarshal(response.Body.Bytes(), &served); err != nil {
		t.Fatal(err)
	}
	items := contractsv1.CountContextFabricResultItems(served).Budgeted()
	if items > served.AnswerPlan.Budget.MaxItems {
		t.Fatalf("served %d items against %d", items, served.AnswerPlan.Budget.MaxItems)
	}
	if len(served.Cohort.Members) >= memberCutCallerCap || len(served.ReadinessGaps) != memberCutGaps {
		t.Fatalf("members %d, gaps %d: want fewer than %d members and every gap", len(served.Cohort.Members), len(served.ReadinessGaps), memberCutCallerCap)
	}
	if served.Cohort.Complete || !served.Cohort.Truncated {
		t.Fatalf("cohort complete=%v truncated=%v, want false/true", served.Cohort.Complete, served.Cohort.Truncated)
	}
	named := false
	for _, limitation := range served.Limitations {
		if contractsv1.IsContextFabricCohortNarrowingLimitation(limitation) && strings.Contains(limitation, "exceeded the item budget") {
			named = true
		}
	}
	if !named {
		t.Fatalf("the cut is not named in the limitations: %q", served.Limitations)
	}
}
