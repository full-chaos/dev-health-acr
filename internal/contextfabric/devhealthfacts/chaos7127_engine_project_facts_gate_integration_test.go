package devhealthfacts_test

// CHAOS-7127 (codex review r1 of #691, finding P1b): a repository-restricted
// caller is admitted to a project through ONE granted repository its owning
// team reaches (design E.2). The project health fact joins ALL repositories
// of the owning teams, so on the ENGINE path the fact bundle handed to
// synthesis, and the stored and served result, carried the name, id and risk
// of a repository outside the grant. K2 extended (chris): remove rows and
// references of unseen subjects, keep a count, label the aggregate.
//
// Real Engine.Investigate over a real ClickHouse; only the graph and the
// model are controlled. The graph's per-subject decision is the fixture's
// stand-in for the FalkorDB adapter's shared predicate: the private
// repository's node is refused, everything else is admitted.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	chaos7127AllowedRepo = "acme/allowed"
	chaos7127PrivateRepo = "acme/private"
	chaos7127ProjectID   = "proj-pay"
	chaos7127TeamID      = "team-pay"
)

// chaos7127Graph reuses the cohort fixture (anchor Team Alpha, cohort of
// projects) and decides each subject: the private repository is refused to
// a caller that is not granted it; every other subject is admitted to a
// caller granted acme/allowed.
type chaos7127Graph struct {
	projectCohortGraph
	decided []contextfabric.SubjectRef
}

func (g *chaos7127Graph) AuthorizeStoredSubjects(_ context.Context, p storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	g.decided = append(g.decided, subjects...)
	outcomes := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for i, subject := range subjects {
		switch {
		case subject.CanonicalID == "repository:"+repoUUID(chaos7127PrivateRepo) && !slices.Contains(p.RepositoryScopes, chaos7127PrivateRepo):
			outcomes[i] = contextfabric.StoredSubjectDenied
		case slices.Contains(p.RepositoryScopes, chaos7127AllowedRepo):
			outcomes[i] = contextfabric.StoredSubjectAdmitted
		default:
			outcomes[i] = contextfabric.StoredSubjectDenied
		}
	}
	return outcomes, nil
}

