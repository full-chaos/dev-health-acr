package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// TestChangeFailureRateColumnTypesAgainstRealClickHouse reads the same
// repository series from a table whose change_failure_rate is each accepted
// type. A stored 0 is a real rate and stays served; a NULL is not applicable
// and is served as no field.
func TestChangeFailureRateColumnTypesAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	_, direct := sharedClickHouseFixture(t)
	addr := sharedClickHouseAddrFor(t)
	const orgID = "org-cfr"
	const repoID = "66666666-6666-6666-6666-000000000001"
	cases := []struct {
		name, typ, database string
		nullDay             bool
	}{
		{"float64", "Float64", "cfr_float64", false},
		{"nullable_float64", "Nullable(Float64)", "cfr_nullable", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := direct.Exec(ctx, "CREATE DATABASE IF NOT EXISTS "+tc.database); err != nil {
				t.Fatalf("create database: %v", err)
			}
			ddl := devhealthschema.DDLWithColumnType(tc.database, "repo_metrics_daily", "change_failure_rate", tc.typ)
			if err := direct.Exec(ctx, ddl); err != nil {
				t.Fatalf("create fixture: %v", err)
			}
			second := "0.0"
			if tc.nullDay {
				second = "NULL"
			}
			for i, value := range []string{"0.25", second, "0.0"} {
				day := time.Now().UTC().AddDate(0, 0, -(3 - i)).Format("2006-01-02")
				stmt := fmt.Sprintf(`INSERT INTO %s.repo_metrics_daily (repo_id, org_id, day, commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, mttr_hours, bus_factor, code_ownership_gini, computed_at) VALUES ('%s', '%s', '%s', 1, 1, 1.0, %s, NULL, 1, 0.1, '%s 10:00:00')`,
					tc.database, repoID, orgID, day, value, day)
				if err := direct.Exec(ctx, stmt); err != nil {
					t.Fatalf("seed day %s: %v", day, err)
				}
			}
			client, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
				DSN: "clickhouse://acr:acr@" + addr + "/" + tc.database, DialTimeout: 10 * time.Second, MaxResultRows: productionMaxResultRows(),
			})
			if err != nil {
				t.Fatalf("open client: %v", err)
			}
			t.Cleanup(func() { _ = client.Close() })
			provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
			result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject(repoID)},
			})
			if err != nil {
				t.Fatalf("ReadFacts: %v", err)
			}
			if len(result.Facts) != 1 {
				t.Fatalf("facts = %d, want 1 (state %s, reason %q)", len(result.Facts), result.State, result.Reason)
			}
			rows := result.Facts[0].Fields["daily_metrics"].Rows // newest day first
			if len(rows) != 3 {
				t.Fatalf("rows = %d, want 3", len(rows))
			}
			if v, ok := rows[0].Fields["change_failure_rate"]; !ok || v.Number == nil || *v.Number != 0 {
				t.Fatalf("a stored 0 must be served as 0: %#v", rows[0].Fields)
			}
			_, middle := rows[1].Fields["change_failure_rate"]
			if middle == tc.nullDay {
				t.Fatalf("middle day field present = %v, want %v", middle, !tc.nullDay)
			}
			if v := rows[2].Fields["change_failure_rate"]; v.Number == nil || *v.Number != 0.25 {
				t.Fatalf("oldest day = %#v, want 0.25", v)
			}
			wantDays := int64(3)
			note := strings.Contains(result.Reason, "change_failure_rate not applicable on 1 of 3 days")
			if tc.nullDay {
				wantDays = 2
			}
			if note != tc.nullDay {
				t.Fatalf("Reason = %q, coverage note present = %v, want %v", result.Reason, note, tc.nullDay)
			}
			if v := result.Facts[0].Fields["change_failure_rate_days"]; v.Integer == nil || *v.Integer != wantDays {
				t.Fatalf("change_failure_rate_days = %#v, want %d", v, wantDays)
			}
		})
	}
}
