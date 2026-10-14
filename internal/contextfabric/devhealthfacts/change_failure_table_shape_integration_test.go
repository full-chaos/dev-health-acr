package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

type statementRecorder struct {
	inner contextpacket.ClickHouseQueryClient
	mu    sync.Mutex
	seen  []string
}

func (r *statementRecorder) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	r.mu.Lock()
	r.seen = append(r.seen, statement)
	r.mu.Unlock()
	return r.inner.Query(ctx, statement, bindings)
}

// TestChangeFailureReadsBothTableShapesAgainstRealClickHouse runs the
// production reader against repo_change_failure_daily as it is before the ops
// migration that adds the status columns and as it is after it. The old shape
// must answer exactly what it answered before, naming no column it does not
// have; the new shape serves the status counts, and a row written before the
// migration (both columns at their default) reads as nothing known.
func TestChangeFailureReadsBothTableShapesAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	_, direct := sharedClickHouseFixture(t)
	addr := sharedClickHouseAddrFor(t)
	const orgID = "org-cf-shape"
	const repoID = "77777777-7777-7777-7777-0000000000a1"
	const oldRepo = "77777777-7777-7777-7777-0000000000a2"
	day := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")

	for _, tc := range []struct {
		name     string
		database string
		newShape bool
	}{
		{"old shape", "cf_shape_old", false},
		{"new shape", "cf_shape_new", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := direct.Exec(ctx, "DROP DATABASE IF EXISTS "+tc.database); err != nil {
				t.Fatalf("drop: %v", err)
			}
			if err := direct.Exec(ctx, "CREATE DATABASE "+tc.database); err != nil {
				t.Fatalf("create database: %v", err)
			}
			t.Cleanup(func() { _ = direct.Exec(ctx, "DROP DATABASE IF EXISTS "+tc.database) })
			table := devhealthschema.DDLWithoutPendingRollout(tc.database, "repo_change_failure_daily")
			if tc.newShape {
				table = devhealthschema.DDLWithColumnType(tc.database, "repo_change_failure_daily", "org_id", "String")
			}
			for _, ddl := range []string{
				devhealthschema.DDLWithColumnType(tc.database, "repo_metrics_daily", "change_failure_rate", "Float64"),
				table,
				devhealthschema.DDLWithColumnType(tc.database, "git_pull_requests", "state", "Nullable(String)"),
			} {
				if err := direct.Exec(ctx, ddl); err != nil {
					t.Fatalf("create fixture: %v", err)
				}
			}
			// devhealthschema:not-a-production-replica rows go into tables whose schema comes only from devhealthschema DDL
			columns := "org_id, repo_id, day, deployments_count, failed_deployments_native, failed_deployments_heuristic, incidents_direct, incidents_via_deployment, computed_at"
			insert := func(repo string, values string) {
				t.Helper()
				if err := direct.Exec(ctx, fmt.Sprintf("INSERT INTO %s.repo_change_failure_daily (%s) VALUES (%s)", tc.database, columns, values)); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}
			// oldRepo: a row written before the migration (status columns left at default).
			insert(oldRepo, fmt.Sprintf("'%s', '%s', '%s', 4, 1, 0, 1, 0, '%s 09:00:00'", orgID, oldRepo, day, day))
			if tc.newShape {
				stmt := fmt.Sprintf("INSERT INTO %s.repo_change_failure_daily (%s, failed_deployments_status, deployments_status_known) VALUES ('%s', '%s', '%s', 4, 1, 0, 1, 0, '%s 09:00:00', 2, 3)",
					tc.database, columns, orgID, repoID, day, day)
				if err := direct.Exec(ctx, stmt); err != nil {
					t.Fatalf("seed new shape: %v", err)
				}
			} else {
				insert(repoID, fmt.Sprintf("'%s', '%s', '%s', 4, 1, 0, 1, 0, '%s 09:00:00'", orgID, repoID, day, day))
			}
			const zeroKnownRepo = "77777777-7777-7777-7777-0000000000a3"
			const mixedRepo = "77777777-7777-7777-7777-0000000000a4"
			subjectsRead := []string{repoID, oldRepo}
			if tc.newShape {
				// An old writer pod during a roll: deployments, both status columns at 0.
				insert(zeroKnownRepo, fmt.Sprintf("'%s', '%s', '%s', 4, 0, 0, 0, 0, '%s 09:00:00'", orgID, zeroKnownRepo, day, day))
				// A window of two days: one written by an old pod (K = 0), one by the new writer (K = 2 of 2, one failed).
				insert(mixedRepo, fmt.Sprintf("'%s', '%s', '%s', 4, 0, 0, 0, 0, '%s 09:00:00'", orgID, mixedRepo, day, day))
				day2 := time.Now().UTC().AddDate(0, 0, -2).Format("2006-01-02")
				stmt := fmt.Sprintf("INSERT INTO %s.repo_change_failure_daily (%s, failed_deployments_status, deployments_status_known) VALUES ('%s', '%s', '%s', 2, 0, 0, 0, 0, '%s 09:00:00', 1, 2)",
					tc.database, columns, orgID, mixedRepo, day2, day2)
				if err := direct.Exec(ctx, stmt); err != nil {
					t.Fatalf("seed mixed window: %v", err)
				}
				subjectsRead = append(subjectsRead, zeroKnownRepo, mixedRepo)
			}
			for _, repo := range subjectsRead {
				stmt := fmt.Sprintf(`INSERT INTO %s.repo_metrics_daily (repo_id, org_id, day, commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, mttr_hours, bus_factor, code_ownership_gini, computed_at) VALUES ('%s', '%s', '%s', 1, 1, 1.0, 0.9, NULL, 1, 0.1, '%s 10:00:00')`,
					tc.database, repo, orgID, day, day)
				if err := direct.Exec(ctx, stmt); err != nil {
					t.Fatalf("seed metrics: %v", err)
				}
			}
			client, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
				DSN: "clickhouse://acr:acr@" + addr + "/" + tc.database, DialTimeout: 10 * time.Second, MaxResultRows: productionMaxResultRows(),
			})
			if err != nil {
				t.Fatalf("open client: %v", err)
			}
			t.Cleanup(func() { _ = client.Close() })
			recorder := &statementRecorder{inner: client}
			provider := findProvider(t, devhealthfacts.NewProviders(recorder), contextfabric.FactMetrics)
			result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind: contextfabric.FactMetrics, Subjects: subjectRefs(subjectsRead),
			})
			if err != nil {
				t.Fatalf("ReadFacts on the %s: %v", tc.name, err)
			}
			byRepo := map[string]contextfabric.CanonicalFact{}
			for _, fact := range result.Facts {
				byRepo[fact.Subject.CanonicalID] = fact
			}
			named := false
			for _, statement := range recorder.seen {
				if strings.Contains(statement, "FROM repo_change_failure_daily") && strings.Contains(statement, "failed_deployments_status") {
					named = true
				}
			}
			if named != tc.newShape {
				t.Fatalf("statement names the status columns = %v on the %s", named, tc.name)
			}
			for _, repo := range []string{repoID, oldRepo} {
				fact, ok := byRepo["repository:"+repo]
				if !ok {
					t.Fatalf("no fact for %s", repo)
				}
				if got := stringField(t, fact, "change_failure_rate_state"); got != "measured" {
					t.Fatalf("state = %q", got)
				}
				wantRate := 0.25
				if tc.newShape && repo == repoID {
					wantRate = 0.75
				}
				if v := fact.Fields["change_failure_rate"]; v.Number == nil || *v.Number != wantRate {
					t.Fatalf("rate = %#v, want %v", v, wantRate)
				}
				_, hasKnown := fact.Fields["change_failure_status_known_deployments_count"]
				if hasKnown != tc.newShape {
					t.Fatalf("status known served = %v on the %s", hasKnown, tc.name)
				}
			}
			if tc.newShape {
				zero := byRepo["repository:"+zeroKnownRepo]
				if got := stringField(t, zero, "change_failure_rate_state"); got != "unknown_no_incident_evidence" {
					t.Fatalf("K = 0 with deployments: state %q, want unknown", got)
				}
				if got := stringField(t, zero, "change_failure_unknown_reason"); got != "deployments_without_outcome" {
					t.Fatalf("K = 0 with deployments: reason %q", got)
				}
				if _, has := zero.Fields["change_failure_rate"]; has {
					t.Fatalf("K = 0 with deployments served a rate: %#v", zero.Fields["change_failure_rate"])
				}
				mixed := byRepo["repository:"+mixedRepo]
				if got := stringField(t, mixed, "change_failure_evidence_basis"); got != "status" {
					t.Fatalf("mixed window basis %q", got)
				}
				if v := mixed.Fields["change_failure_rate"]; v.Number == nil || *v.Number != 0.5 {
					t.Fatalf("mixed window rate = %#v, want 1 failed over K = 2", v)
				}
				if v := mixed.Fields["change_failure_status_coverage"]; v.Number == nil || *v.Number < 0.333 || *v.Number > 0.334 {
					t.Fatalf("mixed window coverage = %#v, want 2 of 6", v)
				}
				fact := byRepo["repository:"+repoID]
				if v := fact.Fields["change_failure_status_failed_deployments_count"]; v.Integer == nil || *v.Integer != 2 {
					t.Fatalf("status failed = %#v", v)
				}
				if v := fact.Fields["change_failure_failed_deployments_count"]; v.Integer == nil || *v.Integer != 3 {
					t.Fatalf("failed total = %#v, want native 1 + status 2", v)
				}
				if v := byRepo["repository:"+oldRepo].Fields["change_failure_status_known_deployments_count"]; v.Integer == nil || *v.Integer != 0 {
					t.Fatalf("a row written before the migration reads known = %#v, want 0", v)
				}
			}
		})
	}
}

func subjectRefs(ids []string) []contextfabric.SubjectRef {
	out := make([]contextfabric.SubjectRef, 0, len(ids))
	for _, id := range ids {
		out = append(out, repoSubject(id))
	}
	return out
}
