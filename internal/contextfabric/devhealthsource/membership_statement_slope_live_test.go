package devhealthsource_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

func TestLiveMembershipStatementSlope(t *testing.T) {
	ctx := context.Background()
	for _, n := range []int{1000, 4000, 16000} {
		n := n
		t.Run(fmt.Sprintf("items_%d", n), func(t *testing.T) {
			query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
			for _, statement := range productionSchemaDDL() {
				if err := direct.Exec(ctx, statement); err != nil {
					t.Fatalf("create table: %v", err)
				}
			}
			createProjectMembershipPresenceView(t, ctx, direct)
			const org = "50000000-0000-4000-8000-000000000001"
			const repo = "50000000-0000-4000-8000-000000000002"
			base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
			seed := func(label, stmt string, args ...any) {
				t.Helper()
				if err := direct.Exec(ctx, stmt, args...); err != nil {
					t.Fatalf("seed %s: %v", label, err)
				}
			}
			seed("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, 'acme/slope', 'linear', ?)`, repo, org, base)
			for i := 0; i < 40; i++ {
				seed("project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, '', 'linear', 'backlog', '', 1, ?)`,
					fmt.Sprintf("60000000-0000-4000-8000-%012d", i), org, fmt.Sprintf("P%d", i), base)
			}
			seed("work items", fmt.Sprintf(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at)
SELECT concat('linear:W-', toString(number)), toUUID(?), ?, 'item', 'open', '', '', 'linear', concat('60000000-0000-4000-8000-', leftPad(toString(number %% 40), 12, '0')), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number) FROM numbers(%d)`, n), repo, org, base)
			for step, tmpl := range []string{
				`'', concat('60000000-0000-4000-8000-', leftPad(toString(number % 40), 12, '0'))`,
				`concat('60000000-0000-4000-8000-', leftPad(toString(number % 40), 12, '0')), concat('60000000-0000-4000-8000-', leftPad(toString((number + 1) % 40), 12, '0'))`,
				`concat('60000000-0000-4000-8000-', leftPad(toString((number + 1) % 40), 12, '0')), concat('60000000-0000-4000-8000-', leftPad(toString(number % 40), 12, '0'))`,
			} {
				seed("transitions", fmt.Sprintf(`INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id)
SELECT ?, NULL, toUUID(?), 'work_item', concat('linear:W-', toString(number)), 'linear', %s, '', '', '', toDateTime64(?, 3, 'UTC') + toIntervalSecond(number * 3 + %d), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number * 3 + %d), concat('ev-', toString(number), '-%d') FROM numbers(%d)`, tmpl, step, step, step, n), org, repo, base, base)
			}
			source, err := devhealthsource.NewTeamsProjectsSource(query, true)
			if err != nil {
				t.Fatal(err)
			}
			start := serverNow(t, ctx, direct)
			cursor, pages := "", 0
			var firstEnd int
			for ; pages < 500; pages++ {
				batch, ok, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: org, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor})
				if err != nil {
					t.Fatal(err)
				}
				if !ok {
					break
				}
				cursor = batch.NextCursor
				if pages == 0 {
					firstEnd = 1
				}
			}
			_ = firstEnd
			drained := serverNow(t, ctx, direct)
			for i := 0; i < 3; i++ {
				if _, _, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: org, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor}); err != nil {
					t.Fatal(err)
				}
			}
			end := serverNow(t, ctx, direct)
			if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
				t.Fatal(err)
			}
			for _, w := range []struct {
				name     string
				from, to time.Time
			}{{"drain", start, drained}, {"3 steady calls", drained, end}} {
				var count, maxMem, maxRead, maxRows uint64
				if err := direct.QueryRow(ctx, `SELECT count(), max(memory_usage), max(read_bytes), max(read_rows) FROM system.query_log
WHERE type = 'QueryFinish' AND query_start_time_microseconds >= toDateTime64(?, 6, 'UTC') AND query_start_time_microseconds < toDateTime64(?, 6, 'UTC')
AND positionCaseInsensitive(query, 'leadInFrame') > 0 AND query NOT LIKE 'INSERT%'`,
					w.from.UTC().Format("2006-01-02 15:04:05.000000"), w.to.UTC().Format("2006-01-02 15:04:05.000000")).Scan(&count, &maxMem, &maxRead, &maxRows); err != nil {
					t.Fatal(err)
				}
				t.Logf("SLOPE items=%d phase=%q membership_statements=%d peak_memory=%d read_bytes=%d read_rows=%d", n, w.name, count, maxMem, maxRead, maxRows)
			}
			t.Logf("SLOPE items=%d drain_pages=%d", n, pages)
		})
	}
}
