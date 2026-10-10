package devhealthfacts_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// TestChangeFailureReadsStoredCountsAgainstRealClickHouse seeds
// repo_change_failure_daily through the production table definition
// (ReplacingMergeTree(computed_at)) and reads the repository facts: the newest
// computed_at per (org, repo, day) wins before the counts are summed, a
// repository with deployments and no incident is unknown, one without
// deployments is not applicable, and one with no row has no count.
func TestChangeFailureReadsStoredCountsAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	_, direct := sharedClickHouseFixture(t)
	addr := sharedClickHouseAddrFor(t)
	const orgID = "org-cf-counts"
	const database = "cf_counts"
	repos := map[string]string{
		"measured":   "77777777-7777-7777-7777-000000000001",
		"unknown":    "77777777-7777-7777-7777-000000000002",
		"notapplic":  "77777777-7777-7777-7777-000000000003",
		"nostoredrw": "77777777-7777-7777-7777-000000000004",
		"nometrics":  "77777777-7777-7777-7777-000000000005",
	}
	if err := direct.Exec(ctx, "DROP DATABASE IF EXISTS "+database); err != nil {
		t.Fatalf("drop stale database: %v", err)
	}
	if err := direct.Exec(ctx, "CREATE DATABASE "+database); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() { _ = direct.Exec(ctx, "DROP DATABASE IF EXISTS "+database) })
	for _, ddl := range []string{
		devhealthschema.DDLWithColumnType(database, "repo_metrics_daily", "change_failure_rate", "Float64"),
		devhealthschema.DDLWithColumnType(database, "repo_change_failure_daily", "org_id", "String"),
		devhealthschema.DDLWithColumnType(database, "git_pull_requests", "state", "Nullable(String)"),
	} {
		if err := direct.Exec(ctx, ddl); err != nil {
			t.Fatalf("create fixture: %v", err)
		}
	}
	// devhealthschema:not-a-production-replica rows are inserted into tables whose schema comes only from devhealthschema.DDL, never defined here
	day := func(back int) string { return time.Now().UTC().AddDate(0, 0, -back).Format("2006-01-02") }
	insert := func(repo, d string, deployments, native, heuristic, direct_, via int, computedAt string) {
		t.Helper()
		stmt := fmt.Sprintf(`INSERT INTO %s.repo_change_failure_daily (org_id, repo_id, day, deployments_count, failed_deployments_native, failed_deployments_heuristic, incidents_direct, incidents_via_deployment, computed_at) VALUES ('%s', '%s', '%s', %d, %d, %d, %d, %d, '%s')`,
			database, orgID, repo, d, deployments, native, heuristic, direct_, via, computedAt)
		if err := direct.Exec(ctx, stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// measured: day 1 is rerun (an older row with a wrong count must lose);
	// 2 deployments + 2 deployments, 1 native + 1 heuristic failed = 0.5.
	insert(repos["measured"], day(1), 9, 9, 0, 1, 0, day(1)+" 08:00:00")
	insert(repos["measured"], day(1), 2, 1, 0, 1, 0, day(1)+" 09:00:00")
	insert(repos["measured"], day(2), 2, 0, 1, 0, 1, day(2)+" 09:00:00")
	insert(repos["unknown"], day(1), 3, 0, 0, 0, 0, day(1)+" 09:00:00")
	insert(repos["notapplic"], day(1), 0, 0, 0, 1, 0, day(1)+" 09:00:00")
	// nometrics has stored counts and no repo_metrics_daily row at all.
	insert(repos["nometrics"], day(1), 4, 1, 0, 1, 0, day(1)+" 09:00:00")
	for name, repo := range repos {
		if name == "nometrics" {
			continue
		}
		stmt := fmt.Sprintf(`INSERT INTO %s.repo_metrics_daily (repo_id, org_id, day, commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, mttr_hours, bus_factor, code_ownership_gini, computed_at) VALUES ('%s', '%s', '%s', 1, 1, 1.0, 0.9, NULL, 1, 0.1, '%s 10:00:00')`,
			database, repo, orgID, day(1), day(1))
		if err := direct.Exec(ctx, stmt); err != nil {
			t.Fatalf("seed metrics %s: %v", name, err)
		}
	}
	client, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/" + database, DialTimeout: 10 * time.Second, MaxResultRows: productionMaxResultRows(),
	})
	if err != nil {
		t.Fatalf("open client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	subjects := make([]contextfabric.SubjectRef, 0, len(repos))
	for _, repo := range repos {
		subjects = append(subjects, repoSubject(repo))
	}
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	byRepo := map[string]contextfabric.CanonicalFact{}
	for _, fact := range result.Facts {
		byRepo[fact.Subject.CanonicalID] = fact
	}
	for name, want := range map[string]string{
		"measured": "measured", "unknown": "unknown_no_incident_evidence",
		"notapplic": "not_applicable_no_deployments", "nostoredrw": "", "nometrics": "measured",
	} {
		fact, ok := byRepo["repository:"+repos[name]]
		if !ok {
			t.Fatalf("%s: no fact", name)
		}
		if want == "" {
			if _, has := fact.Fields["change_failure_rate_state"]; has {
				t.Fatalf("%s: a view with no stored row has no state", name)
			}
			continue
		}
		if got := stringField(t, fact, "change_failure_rate_state"); got != want {
			t.Fatalf("%s: state %q, want %q", name, got, want)
		}
		_, hasRate := fact.Fields["change_failure_rate"]
		if hasRate != (name == "measured" || name == "nometrics") {
			t.Fatalf("%s: rate present = %v", name, hasRate)
		}
	}
	measured := byRepo["repository:"+repos["measured"]]
	if v := measured.Fields["change_failure_rate"]; v.Number == nil || *v.Number != 0.5 {
		t.Fatalf("measured rate = %#v, want 0.5 (newest rerun per day, summed)", v)
	}
	if v := measured.Fields["change_failure_deployments_count"]; v.Integer == nil || *v.Integer != 4 {
		t.Fatalf("deployments = %#v, want 4", v)
	}
}
