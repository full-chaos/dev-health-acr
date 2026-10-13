package devhealthsource_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

func TestLiveMembershipStatementDuration(t *testing.T) {
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
	}{{"end", base.Add(48 * time.Hour)}, {"tail", base.Add(27500 * time.Second)}} {
		prunedSQL, bindings := devhealthsource.MembershipStatementForTest(changedOrg, cur.since, "", 201, true, true)
		fullSQL, _ := devhealthsource.MembershipStatementForTest(changedOrg, cur.since, "", 201, true, false)
		for name, sql := range map[string]string{
			"main":             fullSQL,
			"pruned_4_threads": strings.Replace(prunedSQL, " SETTINGS max_threads = 1", "", 1),
			"pruned_1_thread":  prunedSQL,
		} {
			for i := 0; i < 7; i++ {
				comment := fmt.Sprintf("dur cur=%s %s", cur.name, name)
				rows, err := query.Query(ctx, "SELECT count() FROM ("+sql+") SETTINGS log_comment='"+comment+"'", bindings)
				if err != nil {
					t.Fatalf("%s: %v", comment, err)
				}
				for rows.Next() {
				}
				rows.Close()
			}
		}
	}
	if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	rows, err := direct.Query(ctx, `SELECT log_comment, count(), quantile(0.5)(query_duration_ms), max(query_duration_ms), max(memory_usage) FROM system.query_log WHERE type = 'QueryFinish' AND log_comment LIKE 'dur %' GROUP BY log_comment ORDER BY log_comment`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		var n uint64
		var p50 float64
		var mx, mem uint64
		if err := rows.Scan(&c, &n, &p50, &mx, &mem); err != nil {
			t.Fatal(err)
		}
		t.Logf("SLOPE [%s] runs=%d p50_ms=%.0f max_ms=%d memory=%d", c, n, p50, mx, mem)
	}
}
