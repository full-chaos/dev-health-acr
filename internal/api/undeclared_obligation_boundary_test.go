package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A cohort whose member kind no producer declares health for (pull requests,
// incidents). What an MCP client receives from the real engine, hosted route
// and MCP server: the answer is not degraded by health, the health requirement
// reads not_applicable, and the plan keeps the no_declaring_producer token.

// undeclaredObligationSynthesizer serves the bundle's own coverage and one
// claim per fact, so the read requirements are evaluated over served evidence.
type undeclaredObligationSynthesizer struct{ bundle cf.CanonicalFactBundle }

func (s undeclaredObligationSynthesizer) Synthesize(ctx context.Context, p storage.Principal, in cf.SynthesisInput) (cf.InvestigationResult, error) {
	result, err := rankingSurfaceSynthesizer{}.Synthesize(ctx, p, in)
	if err != nil {
		return result, err
	}
	result.Coverage = s.bundle.Coverage
	for i, fact := range s.bundle.Facts {
		for field, value := range fact.Fields {
			if value.String == nil {
				continue
			}
			text := *value.String
			result.ClaimedFacts = append(result.ClaimedFacts, cf.ClaimedFact{ClaimID: fmt.Sprintf("claim_undeclared_%02d", i+1), Kind: fact.Kind, Subject: fact.Subject, Field: field, Value: cf.ScalarValue{String: &text}})
		}
	}
	return result, nil
}

type undeclaredObligationInterpreter struct {
	frame  cf.QuestionFrame
	family cf.QuestionFamily
	fact   cf.FactKind
}

func (i undeclaredObligationInterpreter) Interpret(context.Context, storage.Principal, cf.InvestigationRequest) (cf.InterpretedQuestion, cf.QuestionFamilyOutcome, error) {
	frame := cf.DeriveFrameObligations(i.frame, nil)
	validated := cf.ValidateFrame(frame, nil, cf.ShapeDiscoveredCohort)
	if validated.Outcome != cf.FrameValidationOutcomeValid {
		return cf.InterpretedQuestion{}, cf.QuestionFamilyOutcome{}, fmt.Errorf("invalid fixture frame: %+v", validated.Failure)
	}
	frame = validated.Frame
	return cf.InterpretedQuestion{Shape: cf.ShapeDiscoveredCohort, RequestedJudgment: "status", WindowClass: cf.WindowClassTrendAssessment, WindowConfidence: cf.WindowConfidenceHigh, TimeContext: cf.TimeContext{Axis: cf.TemporalCurrent}, FactRequirements: []cf.FactRequirement{{Kind: i.fact}}},
		cf.QuestionFamilyOutcome{Family: i.family, Source: cf.QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: cf.DecideFrameGate(validated, true)}, nil
}

func TestAnUndeclaredHealthObligationDoesNotDegradeACohortAnswerAtTheClient(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   cf.SubjectKind
		fact   cf.FactKind
		field  string
		value  string
		family cf.QuestionFamily
		expr   cf.SubjectExpression
		// refuseGrouping serves team-only group rows for a question grouped by
		// project and leaves cohort.Groups to the engine, which then refuses.
		refuseGrouping bool
		// projectRows serves the pull requests' own project_breakdown rows
		// (canonical project ids), so the question is grouped by project.
		projectRows bool
	}{
		{"pull requests grouped by project", cf.SubjectPullRequest, cf.FactPullRequests, "state", "open", cf.QuestionFamilyGroupedCohortStatus,
			cf.SubjectExpression{Kind: cf.SubjectExpressionGroupedMembers, Grouped: &cf.GroupedSetExpression{MemberKind: cf.SubjectPullRequest, GroupKind: cf.SubjectProject}}, false, false},
		{"discovered incidents", cf.SubjectIncident, cf.FactIncidents, "status", "open", cf.QuestionFamilyDiscoveredCohortRanking,
			cf.SubjectExpression{Kind: cf.SubjectExpressionDiscoveredKind, Discovered: &cf.DiscoveredSetExpression{MemberKind: cf.SubjectIncident}}, false, false},
		{"pull requests grouped by project, team rows only", cf.SubjectPullRequest, cf.FactPullRequests, "state", "open", cf.QuestionFamilyGroupedCohortStatus,
			cf.SubjectExpression{Kind: cf.SubjectExpressionGroupedMembers, Grouped: &cf.GroupedSetExpression{MemberKind: cf.SubjectPullRequest, GroupKind: cf.SubjectProject}}, true, false},
		{"pull requests grouped by project, project rows on the facts", cf.SubjectPullRequest, cf.FactPullRequests, "state", "open", cf.QuestionFamilyGroupedCohortStatus,
			cf.SubjectExpression{Kind: cf.SubjectExpressionGroupedMembers, Grouped: &cf.GroupedSetExpression{MemberKind: cf.SubjectPullRequest, GroupKind: cf.SubjectProject}}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := cf.CanonicalFactBundle{Facts: []cf.CanonicalFact{}, Coverage: cf.Coverage{Sources: []cf.SourceObservation{{Source: "canonical_fact:" + string(tc.fact), State: cf.SourceAvailable}}, DegradedReasons: []string{}}, Version: "ops-v1"}
			if tc.kind == cf.SubjectPullRequest {
				// The project group axis reads these kinds; the fixture names
				// them available so the group requirement is evaluated at all.
				for _, kind := range []cf.FactKind{cf.FactFlow, cf.FactHealth, cf.FactInvestment, cf.FactLandscape, cf.FactReadiness, cf.FactWorkload} {
					bundle.Coverage.Sources = append(bundle.Coverage.Sources, cf.SourceObservation{Source: "canonical_fact:" + string(kind), State: cf.SourceAvailable})
				}
			}
			members := []cf.CohortMember{}
			for i := 1; i <= 2; i++ {
				subject := cf.SubjectRef{Kind: tc.kind, CanonicalID: fmt.Sprintf("%s:undeclared%d", tc.kind, i), Label: fmt.Sprintf("Member %d", i)}
				members = append(members, cf.CohortMember{Subject: subject, Rank: i, InclusionReasons: []string{"bounded cohort"}})
				bundle.Facts = append(bundle.Facts, cf.CanonicalFact{Kind: tc.fact, Subject: subject, Fields: map[string]cf.FactValue{tc.field: cf.StringFactValue(tc.value)}})
			}
			cohort := &cf.Cohort{Kind: tc.kind, Members: members, Rationale: "bounded cohort", Complete: true}
			if tc.refuseGrouping {
				// The only group rows the source carries name TEAMS, so a
				// project-grouped question has no axis to be grouped on.
				str := func(v string) cf.FactValue { return cf.StringFactValue(v) }
				for _, member := range members {
					bundle.Facts = append(bundle.Facts, cf.CanonicalFact{Kind: cf.FactHealth, Subject: member.Subject, Fields: map[string]cf.FactValue{
						"risk_breakdown": {Rows: []cf.FactValueRow{{Fields: map[string]cf.FactValue{"scope": str("team"), "scope_id": str("t1"), "scope_name": str("Platform")}}}},
					}})
				}
			}
			projectID := ""
			if tc.projectRows {
				var err error
				projectID, _, err = identity.Derive(identity.KindProject, []string{"jira", "P1"}, nil)
				if err != nil {
					t.Fatal(err)
				}
				for i := range bundle.Facts {
					if bundle.Facts[i].Kind != tc.fact {
						continue
					}
					bundle.Facts[i].Fields["project_breakdown"] = cf.TableFactValue(cf.FactTable{Shape: cf.FactTableBreakdown, Key: []string{"scope_id"}, Rows: []cf.FactValueRow{{Fields: map[string]cf.FactValue{
						"scope": cf.StringFactValue("project"), "scope_id": cf.StringFactValue(projectID), "scope_name": cf.StringFactValue("Payments"), "link_tier": cf.StringFactValue("native"),
					}}}})
				}
			}
			if tc.kind == cf.SubjectPullRequest && !tc.refuseGrouping && !tc.projectRows {
				ids := []string{members[0].Subject.CanonicalID, members[1].Subject.CanonicalID}
				cohort.Groups = []v1.ContextFabricCohortGroup{{Subject: cf.SubjectRef{Kind: cf.SubjectProject, CanonicalID: "project:undeclared-group", Label: "Group"}, MemberCanonicalIDs: ids, Total: len(ids), Complete: true}}
			}
			graph := rankingSurfaceGraph{surfaceGraph: surfaceGraph{resolution: cf.SubjectResolution{Candidates: []cf.SubjectCandidate{}, Committed: []cf.SubjectRef{members[0].Subject}}}, cohort: cohort}
			registry, err := cf.NewFactCapabilityRegistry(devhealthfacts.NewProviders(nil), cf.FactRegistryOptions{})
			if err != nil {
				t.Fatal(err)
			}
			store := rankingSurfaceStore{memoryinvestigation.NewStore()}
			deps := cf.EngineDependencies{
				Interpreter: undeclaredObligationInterpreter{frame: cf.QuestionFrame{Goals: []cf.InvestigationGoal{cf.GoalAssessState}, SubjectExpression: tc.expr, Temporal: cf.TemporalIntentCurrent, Version: cf.QuestionFrameVersion}, family: tc.family, fact: tc.fact},
				Graph:       graph, Facts: liveFactReader{bundle: bundle}, Synthesizer: undeclaredObligationSynthesizer{bundle: bundle}, Results: store, Requirements: registry,
			}
			options := cf.EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(700, 0).UTC() }, NewResultID: func() string { return "result_undeclared_obligation_01" }}
			engine, err := cf.NewEngine(deps, options)
			if err != nil {
				t.Fatal(err)
			}
			app, token := newParityHostedAppWithBudget(t, investigatorFunc(func(ctx context.Context, p storage.Principal, r cf.InvestigationRequest) (cf.InvestigationResult, error) {
				result, err := engine.Investigate(ctx, p, r)
				if err != nil {
					t.Logf("engine error: %v", err)
				}
				return result, err
			}), store, limits.ResourceBudget{MaxItems: 50, MaxTokens: 500_000, MaxBytes: 8 << 20})
			server := httptest.NewTLSServer(app.InstrumentedHandler(app.Handler()))
			defer server.Close()
			configureSidecarEnvironment(t, server, token)
			boot, err := acrmcp.NewBootstrap(context.Background(), "1.2.5")
			if err != nil {
				t.Fatalf("real MCP bootstrap: %v", err)
			}
			answer := callRealMCPTool(t, boot, "investigate_question", v1.MCPInvestigateQuestionRequest{
				Question:       "What is the state of the cohort?",
				EvidenceWindow: &v1.ContextFabricRequestedEvidenceWindow{RelativeID: v1.ContextFabricRelativeWindowTrailing90D},
			})
			var node struct {
				Structured struct {
					ResultID     string `json:"result_id"`
					Completeness struct {
						State    string `json:"state"`
						Outcomes []struct {
							Requirement string `json:"requirement"`
							Outcome     string `json:"outcome"`
							Stage       string `json:"stage"`
							Cause       string `json:"cause_coverage"`
						} `json:"outcomes"`
					} `json:"completeness"`
				} `json:"structured"`
			}
			if err := json.Unmarshal(answer.structured, &node); err != nil {
				t.Fatal(err)
			}
			health := "health/member/" + string(tc.kind)
			found := false
			var seen []string
			for _, row := range node.Structured.Completeness.Outcomes {
				seen = append(seen, row.Stage+":"+row.Requirement+"="+row.Outcome+"/"+row.Cause)
				if row.Requirement == health {
					found = true
					if row.Outcome != "not_applicable" || row.Cause != "fact_no_declaring_producer" {
						t.Fatalf("%s = %s/%s, want not_applicable naming fact_no_declaring_producer", health, row.Outcome, row.Cause)
					}
				}
			}
			if !found {
				t.Fatalf("no %s outcome row served: %v", health, seen)
			}
			if node.Structured.Completeness.State == "degraded" {
				t.Fatalf("completeness degraded by an obligation no producer declares: %v", seen)
			}
			if tc.refuseGrouping {
				groupRow := false
				for _, row := range node.Structured.Completeness.Outcomes {
					if row.Requirement == "state/group/project" && row.Stage == "assembled_result" {
						groupRow = true
						if row.Outcome != "not_applicable" || row.Cause != "fact_no_declaring_producer" {
							t.Fatalf("state/group/project = %s/%s, want not_applicable naming fact_no_declaring_producer", row.Outcome, row.Cause)
						}
					}
				}
				if !groupRow {
					t.Fatalf("no assembled state/group/project outcome row served: %v", seen)
				}
			}
			full := callRealMCPInvestigationResult(t, boot, node.Structured.ResultID)
			if tc.projectRows {
				groups := 0
				for _, group := range full.Cohort.Groups {
					if group.Subject.Kind == cf.SubjectProject && group.Subject.CanonicalID == projectID && group.Total == 2 {
						groups++
					}
				}
				if groups != 1 || len(full.Cohort.Groups) != 1 {
					t.Fatalf("cohort groups = %+v, want one project group %s of two members", full.Cohort.Groups, projectID)
				}
				for _, row := range node.Structured.Completeness.Outcomes {
					if row.Requirement == "state/group/project" && row.Outcome == "not_applicable" {
						t.Fatalf("a placed project axis is stated not_applicable: %v", seen)
					}
				}
			}
			disclosed := false
			if full.AnswerPlan != nil {
				for _, requirement := range full.AnswerPlan.Requirements {
					if requirement.Requirement == health && requirement.Unavailable == "no_declaring_producer" {
						disclosed = true
					}
				}
			}
			if !disclosed {
				t.Fatalf("plan requirement %s does not carry unavailable=no_declaring_producer", health)
			}
		})
	}
}
