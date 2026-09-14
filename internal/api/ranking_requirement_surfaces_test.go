package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The memory store omits graph epochs. This fixture has one immutable graph
// binding at epoch zero, so the store adapter supplies that same epoch on reads.
// Production Postgres stores carry the epoch written by the engine.
type rankingSurfaceStore struct{ *memoryinvestigation.Store }

func (s rankingSurfaceStore) Get(ctx context.Context, p storage.Principal, id string) (cf.StoredInvestigationResult, error) {
	result, err := s.Store.Get(ctx, p, id)
	if err == nil {
		epoch := int64(0)
		result.GraphEpoch = &epoch
	}
	return result, err
}

type rankingSurfaceInterpreter struct{}

func (rankingSurfaceInterpreter) Interpret(context.Context, storage.Principal, cf.InvestigationRequest) (cf.InterpretedQuestion, cf.QuestionFamilyOutcome, error) {
	frame := cf.DeriveFrameObligations(cf.QuestionFrame{Goals: []cf.InvestigationGoal{cf.GoalRankOrSurvey}, SubjectExpression: cf.SubjectExpression{Kind: cf.SubjectExpressionDiscoveredKind, Discovered: &cf.DiscoveredSetExpression{MemberKind: cf.SubjectTeam}}, Temporal: cf.TemporalIntentCurrent, Version: cf.QuestionFrameVersion}, nil)
	validated := cf.ValidateFrame(frame, nil, cf.ShapeDiscoveredCohort)
	if validated.Outcome != cf.FrameValidationOutcomeValid {
		return cf.InterpretedQuestion{}, cf.QuestionFamilyOutcome{}, fmt.Errorf("invalid ranking fixture frame: %+v", validated.Failure)
	}
	frame = validated.Frame
	return cf.InterpretedQuestion{Shape: cf.ShapeDiscoveredCohort, RequestedJudgment: "ranking", WindowClass: cf.WindowClassTrendAssessment, WindowConfidence: cf.WindowConfidenceHigh, TimeContext: cf.TimeContext{Axis: cf.TemporalCurrent}, FactRequirements: []cf.FactRequirement{{Kind: cf.FactHealth}}}, cf.QuestionFamilyOutcome{Family: cf.QuestionFamilyDiscoveredCohortRanking, Source: cf.QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: cf.DecideFrameGate(validated, true)}, nil
}

type rankingSurfaceGraph struct {
	surfaceGraph
	cohort *cf.Cohort
}

func (g rankingSurfaceGraph) DiscoverContext(context.Context, storage.Principal, cf.GraphDiscoveryRequest) (cf.GraphContext, error) {
	return cf.GraphContext{Cohort: g.cohort, Coverage: cf.Coverage{Sources: []cf.SourceObservation{}, DegradedReasons: []string{}}}, nil
}

type rankingSurfaceSynthesizer struct{}

