package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Every saved answer in this test is produced by a fresh Engine dispatch.
// PostgreSQL and its migration/schema/codec are real. The existing producer
// fixture controls graph nodes, model drafts and ClickHouse backend rows;
// S1/status/work adapters and the registry execute their production code.
func TestWorkItemFreshEnginePostgresReuseAcrossInstances(t *testing.T) {
	db := freshPGReuseDatabase(t)
	for _, scenario := range []string{"same_request_hit", "membership_changed", "requested_scope_changed"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			principal := storage.Principal{OrgID: "fresh-pg-" + scenario, RepositoryScopes: []string{hostedTestRepository}}
			_, err := db.ExecContext(ctx, `INSERT INTO acr.context_fabric_projection_checkpoints (org_id, source, cursor, source_version, backend_watermark, updated_at) VALUES ($1,'linear','cursor','v1','wm-fresh',now())`, principal.OrgID)
			if err != nil {
				t.Fatal(err)
			}
			gate := newResponseOwnerAPITestGate(t)
			first := newFreshPGReuseEngine(t, db, principal, gate, "result_fresh_pg_"+scenario+"_first")
			request := investigationRequestBody()
			request.Question = "What is the state and count of Project Alpha work items?"
			request.RequestedScope = contextfabric.RequestedScope{RepositorySlugs: []string{hostedTestRepository}}
			request.TimeContext.EvidenceWindow = &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
			initial, err := first.fixture.engine.Investigate(ctx, principal, request)
			if err != nil {
				t.Fatal(err)
			}
			if initial.Reused || initial.Cohort == nil || len(initial.Cohort.Members) != 1 {
				t.Fatalf("fresh producer result=%+v", initial)
			}
			if !reflect.DeepEqual(first.fixture.client.phases, []string{"s1", "status", "work"}) || first.interpreter.calls != 1 || first.synthesisCalls != 1 {
				t.Fatalf("fresh phases=%v interpret=%d synthesize=%d", first.fixture.client.phases, first.interpreter.calls, first.synthesisCalls)
			}
			if first.lookup.calls != 1 || first.lookup.found != 0 {
				t.Fatalf("first lookup calls=%d found=%d", first.lookup.calls, first.lookup.found)
			}
			if len(first.membership.measured) != 1 || first.membership.measured[0].Census.AuthorizedPopulation != 1 {
				t.Fatal("fresh S1 did not measure the persisted population")
			}
			assertResponseOwnerGateFree(t, gate)

			stored, err := first.store.Get(ctx, principal, initial.ResultID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.SemanticStateRead != contextfabric.SemanticStateReadAvailable || stored.SemanticState == nil || stored.SemanticState.WorkItemCensus == nil {
				t.Fatalf("fresh PG semantic read=%s state=%+v", stored.SemanticStateRead, stored.SemanticState)
			}
			census := stored.SemanticState.WorkItemCensus
			digest, err := contextfabric.WorkItemAuthorizationDigest(principal, request.RequestedScope.RepositorySlugs)
			if err != nil {
				t.Fatal(err)
			}
			if census.State != contextfabric.WorkItemMembershipCensusExact || census.Value != 1 || census.Retained != 1 || census.AuthorizationDigest != digest || !reflect.DeepEqual(census.RequestedRepositoryScope, request.RequestedScope.RepositorySlugs) {
				t.Fatalf("fresh PG census=%+v", census)
			}
			if !reflect.DeepEqual(stored.Result.ClaimedFacts, initial.ClaimedFacts) {
				t.Fatal("PG lost fresh producer claims")
			}
			// Read the native column too: the record must exist durably in PostgreSQL,
			// not only in a fixture-owned in-memory copy of the Save arguments.
			var rawBefore []byte
			if err := db.QueryRowContext(ctx, `SELECT semantic_state FROM acr.context_fabric_investigation_results WHERE org_id=$1 AND result_id=$2`, principal.OrgID, initial.ResultID).Scan(&rawBefore); err != nil {
				t.Fatal(err)
			}
			var native struct {
				WorkItemCensus *contextfabric.WorkItemTupleCensus `json:"work_item_census"`
			}
			if err := json.Unmarshal(rawBefore, &native); err != nil {
				t.Fatal(err)
			}
			if native.WorkItemCensus == nil || native.WorkItemCensus.Value != 1 || native.WorkItemCensus.Retained != 1 {
				t.Fatalf("native PG census=%+v", native.WorkItemCensus)
			}

			// New Engine, model adapter, graph fixture, PG Store and membership reader.
			// Only the durable database and process gate survive construction.
			second := newFreshPGReuseEngine(t, db, principal, gate, "result_fresh_pg_"+scenario+"_second")
			secondRequest := request
			secondRequest.RequestID = "request_fresh_pg_second"
			if scenario == "membership_changed" {
				changedID, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", "work-2"}, nil)
				if err != nil {
					t.Fatal(err)
				}
				second.fixture.client.rowsByPhase = map[string][][]any{
					"s1":     {{changedID, "repo-1", "work-2", hostedTestRepository, uint8(1), uint64(1), uint64(1), uint64(0), uint64(0), uint64(0)}},
					"status": {{"work-2", "open", "repo-1"}}, "work": {{"work-2", "A changed member", "repo-1"}},
				}
			}
			if scenario == "requested_scope_changed" {
				secondRequest.RequestedScope.RepositorySlugs = nil
			}
			served, err := second.fixture.engine.Investigate(ctx, principal, secondRequest)
			if err != nil {
				t.Fatalf("second Engine: %v; phases=%v reuse=%v", err, second.fixture.client.phases, second.telemetry.decisions)
			}
			if second.lookup.calls != 1 || second.lookup.found != 1 {
				t.Fatalf("actual PG FindReusable calls=%d found=%d reason=%s error=%v", second.lookup.calls, second.lookup.found, second.lookup.reason, second.lookup.err)
			}
			assertResponseOwnerGateFree(t, gate)
			if second.fixture.graph.discover != 0 {
				t.Fatal("tuple reached DiscoverContext")
			}
			wantPhases := []string{"s1"}
			wantDecision := "hit"
			wantFresh := 0
			if scenario == "membership_changed" {
				wantPhases = []string{"s1", "s1", "status", "work"}
				wantDecision = "membership_changed"
				wantFresh = 1
			}
			if scenario == "requested_scope_changed" {
				wantPhases = []string{"s1", "status", "work"}
				wantDecision = "digest_changed"
				wantFresh = 1
			}
			if !reflect.DeepEqual(second.fixture.client.phases, wantPhases) {
				t.Fatalf("second phases=%v want=%v", second.fixture.client.phases, wantPhases)
			}
			if !reflect.DeepEqual(second.telemetry.decisions, []string{wantDecision}) {
				t.Fatalf("reuse decision=%v want=%s", second.telemetry.decisions, wantDecision)
			}
			if second.interpreter.calls != wantFresh || second.synthesisCalls != wantFresh || second.fixture.graph.resolve != wantFresh {
				t.Fatalf("second interpret=%d synthesize=%d resolve=%d want=%d", second.interpreter.calls, second.synthesisCalls, second.fixture.graph.resolve, wantFresh)
			}
			if scenario == "same_request_hit" {
				if !served.Reused || served.ResultID != initial.ResultID || !reflect.DeepEqual(served.ClaimedFacts, initial.ClaimedFacts) || !reflect.DeepEqual(served.Cohort.Members, initial.Cohort.Members) {
					t.Fatalf("PG reuse lost fresh result identity/content: %+v", served)
				}
				if second.anchorCalls != 1 || len(second.membership.measured) != 1 {
					t.Fatalf("reuse anchor=%d S1=%d", second.anchorCalls, len(second.membership.measured))
				}
				if !reflect.DeepEqual(first.membership.measured[0].Members, second.membership.measured[0].Members) {
					t.Fatal("reuse did not remeasure the identical S1 identities")
				}
			} else {
				if served.Reused || served.ResultID == initial.ResultID {
					t.Fatal("changed input reused the old result")
				}
				if scenario == "membership_changed" && served.Cohort.Members[0].Subject.CanonicalID == initial.Cohort.Members[0].Subject.CanonicalID {
					t.Fatal("fresh miss did not serve the changed member")
				}
				freshStored, err := second.store.Get(ctx, principal, served.ResultID)
				if err != nil {
					t.Fatal(err)
				}
				if freshStored.SemanticState == nil || freshStored.SemanticState.WorkItemCensus == nil {
					t.Fatal("fresh miss failed to persist new census")
				}
			}
			// A lookup/recheck cannot rewrite the first row's semantic snapshot.
			var rawAfter []byte
			if err := db.QueryRowContext(ctx, `SELECT semantic_state FROM acr.context_fabric_investigation_results WHERE org_id=$1 AND result_id=$2`, principal.OrgID, initial.ResultID).Scan(&rawAfter); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(rawBefore, rawAfter) {
				t.Fatal("reuse changed the stored semantic snapshot")
			}
			t.Logf("fresh PG -> new Engine: decision=%s phases=%v first_population=%d first_retained=%d fresh_interpret=%d fresh_synthesis=%d", wantDecision, second.fixture.client.phases, census.Value, census.Retained, second.interpreter.calls, second.synthesisCalls)
		})
	}
}

