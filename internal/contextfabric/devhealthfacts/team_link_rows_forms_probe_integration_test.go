package devhealthfacts_test

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

type linkRowsForm struct {
	name  string
	shape string
	build func(rows string) string
}

func linkRowsForms() []linkRowsForm {
	const distinct = "SELECT DISTINCT o.team_id AS team_id"
	noDistinct := func(rows string) string {
		if strings.Count(rows, distinct) != 1 {
			panic("inner DISTINCT not found")
		}
		return strings.Replace(rows, distinct, "SELECT o.team_id AS team_id", 1)
	}
	groupBy := func(rows string) string {
		rows = noDistinct(rows)
		if !strings.HasSuffix(rows, "\n)") {
			panic("link rows tail not found")
		}
		return strings.TrimSuffix(rows, "\n)") + "\n  GROUP BY team_id, work_item_id, pr_repo_id, pr_number, link_rank\n)"
	}
	full := func(rows string) string {
		return `SELECT team_id, work_item_id, toString(pr_repo_id), toString(pr_number), toString(link_rank) FROM ` + rows + ` AS lr`
	}
	pr := func(rows string) string {
		return `SELECT lr.team_id, toString(lr.pr_repo_id), toString(lr.pr_number), pr.state, '' FROM (SELECT DISTINCT team_id, pr_repo_id, pr_number FROM ` + rows + `) AS lr
INNER JOIN git_pull_requests AS pr FINAL ON pr.org_id = {org_id:String} AND pr.repo_id = lr.pr_repo_id AND pr.number = lr.pr_number`
	}
	id := func(rows string) string { return rows }
	raw := func(settings string) func(string) string {
		return func(s string) string { return s + " SETTINGS " + settings }
	}
	var out []linkRowsForm
	for _, shape := range []struct {
		name string
		wrap func(string) string
	}{{"full", full}, {"pr", pr}} {
		wrap := shape.wrap
		add := func(name string, rows func(string) string, tail func(string) string) {
			out = append(out, linkRowsForm{name: name, shape: shape.name, build: func(r string) string { return tail(wrap(rows(r))) }})
		}
		add("base", id, id)
		add("1-max_threads=1", id, func(s string) string { return readers.WithSettings(s, readers.Settings{MaxThreads: 1}) })
		add("2-group_by", groupBy, id)
		if shape.name == "pr" {
			add("3-no_inner_distinct", noDistinct, id)
		}
		add("4-join_partial_merge", id, raw("join_algorithm = 'partial_merge'"))
		add("4-join_grace_hash", id, raw("join_algorithm = 'grace_hash'"))
		add("5-group_by+max_threads=1", groupBy, func(s string) string { return readers.WithSettings(s, readers.Settings{MaxThreads: 1}) })
	}
	return out
}

func canonicalRows(t *testing.T, ctx context.Context, client contextpacket.ClickHouseQueryClient, statement, org string) ([]string, error) {
	var rows []string
	err := readers.QueryOrgScopedNamed(ctx, client, "ProbeTeamLinkRows", statement, org, []string{"team-owner"}, func(row contextpacket.ClickHouseRowScanner) error {
		var a, b, c, d, e string
		if err := row.Scan(&a, &b, &c, &d, &e); err != nil {
			return err
		}
		rows = append(rows, a+"|"+b+"|"+c+"|"+d+"|"+e)
		return nil
	})
	sort.Strings(rows)
	return rows, err
}

// probeLinkRowsForms runs every form of the team project link rows at the
// current data size, logs peak memory and median server time, and checks the
// exact row multiset against the base form.
func probeLinkRowsForms(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, direct clickhousedriver.Conn, org, axis, size string) {
	t.Helper()
	ctx, err := devhealthfacts.TeamLinkCandidatesContextForTest(ctx, query, org, []string{"team-owner"})
	if err != nil {
		t.Logf("FORM %s %s: candidates: %v", axis, size, err)
		return
	}
	rowsSQL := devhealthfacts.TeamProjectLinkRowsSQLForTest()
	base := map[string][]string{}
	for _, form := range linkRowsForms() {
		statement := form.build(rowsSQL)
		var peaks, millis []uint64
		var got []string
		var runErr error
		for run := 0; run < 4 && runErr == nil; run++ {
			mark := serverNowMicros(t, ctx, direct)
			got, runErr = canonicalRows(t, ctx, query, statement, org)
			if runErr != nil {
				break
			}
			if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS query_log`); err != nil {
				t.Fatalf("flush logs: %v", err)
			}
			var peak, ms uint64
			if err := direct.QueryRow(ctx, `SELECT max(memory_usage), max(query_duration_ms) FROM system.query_log
WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND query_kind = 'Select' AND position(query, 'ProbeTeamLinkRows') = 0
  AND has(tables, concat(currentDatabase(), '.work_graph_issue_pr')) AND position(query, '_team_link_candidates') > 0`, mark).Scan(&peak, &ms); err != nil {
				t.Fatalf("read query_log: %v", err)
			}
			if run > 0 {
				peaks, millis = append(peaks, peak), append(millis, ms)
			}
		}
		if runErr != nil {
			t.Logf("FORM %s %s %s/%s: ERROR %v", axis, size, form.shape, form.name, runErr)
			continue
		}
		slices.Sort(millis)
		equal := "base"
		if form.name == "base" {
			base[form.shape] = got
		} else if !slices.Equal(base[form.shape], got) {
			equal = fmt.Sprintf("DIFFERENT (%d rows, base %d)", len(got), len(base[form.shape]))
		} else {
			equal = "equal"
		}
		t.Logf("FORM %s %s %s/%s: peak_min=%d peak_max=%d bytes, median_ms=%d, rows=%d, result=%s", axis, size, form.shape, form.name, slices.Min(peaks), slices.Max(peaks), millis[len(millis)/2], len(got), equal)
	}
}
