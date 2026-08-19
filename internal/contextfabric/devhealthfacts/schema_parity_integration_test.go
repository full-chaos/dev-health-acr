package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Schema parity for the FACT READERS (CHAOS-3781 codex round-2 F1).
//
// devhealthsource gained this guard under CHAOS-3789 after it was found
// scanning git_pull_requests.number (UInt32) into an *int64 -- a
// conversion clickhouse-go rejects outright, so every live row failed
// Scan and the producer silently returned nothing. Round 2 found the
// IDENTICAL defect still live in THIS package, which reads the same
// column: the fix had been applied to one package and not the other, and
// this package's fixtures modeled the column as int64, so its tests
// agreed with the bug.
//
// A fixture cannot catch a fixture's own error. This runs every provider
// against a schema typed exactly like production -- rendered from
// devhealthschema, the single declaration devhealthsource's guard and its
// fixtures now share -- so a Scan destination that disagrees with the real
// column type fails here, on a real server, the way it would in
// production.
//
// The seeded values are deliberately typed to the DECLARED column types
// (uint32 for a pull-request number, not int64): the driver enforces the
// same conversion rules on the way in, so a wrongly-typed seed fails to
// insert rather than silently proving the wrong thing.

// devhealthschema:not-a-production-replica this list names WHICH declared tables to render;
// every column type, engine and sort key still comes from
// devhealthschema.DDL. Naming a subset is the point of the guard, not a rival
// source of schema truth.
// factSchemaTables are the tables this package's providers read. Rendered
// from devhealthschema so a column added to a provider's SELECT without
// being declared there fails loudly rather than silently going unasserted.
var factSchemaTables = []string{
	"repos", "work_items", "git_pull_requests", "git_pull_request_reviews",
	"ci_pipeline_runs", "deployments", "operational_incidents", "work_item_dependencies",
	"repo_metrics_daily", "compounding_risk_daily", "estimate_coverage_metrics_daily",
	"capacity_forecasts", "investment_metrics_daily", "recommendations_daily", "backfill_log",
}

