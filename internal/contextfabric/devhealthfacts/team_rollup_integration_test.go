package devhealthfacts_test

// A team subject's deployments, incidents, pull_requests and blockers are a
// rollup over the repositories the team owns, EXECUTED against a real
// ClickHouse through the real providers: the team's window totals equal the
// merge of what its owned repositories hold, the owned_repositories pointer
// names every owned repository, a team that owns nothing is not_applicable
// (never a zero), and the repository subject path is unchanged.

import (
	"context"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const teamRollupOrg = "0b8f2c1e-5a3d-4e7b-9c10-2f6a8d4e1b77"

type teamRollupFixture struct {
	t         *testing.T
	providers []contextfabric.FactProvider
	window    contextfabric.TimeContext
}

func newTeamRollupFixture(t *testing.T) *teamRollupFixture {
	t.Helper()
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(
		"repos", "team_repo_ownership", "deploy_metrics_daily", "git_pull_requests",
		"operational_incidents", "work_graph_deployment_incident_edges",
		"work_items", "work_item_dependencies", "projects", "project_membership_transitions", "work_graph_issue_pr", "team_project_ownership", "work_item_team_attributions",
	) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	f := &teamRollupFixture{t: t, providers: devhealthfacts.NewProviders(query)}
	start, end := ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 30, 23, 59, 59)
	f.window = contractsv1TimeContext(start, end)
	f.seed(ctx, direct)
	return f
}

func contractsv1TimeContext(start, end time.Time) contextfabric.TimeContext {
	return contextfabric.TimeContext{
		Axis:           contextfabric.TemporalCurrent,
		EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &start, End: &end},
	}
}