func seedChaos7127(t *testing.T, ctx context.Context, direct interface {
	Exec(context.Context, string, ...any) error
}, orgID string) {
	t.Helper()
	// devhealthschema:not-a-production-replica rows are inserted into tables whose schema comes only from devhealthschema.DDL, never defined here
	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	workAt := ts(2026, 9, 18, 0, 0, 0)
	day := recentHealthDay(1)
	exec := func(what, sql string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	exec("project", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		chaos7127ProjectID, orgID, "linear", "PAY", "Payments", uint8(1), "active", "", epoch)
	exec("team", `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		chaos7127TeamID, "Payments Team", epoch, orgID, "linear", uint8(1))
	exec("team_project_ownership", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, "linear", chaos7127TeamID, chaos7127ProjectID, "PAY", "native", epoch, nil, epoch)
	exec("compounding_risk_daily team", `INSERT INTO compounding_risk_daily (org_id, day, scope, scope_id, compounding_risk, severity, computed_at) VALUES (?,?,?,?,?,?,?)`,
		orgID, day, "team", chaos7127TeamID, 0.40, "elevated", day.Add(6*time.Hour))
	for _, repo := range []string{chaos7127AllowedRepo, chaos7127PrivateRepo} {
		exec("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
			repoUUID(repo), orgID, repo, "github", workAt)
		exec("team_repo_ownership", `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", chaos7127TeamID, repoUUID(repo), repo, "exact", "native", uint8(1), uint16(100), int32(0), epoch, nil, epoch)
	}
	exec("compounding_risk_daily allowed", `INSERT INTO compounding_risk_daily (org_id, day, scope, scope_id, compounding_risk, severity, computed_at) VALUES (?,?,?,?,?,?,?)`,
		orgID, day, "repo", repoUUID(chaos7127AllowedRepo), 0.30, "low", day.Add(6*time.Hour))
	exec("compounding_risk_daily private", `INSERT INTO compounding_risk_daily (org_id, day, scope, scope_id, compounding_risk, severity, computed_at) VALUES (?,?,?,?,?,?,?)`,
		orgID, day, "repo", repoUUID(chaos7127PrivateRepo), 0.91, "high", day.Add(6*time.Hour))
}

type chaos7127Run struct {
	result contextfabric.InvestigationResult
	stored []byte
	model  *projectCohortModel
	graph  *chaos7127Graph
	logs   string
}

func runChaos7127(t *testing.T, ctx context.Context, grant []string) chaos7127Run {
	t.Helper()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	// devhealthschema:not-a-production-replica rows are inserted into tables whose schema comes only from devhealthschema.DDL, never defined here
	for _, statement := range devhealthschema.DDL(
		"projects", "team_project_ownership", "team_repo_ownership", "teams",
		"investment_metrics_daily", "capacity_forecasts", "estimate_coverage_metrics_daily",
		"compounding_risk_daily", "work_unit_investments", "work_unit_supersessions", "work_unit_membership_runs", "work_unit_membership", "repos", "work_item_team_attributions",
		"recommendations_daily", "work_items", "project_membership_transitions",
	) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	if err := direct.Exec(ctx, devhealthschema.ProjectMembershipPresenceViewDDL); err != nil {
		t.Fatalf("create view: %v", err)
	}
	orgID := sharedTestOrgID(t)
	seedChaos7127(t, ctx, direct, orgID)

	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	graph := &chaos7127Graph{projectCohortGraph: projectCohortGraph{t: t, members: []contextfabric.SubjectRef{projectSubject("linear", chaos7127ProjectID)}}}
	model := &projectCohortModel{}
	results := memoryinvestigation.NewStore()
	at := time.Now().UTC()
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter:  contextfabric.RuntimeQuestionInterpreter{Runtime: model, Requirements: registry},
		Graph:        graph,
		Facts:        registry,
		Requirements: registry,
		Telemetry:    contextfabric.NewSlogEngineTelemetry(logger),
		Synthesizer:  contextfabric.RuntimeAnswerSynthesizer{Runtime: model, Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "chaos7127", Backend: "graph", ProjectionVersion: "v1", QueryVersion: "v1"}},
		Results:      results,
		CandidateVerifier: func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (bool, contextfabric.CandidateVerificationReason) {
			return true, contextfabric.CandidateVerificationValid
		},
	}, contextfabric.EngineOptions{ServiceVersion: "chaos7127", Now: func() time.Time { return at }, NewResultID: func() string { return "result_chaos7127" }})
	if err != nil {
		t.Fatal(err)
	}
	principal := storage.Principal{OrgID: orgID, RepositoryScopes: grant}
	request := contextfabric.InvestigationRequest{SchemaVersion: contractsv1.ContextFabricInvestigationRequestSchema, RequestID: "request_chaos7127", Question: "Which projects owned by Team Alpha need the most attention?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}}, RequestedScope: contextfabric.RequestedScope{RepositorySlugs: []string{chaos7127AllowedRepo}}, Options: contractsv1.ContextFabricInvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1048576, AllowClarification: true}, Consumer: contractsv1.ContextFabricConsumerInfo{Name: "chaos7127", Version: "1.0.0", Surface: "workbench"}}
	result, err := engine.Investigate(ctx, principal, request)
	if err != nil {
		t.Fatalf("Engine.Investigate: %v\nlogs:\n%s", err, logBuffer.String())
	}
	var stored []byte
	if snapshot, err := results.Get(ctx, principal, result.ResultID); err == nil {
		stored, _ = json.Marshal(snapshot)
	}
	return chaos7127Run{result: result, stored: stored, model: model, graph: graph, logs: logBuffer.String()}
}

// riskRowsFor returns the scope_name values of the project's risk_breakdown
// rows in bundle.
func chaos7127RiskScopeNames(bundle contextfabric.CanonicalFactBundle) []string {
	var names []string
	for _, fact := range bundle.Facts {
		if fact.Kind != contextfabric.FactHealth || fact.Subject.Kind != contextfabric.SubjectProject {
			continue
		}
		table, ok := fact.Fields["risk_breakdown"]
		if !ok {
			continue
		}
		for _, row := range table.Rows {
			if cell, ok := row.Fields["scope_name"]; ok && cell.String != nil {
				names = append(names, *cell.String)
			}
		}
	}
	return names
}

