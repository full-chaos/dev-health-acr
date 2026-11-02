package devhealthfacts_test

// A landscape question through the real Engine.Investigate over a real
// ClickHouse and answerprojection.Project. The landscape facts come from the
// production provider; only graph discovery and the model are controlled.
// CHAOS-12533: 0 of 28 answers carried a landscape row on askdev-1, because a
// team fact gave the model no field to claim and two tables on one fact made
// the row attachment fail closed.

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

const servedLandscapeRepo = "acme/allowed"

type servedLandscapeGraph struct {
	t       *testing.T
	subject contextfabric.SubjectRef
}

func (*servedLandscapeGraph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	outcomes := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for i := range outcomes {
		outcomes[i] = contextfabric.StoredSubjectAdmitted
	}
	return outcomes, nil
}

func (*servedLandscapeGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "served-landscape", Epoch: 1}, nil
}

func (g *servedLandscapeGraph) ResolveSubjects(_ context.Context, p storage.Principal, r contextfabric.InvestigationRequest, _ contextfabric.InterpretedQuestion, _ contextfabric.ResolvedGraphBinding, _ *contextfabric.ConfirmedExpectedKind, _ *contextfabric.ConfirmedAnchorSelection, _ *contextfabric.QuestionFrame, _ contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	candidate, ok := graphrank.NodeCandidate(p, r.RequestedScope, "Platform", graphrank.CandidateNode{UUID: "subject-node", Name: "Platform", Attributes: map[string]interface{}{"subject_kind": string(g.subject.Kind), "canonical_id": g.subject.CanonicalID, "label": "Platform", "authorization_repositories": []string{servedLandscapeRepo}, "authorization_projects": "*", "authorization_teams": "*"}}, func(contextfabric.SubjectRef) bool { return false }, true, nil, r.RequestID)
	if !ok {
		g.t.Fatal("NodeCandidate rejected the subject")
	}
	candidate.State = contextfabric.ResolutionCommitted
	candidate.ReceiptID = "receipt_served_landscape"
	return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{candidate}, Committed: []contextfabric.SubjectRef{candidate.Subject}}, contextfabric.StructureOfferMaterial{}, nil, nil, nil
}

func (g *servedLandscapeGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	return contextfabric.GraphContext{
		Paths: []contextfabric.RelationshipPath{}, DriverCandidates: []contextfabric.DriverJudgment{},
		FactRequirements: []contextfabric.FactRequirement{}, EvidenceRefIDs: []string{},
		Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
	}, nil
}

// servedLandscapeModel claims one field of the landscape fact, as a model reads
// it: only from what the synthesis input carries as a scalar.
type servedLandscapeModel struct {
	claimField string
	offered    []string
	facts      []contextfabric.CanonicalFact
	draftErr   error
}

func (m *servedLandscapeModel) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	receipt := freshLiveReceipt(contextfabric.ModelOperationInterpret)
	receipt.QuestionFamily = contextfabric.QuestionFamilySubjectInvestigation
	receipt.QuestionFrame = &contextfabric.QuestionFrame{
		Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{"Platform"}}},
		Temporal:          contextfabric.TemporalIntentCurrent,
	}
	return contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, SubjectTerms: []string{"Platform"}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactLandscape}}}, receipt, nil
}

func scalarOfFactValue(value contextfabric.FactValue) (contextfabric.ScalarValue, bool) {
	switch {
	case value.String != nil:
		return contextfabric.ScalarValue{String: value.String}, true
	case value.Integer != nil:
		return contextfabric.ScalarValue{Integer: value.Integer}, true
	case value.Number != nil:
		return contextfabric.ScalarValue{Number: value.Number}, true
	}
	return contextfabric.ScalarValue{}, false
}

func (m *servedLandscapeModel) SynthesizeAnswer(_ context.Context, _ storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	draft := contextfabric.SynthesisDraft{Status: contextfabric.InvestigationComplete, DirectJudgment: "Landscape of the subject.", CurrentState: "Landscape of the subject.", DeterministicAnswer: "Landscape of the subject.", StrongestPressures: []string{}, RemainingWork: []contextfabric.Finding{}, ReadinessGaps: []contextfabric.Finding{}, Conflicts: []contextfabric.Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, Warnings: []string{}, Drivers: []contextfabric.DriverJudgment{}, ClaimedFacts: []contextfabric.ClaimedFact{}}
	for _, fact := range input.Facts.Facts {
		if fact.Kind != contextfabric.FactLandscape {
			continue
		}
		m.facts = append(m.facts, fact)
		for name, value := range fact.Fields {
			if _, scalar := scalarOfFactValue(value); scalar {
				m.offered = append(m.offered, name)
			}
		}
		value, scalar := scalarOfFactValue(fact.Fields[m.claimField])
		if !scalar {
			continue
		}
		draft.ClaimedFacts = append(draft.ClaimedFacts, contextfabric.ClaimedFact{ClaimID: "claim_served_landscape_01", Kind: fact.Kind, Subject: fact.Subject, Field: m.claimField, Value: value})
		draft.EvidenceRefIDs = append(draft.EvidenceRefIDs, fact.EvidenceRefIDs...)
		draft.Drivers = append(draft.Drivers, contextfabric.DriverJudgment{
			DriverID: "driver_served_landscape_1", Standing: contextfabric.DriverPrincipal, Category: "narrative",
			Title: "Landscape", Summary: "Landscape of the subject.",
			AffectedSubjects: []contextfabric.SubjectRef{fact.Subject}, EvidenceRefIDs: append([]string(nil), fact.EvidenceRefIDs...),
			ClaimedFactIDs: []string{"claim_served_landscape_01"},
			Derivation:     contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved, Confidence: 0.9, Current: true,
		})
		break
	}
	m.draftErr = draft.ValidateAgainst(input)
	return draft, freshLiveReceipt(contextfabric.ModelOperationSynthesize), nil
}