type freshPGReuseFixture struct {
	fixture                     *freshTupleProducerFixture
	store                       *pginvestigation.Store
	lookup                      *freshPGReuseLookup
	interpreter                 *freshPGReuseInterpreter
	membership                  *freshPGReuseMembership
	telemetry                   *freshPGReuseTelemetry
	synthesisCalls, anchorCalls int
}

func newFreshPGReuseEngine(t *testing.T, db *sql.DB, principal storage.Principal, gate *contextfabric.WorkItemMembershipGate, resultID string) *freshPGReuseFixture {
	t.Helper()
	fixture := newFreshTupleProducerFixture(t, "")
	store, err := pginvestigation.NewStore(db, pginvestigation.WithAnswerReuse(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	f := &freshPGReuseFixture{fixture: fixture, store: store, lookup: &freshPGReuseLookup{Store: store}, interpreter: &freshPGReuseInterpreter{QuestionInterpreter: fixture.dependencies.Interpreter}, telemetry: &freshPGReuseTelemetry{SlogEngineTelemetry: contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(io.Discard, nil)))}}
	reader, err := devhealthfacts.NewWorkItemMembershipReader(fixture.client, devhealthfacts.WorkItemMembershipReaderOptions{Gate: gate, Telemetry: contextfabric.NoopWorkItemMembershipTelemetry{}})
	if err != nil {
		t.Fatal(err)
	}
	f.membership = &freshPGReuseMembership{t: t, inner: reader, gate: gate}
	fixture.model.observe = func(contextfabric.SynthesisInput) { f.synthesisCalls++ }
	fixture.dependencies.Interpreter = f.interpreter
	fixture.dependencies.WorkItemMembership = f.membership
	fixture.dependencies.Results = store
	fixture.dependencies.ReuseGate = f.lookup
	fixture.dependencies.ReuseSnapshotter = store
	fixture.dependencies.ReuseEpochSnapshotter = store
	fixture.dependencies.Telemetry = f.telemetry
	fixture.dependencies.CandidateVerifier = func(_ context.Context, p storage.Principal, scope contextfabric.RequestedScope, binding contextfabric.ResolvedGraphBinding, kind contextfabric.SubjectKind, id string) (bool, contextfabric.CandidateVerificationReason) {
		f.anchorCalls++
		if !reflect.DeepEqual(p, principal) || binding.Epoch != 1 || kind != contextfabric.SubjectProject || id != fixture.graph.projectID {
			t.Fatalf("live anchor input principal=%+v scope=%+v binding=%+v kind=%s id=%s", p, scope, binding, kind, id)
		}
		allowed := graphrank.AuthorizedAttributes(p, scope, map[string]interface{}{"authorization_repositories": []string{hostedTestRepository}, "authorization_projects": []string{fixture.graph.projectID}, "authorization_teams": "*"})
		return allowed, contextfabric.CandidateVerificationValid
	}
	options := fixture.engineOptions
	options.NewResultID = func() string { return resultID }
	options.ReuseProjectionVersion = "projection-v1"
	options.ReuseModelIdentities = []string{"test-provider/test-model"}
	options.ReuseRetrievalIdentity = contextfabric.ReuseRetrievalIdentity{EmbedRetrievalIdentity: "none", RetrievalPolicyVersion: "fresh-pg-v1"}
	options.ReusePromptVersions = contextfabric.ReusePromptVersions{InterpretationPromptVersion: "fresh-pg-interpret-v1", SynthesisPromptVersion: "fresh-pg-synthesis-v1"}
	options.ReuseVersionAuthorities = contextfabric.ReuseVersionAuthorities{QueryVersion: "query-v1", CanonicalServiceVersion: "fresh-pg-facts-v1", ModelOutputSchemaVersion: "fresh-pg-schema-v1", IdentityNormalizationVersion: "fresh-pg-identity-v1", WindowInferenceVersion: contextfabric.WindowInferenceVersion, CommitGateVersion: contextfabric.CommitGateVersion, RankingFormulaVersion: contextfabric.RankingFormulaVersion, QuestionFamilyVersion: contextfabric.QuestionFamilyTableVersion}
	fixture.engine, err = contextfabric.NewEngine(fixture.dependencies, options)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

type freshPGReuseLookup struct {
	*pginvestigation.Store
	calls, found int
	reason       contextfabric.ReuseMissReason
	err          error
}

func (s *freshPGReuseLookup) FindReusable(ctx context.Context, p storage.Principal, key contextfabric.ReuseKey) (contextfabric.StoredInvestigationResult, bool, contextfabric.ReuseMissReason, error) {
	s.calls++
	stored, found, reason, err := s.Store.FindReusable(ctx, p, key)
	if found {
		s.found++
	}
	s.reason, s.err = reason, err
	return stored, found, reason, err
}

type freshPGReuseInterpreter struct {
	contextfabric.QuestionInterpreter
	calls int
}

func (i *freshPGReuseInterpreter) Interpret(ctx context.Context, p storage.Principal, r contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.QuestionFamilyOutcome, error) {
	i.calls++
	return i.QuestionInterpreter.Interpret(ctx, p, r)
}

type freshPGReuseTelemetry struct {
	contextfabric.SlogEngineTelemetry
	decisions []string
}

func (t *freshPGReuseTelemetry) RecordWorkItemReuse(_ context.Context, _ storage.Principal, e contextfabric.WorkItemReuseEvent) {
	t.decisions = append(t.decisions, e.Decision)
}

type freshPGReuseMembership struct {
	t        *testing.T
	inner    contextfabric.WorkItemMembershipPort
	gate     *contextfabric.WorkItemMembershipGate
	measured []contextfabric.WorkItemMembershipResult
}

func (m *freshPGReuseMembership) BeginWorkItemMembership(ctx context.Context, p storage.Principal, r contextfabric.WorkItemMembershipRequest) (*contextfabric.WorkItemMembershipLease, contextfabric.WorkItemMembershipResult, error) {
	// With one permit and no queue, a stale reuse lease must prevent the next
	// fresh S1. Observe release before acquisition, not merely at turn end.
	if stats := m.gate.Stats(); stats.InFlight != 0 || stats.Queued != 0 {
		m.t.Fatalf("previous membership lease still held before next S1: %+v", stats)
	}
	lease, result, err := m.inner.BeginWorkItemMembership(ctx, p, r)
	m.measured = append(m.measured, result)
	return lease, result, err
}

func freshPGReuseDatabase(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44", tcpostgres.WithDatabase("acr"), tcpostgres.WithUsername("acr"), tcpostgres.WithPassword("acr"), tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Error(err)
		}
	})
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	runner, err := migrations.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Apply(ctx, db); err != nil {
		t.Fatal(fmt.Errorf("apply production migrations: %w", err))
	}
	return db
}
