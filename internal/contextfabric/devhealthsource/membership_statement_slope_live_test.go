package devhealthsource_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

func TestLiveMembershipStatementSlope(t *testing.T) {
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
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for _, cur := range []struct {
		name  string
		since time.Time
	}{{"end", base.Add(48 * time.Hour)}, {"zero", time.Time{}}} {
		for _, pruned := range []bool{true, false} {
			if cur.name == "zero" && !pruned {
				continue
			}
			stmt, bindings := devhealthsource.MembershipStatementForTest(changedOrg, cur.since, "", 100000, true, pruned)
			head := strings.Index(stmt, "FROM (\n") + len("FROM (\n")
			tail := strings.Index(stmt, "\n)\nWHERE 1 = 1")
			body := stmt[head:tail]
			columnAt := strings.Index(body, "\n  UNION ALL\n  SELECT m.subject_kind AS subject_kind, toString(m.repo_id) AS repo_id_str, m.subject_id AS subject_id, ifNull(r.repo, '') AS repo_slug,\n    m.observed_at AS observed_at, '' AS event_id")
			supersededAt := strings.Index(body, "\n  UNION ALL\n  SELECT 'work_item' AS subject_kind, toString(m.repo_id)")
			if columnAt < 0 || supersededAt < columnAt {
				t.Fatalf("arms not found: %d %d", columnAt, supersededAt)
			}
			arms := []string{body[:columnAt], body[columnAt+len("\n  UNION ALL\n"):supersededAt], body[supersededAt+len("\n  UNION ALL\n"):]}
			variants := map[string]string{"full": stmt}
			for i, arm := range arms {
				variants[fmt.Sprintf("arm%d", i+1)] = stmt[:head] + arm + stmt[tail:]
			}
			for name, sql := range variants {
				for _, setting := range []string{"", "max_threads=1"} {
					comment := fmt.Sprintf("cur=%s pruned=%v %s %s", cur.name, pruned, name, setting)
					sets := "log_comment='" + comment + "'"
					if setting != "" {
						sets += ", " + setting
					}
					rows, err := query.Query(ctx, "SELECT count() FROM ("+sql+") SETTINGS "+sets, bindings)
					if err != nil {
						t.Fatalf("%s: %v", comment, err)
					}
					for rows.Next() {
					}
					rows.Close()
				}
			}
		}
	}
	if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	rows, err := direct.Query(ctx, `SELECT log_comment, memory_usage, read_rows, read_bytes FROM system.query_log WHERE type = 'QueryFinish' AND log_comment LIKE 'cur=%' ORDER BY log_comment`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		var m, r, b uint64
		if err := rows.Scan(&c, &m, &r, &b); err != nil {
			t.Fatal(err)
		}
		t.Logf("SLOPE [%s] memory=%d read_rows=%d read_bytes=%d", c, m, r, b)
	}
}
