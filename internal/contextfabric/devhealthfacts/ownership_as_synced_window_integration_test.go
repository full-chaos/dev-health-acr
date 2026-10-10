package devhealthfacts_test

// Ownership is synced state: its valid_from is the sync stamp, not the start
// of ownership. A past range window that ended before that stamp still serves
// the facts of the rows an open-ended ownership covers. EXECUTED against a real
// ClickHouse.

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func pastRangeWindow() contextfabric.TimeContext {
	start, end := ts(2026, 6, 1, 0, 0, 0), ts(2026, 7, 1, 0, 0, 0)
	return contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}
}

// ownershipSyncedAfterWindow is a sync stamp later than pastRangeWindow's end.
func ownershipSyncedAfterWindow() time.Time { return ts(2026, 9, 1, 0, 0, 0) }

func TestPastRangeWindowServesProjectHealthWhenOwnershipSyncedAfterWindow(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS4363Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactHealth)
	const orgID = "org-health-as-synced"
	synced := ownershipSyncedAfterWindow()
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-hs", orgID, "linear", "HS1", "Project", uint8(1), "active", "", synced)
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, "linear", "team-hs", "irrelevant", "HS1", "native", synced, nil, synced)
	exec(`INSERT INTO compounding_risk_daily (org_id, day, scope, scope_id, compounding_risk, severity, computed_at) VALUES (?,?,?,?,?,?,?)`,
		orgID, date(2026, 6, 28), "team", "team-hs", 0.55, "elevated", ts(2026, 6, 28, 6, 0, 0))

	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: pastRangeWindow(), Kind: contextfabric.FactHealth,
		Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-hs")},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %d, want 1 (state %s, reason %q)", len(result.Facts), result.State, result.Reason)
	}
	fact := result.Facts[0]
	if got := fact.Fields["team_count"].Integer; got == nil || *got != 1 {
		t.Fatalf("team_count = %#v, want 1", fact.Fields["team_count"])
	}
	if got := fact.Fields["severity"].String; got == nil || *got != "elevated" {
		t.Fatalf("severity = %#v, want elevated", fact.Fields["severity"])
	}
}

func TestPastRangeWindowServesProjectLandscapeWhenOwnershipSyncedAfterWindow(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL("projects", "team_project_ownership", "ic_landscape_rolling_30d") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactLandscape)
	const orgID = "org-landscape-as-synced"
	synced := ownershipSyncedAfterWindow()
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-ls", orgID, "github", "LS1", "Project", uint8(1), "active", "", synced)
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, "github", "team-ls", "proj-ls", "LS1", "native", synced, nil, synced)
	exec(`INSERT INTO ic_landscape_rolling_30d (org_id, repo_id, as_of_day, identity_id, team_id, map_name, churn_loc_30d, delivery_units_30d, cycle_p50_30d_hours, wip_max_30d, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, repoUUID("repo-ls"), date(2026, 6, 28), "identity-ls", "team-ls", "backend", uint64(100), uint32(2), 5.0, uint32(3), ts(2026, 6, 28, 6, 0, 0))

	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: pastRangeWindow(), Kind: contextfabric.FactLandscape,
		Subjects: []contextfabric.SubjectRef{projectSubject("github", "proj-ls")},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %d, want 1 (state %s, reason %q)", len(result.Facts), result.State, result.Reason)
	}
	fact := result.Facts[0]
	if got := fact.Fields["team_count"].Integer; got == nil || *got != 1 {
		t.Fatalf("team_count = %#v, want 1", fact.Fields["team_count"])
	}
	if rows := fact.Fields["team_breakdown"].Rows; len(rows) != 1 {
		t.Fatalf("team_breakdown = %#v, want 1 row", rows)
	}
}

func TestPastRangeWindowServesTeamRollupWhenOwnershipSyncedAfterWindow(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL("repos", "team_repo_ownership", "deploy_metrics_daily") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactDeployments)
	const orgID = "org-rollup-as-synced"
	synced := ownershipSyncedAfterWindow()
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("repo-rs"), orgID, "acme/repo-rs", "github", synced)
	exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, "github", "team-rs", repoUUID("repo-rs"), "acme/repo-rs", "exact", "native", uint8(1), uint16(100), int32(0), synced, nil, synced)
	exec(`INSERT INTO deploy_metrics_daily (repo_id, day, deployments_count, failed_deployments_count, deploy_time_p50_hours, lead_time_p50_hours, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?)`,
		repoUUID("repo-rs"), date(2026, 6, 15), uint32(6), uint32(1), 2.0, 3.0, ts(2026, 6, 15, 6, 0, 0), orgID)

	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: pastRangeWindow(), Kind: contextfabric.FactDeployments,
		Subjects: []contextfabric.SubjectRef{teamSubject("team-rs")},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	fields := factFor(t, result, teamSubject("team-rs")).Fields
	wantOwnedPointer(t, fields, "repo-rs")
	wantInt(t, fields, "deployments_count_window", 6)
}
