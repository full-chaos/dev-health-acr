package devhealthfacts_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2"
	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/full-chaos/dev-health-go/readers"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

const (
	// The cover statement reads work_graph_issue_pr once. A second pass over
	// the table (a per-team or per-chunk scan) doubles the rows read.
	nativeCoverReadRowsPerLink = 1.5
	// Its result is the covered ids of the fallback rows only; 16 MiB is far
	// above the prod-width measurement and far below an org-wide id set at
	// fixture width.
	nativeCoverPeakBoundMiB = 16.0
)

// The team linked mix drops a fallback link (explicit_text, heuristic) that a
// native link covers, by its issue or by its pull request, before the linked
// set is sent. EXECUTED at the largest real organization's shape (24,024 link
// rows, a third of them explicit_text next to native links, every repository
// and team): the served rows equal the rows read after the covered fallback
// rows are deleted from the table, differ from the rev 88 statement (which
// counts every tier), and the one cover statement reads the table once with
// a small peak.
func TestTeamLinkedMixDropsFallbackLinksCoveredByNativeAtTheLargestOrgShape(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-linked-native-cover"
	shape := linkedMemoryProdShape
	shape.fallbackEvery = 3
	query, direct, _, ids := newLinkedProdShapeStore(t, ctx, orgID, shape)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	teams := make([]string, shape.teams)
	for i := range teams {
		teams[i] = fmt.Sprintf("T-%d", i)
	}
	scopeJSON, err := json.Marshal(ids)
	if err != nil {
		t.Fatal(err)
	}

	var covered uint64
	if err := direct.QueryRow(ctx, `SELECT count() FROM work_graph_issue_pr AS f WHERE f.org_id = ? AND f.provenance = 'explicit_text'
  AND (f.work_item_id IN (SELECT work_item_id FROM work_graph_issue_pr WHERE org_id = ? AND provenance = 'native')
    OR concat(toString(f.repo_id), '|', toString(f.pr_number)) IN (SELECT concat(toString(repo_id), '|', toString(pr_number)) FROM work_graph_issue_pr WHERE org_id = ? AND provenance = 'native'))`,
		orgID, orgID, orgID).Scan(&covered); err != nil {
		t.Fatal(err)
	}
	var fallback uint64
	if err := direct.QueryRow(ctx, `SELECT count() FROM work_graph_issue_pr WHERE org_id = ? AND provenance = 'explicit_text'`, orgID).Scan(&fallback); err != nil {
		t.Fatal(err)
	}
	if covered == 0 || covered >= fallback {
		t.Fatalf("the seed does not hold both covered and uncovered fallback rows (%d covered of %d): the rule cannot be told from no rule", covered, fallback)
	}

	_ = linkedMixRowsOf(t, ctx, provider, orgID, teams)
	mark := serverNowMicros(t, ctx, direct)
	got := linkedMixRowsOf(t, ctx, provider, orgID, teams)

	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (measurement did not happen): %v", err)
	}
	var peak, readRows, runs uint64
	if err := direct.QueryRow(ctx, `SELECT max(memory_usage), max(read_rows), count() FROM system.query_log
WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.work_graph_issue_pr')) AND position(query, 'groupUniqArray(work_item_id)') > 0`, mark).Scan(&peak, &readRows, &runs); err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	chunks := (shape.teams + 99) / 100
	t.Logf("native cover at the largest org shape: %d link rows (%d fallback, %d covered), %d cover statements for one read of %d teams; peak %.2f MiB (bound %.0f), read_rows %d (bound %.0f)",
		shape.links, fallback, covered, runs, shape.teams, float64(peak)/(1<<20), nativeCoverPeakBoundMiB, readRows, nativeCoverReadRowsPerLink*float64(shape.links))
	if int(runs) != chunks {
		t.Errorf("the cover statement ran %d times for %d team chunks: measurement did not happen or it runs per team", runs, chunks)
	}
	if float64(peak)/(1<<20) > nativeCoverPeakBoundMiB {
		t.Errorf("the native cover statement peaked at %.2f MiB, bound %.0f MiB", float64(peak)/(1<<20), nativeCoverPeakBoundMiB)
	}
	if float64(readRows) > nativeCoverReadRowsPerLink*float64(shape.links) {
		t.Errorf("the native cover statement read %d rows of a %d row table, bound %.0f: it scans the table more than once", readRows, shape.links, nativeCoverReadRowsPerLink*float64(shape.links))
	}

	rev88 := readLinkedMixRows(t, ctx, query, orgID, teams, devhealthfacts.LinkedMixFragment{Name: "rev88", SQL: readWideIDForm(t, "team_linked_mix_rev88.sql"), Bindings: []readers.Binding{{Name: "scope_json", Value: string(scopeJSON)}}})
	differs := false
	for team, want := range rev88 {
		g := got[team]
		if g.Repos != want.repos || !closeEnough(g.Bugfix, want.bugfix) {
			differs = true
		}
	}
	if !differs {
		t.Errorf("the served rows equal the rev 88 rows that count every tier: the covered fallback rows changed nothing, the seed cannot show the rule")
	}

	keysIssue, keysPR := coveredFallbackKeys(t, ctx, direct, orgID)
	// Merges are stopped on this table (the measurement keeps its parts), so an
	// ALTER mutation would never finish; a lightweight delete masks the rows at
	// once.
	if err := direct.Exec(ctx, `DELETE FROM work_graph_issue_pr WHERE org_id = {org:String} AND provenance = 'explicit_text' AND (work_item_id IN {issues:Array(String)} OR concat(toString(repo_id), '|', toString(pr_number)) IN {prs:Array(String)}) SETTINGS lightweight_deletes_sync = 2`,
		clickhouse.Named("org", orgID), clickhouse.Named("issues", keysIssue), clickhouse.Named("prs", keysPR)); err != nil {
		t.Fatalf("delete the covered fallback rows: %v", err)
	}
	want := linkedMixRowsOf(t, ctx, provider, orgID, teams)
	if len(want) != len(got) || len(want) < 2 {
		t.Fatalf("rows after deleting the covered fallback rows: %d teams, served with the rule: %d", len(want), len(got))
	}
	for team, w := range want {
		g, ok := got[team]
		if !ok || g.Repos != w.Repos || !closeEnough(g.Bugfix, w.Bugfix) {
			t.Errorf("team %s: served with the rule %+v, with the covered fallback rows deleted %+v", team, g, w)
		}
		for theme, v := range w.Theme {
			if !closeEnough(g.Theme[theme], v) {
				t.Errorf("team %s theme %s: served with the rule %v, with the covered fallback rows deleted %v", team, theme, g.Theme[theme], v)
			}
		}
	}
}

// coveredFallbackKeys are the native issues and the native pull request keys of
// the organization: the covers of its fallback rows.
func coveredFallbackKeys(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string) (issues, pullRequests []string) {
	t.Helper()
	if err := direct.QueryRow(ctx, `SELECT groupUniqArray(work_item_id), groupUniqArray(concat(toString(repo_id), '|', toString(pr_number))) FROM work_graph_issue_pr WHERE org_id = ? AND provenance = 'native'`, orgID).Scan(&issues, &pullRequests); err != nil {
		t.Fatalf("read the native covers: %v", err)
	}
	return issues, pullRequests
}

func linkedMixRowsOf(t *testing.T, ctx context.Context, provider contextfabric.FactProvider, orgID string, teams []string) map[string]devhealthfacts.LinkedMixValues {
	t.Helper()
	rows, err := devhealthfacts.TeamLinkedMixRowsForTest(ctx, provider, orgID, teams)
	if err != nil {
		t.Fatalf("production linked mix read: %v", err)
	}
	return rows
}