func (f *teamRollupFixture) seed(ctx context.Context, direct clickhousedriver.Conn) {
	t := f.t
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	at := ts(2026, 9, 29, 0, 0, 0)
	for _, label := range []string{"repo-a", "repo-b", "repo-c"} {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(label), teamRollupOrg, "acme/"+label, "github", at)
	}
	own := func(teamID, label string) {
		exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			teamRollupOrg, "github", teamID, repoUUID(label), "acme/"+label, "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, at)
	}
	// team-two owns repo-a and repo-b; team-solo owns repo-a alone; repo-c is
	// owned by nobody; team-none owns nothing.
	own("team-two", "repo-a")
	own("team-two", "repo-b")
	own("team-solo", "repo-a")

	deploy := func(label string, day time.Time, total, failed uint32, deployTime, leadTime any) {
		exec(`INSERT INTO deploy_metrics_daily (repo_id, day, deployments_count, failed_deployments_count, deploy_time_p50_hours, lead_time_p50_hours, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?)`,
			repoUUID(label), day, total, failed, deployTime, leadTime, day.Add(6*time.Hour), teamRollupOrg)
	}
	deploy("repo-a", date(2026, 9, 10), 5, 1, 2.0, 3.0)
	deploy("repo-a", date(2026, 9, 20), 3, 0, 1.5, nil)
	deploy("repo-a", date(2026, 8, 1), 100, 50, 9.0, 9.0) // outside the window
	deploy("repo-b", date(2026, 9, 15), 4, 2, 4.0, 5.0)
	deploy("repo-c", date(2026, 9, 15), 40, 4, 1.0, 1.0) // unowned

	pr := func(label string, number uint32, created time.Time, merged, closed any) {
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			repoUUID(label), teamRollupOrg, number, "pr", "open", at, created, merged, closed, "feature", "")
	}
	pr("repo-a", 1, ts(2026, 9, 5, 9, 0, 0), ts(2026, 9, 7, 9, 0, 0), ts(2026, 9, 7, 9, 0, 0))
	pr("repo-a", 2, ts(2026, 9, 10, 9, 0, 0), nil, ts(2026, 9, 12, 9, 0, 0))
	pr("repo-a", 3, ts(2026, 8, 1, 9, 0, 0), ts(2026, 9, 3, 9, 0, 0), ts(2026, 9, 3, 9, 0, 0)) // opened before the window, merged inside it
	pr("repo-b", 1, ts(2026, 9, 20, 9, 0, 0), nil, nil)
	pr("repo-c", 1, ts(2026, 9, 20, 9, 0, 0), nil, nil) // unowned

	incident := func(id string, started time.Time, resolved any, deleted uint8) {
		exec(`INSERT INTO operational_incidents (id, org_id, normalized_status, normalized_severity, is_deleted, started_at, resolved_at, observed_at) VALUES (?,?,?,?,?,?,?,?)`,
			id, teamRollupOrg, "open", "sev2", deleted, started, resolved, started)
	}
	edge := func(deployment, incidentID, label string) {
		exec(`INSERT INTO work_graph_deployment_incident_edges (edge_id, org_id, deployment_id, incident_id, repo_id, confidence, source, evidence, observed_at, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			deployment+"-"+incidentID+"-"+label, teamRollupOrg, deployment, incidentID, repoUUID(label), float32(1), "fixture", "", at, at)
	}
	incident("inc-1", ts(2026, 9, 5, 0, 0, 0), ts(2026, 9, 6, 0, 0, 0), 0)
	incident("inc-2", ts(2026, 9, 10, 0, 0, 0), nil, 0)
	incident("inc-3", ts(2026, 9, 12, 0, 0, 0), nil, 0) // no edge: not attributable
	incident("inc-4", ts(2026, 9, 13, 0, 0, 0), nil, 1) // soft-deleted
	incident("inc-5", ts(2026, 8, 1, 0, 0, 0), nil, 0)  // outside the window
	edge("dep-1", "inc-1", "repo-a")
	edge("dep-2", "inc-2", "repo-a")
	edge("dep-3", "inc-2", "repo-b") // one incident linked to both owned repositories
	edge("dep-4", "inc-4", "repo-a")
	edge("dep-5", "inc-5", "repo-a")

	item := func(id, label string) {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', '', NULL)`,
			id, repoUUID(label), teamRollupOrg, "title "+id, at)
	}
	item("wi-a1", "repo-a")
	item("wi-a2", "repo-a")
	item("wi-b1", "repo-b")
	dependency := func(source, target, relationship string) {
		exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, last_synced) VALUES (?, ?, ?, ?, ?)`,
			teamRollupOrg, source, target, relationship, at)
	}
	dependency("wi-a2", "wi-a1", "blocks")
	dependency("wi-b1", "wi-a1", "blocks")
	dependency("wi-b1", "wi-a1", "BLOCKS") // the same pair under another case counts once
	dependency("wi-a2", "wi-b1", "blocks")
	dependency("wi-a1", "wi-a2", "parent_of") // not a blocker
}

func (f *teamRollupFixture) read(kind contextfabric.FactKind, timeContext contextfabric.TimeContext, subjects ...contextfabric.SubjectRef) contextfabric.FactProviderResult {
	f.t.Helper()
	provider := findProvider(f.t, f.providers, kind)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: teamRollupOrg}, contextfabric.FactQuery{Time: timeContext, Kind: kind, Subjects: subjects})
	if err != nil {
		f.t.Fatalf("%s ReadFacts: %v", kind, err)
	}
	return result
}

func factFor(t *testing.T, result contextfabric.FactProviderResult, subject contextfabric.SubjectRef) contextfabric.CanonicalFact {
	t.Helper()
	for _, fact := range result.Facts {
		if fact.Subject.CanonicalID == subject.CanonicalID {
			return fact
		}
	}
	t.Fatalf("no fact for %s in %#v", subject.CanonicalID, result)
	return contextfabric.CanonicalFact{}
}

func wantInt(t *testing.T, fields map[string]contextfabric.FactValue, name string, want int64) {
	t.Helper()
	value, ok := fields[name]
	if !ok || value.Integer == nil {
		t.Fatalf("%s missing or not an integer: %#v", name, fields[name])
	}
	if *value.Integer != want {
		t.Fatalf("%s = %d, want %d", name, *value.Integer, want)
	}
}

func wantTableRows(t *testing.T, fields map[string]contextfabric.FactValue, name string) map[string]map[string]contextfabric.FactValue {
	t.Helper()
	value, ok := fields[name]
	if !ok || value.Table == nil {
		t.Fatalf("%s table missing: %#v", name, fields[name])
	}
	if err := value.Validate(); err != nil {
		t.Fatalf("%s fails FactValue.Validate: %v", name, err)
	}
	byRepo := map[string]map[string]contextfabric.FactValue{}
	for _, row := range value.Rows {
		id := row.Fields["repository_id"].String
		if id == nil {
			t.Fatalf("%s row without repository_id: %#v", name, row)
		}
		byRepo[*id] = row.Fields
	}
	return byRepo
}

func wantOwnedPointer(t *testing.T, fields map[string]contextfabric.FactValue, labels ...string) {
	t.Helper()
	rows := wantTableRows(t, fields, "owned_repositories")
	if len(rows) != len(labels) {
		t.Fatalf("owned_repositories = %v, want exactly %v", rows, labels)
	}
	for _, label := range labels {
		if _, ok := rows[repoUUID(label)]; !ok {
			t.Fatalf("owned_repositories lacks %s (%s): %v", label, repoUUID(label), rows)
		}
	}
	wantInt(t, fields, "owned_repository_count", int64(len(labels)))
	if got := fields["rollup_basis"].String; got == nil || *got != "team_owned_repositories" {
		t.Fatalf("rollup_basis = %#v", fields["rollup_basis"])
	}
}

func TestTeamRollupsAgainstRealClickHouse(t *testing.T) {
	f := newTeamRollupFixture(t)
	two, solo, none := teamSubject("team-two"), teamSubject("team-solo"), teamSubject("team-none")

	t.Run("deployments", func(t *testing.T) {
		result := f.read(contextfabric.FactDeployments, f.window, two, solo)
		fields := factFor(t, result, two).Fields
		wantOwnedPointer(t, fields, "repo-a", "repo-b")
		// Window totals: repo-a 5+3, repo-b 4; the August row and repo-c are out.
		wantInt(t, fields, "deployments_count_window", 12)
		wantInt(t, fields, "failed_deployments_count_window", 3)
		wantInt(t, fields, "repositories_with_data_count", 2)
		rows := wantTableRows(t, fields, "repository_breakdown")
		a := rows[repoUUID("repo-a")]
		wantInt(t, a, "deployments_count_window", 8)
		wantInt(t, a, "days_with_data_window", 2)
		if got := a["latest_day"].String; got == nil || *got != "2026-09-20" {
			t.Fatalf("latest_day = %#v", a["latest_day"])
		}
		// The latest day's own percentile is absent (NULL that day), never carried from an older day.
		if _, ok := a["deploy_time_p50_hours_latest_day"]; !ok {
			t.Fatalf("latest-day deploy time missing: %#v", a)
		}
		if _, ok := a["lead_time_p50_hours_latest_day"]; ok {
			t.Fatalf("a NULL latest-day percentile must stay absent: %#v", a)
		}
		if _, summed := fields["deploy_time_p50_hours"]; summed {
			t.Fatalf("a percentile must never be rolled up: %#v", fields)
		}
		if got := fields["window_start"].String; got == nil || !strings.HasPrefix(*got, "2026-09-01") {
			t.Fatalf("window_start = %#v", fields["window_start"])
		}
		soloFields := factFor(t, result, solo).Fields
		wantOwnedPointer(t, soloFields, "repo-a")
		wantInt(t, soloFields, "deployments_count_window", 8)

		// Control: the repository subject path still serves its latest-day aggregate.
		repo := f.read(contextfabric.FactDeployments, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, repositorySubject("repo-a"))
		repoFields := factFor(t, repo, repositorySubject("repo-a")).Fields
		wantInt(t, repoFields, "deployments_count", 3)
		if _, leaked := repoFields["deployments_count_window"]; leaked {
			t.Fatalf("repository fact gained a team field: %#v", repoFields)
		}
	})

	t.Run("pull_requests", func(t *testing.T) {
		result := f.read(contextfabric.FactPullRequests, f.window, two)
		fields := factFor(t, result, two).Fields
		wantOwnedPointer(t, fields, "repo-a", "repo-b")
		wantInt(t, fields, "pull_requests_opened_window", 3)
		wantInt(t, fields, "pull_requests_merged_window", 2)
		wantInt(t, fields, "pull_requests_closed_unmerged_window", 1)
		rows := wantTableRows(t, fields, "repository_breakdown")
		wantInt(t, rows[repoUUID("repo-a")], "pull_requests_opened_window", 2)
		wantInt(t, rows[repoUUID("repo-b")], "pull_requests_opened_window", 1)
		wantInt(t, rows[repoUUID("repo-b")], "pull_requests_merged_window", 0)
	})

	t.Run("incidents", func(t *testing.T) {
		result := f.read(contextfabric.FactIncidents, f.window, two)
		fields := factFor(t, result, two).Fields
		wantOwnedPointer(t, fields, "repo-a", "repo-b")
		// inc-2 is linked to both owned repositories: the team counts it once.
		wantInt(t, fields, "incidents_count_window", 2)
		wantInt(t, fields, "resolved_incidents_count_window", 1)
		rows := wantTableRows(t, fields, "repository_breakdown")
		wantInt(t, rows[repoUUID("repo-a")], "incidents_count_window", 2)
		wantInt(t, rows[repoUUID("repo-b")], "incidents_count_window", 1)
		// inc-3 has no deployment-incident edge: org-wide, not attributable.
		wantInt(t, fields, "org_incidents_not_attributable_count_window", 1)
		if got := fields["incident_attribution_basis"].String; got == nil || *got != "deployment_linked" {
			t.Fatalf("incident_attribution_basis = %#v", fields["incident_attribution_basis"])
		}
	})

	t.Run("blockers", func(t *testing.T) {
		result := f.read(contextfabric.FactBlockers, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, two)
		fields := factFor(t, result, two).Fields
		wantOwnedPointer(t, fields, "repo-a", "repo-b")
		// wi-a1 (repo-a) is blocked twice (the BLOCKS duplicate is one pair); wi-b1 (repo-b) once.
		wantInt(t, fields, "blocked_work_items_current", 2)
		wantInt(t, fields, "blocker_dependencies_current", 3)
		rows := wantTableRows(t, fields, "repository_breakdown")
		wantInt(t, rows[repoUUID("repo-a")], "blocker_dependencies_current", 2)
		wantInt(t, rows[repoUUID("repo-b")], "blocker_dependencies_current", 1)

		asOf := ts(2026, 9, 15, 0, 0, 0)
		historical := f.read(contextfabric.FactBlockers, contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf}, two)
		if historical.State != contextfabric.SourceNotApplicable || len(historical.Facts) != 0 {
			t.Fatalf("blockers have no history; a past time must be refused: %#v", historical)
		}
	})

	t.Run("a team that owns nothing is not applicable, never zero", func(t *testing.T) {
		for _, kind := range []contextfabric.FactKind{contextfabric.FactDeployments, contextfabric.FactPullRequests, contextfabric.FactIncidents, contextfabric.FactBlockers} {
			result := f.read(kind, f.window, none)
			if len(result.Facts) != 0 {
				t.Fatalf("%s: a team with no owned repository served a fact: %#v", kind, result.Facts)
			}
			if result.State != contextfabric.SourceNotApplicable || !strings.Contains(result.Reason, "owns no repository") {
				t.Fatalf("%s: state=%s reason=%q, want not_applicable with the ownership reason", kind, result.State, result.Reason)
			}
		}
	})
}
