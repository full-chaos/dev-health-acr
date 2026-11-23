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

// checksum folds the rows into count and two independent 64-bit hash sums: the
// client caps result rows, and equal sums of both hashes make a row-multiset difference negligible.
func checksum(inner string) string {
	return `SELECT toString(count()), toString(sum(cityHash64(c1, c2, c3, c4, c5))), toString(sum(sipHash64(c1, c2, c3, c4, c5))), '', '' FROM (` + inner + `)`
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
		return checksum(`SELECT team_id AS c1, work_item_id AS c2, toString(pr_repo_id) AS c3, toString(pr_number) AS c4, toString(link_rank) AS c5 FROM ` + rows + ` AS lr`)
	}
	pr := func(rows string) string {
		return checksum(`SELECT lr.team_id AS c1, toString(lr.pr_repo_id) AS c2, toString(lr.pr_number) AS c3, pr.state AS c4, '' AS c5 FROM (SELECT DISTINCT team_id, pr_repo_id, pr_number FROM ` + rows + `) AS lr
INNER JOIN git_pull_requests AS pr FINAL ON pr.org_id = {org_id:String} AND pr.repo_id = lr.pr_repo_id AND pr.number = lr.pr_number`)
	}
	mix := func(rows string) string {
		return checksum(`SELECT DISTINCT toString(pr_repo_id) AS c1, toString(pr_number) AS c2, team_id AS c3, if(link_rank < 3, work_item_id, '') AS c4, toString(toUInt8(link_rank)) AS c5 FROM ` + rows)
	}
	rollup := func(rows string) string {
		return checksum(`SELECT lr.team_id AS c1, toString(lr.pr_repo_id) AS c2, ifNull(any(r.repo), '') AS c3, toString(min(lr.link_rank)) AS c4, toString(uniqExact(lr.pr_number)) AS c5
FROM ` + rows + ` AS lr
LEFT JOIN repos AS r FINAL ON r.id = lr.pr_repo_id AND r.org_id = {org_id:String}
GROUP BY lr.team_id, lr.pr_repo_id`)
	}
	id := func(rows string) string { return rows }
	setting := func(clause string) func(string) string {
		return func(s string) string { return s + " SETTINGS " + clause }
	}
	const sent = "cw.work_item_id IN (SELECT work_item_id FROM " + devhealthfacts.TeamLinkCandidatesTableName + ")"
	prefilter := func(rows string) string {
		if strings.Count(rows, sent) != 1 {
			panic("candidate prune not found")
		}
		return strings.Replace(rows, sent, sent+" AND cw.work_item_id IN (SELECT work_item_id FROM work_graph_issue_pr WHERE org_id = {org_id:String} AND cityHash64(work_item_id) IN (SELECT cityHash64(work_item_id) FROM "+devhealthfacts.TeamLinkCandidatesTableName+"))", 1)
	}
	var out []linkRowsForm
	for _, shape := range []struct {
		name string
		wrap func(string) string
	}{{"full", full}, {"pr", pr}, {"mix", mix}, {"rollup", rollup}} {
		wrap := shape.wrap
		add := func(name string, rows func(string) string, tail func(string) string) {
			out = append(out, linkRowsForm{name: name, shape: shape.name, build: func(r string) string { return tail(wrap(rows(r))) }})
		}
		add("base", id, id)
		add("pm", id, setting("join_algorithm = 'partial_merge'"))
		add("auto", id, setting("join_algorithm = 'auto'"))
		add("full_sorting_merge", id, setting("join_algorithm = 'full_sorting_merge'"))
		add("pm+max_rows_in_join=100000", id, setting("join_algorithm = 'partial_merge', max_rows_in_join = 100000"))
		add("pm+right_blocks=8192", id, setting("join_algorithm = 'partial_merge', partial_merge_join_rows_in_right_blocks = 8192"))
		add("prefilter", prefilter, id)
		add("prefilter+pm", prefilter, setting("join_algorithm = 'partial_merge'"))
	}
	_, _ = groupBy, noDistinct
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
		for run := 0; run < 6 && runErr == nil; run++ {
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
