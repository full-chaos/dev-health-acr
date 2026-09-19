package devhealthfacts_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// The Engine, membership adapter, registry, v0.7 shared readers, schema and
// ClickHouse client are real. Only project resolution and model drafts are
// controlled. Every member below must originate in the actual S1 SQL stream.
// This is fresh Engine dispatch evidence, not an HTTP, MCP or graph-server test.
func TestWorkItemFreshEngineAgainstRealClickHouse(t *testing.T) {
	at := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	const repo = "70000000-0000-4000-8000-000000000001"
	const otherRepo = "70000000-0000-4000-8000-000000000002"
	type scenario struct {
		name, provider, project, failure string
		rows, cap                        int
		transitions, excluded, move      bool
	}
	cases := []scenario{
		{name: "K200_exact_201", provider: "linear", project: "P1", rows: 201, cap: 234},
		{name: "request_cap_7", provider: "linear", project: "P1", rows: 201, cap: 7},
		{name: "floor_C_plus_1", provider: "linear", project: "P1", rows: 2001, cap: 234},
		{name: "floor_C_plus_2", provider: "linear", project: "P1", rows: 2002, cap: 234},
		{name: "measured_zero", provider: "linear", project: "P1"},
		{name: "status_server_error", provider: "linear", project: "P1", rows: 3, failure: "status"},
		{name: "title_server_error", provider: "linear", project: "P1", rows: 3, failure: "work"},
		{name: "S1_server_error", provider: "linear", project: "P1", rows: 3, failure: "s1"},
		{name: "current_repo_moves_after_S1", provider: "linear", project: "P1", rows: 3, move: true},
	}
	for _, provider := range []string{"linear", "jira", "github", "gitlab"} {
		project := "P1"
		if provider == "github" {
			project = "ghprojv2:P1"
		}
		cases = append(cases, scenario{name: provider + "_M1_M2_M3", provider: provider, project: project, transitions: true, excluded: provider == "gitlab"})
	}
	cases = append(cases,
		scenario{name: "gitlab_M4_no_assertions", provider: "gitlab", project: "P1", excluded: true},
		scenario{name: "github_legacy_M4_no_assertions", provider: "github", project: "P1", excluded: true},
		scenario{name: "github_legacy_M4_assertions", provider: "github", project: "P1", excluded: true, transitions: true})
	for _, phase := range []string{"s1", "status", "work"} {
		for _, failure := range []string{"scan", "err"} {
			cases = append(cases, scenario{name: phase + "_" + failure, provider: "linear", project: "P1", rows: 3, failure: phase + "_" + failure})
		}
	}
	var floorMembers []string
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			query, direct := freshLiveDatabase(t)
			org := sharedTestOrgID(t)
			seed := func(sql string, args ...any) {
				t.Helper()
				if err := direct.Exec(context.Background(), sql, args...); err != nil {
					t.Fatal(err)
				}
			}
			seed(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?), (?, ?, ?, ?, ?)`, repo, org, "acme/allowed", tc.provider, at, otherRepo, org, "acme/other", tc.provider, at)
			seed(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?)`, tc.project, org, tc.provider, tc.project, "Project Alpha", uint8(1), "active", "", at, "OTHER", org, tc.provider, "OTHER", "Other project", uint8(1), "active", "", at)
			work := func(id, repoID, rowOrg, project string) {
				seed(`INSERT INTO work_items (work_item_id, repo_id, org_id, provider, project_id, title, status, created_at, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, repoID, rowOrg, tc.provider, project, "Title "+id, "open", at, at, at)
			}
			transition := func(id, from, to string, when time.Time) {
				seed(`INSERT INTO project_membership_transitions (org_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, org, repo, "work_item", id, tc.provider, from, to, from, to, "fixture", when, at, "event-"+id)
			}
			// Deliberately reverse insertion order. Expectations use independent
			// canonical identity construction and lexical sort, never S1's output.
			expected := []string{}
			for start := tc.rows; start > 0; {
				end := start - 250
				if end < 0 {
					end = 0
				}
				values := []string{}
				args := []any{}
				for i := start - 1; i >= end; i-- {
					id := fmt.Sprintf("WI-%04d", i)
					values = append(values, "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
					args = append(args, id, repo, org, tc.provider, tc.project, "Title "+id, "open", at, at, at)
					expected = append(expected, workItemSubject(repo, id).CanonicalID)
				}
				seed(`INSERT INTO work_items (work_item_id, repo_id, org_id, provider, project_id, title, status, created_at, updated_at, last_synced) VALUES `+strings.Join(values, ","), args...)
				start = end
			}
			if tc.transitions {
				for _, id := range []string{"self", "future", "first-removal", "column"} {
					work(id, repo, org, tc.project)
				}
				transition("self", tc.project, tc.project, at.Add(-time.Hour))
				transition("future", "", tc.project, at.Add(time.Hour))
				transition("first-removal", tc.project, "OTHER", at.Add(-time.Hour))
				if !tc.excluded {
					for _, id := range []string{"self", "future", "column"} {
						expected = append(expected, workItemSubject(repo, id).CanonicalID)
					}
				}
			}
			if tc.excluded && !tc.transitions {
				work("column", repo, org, tc.project)
			}
			// All three must be excluded by the production population predicate.
			work("wrong-project", repo, org, "OTHER")
			work("denied", otherRepo, org, tc.project)
			work("foreign-org", repo, org+"-foreign", tc.project)
			sort.Strings(expected)
			population := len(expected)
			cap := tc.cap
			if cap == 0 {
				cap = 50
			}
			retain := cap
			if retain > 200 {
				retain = 200
			}
			if retain > len(expected) {
				retain = len(expected)
			}
			expected = expected[:retain]
			client := &freshLiveQuery{inner: query, failure: tc.failure, t: t}
			if tc.move {
				client.beforeStatus = func() {
					seed(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repo, org, "acme/other", tc.provider, at.Add(time.Minute))
				}
			}
			fixture := newFreshLiveEngine(t, client, org, tc.provider, tc.project, at, cap)
			result, err := fixture.engine.Investigate(context.Background(), fixture.principal, fixture.request)
			if err != nil {
				t.Fatalf("Engine.Investigate: %v; statements=%v", err, client.phases)
			}
			if fixture.graph.resolve != 1 || fixture.graph.discover != 0 || fixture.telemetry.ranked != 0 || fixture.expander.calls != 0 {
				t.Fatalf("dispatch resolve=%d discover=%d ranked=%d expanded=%d", fixture.graph.resolve, fixture.graph.discover, fixture.telemetry.ranked, fixture.expander.calls)
			}
			failurePhase := strings.Split(tc.failure, "_")[0]
			unmeasured := tc.excluded || failurePhase == "s1"
			phases := []string{"s1"}
			if !unmeasured && population > 0 {
				phases = append(phases, "status", "work")
			}
			if !reflect.DeepEqual(client.phases, phases) {
				t.Fatalf("statements=%v want=%v", client.phases, phases)
			}
			for _, sql := range client.statements {
				for _, setting := range []string{"max_execution_time", "max_rows_to_read", "max_memory_usage", "max_result_rows", "read_overflow_mode = 'throw'", "result_overflow_mode = 'throw'"} {
					if !strings.Contains(sql, setting) {
						t.Errorf("statement missing %s", setting)
					}
				}
			}
			if strings.Contains(tc.failure, "_") && !client.injected {
				t.Fatal("requested stream failure was never measured")
			}
			d47 := 0
			for _, detail := range result.Coverage.Details {
				if detail.Code == contractsv1.ContextFabricCoverageDetailKindCensusTruncated {
					d47++
					if detail.Kind != contextfabric.SubjectWorkItem || detail.Declared == nil || *detail.Declared != 2000 || detail.Served == nil || *detail.Served != retain {
						t.Fatalf("D47=%+v", detail)
					}
				}
			}
			wantD47 := 0
			if population > 2000 && !unmeasured {
				wantD47 = 1
			}
			if d47 != wantD47 {
				t.Fatalf("D47 rows=%d want=%d", d47, wantD47)
			}
			if tc.cap == 234 {
				found := false
				for _, step := range result.AnswerPlan.Narrowing {
					if step.Before == 234 && step.After == 200 && step.Basis == contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical {
						found = true
					}
				}
				if !found {
					t.Errorf("missing 234 to 200 lexical narrowing: %+v", result.AnswerPlan)
				}
			}
			if fixture.membership.calls != 1 {
				t.Fatalf("S1 calls=%d", fixture.membership.calls)
			}
			census := fixture.membership.result.Census
			stored, err := fixture.store.Get(context.Background(), fixture.principal, result.ResultID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.SemanticState == nil || stored.SemanticState.WorkItemCensus == nil {
				t.Fatalf("missing persisted census: %+v", stored)
			}
			persisted := stored.SemanticState.WorkItemCensus
			if unmeasured {
				if result.Cohort != nil || census.State != contextfabric.WorkItemMembershipCensusUnmeasured || persisted.State != contextfabric.WorkItemMembershipCensusUnmeasured {
					t.Fatalf("unmeasured result cohort=%+v census=%+v persisted=%+v", result.Cohort, census, persisted)
				}
				if !containsFreshLive(result.Limitations, contractsv1.ContextFabricFactScopeUnexpandedLimitation) {
					t.Fatal("unmeasured limitation absent")
				}
				if tc.excluded && tc.transitions && census.TransitionAssertionCount != 2 {
					t.Fatalf("M4 transition assertions=%d", census.TransitionAssertionCount)
				}
				return
			}
			if population == 0 {
				// The fixture always seeds one member the principal may not read, so a
				// measured population of zero authorized is a disclosed gap, not an
				// empty project.
				if result.Cohort != nil || result.Status != contextfabric.InvestigationDegraded {
					t.Fatalf("all-denied census served status=%q cohort=%+v", result.Status, result.Cohort)
				}
				if persisted.Value != 0 || persisted.Retained != 0 || census.DeniedPopulation != 1 {
					t.Fatalf("all-denied census=%+v persisted=%+v", census, persisted)
				}
				gapDisclosed := false
				for _, limitation := range result.Limitations {
					gapDisclosed = gapDisclosed || strings.Contains(limitation, "1 work items were observed and none are authorized")
				}
				if !gapDisclosed {
					t.Fatalf("denied population not disclosed: %q", result.Limitations)
				}
				return
			}
			if result.Cohort == nil {
				t.Fatal("completed S1 lost cohort")
			}
			ids := []string{}
			for _, member := range result.Cohort.Members {
				ids = append(ids, member.Subject.CanonicalID)
				if member.RankingComputed {
					t.Fatal("tuple member was ranked")
				}
				parts, ok := identity.Segments(identity.KindWorkItem, member.Subject.CanonicalID)
				if !ok {
					t.Fatal("invalid member identity")
				}
				label := "Title " + parts[1]
				if failurePhase == "work" || tc.move {
					label = parts[1]
				}
				if member.Subject.Label != label {
					t.Errorf("label=%q want=%q", member.Subject.Label, label)
				}
			}
			if !reflect.DeepEqual(ids, expected) {
				t.Fatalf("retained identities=%v want=%v", ids, expected)
			}
			state := contextfabric.WorkItemMembershipCensusExact
			value := population
			if population > 2000 {
				state = contextfabric.WorkItemMembershipCensusFloor
				value = 2000
			}
			if census.State != state || persisted.State != state || persisted.Value != value || persisted.Retained != retain {
				t.Fatalf("census=%+v persisted=%+v want state=%s value=%d retained=%d", census, persisted, state, value, retain)
			}
			if tc.transitions && (census.FutureBoundaryCount != 1 || census.TransitionAssertionCount != 2) {
				t.Fatalf("M1/M2/M3 census=%+v", census)
			}
			if strings.HasPrefix(tc.name, "floor_") {
				if floorMembers == nil {
					floorMembers = ids
				} else if !reflect.DeepEqual(floorMembers, ids) {
					t.Fatal("C+2 changed retained identities")
				}
			}
			counts := map[contextfabric.FactKind]int{}
			factIDs := map[contextfabric.FactKind]map[string]bool{contextfabric.FactStatus: {}, contextfabric.FactWork: {}}
			retained := map[string]bool{}
			refs := map[string]bool{}
			for _, id := range expected {
				retained[id] = true
				parts, _ := identity.Segments(identity.KindWorkItem, id)
				refs[contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, parts[0]+":"+parts[1])] = true
			}
			for _, fact := range fixture.model.facts.Facts {
				if !retained[fact.Subject.CanonicalID] {
					t.Fatalf("fact outside retained M: %+v", fact)
				}
				if fact.Kind != contextfabric.FactStatus && fact.Kind != contextfabric.FactWork {
					t.Fatalf("unexpected fact kind %s", fact.Kind)
				}
				if factIDs[fact.Kind][fact.Subject.CanonicalID] {
					t.Fatalf("duplicate %s fact for %s", fact.Kind, fact.Subject.CanonicalID)
				}
				factIDs[fact.Kind][fact.Subject.CanonicalID] = true
				parts, _ := identity.Segments(identity.KindWorkItem, fact.Subject.CanonicalID)
				field, wantValue := "status", "open"
				if fact.Kind == contextfabric.FactWork {
					field, wantValue = "title", "Title "+parts[1]
				}
				if value := fact.Fields[field].String; value == nil || *value != wantValue {
					t.Fatalf("%s value for %s=%+v want=%q", field, fact.Subject.CanonicalID, fact.Fields[field], wantValue)
				}
				counts[fact.Kind]++
				for _, ref := range fact.EvidenceRefIDs {
					if !refs[ref] {
						t.Fatalf("foreign evidence %q", ref)
					}
				}
			}
			for _, kind := range []contextfabric.FactKind{contextfabric.FactStatus, contextfabric.FactWork} {
				want := retain
				if tc.move || failurePhase == string(kind) {
					want = 0
				}
				if want > 0 {
					for _, id := range expected {
						if !factIDs[kind][id] {
							t.Errorf("%s fact absent for retained %s", kind, id)
						}
					}
				}
				if counts[kind] != want {
					t.Errorf("%s facts=%d want=%d", kind, counts[kind], want)
				}
			}
			if failurePhase == "status" || failurePhase == "work" {
				if !fixture.model.facts.Coverage.Partial {
					t.Fatal("content failure did not degrade coverage")
				}
			}
			for _, candidate := range result.SubjectResolution.Candidates {
				if len(candidate.EvidenceRefIDs) > 0 {
					t.Fatal("anchor retained foreign evidence")
				}
			}
			for ref := range result.EvidenceRefLabels {
				if !refs[ref] {
					t.Fatalf("evidence label outside M: %q", ref)
				}
			}
			for _, ref := range result.EvidenceRefIDs {
				if !refs[ref] {
					t.Fatalf("result reference outside M: %q", ref)
				}
			}
			t.Logf("fresh dispatch: state=%s population=%d retained=%d status=%d title=%d queries=%v", state, value, retain, counts[contextfabric.FactStatus], counts[contextfabric.FactWork], client.phases)
		})
	}
}

