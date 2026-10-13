package devhealthsource_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

const (
	changedOrg     = "70000000-0000-4000-8000-000000000001"
	changedRepo    = "70000000-0000-4000-8000-000000000002"
	changedBaseIso = "2026-09-01 00:00:00"
)

func changedProjectID(i int) string { return fmt.Sprintf("71000000-0000-4000-8000-%012d", i) }

type changedSeeder func(label, statement string, args ...any)

// seedChangedSubjectsStore writes subjects in every arm of the statement: 3 touches
// (add, move, move back) with ingest stamps spread over time, work items that
// the column arm reads (no history), and work items that history supersedes.
func seedChangedSubjectsStore(t *testing.T, seed changedSeeder, subjects int) {
	t.Helper()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	seed("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, 'acme/changed', 'linear', ?)`, changedRepo, changedOrg, base)
	for i := 0; i < 12; i++ {
		seed("project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, '', 'linear', 'backlog', '', 1, ?)`,
			changedProjectID(i), changedOrg, fmt.Sprintf("P%d", i), base)
	}
	pid := func(expr string) string {
		return fmt.Sprintf("concat('71000000-0000-4000-8000-', leftPad(toString(%s), 12, '0'))", expr)
	}
	seed("history work items", fmt.Sprintf(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, ingested_at)
SELECT concat('linear:H-', toString(number)), toUUID(?), ?, 'h', 'open', '', '', 'linear', %s, toDateTime64(?, 3, 'UTC') + toIntervalSecond(number * 7), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number * 7 + 5) FROM numbers(%d)`, pid("number % 12"), subjects), changedRepo, changedOrg, base, base)
	seed("column work items", fmt.Sprintf(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, ingested_at)
SELECT concat('linear:C-', toString(number)), toUUID(?), ?, 'c', 'open', '', '', 'linear', %s, toDateTime64(?, 3, 'UTC') + toIntervalSecond(number * 5), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number * 5 + 3) FROM numbers(%d)`, pid("number % 12"), subjects), changedRepo, changedOrg, base, base)
	for step, pair := range [][2]string{{"''", pid("number % 12")}, {pid("number % 12"), pid("(number + 1) % 12")}, {pid("(number + 1) % 12"), pid("number % 12")}} {
		seed("transitions", fmt.Sprintf(`INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at)
SELECT ?, NULL, toUUID(?), 'work_item', concat('linear:H-', toString(number)), 'linear', %s, %s, '', '', '', toDateTime64(?, 3, 'UTC') + toIntervalSecond(number * 7 + %d), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number * 7 + %d), concat('ev-', toString(number), '-%d'), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number * 7 + %d + 11) FROM numbers(%d)`,
			pair[0], pair[1], step, step, step, step, subjects), changedOrg, changedRepo, base, base, base)
	}
}