// TestLiveSchemaParityAcrossEveryFactProvider is the round-2 F1 guard: no
// provider may hold a Scan destination that the production column type
// rejects.
//
// It asserts NO FACTS -- coverage of what each provider returns lives in
// the per-provider tests. What it asserts is narrower and is exactly what
// the fakes cannot: that the read does not ERROR against production
// typing. A provider whose scan is wrong surfaces as a read failure here.
func TestLiveSchemaParityAcrossEveryFactProvider(t *testing.T) {
	ctx := context.Background()
	client, direct := newCHAOS3780IntegrationClient(t, ctx)

	for _, statement := range devhealthschema.DDL(factSchemaTables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}

	const orgID = "org-parity"
	repoID := "3f2504e0-4f89-11d3-9a0c-0305e82c3301"
	at := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	seed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	// Each seed names its columns: the declared tables carry every column
	// the readers touch, and a positional list would break the moment one
	// is added.
	// devhealthschema:not-a-production-replica these are INSERT statements seeding rows into tables
	// devhealthschema.DDL already created; the table name is an argument
	// selecting where the row goes. No schema is declared here.
	seed("repos", `INSERT INTO repos (id, org_id, repo, provider) VALUES (?, ?, ?, ?)`,
		repoID, orgID, "acme/service", "github")
	seed("work_items", `INSERT INTO work_items (work_item_id, org_id, repo_id, status, title, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"WI-1", orgID, repoID, "done", "Parity work item", at)
	// WI-2 is the work_item_dependencies row's target below -- BlockersProvider's
	// v2Index-backed WHERE now resolves the target's repo_id via an INNER JOIN
	// to work_items (dependencies.go), so WI-2 must exist there too, or that
	// join alone (not this test's own typing) would return zero rows and the
	// parity check's Scan callback would never run for FactBlockers.
	seed("work_items", `INSERT INTO work_items (work_item_id, org_id, repo_id, status, title, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"WI-2", orgID, repoID, "open", "Parity blocked work item", at)
	// uint32, matching the declared column -- an int64 here would be
	// rejected by the driver on insert, which is the point.
	seed("git_pull_requests", `INSERT INTO git_pull_requests (repo_id, org_id, number, state, created_at) VALUES (?, ?, ?, ?, ?)`,
		repoID, orgID, uint32(4242), "merged", at)
	// repo_id is now seeded on these three rows (it was not before CHAOS-3898):
	// ReviewsProvider/ContinuousIntegrationProvider/DeploymentsProvider all
	// scope their WHERE clause on a repo_id-qualified composite key now
	// (v2Index, shared.go), matching the subject's own decoded repo_id below.
	seed("git_pull_request_reviews", `INSERT INTO git_pull_request_reviews (review_id, org_id, repo_id, state, submitted_at) VALUES (?, ?, ?, ?, ?)`,
		"review-parity", orgID, repoID, "approved", at)
	seed("ci_pipeline_runs", `INSERT INTO ci_pipeline_runs (run_id, org_id, repo_id, status, started_at) VALUES (?, ?, ?, ?, ?)`,
		"run-parity", orgID, repoID, "success", at)
	seed("deployments", `INSERT INTO deployments (deployment_id, org_id, repo_id, status, environment, started_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"deploy-parity", orgID, repoID, "success", "production", at)
	seed("operational_incidents", `INSERT INTO operational_incidents (id, org_id, normalized_status, normalized_severity, is_deleted, started_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"incident-parity", orgID, "resolved", "low", uint8(0), at)
	seed("work_item_dependencies", `INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, org_id, relationship_type) VALUES (?, ?, ?, ?)`,
		"WI-1", "WI-2", orgID, "blocks")
	seed("repo_metrics_daily", `INSERT INTO repo_metrics_daily (org_id, repo_id, day, computed_at) VALUES (?, ?, ?, ?)`,
		orgID, repoID, day, at)
	seed("compounding_risk_daily", `INSERT INTO compounding_risk_daily (org_id, scope, scope_id, day, computed_at) VALUES (?, ?, ?, ?, ?)`,
		orgID, "repo", repoID, day, at)
	seed("estimate_coverage_metrics_daily", `INSERT INTO estimate_coverage_metrics_daily (org_id, team_id, day, computed_at) VALUES (?, ?, ?, ?)`,
		orgID, "CHAOS", day, at)
	// devhealthschema:not-a-production-replica the seeding block continues here, past the reach of
	// the marker above it. Same INSERT-into-an-already-created-table shape:
	// the table name selects a destination, it declares nothing.
	seed("capacity_forecasts", `INSERT INTO capacity_forecasts (org_id, team_id, computed_at) VALUES (?, ?, ?)`,
		orgID, "CHAOS", at)
	seed("investment_metrics_daily", `INSERT INTO investment_metrics_daily (org_id, team_id, day, computed_at) VALUES (?, ?, ?, ?)`,
		orgID, "CHAOS", day, at)
	seed("recommendations_daily", `INSERT INTO recommendations_daily (org_id, team_id, window_end, computed_at) VALUES (?, ?, ?, ?)`,
		orgID, "CHAOS", at, at)
	seed("backfill_log", `INSERT INTO backfill_log (org_id, provider, status, created_at) VALUES (?, ?, ?, ?)`,
		orgID, "github", "ok", at)

	principal := storage.Principal{OrgID: orgID}
	subjects := map[contextfabric.FactKind]contextfabric.SubjectRef{
		contextfabric.FactIdentity:                repoSubject(repoID),
		contextfabric.FactMembership:              repoSubject(repoID),
		contextfabric.FactStatus:                  workItemSubject(repoID, "WI-1"),
		contextfabric.FactWork:                    workItemSubject(repoID, "WI-1"),
		contextfabric.FactActualCompletion:        workItemSubject(repoID, "WI-1"),
		contextfabric.FactBlockers:                workItemSubject(repoID, "WI-2"),
		contextfabric.FactRequiredChildren:        workItemSubject(repoID, "WI-1"),
		contextfabric.FactPullRequests:            pullRequestSubject(repoID, "4242"),
		contextfabric.FactReviews:                 reviewSubject(repoID, "review-parity"),
		contextfabric.FactContinuousIntegration:   ciRunSubject(repoID, "run-parity"),
		contextfabric.FactDeployments:             deploymentSubject(repoID, "deploy-parity"),
		contextfabric.FactIncidents:               incidentSubject("incident-parity"),
		contextfabric.FactMetrics:                 repoSubject(repoID),
		contextfabric.FactHealth:                  repoSubject(repoID),
		contextfabric.FactWorkload:                teamSubject("CHAOS"),
		contextfabric.FactInvestment:              teamSubject("CHAOS"),
		contextfabric.FactReadiness:               teamSubject("CHAOS"),
		contextfabric.FactOperationalDeficiencies: teamSubject("CHAOS"),
		contextfabric.FactSourceHealth:            organizationSubject(orgID),
	}

	providers := devhealthfacts.NewProviders(client)
	if len(providers) == 0 {
		t.Fatal("no providers registered")
	}
	for _, provider := range providers {
		capability := provider.Capability()
		subject, ok := subjects[capability.Kind]
		if !ok {
			t.Fatalf("fact kind %q has no parity subject; add one so a new provider cannot go unasserted", capability.Kind)
		}
		t.Run(string(capability.Kind), func(t *testing.T) {
			result, err := provider.ReadFacts(ctx, principal, contextfabric.FactQuery{
				Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind:     capability.Kind,
				Subjects: []contextfabric.SubjectRef{subject},
			})
			if err != nil {
				t.Fatalf("ReadFacts() against production-typed schema: %v", err)
			}
			// A Scan-destination mismatch surfaces as a read FAILURE,
			// which the provider reports as unavailable rather than
			// returning as an error -- so the state has to be checked
			// too, or the defect passes silently.
			if result.State == contextfabric.SourceUnavailable {
				t.Fatalf("provider degraded to unavailable against production typing: %s", result.Reason)
			}
		})
	}
}

// TestLiveHistoricalReadsMatchProductionTyping runs the same parity check
// on the HISTORICAL path, whose SQL differs (bound predicates, derived
// state expressions) and could drift independently.
func TestLiveHistoricalReadsMatchProductionTyping(t *testing.T) {
	ctx := context.Background()
	client, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(factSchemaTables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}

	const orgID = "org-parity-historical"
	repoID := "3f2504e0-4f89-11d3-9a0c-0305e82c3302"
	created := time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)
	merged := time.Date(2026, 5, 5, 9, 0, 0, 0, time.UTC)
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, state, created_at, merged_at) VALUES (?, ?, ?, ?, ?, ?)`,
		repoID, orgID, uint32(7), "merged", created, merged); err != nil {
		t.Fatalf("seed git_pull_requests: %v", err)
	}

	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactPullRequests)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf},
		Kind:     contextfabric.FactPullRequests,
		Subjects: []contextfabric.SubjectRef{pullRequestSubject(repoID, "7")},
	})
	if err != nil {
		t.Fatalf("historical ReadFacts() against production-typed schema: %v", err)
	}
	if result.State == contextfabric.SourceUnavailable {
		t.Fatalf("historical read degraded to unavailable against production typing: %s", result.Reason)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("Facts = %#v, want the pull request that existed at the requested time", result.Facts)
	}
	// And the Tier B derivation is right on a REAL row: created before the
	// requested time, merged after it, so it was open then.
	state, ok := result.Facts[0].Fields["state"]
	if !ok || state.String == nil {
		t.Fatalf("fact carries no state: %#v", result.Facts[0])
	}
	if *state.String != "open" {
		t.Fatalf("state at %v = %q, want \"open\": the pull request merged later, on %v", asOf, *state.String, merged)
	}
}

// TestLiveFinalKeepsTheVersionColumnWinner is CHAOS-3781 round-4 R4-1,
// red→green, and it is a BEHAVIOURAL proof rather than a metadata one.
//
// The declaration used to carry only the engine CLASS
// (`ReplacingMergeTree`), dropping the VERSION column production declares
// (`ReplacingMergeTree(last_synced)`). A versionless ReplacingMergeTree
// keeps an ARBITRARY row among those sharing a sort key; production keeps
// the one with the highest version. Several providers query these tables
// with FINAL and depend on that choice, so every fixture built from the
// class alone was proving the wrong thing about the exact semantics under
// test — silently, because both engines accept FINAL and return one row.
//
// The rows are inserted OLDEST LAST, so insert order and version order
// disagree. Under a versionless engine the reader is free to return the
// stale row; under production's definition it must return the newer one.
func TestLiveFinalKeepsTheVersionColumnWinner(t *testing.T) {
	ctx := context.Background()
	client, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL("work_items") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}

	const orgID = "org-final"
	repoID := "3f2504e0-4f89-11d3-9a0c-0305e82c3303"
	newer := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Same sort key (org_id, repo_id, work_item_id) -- two versions of one
	// logical row. The NEWER version is inserted FIRST so that "last
	// written" and "highest version" point at different rows.
	seed := func(status string, lastSynced time.Time) {
		t.Helper()
		if err := direct.Exec(ctx,
			`INSERT INTO work_items (work_item_id, org_id, repo_id, status, title, created_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"WI-FINAL", orgID, repoID, status, "Version race", older, lastSynced); err != nil {
			t.Fatalf("seed work_items: %v", err)
		}
	}
	seed("done", newer)
	seed("in_progress", older)

	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactStatus)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     contextfabric.FactStatus,
		Subjects: []contextfabric.SubjectRef{workItemSubject(repoID, "WI-FINAL")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("Facts = %#v, want exactly one row after FINAL deduped the two versions", result.Facts)
	}
	status, ok := result.Facts[0].Fields["status"]
	if !ok || status.String == nil {
		t.Fatalf("fact carries no status: %#v", result.Facts[0])
	}
	if *status.String != "done" {
		t.Fatalf("FINAL returned status %q, want \"done\" -- the row with the highest last_synced. A ReplacingMergeTree declared without its version column keeps an arbitrary row instead", *status.String)
	}
}