// Each scenario has its own physical database on the package's existing
// container. Organization filtering alone does not isolate physical read
// ceilings: small parts from prior scenarios can be read before filtering.
func freshLiveDatabase(t *testing.T) (*runtimeclickhouse.Client, clickhousedriver.Conn) {
	t.Helper()
	_, control := sharedClickHouseFixture(t)
	database := fmt.Sprintf("fresh_%x", sha256.Sum256([]byte(t.Name())))
	ctx := context.Background()
	if err := control.Exec(ctx, "CREATE DATABASE "+database); err != nil {
		t.Fatal(err)
	}
	address := sharedClickHouseAddrFor(t)
	direct, err := clickhousedriver.Open(&clickhousedriver.Options{Addr: []string{address}, Auth: clickhousedriver.Auth{Database: database, Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := direct.Close(); err != nil {
			t.Error(err)
		}
	})
	query, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{DSN: "clickhouse://acr:acr@" + address + "/" + database, DialTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := query.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, ddl := range devhealthschema.DDL("repos", "work_items", "projects", "project_membership_transitions") {
		if err := direct.Exec(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	if err := direct.Exec(ctx, devhealthschema.ProjectMembershipPresenceViewDDL); err != nil {
		t.Fatal(err)
	}
	return query, direct
}

func containsFreshLive(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type freshLiveQuery struct {
	t                  *testing.T
	inner              contextpacket.ClickHouseQueryClient
	phases, statements []string
	failure            string
	injected           bool
	beforeStatus       func()
}

func (c *freshLiveQuery) Query(ctx context.Context, sql string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	phase := "s1"
	if strings.Contains(sql, "w.status") {
		phase = "status"
	}
	if strings.Contains(sql, "w.title") {
		phase = "work"
	}
	c.phases = append(c.phases, phase)
	c.statements = append(c.statements, sql)
	if phase == "status" && c.beforeStatus != nil {
		c.beforeStatus()
		c.beforeStatus = nil
	}
	// Fail through the real server/client, without modifying the shared DDL.
	if phase == c.failure {
		return c.inner.Query(ctx, "SELECT fresh_dispatch_missing_column FROM system.one", nil)
	}
	rows, err := c.inner.Query(ctx, sql, bindings)
	if err != nil {
		c.t.Logf("%s query failure: %v", phase, err)
		return rows, err
	}
	return &freshLiveRows{ClickHouseRowScanner: rows, t: c.t, phase: phase, owner: c}, nil
}

type freshLiveRows struct {
	owner   *freshLiveQuery
	scanned int
	contextpacket.ClickHouseRowScanner
	t     *testing.T
	phase string
}

func (r *freshLiveRows) Scan(dest ...any) error {
	err := r.ClickHouseRowScanner.Scan(dest...)
	if err == nil {
		r.scanned++
		if r.owner.failure == r.phase+"_scan" && r.scanned == 2 {
			r.owner.injected = true
			return errors.New("controlled failure after real row scan")
		}
	}
	if err != nil {
		r.t.Logf("%s scan failure: %v", r.phase, err)
	}
	return err
}
func (r *freshLiveRows) Err() error {
	err := r.ClickHouseRowScanner.Err()
	if err == nil && r.owner.failure == r.phase+"_err" && r.scanned > 0 {
		r.owner.injected = true
		return errors.New("controlled failure after real stream completion")
	}
	if err != nil {
		r.t.Logf("%s stream failure: %v", r.phase, err)
	}
	return err
}

type freshLiveMembership struct {
	inner  contextfabric.WorkItemMembershipPort
	calls  int
	result contextfabric.WorkItemMembershipResult
}

func (m *freshLiveMembership) BeginWorkItemMembership(ctx context.Context, p storage.Principal, r contextfabric.WorkItemMembershipRequest) (*contextfabric.WorkItemMembershipLease, contextfabric.WorkItemMembershipResult, error) {
	m.calls++
	lease, result, err := m.inner.BeginWorkItemMembership(ctx, p, r)
	m.result = result
	return lease, result, err
}

type freshLiveExpander struct{ calls int }

func (e *freshLiveExpander) ExpandFactScope(context.Context, contextfabric.FactScopeExpansionRequest) (contextfabric.FactScopeExpansionResult, error) {
	e.calls++
	return contextfabric.FactScopeExpansionResult{}, fmt.Errorf("unexpected tuple expansion")
}

type freshLiveTelemetry struct {
	contextfabric.SlogEngineTelemetry
	ranked int
}

func (t *freshLiveTelemetry) RecordCohortRanked(context.Context, storage.Principal, contextfabric.CohortRankedEvent) {
	t.ranked++
}

type freshLiveGraph struct {
	t                 *testing.T
	project           string
	resolve, discover int
}

func (*freshLiveGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "fresh-live", Epoch: 1}, nil
}
func (g *freshLiveGraph) ResolveSubjects(_ context.Context, p storage.Principal, r contextfabric.InvestigationRequest, _ contextfabric.InterpretedQuestion, _ contextfabric.ResolvedGraphBinding, _ *contextfabric.ConfirmedExpectedKind, _ *contextfabric.ConfirmedAnchorSelection, _ *contextfabric.QuestionFrame, _ contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	g.resolve++
	candidate, ok := graphrank.NodeCandidate(p, r.RequestedScope, "Project Alpha", graphrank.CandidateNode{UUID: "project-node", Name: "Project Alpha", Attributes: map[string]interface{}{"subject_kind": "project", "canonical_id": g.project, "label": "Project Alpha", "evidence_refs": []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, "foreign:foreign")}, "authorization_repositories": []string{"acme/allowed"}, "authorization_projects": []string{g.project}, "authorization_teams": "*"}}, func(contextfabric.SubjectRef) bool { return false }, true, nil, r.RequestID)
	if !ok {
		g.t.Fatal("actual NodeCandidate rejected the scoped anchor")
	}
	candidate.State = contextfabric.ResolutionCommitted
	candidate.ReceiptID = "receipt_fresh_live"
	return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{candidate}, Committed: []contextfabric.SubjectRef{candidate.Subject}}, contextfabric.StructureOfferMaterial{}, nil, nil, nil
}
func (g *freshLiveGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	g.discover++
	return contextfabric.GraphContext{}, fmt.Errorf("unexpected tuple discovery")
}

type freshLiveModel struct {
	facts contextfabric.CanonicalFactBundle
}

func freshLiveReceipt(operation contextfabric.ModelOperation) contextfabric.ModelExecutionReceipt {
	at := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	return contextfabric.ModelExecutionReceipt{Operation: operation, Provider: "test-provider", Model: "test-model", ModelVersion: "v1", PromptVersion: "v1", SchemaVersion: "v1", EvaluatorVersion: "v1", StartedAt: at, CompletedAt: at.Add(time.Second), Attempts: 1, InputDigest: contextfabric.DigestModelValue([]byte("fresh-live")), Outcome: "success"}
}
func (m *freshLiveModel) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	receipt := freshLiveReceipt(contextfabric.ModelOperationInterpret)
	receipt.QuestionFrame = &contextfabric.QuestionFrame{Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState, contextfabric.GoalCountOrAggregate}, SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Project Alpha"}, MemberKind: contextfabric.SubjectWorkItem}}, Temporal: contextfabric.TemporalIntentCurrent}
	receipt.QuestionFamily = contextfabric.QuestionFamilyScopedCohortStatus
	receipt.ScopeAnchorKind = contextfabric.SubjectProject
	receipt.ScopeAnchorTerm = "Project Alpha"
	receipt.RequestedSubjectKind = contextfabric.SubjectWorkItem
	return contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "status", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, SubjectTerms: []string{"Project Alpha"}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactHealth}}}, receipt, nil
}
func (m *freshLiveModel) SynthesizeAnswer(_ context.Context, _ storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	m.facts = input.Facts
	draft := contextfabric.SynthesisDraft{Status: contextfabric.InvestigationComplete, DirectJudgment: "Available work item evidence.", CurrentState: "Available work item evidence.", DeterministicAnswer: "Available work item evidence.", StrongestPressures: []string{}, RemainingWork: []contextfabric.Finding{}, ReadinessGaps: []contextfabric.Finding{}, Conflicts: []contextfabric.Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, Warnings: []string{}, Drivers: []contextfabric.DriverJudgment{}, ClaimedFacts: []contextfabric.ClaimedFact{}}
	subjects := map[string]contextfabric.SubjectRef{}
	if input.Graph.Cohort != nil {
		for _, member := range input.Graph.Cohort.Members {
			subjects[member.Subject.CanonicalID] = member.Subject
		}
	}
	// Two actual producer facts suffice for a small draft. The test separately
	// asserts all retained status/title facts at the real synthesis input seam.
	seen := map[contextfabric.FactKind]bool{}
	for _, fact := range input.Facts.Facts {
		if seen[fact.Kind] {
			continue
		}
		field := "status"
		if fact.Kind == contextfabric.FactWork {
			field = "title"
		}
		if value := fact.Fields[field].String; value != nil {
			seen[fact.Kind] = true
			draft.ClaimedFacts = append(draft.ClaimedFacts, contextfabric.ClaimedFact{ClaimID: fmt.Sprintf("claim_fresh_%d", len(draft.ClaimedFacts)), Kind: fact.Kind, Subject: subjects[fact.Subject.CanonicalID], Field: field, Value: contextfabric.ScalarValue{String: value}})
			for _, ref := range fact.EvidenceRefIDs {
				if !containsFreshLive(draft.EvidenceRefIDs, ref) {
					draft.EvidenceRefIDs = append(draft.EvidenceRefIDs, ref)
				}
			}
		}
	}
	return draft, freshLiveReceipt(contextfabric.ModelOperationSynthesize), nil
}

