package devhealthsource

// CHAOS-7263 inventory: every table either projection source reads must page
// on the row's INGEST column (the value ops stamps when it writes the row), or
// be one of the disclosed tables that have no ingest column yet. The table set
// is read from the two registries the sources themselves use, and each cursor
// expression is read from the SQL the producer actually sends -- so a table
// added to a registry, or a producer switched back to a provider timestamp,
// fails here without anyone editing this file first.

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// ingestCursorColumns is the declared cursor expression per table, with the
// ops writer that stamps it (file:line in the ops repository).
var ingestCursorColumns = map[string]struct{ expr, writer string }{
	"repos":                                {"last_synced", "providersync github_prs_route.go / repos upsert: last_synced = normalizedAt"},
	"work_items":                           {"w.last_synced", "migrations/clickhouse/009_raw_work_items.sql:24; providersync/linear_work_items_route.go:883, gitlab_work_items_rows.go:232"},
	"work_items_hierarchy":                 {"c.last_synced", "same column as work_items"},
	"git_pull_requests":                    {"p.last_synced", "providersync/github_prs_route.go:394"},
	"deployments":                          {"d.last_synced", "providersync/github_deployments_route.go:303"},
	"operational_incidents":                {"i.last_synced", "providersync/pagerduty_incidents_route.go:1008, gitlab_incidents_route.go:402"},
	"work_item_dependencies":               {"d.last_synced", "ops metrics job stamps the write time"},
	"work_graph_deployment_incident_edges": {"e.computed_at", "migrations/clickhouse/037_ai_workgraph.sql:122 DEFAULT now64()"},
	"git_pull_request_reviews":             {"r.last_synced", "providersync/github_pr_reviews.go:58"},
	"ci_pipeline_runs":                     {"c.last_synced", "metrics/sinks/clickhouse/ci.py:116-134"},
	"teams":                                {queryTeamsIngestExpr, "teams.last_synced (011_ensure_teams.sql:8 DEFAULT now()) or the team_repo_ownership write-time watermark"},
	"projects":                             {"last_synced", "migrations/clickhouse/051_team_attribution_dimensions.sql:13 DEFAULT now64(3)"},
	"work_item_team_attributions":          {"a.computed_at", "the attribution compute job stamps computed_at"},
	"team_repo_ownership":                  {repositoryTeamsWatermark, "updated_at = write time on every writer (team_repo_ownership_derivation_clickhouse.go:89,692; github_team_catalog.go:269; linear_reference_catalog_route.go:487) folded with repos.last_synced"},
}

// cursorUnsoundTables are the tables with NO ingest column in ops yet. Their
// cursor still reads a provider/event time, so a row stamped older than the
// cursor that lands after it can be skipped until a rebuild (the pre-existing
// hazard, unchanged). The value is the expression they page on today; the
// test fails when either one changes, so the exemption cannot outlive the fix.
var cursorUnsoundTables = map[string]string{
	"team_project_ownership":      projectTeamsWatermark,
	"project_membership_presence": "observed_at",
}

var errStatementRecorded = errors.New("statement recorded")

type statementRecorder struct{ statements []string }

func (r *statementRecorder) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	r.statements = append(r.statements, statement)
	return nil, errStatementRecorded
}

// cursorExpression extracts the keyset expression from a statement built by
// sincePredicate/havingSincePredicate + orderBy, and checks both agree.
func cursorExpression(t *testing.T, table, statement string) string {
	t.Helper()
	const gt = " > {since:DateTime64(6,'UTC')} OR ("
	const eq = " = {since:DateTime64(6,'UTC')} AND toString("
	i := strings.LastIndex(statement, gt)
	if i < 0 {
		t.Fatalf("%s: statement carries no keyset predicate:\n%s", table, statement)
	}
	rest := statement[i+len(gt):]
	j := strings.Index(rest, eq)
	if j < 0 {
		t.Fatalf("%s: keyset predicate has no equality arm:\n%s", table, statement)
	}
	expr := rest[:j]
	if !strings.Contains(statement, " ORDER BY "+expr+" ASC, toString(") {
		t.Fatalf("%s: ORDER BY does not page on the predicate's expression %q:\n%s", table, expr, statement)
	}
	return expr
}

func TestEveryProjectedTablePagesOnItsIngestColumnOrIsDisclosed(t *testing.T) {
	registries := map[string][]entityTable{
		"dev_health_clickhouse": entityTables,
		"teams_projects":        teamsProjectsTables(&ambiguityLedger{}, &presenceTelemetryLedger{}, &teamAuthorizationLedger{}, &repositoryOwnershipLedger{}),
	}
	cursor := cursorState{Since: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), After: "k", Space: cursorSpaceIngest}
	seen := map[string]bool{}
	for source, tables := range registries {
		if len(tables) == 0 {
			t.Fatalf("%s: empty registry -- nothing would be checked", source)
		}
		for _, table := range tables {
			seen[table.name] = true
			rec := &statementRecorder{}
			if _, _, err := table.query(context.Background(), rec, "org-1", cursor, 10); !errors.Is(err, errStatementRecorded) {
				t.Fatalf("%s/%s: producer did not reach its query (err=%v)", source, table.name, err)
			}
			if len(rec.statements) != 1 {
				t.Fatalf("%s/%s: %d statements, want exactly 1", source, table.name, len(rec.statements))
			}
			got := cursorExpression(t, table.name, rec.statements[0])
			if want, ok := ingestCursorColumns[table.name]; ok {
				if got != want.expr {
					t.Errorf("%s/%s pages on %q, want its ingest column %q (%s)", source, table.name, got, want.expr, want.writer)
				}
				continue
			}
			if legacy, ok := cursorUnsoundTables[table.name]; ok {
				if got != legacy {
					t.Errorf("%s/%s is disclosed as cursor-unsound on %q but now pages on %q: if it moved to an ingest column, move it to ingestCursorColumns", source, table.name, legacy, got)
				}
				continue
			}
			t.Errorf("%s/%s pages on %q and is in neither the ingest inventory nor the disclosed exemption list", source, table.name, got)
		}
	}
	var stale []string
	for name := range ingestCursorColumns {
		if !seen[name] {
			stale = append(stale, name)
		}
	}
	for name := range cursorUnsoundTables {
		if !seen[name] {
			stale = append(stale, name)
		}
		if _, both := ingestCursorColumns[name]; both {
			t.Errorf("%s is in both the ingest inventory and the exemption list", name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("inventory names tables no registry reads: %v", stale)
	}
	if len(cursorUnsoundTables) != 2 {
		t.Errorf("the disclosed exemption list has %d tables, want exactly the 2 without an ingest column", len(cursorUnsoundTables))
	}
}