// servedLandscapeStore is a real ClickHouse with the landscape tables, and two
// identities on each of the three maps of the given team.
func servedLandscapeStore(t *testing.T, ctx context.Context, orgID string, teams ...string) contextpacket.ClickHouseQueryClient {
	t.Helper()
	rawQuery, direct := newDedicatedClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL("projects", "team_project_ownership", "ic_landscape_rolling_30d") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	day := time.Now().UTC().AddDate(0, 0, -2).Truncate(24 * time.Hour)
	synced := day.Add(-48 * time.Hour)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-sl", orgID, "github", "SL1", "Project", uint8(1), "active", "", synced)
	for _, team := range teams {
		exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "github", team, "proj-sl", "SL1", "native", synced, nil, synced)
		for _, mapName := range []string{"churn_throughput", "cycle_throughput", "wip_throughput"} {
			for _, person := range []struct {
				identity string
				churn    uint64
				delivery uint32
				cycle    float64
				wip      uint32
			}{{"alice", 100, 2, 5, 3}, {"bob", 50, 1, 7, 4}} {
				exec(`INSERT INTO ic_landscape_rolling_30d (org_id, repo_id, as_of_day, identity_id, team_id, map_name, x_raw, y_raw, x_norm, y_norm, churn_loc_30d, delivery_units_30d, cycle_p50_30d_hours, wip_max_30d, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
					orgID, repoUUID("repo-sl"), day, person.identity, team, mapName, 1.5, 2.5, 0.5, 0.5, person.churn, person.delivery, person.cycle, person.wip, day)
			}
		}
	}
	return statementthreads.API(rawQuery)
}

func serveLandscapeQuestion(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, orgID string, subject contextfabric.SubjectRef, claimField string) (contextfabric.InvestigationResult, *servedLandscapeModel) {
	t.Helper()
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	model := &servedLandscapeModel{claimField: claimField}
	at := time.Now().UTC()
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter:  contextfabric.RuntimeQuestionInterpreter{Runtime: model, Requirements: registry},
		Graph:        &servedLandscapeGraph{t: t, subject: subject},
		Facts:        registry,
		Requirements: registry,
		Synthesizer:  contextfabric.RuntimeAnswerSynthesizer{Runtime: model, Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "served-landscape", Backend: "graph", ProjectionVersion: "v1", QueryVersion: "v1"}},
		Results:      memoryinvestigation.NewStore(),
		CandidateVerifier: func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (bool, contextfabric.CandidateVerificationReason) {
			return true, contextfabric.CandidateVerificationValid
		},
	}, contextfabric.EngineOptions{ServiceVersion: "served-landscape", Now: func() time.Time { return at }, NewResultID: func() string { return "result_served_landscape" }})
	if err != nil {
		t.Fatal(err)
	}
	request := contextfabric.InvestigationRequest{SchemaVersion: contractsv1.ContextFabricInvestigationRequestSchema, RequestID: "request_served_landscape", Question: "What is the landscape of the Platform team?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}}, RequestedScope: contextfabric.RequestedScope{RepositorySlugs: []string{servedLandscapeRepo}}, Options: contractsv1.ContextFabricInvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1048576, AllowClarification: true}, Consumer: contractsv1.ContextFabricConsumerInfo{Name: "served-landscape", Version: "1.0.0", Surface: "workbench"}}
	result, err := engine.Investigate(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{servedLandscapeRepo}}, request)
	if err != nil {
		t.Fatalf("Engine.Investigate: %v", err)
	}
	return result, model
}

func servedLandscapeClaim(t *testing.T, result contextfabric.InvestigationResult, model *servedLandscapeModel) contractsv1.ContextFabricClaimedFact {
	t.Helper()
	if model.draftErr != nil {
		t.Fatalf("the draft does not validate: %v", model.draftErr)
	}
	for _, claim := range result.ClaimedFacts {
		if claim.Kind == contextfabric.FactLandscape {
			return claim
		}
	}
	t.Fatalf("no landscape claim was served. The model was offered the scalars %v; claims %+v, limitations %v", model.offered, result.ClaimedFacts, result.Limitations)
	return contractsv1.ContextFabricClaimedFact{}
}

func cellInteger(t *testing.T, row contractsv1.ContextFabricClaimedFactRow, column string) int64 {
	t.Helper()
	cell, ok := row.Fields[column]
	if !ok || cell.Integer == nil {
		t.Fatalf("row has no integer %q: %+v", column, row.Fields)
	}
	return *cell.Integer
}

// The numbers of a team's one row reach the model as scalars and the answer as
// a row, for a team of every provider. The attribution contract tests the
// provider × entity matrix: a team id carries its provider prefix.
func TestAClaimOnATeamLandscapeFieldCarriesTheTeamsRow(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-landscape-served-team"
	teams := []string{"jira:tm-sl", "gh:tm-sl", "gl:tm-sl", "linear:TMSL"}
	query := servedLandscapeStore(t, ctx, orgID, teams...)
	for _, team := range teams {
		t.Run(strings.SplitN(team, ":", 2)[0], func(t *testing.T) {
			result, model := serveLandscapeQuestion(t, ctx, query, orgID, teamSubject(team), "delivery_units_30d")
			claim := servedLandscapeClaim(t, result, model)
			if claim.Value.Integer == nil || *claim.Value.Integer != 3 {
				t.Fatalf("claimed delivery units = %+v, want 3 (alice 2 + bob 1, once)", claim.Value)
			}
			if len(claim.Rows) != 1 {
				t.Fatalf("claim rows = %d, want the team's one row", len(claim.Rows))
			}
			if claim.Table == nil || claim.Table.Field != "area_breakdown" {
				t.Fatalf("claim table = %+v, want the declared area_breakdown", claim.Table)
			}
			row := claim.Rows[0]
			if got := cellInteger(t, row, "delivery_units_30d"); got != *claim.Value.Integer {
				t.Fatalf("the row says %d and the scalar %d: they must describe one observation", got, *claim.Value.Integer)
			}
			if got := cellInteger(t, row, "churn_loc_30d"); got != 150 {
				t.Fatalf("churn_loc_30d = %d, want 150 (one copy of each identity)", got)
			}
			if got := cellInteger(t, row, "identity_count"); got != 2 {
				t.Fatalf("identity_count = %d, want 2", got)
			}
			// A missing cycle is stored as 0 (CHAOS-12562), so cycle is held out
			// with WIP (CHAOS-12565).
			for _, absent := range []string{"wip_max_30d", "cycle_p50_30d_hours_avg", "map_name", "x_raw_mean", "y_raw_mean", "identity_id", "x_norm", "y_norm"} {
				if _, present := row.Fields[absent]; present {
					t.Fatalf("the answer's row carries %q: %+v", absent, row.Fields)
				}
				if slices.Contains(claim.Table.Measures, absent) {
					t.Fatalf("the served declaration names %q: %+v", absent, claim.Table)
				}
				if slices.Contains(model.offered, absent) {
					t.Fatalf("the model was offered %q: %v", absent, model.offered)
				}
			}
			// Each scalar a model reads is the value of the row the claim carries.
			facts := model.facts
			if len(facts) != 1 {
				t.Fatalf("landscape facts at synthesis = %d, want 1", len(facts))
			}
			for _, name := range []string{"identity_count", "churn_loc_30d", "delivery_units_30d", "as_of_day"} {
				scalar, ok := scalarOfFactValue(facts[0].Fields[name])
				if !ok {
					t.Fatalf("the model cannot read %s: offered %v", name, model.offered)
				}
				cell := row.Fields[name]
				if !reflect.DeepEqual(scalar, cell) {
					t.Fatalf("%s: scalar %+v, row %+v", name, scalar, cell)
				}
			}
			projection := answerprojection.Project(result, answerprojection.DefaultBudget)
			if !strings.Contains(projection.DirectJudgment+projection.CurrentState, "Landscape") {
				t.Fatalf("the projection lost the answer: %+v", projection)
			}
		})
	}
}

// A project fact holds team_breakdown beside the deprecated table, and a claim
// on any of its scalars carries the teams' rows.
func TestAClaimOnAProjectLandscapeFieldCarriesItsTeams(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-landscape-served-project"
	query := servedLandscapeStore(t, ctx, orgID, "gh:tm-a", "gh:tm-b")
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:github:proj-sl", Label: "Platform"}
	result, model := serveLandscapeQuestion(t, ctx, query, orgID, project, "team_count")
	claim := servedLandscapeClaim(t, result, model)
	if len(claim.Rows) != 2 {
		t.Fatalf("claim rows = %d, want one per team; table %+v", len(claim.Rows), claim.Table)
	}
	if claim.Table == nil || claim.Table.Field != "team_breakdown" {
		t.Fatalf("claim table = %+v, want the declared team_breakdown", claim.Table)
	}
	for _, row := range claim.Rows {
		if got := cellInteger(t, row, "delivery_units_30d"); got != 3 {
			t.Fatalf("delivery_units_30d = %d, want 3", got)
		}
		for _, absent := range []string{"wip_max_30d", "cycle_p50_30d_hours_avg", "map_name", "x_raw_mean", "identity_id"} {
			if _, present := row.Fields[absent]; present {
				t.Fatalf("the answer's row carries %q: %+v", absent, row.Fields)
			}
		}
	}
}