type freshLiveFixture struct {
	engine     *contextfabric.Engine
	principal  storage.Principal
	request    contextfabric.InvestigationRequest
	graph      *freshLiveGraph
	model      *freshLiveModel
	membership *freshLiveMembership
	expander   *freshLiveExpander
	telemetry  *freshLiveTelemetry
	store      *memoryinvestigation.Store
}

func newFreshLiveEngine(t *testing.T, client contextpacket.ClickHouseQueryClient, org, provider, project string, at time.Time, cap int) *freshLiveFixture {
	t.Helper()
	projectID, _, err := identity.Derive(identity.KindProject, []string{provider, project}, nil)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	gate, err := contextfabric.NewWorkItemMembershipGate(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := devhealthfacts.NewWorkItemMembershipReader(client, devhealthfacts.WorkItemMembershipReaderOptions{Gate: gate, Telemetry: contextfabric.NewSlogWorkItemMembershipTelemetry(logger), Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	f := &freshLiveFixture{principal: storage.Principal{OrgID: org, RepositoryScopes: []string{"acme/allowed", "acme/other"}}, graph: &freshLiveGraph{t: t, project: projectID}, model: &freshLiveModel{}, membership: &freshLiveMembership{inner: reader}, expander: &freshLiveExpander{}, telemetry: &freshLiveTelemetry{SlogEngineTelemetry: contextfabric.NewSlogEngineTelemetry(logger)}, store: memoryinvestigation.NewStore()}
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(client), contextfabric.FactRegistryOptions{ScopeExpander: f.expander, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	f.request = contextfabric.InvestigationRequest{SchemaVersion: contractsv1.ContextFabricInvestigationRequestSchema, RequestID: "request_fresh_live", Question: "What is the state and count of Project Alpha work items?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}}, RequestedScope: contextfabric.RequestedScope{RepositorySlugs: []string{"acme/allowed"}, ProjectIDs: []string{projectID}, TeamIDs: []string{"unrelated-team"}}, Options: contractsv1.ContextFabricInvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: cap, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1048576, AllowClarification: true}, Consumer: contractsv1.ContextFabricConsumerInfo{Name: "fresh-live", Version: "1.0.0", Surface: "workbench"}}
	f.engine, err = contextfabric.NewEngine(contextfabric.EngineDependencies{Interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: f.model, Requirements: registry}, Graph: f.graph, Facts: registry, Requirements: registry, WorkItemMembership: f.membership, Telemetry: f.telemetry, CandidateVerifier: func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (bool, contextfabric.CandidateVerificationReason) {
		return true, contextfabric.CandidateVerificationValid
	}, Synthesizer: contextfabric.RuntimeAnswerSynthesizer{Runtime: f.model, Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "fresh-live", Backend: "graph", ProjectionVersion: "v1", QueryVersion: "v1"}}, Results: f.store}, contextfabric.EngineOptions{ServiceVersion: "fresh-live", Now: func() time.Time { return at }, NewResultID: func() string { return "result_fresh_live" }})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