func changedStatementRows(t *testing.T, ctx context.Context, client contextpacket.ClickHouseQueryClient, statement string, bindings []contextpacket.ClickHouseBinding) []string {
	t.Helper()
	rows, err := client.Query(ctx, "SELECT toString(tuple(*)) FROM ("+statement+")", bindings)
	if err != nil {
		t.Fatalf("run statement: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// TestLiveMembershipStatementPrunedEqualsFull runs the statement without and
// with the changed-subject pruning at several cursors and requires the same
// rows. A project and a repository are changed after the cursor with no touch,
// column or history stamp: their rows are not past the cursor either way.
func TestLiveMembershipStatementPrunedEqualsFull(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	seed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	seedChangedSubjectsStore(t, seed, 200)
	late := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Add(30 * 24 * time.Hour)
	seed("project changed alone", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, 'P3 renamed', '', 'linear', 'started', '', 1, ?)`, changedProjectID(3), changedOrg, late)
	seed("repo changed alone", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, 'acme/changed-renamed', 'linear', ?)`, changedRepo, changedOrg, late)

	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for _, ingest := range []bool{true, false} {
		for _, c := range []struct {
			name  string
			since time.Time
			after string
		}{
			{"empty cursor", time.Time{}, ""},
			{"early", base.Add(100 * time.Second), ""},
			{"middle", base.Add(700 * time.Second), ""},
			{"middle with tie key", base.Add(700 * time.Second), "work_item"},
			{"beyond every stamp", base.Add(48 * time.Hour), ""},
		} {
			t.Run(fmt.Sprintf("ingest=%v/%s", ingest, c.name), func(t *testing.T) {
				fullSQL, bindings := devhealthsource.MembershipStatementForTest(changedOrg, c.since, c.after, 100000, ingest, false)
				prunedSQL, _ := devhealthsource.MembershipStatementForTest(changedOrg, c.since, c.after, 100000, ingest, true)
				if fullSQL == prunedSQL && !c.since.IsZero() {
					t.Fatal("pruned and full statements are the same text: the pruning is not applied")
				}
				full := changedStatementRows(t, ctx, query, fullSQL, bindings)
				pruned := changedStatementRows(t, ctx, query, prunedSQL, bindings)
				if len(full) != len(pruned) {
					t.Fatalf("pruned statement returned %d rows, full statement %d", len(pruned), len(full))
				}
				for i := range full {
					if full[i] != pruned[i] {
						t.Fatalf("row %d differs:\nfull   %s\npruned %s", i, full[i], pruned[i])
					}
				}
				t.Logf("%s ingest=%v: %d rows equal", c.name, ingest, len(full))
				if c.name == "middle" && (len(full) == 0) {
					t.Fatal("the middle cursor returned no row: the differential compares nothing")
				}
			})
		}
	}
}

type countingMembershipClient struct {
	contextpacket.ClickHouseQueryClient
	membership int
}

func (c *countingMembershipClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if containsFold(statement, "leadInFrame") {
		c.membership++
	}
	return c.ClickHouseQueryClient.Query(ctx, statement, bindings)
}

func containsFold(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if equalFold(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// TestLiveMembershipStatementRunsPerTick drains the teams/projects source and
// counts how often the membership statement runs per call at the caught-up
// cursor, and what the server measured for it.
func TestLiveMembershipStatementRunsPerTick(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	seed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	seedChangedSubjectsStore(t, seed, 4000)
	counting := &countingMembershipClient{ClickHouseQueryClient: query}
	source, err := devhealthsource.NewTeamsProjectsSource(counting, true)
	if err != nil {
		t.Fatal(err)
	}
	cursor := ""
	for page := 0; ; page++ {
		if page > 2000 {
			t.Fatal("the source did not catch up")
		}
		batch, ok, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: changedOrg, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		cursor = batch.NextCursor
	}
	steadyStart := serverNow(t, ctx, direct)
	for call := 0; call < 4; call++ {
		before := counting.membership
		if _, _, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: changedOrg, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor}); err != nil {
			t.Fatal(err)
		}
		ran := counting.membership - before
		t.Logf("SLOPE steady call %d: membership statements=%d", call, ran)
		if limit := map[bool]int{true: 2, false: 1}[call == 0]; ran > limit {
			t.Fatalf("steady call %d ran the membership statement %d times, want at most %d", call, ran, limit)
		}
	}
	if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	var count, peak uint64
	if err := direct.QueryRow(ctx, `SELECT count(), max(memory_usage) FROM system.query_log
WHERE type = 'QueryFinish' AND query_start_time_microseconds >= toDateTime64(?, 6, 'UTC') AND positionCaseInsensitive(query, 'leadInFrame') > 0 AND query NOT LIKE 'INSERT%'`,
		steadyStart.UTC().Format("2006-01-02 15:04:05.000000")).Scan(&count, &peak); err != nil {
		t.Fatal(err)
	}
	t.Logf("SLOPE steady 4 calls: statements=%d peak_memory=%d", count, peak)
	if count == 0 {
		t.Fatal("the query log holds no membership statement: the measure did not happen")
	}
	if peak >= 32<<20 {
		t.Fatalf("caught-up membership statement peaked at %d bytes at 4000 subjects, want under 32 MiB", peak)
	}
}