func TestCHAOS7127EngineProjectHealthCarriesNoUngrantedRepository(t *testing.T) {
	ctx := context.Background()
	run := runChaos7127(t, ctx, []string{chaos7127AllowedRepo})

	names := chaos7127RiskScopeNames(run.model.facts)
	if !slices.Contains(names, chaos7127AllowedRepo) {
		t.Fatalf("precondition: the granted repository's risk row must reach synthesis; got %v\nlogs:\n%s", names, run.logs)
	}
	if len(run.stored) == 0 {
		t.Fatalf("precondition: the stored result must be readable so its payload is checked")
	}
	privateUUID := repoUUID(chaos7127PrivateRepo)
	synthesis, _ := json.Marshal(run.model.facts)
	served, _ := json.Marshal(run.result)
	for surface, payload := range map[string][]byte{"synthesis input": synthesis, "served result": served, "stored result": run.stored} {
		for _, secret := range []string{chaos7127PrivateRepo, privateUUID} {
			if bytes.Contains(payload, []byte(secret)) {
				t.Errorf("%s carries %q: a principal granted only %s received an ungranted repository's reference", surface, secret, chaos7127AllowedRepo)
			}
		}
	}
	if slices.Contains(names, chaos7127PrivateRepo) {
		t.Errorf("served project risk rows %v include %s", names, chaos7127PrivateRepo)
	}
	if strings.Contains(run.logs, privateUUID) || strings.Contains(run.logs, chaos7127PrivateRepo) {
		t.Errorf("logs name the withheld repository")
	}
	// K2 extended: the count is kept and the aggregate is labelled.
	health := chaos7127ProjectHealth(t, run.model.facts)
	if got := health.Fields["risk_breakdown"+contextfabric.FactFieldRowsWithheldByGrantSuffix]; got.Integer == nil || *got.Integer != 1 {
		t.Errorf("risk_breakdown rows withheld = %+v, want 1", got)
	}
	if got := health.Fields[contextfabric.FactFieldAggregateScope]; got.String == nil || *got.String != contextfabric.AggregateScopeAllOwnedRepositories {
		t.Errorf("aggregate_scope = %+v, want %q", got, contextfabric.AggregateScopeAllOwnedRepositories)
	}
	// The decision line carries the count and no id.
	if !strings.Contains(run.logs, contextfabric.EngineFactGateLogMessage) || !strings.Contains(run.logs, "decision=filtered") || !strings.Contains(run.logs, "rows_withheld=1") {
		t.Errorf("no filtered engine fact gate decision line with rows_withheld=1 in logs:\n%s", run.logs)
	}
}

// An unrestricted caller is not gated: it keeps every row, and no label is
// added. This pins that the gate is scoped to restricted callers.
func TestCHAOS7127EngineUnrestrictedCallerIsUnchanged(t *testing.T) {
	ctx := context.Background()
	run := runChaos7127(t, ctx, nil)
	names := chaos7127RiskScopeNames(run.model.facts)
	if !slices.Contains(names, chaos7127AllowedRepo) || !slices.Contains(names, chaos7127PrivateRepo) {
		t.Fatalf("unrestricted caller risk rows = %v, want both repositories", names)
	}
	health := chaos7127ProjectHealth(t, run.model.facts)
	for name := range health.Fields {
		if strings.HasSuffix(name, contextfabric.FactFieldRowsWithheldByGrantSuffix) || name == contextfabric.FactFieldAggregateScope || name == contextfabric.FactFieldReferencesWithheldByGrant {
			t.Errorf("unrestricted caller's fact carries gate label %q", name)
		}
	}
	if strings.Contains(run.logs, contextfabric.EngineFactGateLogMessage) {
		t.Errorf("an unrestricted caller's read wrote an engine fact gate decision")
	}
}

func chaos7127ProjectHealth(t *testing.T, bundle contextfabric.CanonicalFactBundle) contextfabric.CanonicalFact {
	t.Helper()
	for _, fact := range bundle.Facts {
		if fact.Kind == contextfabric.FactHealth && fact.Subject.Kind == contextfabric.SubjectProject {
			return fact
		}
	}
	t.Fatalf("no project health fact reached synthesis")
	return contextfabric.CanonicalFact{}
}