func (rankingSurfaceSynthesizer) Synthesize(context.Context, storage.Principal, cf.SynthesisInput) (cf.InvestigationResult, error) {
	return cf.InvestigationResult{Status: cf.InvestigationComplete, DirectJudgment: "Ranked.", CurrentState: "Bounded team ranking.", StrongestPressures: []string{}, Drivers: []cf.DriverJudgment{}, RemainingWork: []cf.Finding{}, ReadinessGaps: []cf.Finding{}, Paths: []cf.RelationshipPath{}, Conflicts: []cf.Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []cf.ClaimedFact{}, Coverage: cf.Coverage{Sources: []cf.SourceObservation{}, DegradedReasons: []string{}}, DeterministicAnswer: "Ranked.", Warnings: []string{}, Versions: cf.VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"}}, nil
}

func TestRankingRequirementSurvivesRealServingSurfaces(t *testing.T) {
	for _, haveFacts := range []bool{true, false} {
		t.Run(fmt.Sprintf("facts=%v", haveFacts), func(t *testing.T) {
			members := []cf.CohortMember{}
			bundle := cf.CanonicalFactBundle{Facts: []cf.CanonicalFact{}, Coverage: cf.Coverage{Sources: []cf.SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1"}
			for _, kind := range []cf.FactKind{cf.FactInvestment, cf.FactHealth, cf.FactOperationalDeficiencies, cf.FactReadiness, cf.FactWorkload} {
				bundle.Coverage.Sources = append(bundle.Coverage.Sources, cf.SourceObservation{Source: "canonical_fact:" + string(kind), State: cf.SourceAvailable})
			}
			for i := 1; i <= 2; i++ {
				subject := cf.SubjectRef{Kind: cf.SubjectTeam, CanonicalID: fmt.Sprintf("team:surface%d", i), Label: fmt.Sprintf("Team %d", i)}
				members = append(members, cf.CohortMember{Subject: subject, Rank: i, InclusionReasons: []string{"bounded cohort"}})
				if !haveFacts {
					continue
				}
				investment := map[string]cf.FactValue{cf.FactFieldThemeQualityBugfix: cf.NumberFactValue(0.05)}
				for _, theme := range []string{cf.ThemeFeatureDelivery, cf.ThemeOperational, cf.ThemeMaintenance, cf.ThemeQuality, cf.ThemeRisk} {
					investment[cf.FactFieldTheme(theme)] = cf.NumberFactValue(0.2)
				}
				for kind, fields := range map[cf.FactKind]map[string]cf.FactValue{
					cf.FactInvestment: investment, cf.FactHealth: {"severity": cf.StringFactValue("low")}, cf.FactOperationalDeficiencies: {"severity": cf.StringFactValue("high")}, cf.FactReadiness: {"estimate_coverage_ratio": cf.NumberFactValue(0.9)}, cf.FactWorkload: {"forecast_p50_days": cf.IntegerFactValue(8)},
				} {
					bundle.Facts = append(bundle.Facts, cf.CanonicalFact{Kind: kind, Subject: subject, Fields: fields})
				}
			}
			cohort := &cf.Cohort{Kind: cf.SubjectTeam, Members: members, Rationale: "bounded team cohort", Complete: true}
			graph := rankingSurfaceGraph{surfaceGraph: surfaceGraph{resolution: cf.SubjectResolution{Candidates: []cf.SubjectCandidate{}, Committed: []cf.SubjectRef{members[0].Subject}}}, cohort: cohort}
			registry, err := cf.NewFactCapabilityRegistry(devhealthfacts.NewProviders(nil), cf.FactRegistryOptions{})
			if err != nil {
				t.Fatal(err)
			}
			store := rankingSurfaceStore{memoryinvestigation.NewStore()}
			next := 0
			trace := &bytes.Buffer{}
			deps := cf.EngineDependencies{Telemetry: cf.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(trace, nil))), Interpreter: rankingSurfaceInterpreter{}, Graph: graph, Facts: liveFactReader{bundle: bundle}, Synthesizer: rankingSurfaceSynthesizer{}, Results: store, Requirements: registry}
			options := cf.EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(700, 0).UTC() }, NewResultID: func() string { next++; return fmt.Sprintf("result_ranking_surface_%02d", next) }}
			engine, err := cf.NewEngine(deps, options)
			if err != nil {
				t.Fatal(err)
			}
			app, token := newParityHostedApp(t, engine, store)
			server := httptest.NewTLSServer(app.Handler())
			defer server.Close()
			request := surfaceRequest("request_ranking_surface")
			request.Question = "Rank the team cohort"
			payload, _ := json.Marshal(request)
			req, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/context-fabric/investigations", bytes.NewReader(payload))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-ACR-Client-Version", "1.2.5")
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var composed cf.InvestigationResult
			if err := json.NewDecoder(response.Body).Decode(&composed); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != 200 {
				t.Fatalf("compose HTTP status=%d result=%+v", response.StatusCode, composed)
			}
			want := v1.ContextFabricRequirementSatisfied
			if !haveFacts {
				want = v1.ContextFabricRequirementUnavailable
			}
			var ranking v1.ContextFabricPlanRequirementOutcomeRow
			for _, row := range composed.Completeness.Outcomes {
				if row.Requirement == "ranking/member/team" && row.Stage == v1.ContextFabricOutcomeStageAssembledResult {
					if ranking.Requirement != "" {
						t.Fatal("duplicate ranking outcome")
					}
					ranking = row
				}
			}
			if ranking.Requirement == "" || ranking.Outcome != want {
				t.Fatalf("actual engine ranking=%+v want %s", ranking, want)
			}
			assertRows := func(label string, rows []v1.ContextFabricPlanRequirementOutcomeRow) {
				t.Helper()
				found := 0
				for _, row := range rows {
					if row.Requirement == ranking.Requirement && row.Stage == ranking.Stage {
						found++
						if !reflect.DeepEqual(row, ranking) {
							t.Fatalf("%s rewrote ranking: %+v", label, row)
						}
					}
				}
				if found != 1 {
					t.Fatalf("%s ranking rows=%d all=%+v", label, found, rows)
				}
			}
			byID := getRealAPIResult(t, server, token, composed.ResultID)
			assertRows("API by ID", byID.Completeness.Outcomes)
			configureSidecarEnvironment(t, server, token)
			boot, err := acrmcp.NewBootstrap(context.Background(), "1.2.5")
			if err != nil {
				t.Fatal(err)
			}
			mcpResult := callRealMCPInvestigationResult(t, boot, composed.ResultID)
			assertRows("MCP by ID", mcpResult.Completeness.Outcomes)
			for _, limit := range []int{2, 1} {
				apiProjection := getRealAPIProjection(t, server, token, composed.ResultID, 10, limit, 100)
				assertRows("API projection", apiProjection.Completeness.Outcomes)
				offered := callRealMCPInvestigateQuestion(t, boot, request.Question, 10, limit, 100)
				if offered.StructureNeeds == nil || len(offered.StructureNeeds.WindowOptions) == 0 {
					t.Fatalf("MCP did not offer a required window: %+v", offered)
				}
				storedOffer, err := store.Get(context.Background(), storage.Principal{OrgID: callerOrgID}, offered.ResultID)
				if err != nil {
					t.Fatal(err)
				}
				if storedOffer.SemanticStateRead != cf.SemanticStateReadAvailable {
					t.Fatalf("offer semantic state %s: %+v", storedOffer.SemanticStateRead, storedOffer.SemanticState)
				}
				receipt := v1.ContextFabricBoundSubjectReceipt{ResultID: offered.ResultID, ReceiptID: offered.StructureNeeds.WindowOptions[0].ReceiptID}
				mcpProjection := callRealMCPInvestigateQuestion(t, boot, request.Question, 10, limit, 100, receipt)
				if len(mcpProjection.Completeness.Outcomes) == 0 {
					t.Fatalf("MCP result never reached ranking: %+v TRACE=%s", mcpProjection, trace.String())
				}
				mcpCanonical := getRealAPIResult(t, server, token, mcpProjection.ResultID)
				var canonicalRank v1.ContextFabricPlanRequirementOutcomeRow
				for _, row := range mcpCanonical.Completeness.Outcomes {
					if row.Requirement == ranking.Requirement && row.Stage == ranking.Stage {
						canonicalRank = row
					}
				}
				if canonicalRank.Requirement == "" {
					t.Fatal("MCP execution did not persist an assembled ranking outcome")
				}
				// MCP forwards its cohort budget into execution. A one-member
				// request therefore narrows canonical scope before projection.
				wantMCP := want
				if haveFacts && limit == 1 {
					wantMCP = v1.ContextFabricRequirementNarrowed
					if canonicalRank.Impact != v1.ContextFabricAnswerImpactScope {
						t.Fatalf("MCP bounded execution lost scope narrowing: %+v", canonicalRank)
					}
				}
				if canonicalRank.Outcome != wantMCP {
					t.Fatalf("MCP ranking outcome=%+v want %s", canonicalRank, wantMCP)
				}
				count := 0
				for _, row := range mcpProjection.Completeness.Outcomes {
					if row.Requirement == canonicalRank.Requirement && row.Stage == canonicalRank.Stage {
						count++
						if !reflect.DeepEqual(row, canonicalRank) {
							t.Fatalf("MCP projection rewrote its canonical outcome: %+v", row)
						}
					}
				}
				if count != 1 {
					t.Fatalf("MCP projection ranking rows=%d", count)
				}
				if haveFacts && limit == 1 && apiProjection.ProjectionBudget.CohortMembersOmitted == 0 {
					t.Fatal("API projection did not exercise member narrowing")
				}
			}
			deps.ReuseGate = surfaceReuseGate{candidate: composed}
			reusedEngine, err := cf.NewEngine(deps, options)
			if err != nil {
				t.Fatal(err)
			}
			reused, err := reusedEngine.Investigate(context.Background(), storage.Principal{OrgID: callerOrgID}, request)
			if err != nil {
				t.Fatal(err)
			}
			if !reused.Reused {
				t.Fatal("the real reuse path did not run")
			}
			assertRows("reuse", reused.Completeness.Outcomes)
		})
	}
}
