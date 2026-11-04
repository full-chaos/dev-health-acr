package devhealthfacts_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// teamBlockerTables seeds a team blockers read that reaches the edge-row statements.
func teamBlockerTables() []fakeTable {
	return []fakeTable{
		{match: "GROUP BY team_id, repo_key", rows: [][]any{{"CHAOS", "00000000-0000-4000-8000-000000000001", "acme/repo"}}},
		{match: "is_open AND start_unix IS NOT NULL", rows: [][]any{{"CHAOS", int64(4), int64(5), int64(0), int64(0), int64(0), int64(0), int64(5), int64(5), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).UnixMilli()}}},
		{match: "SELECT toInt64(uniqExact(if(", rows: [][]any{{int64(0)}}},
		{match: "arrayJoin({team_repo_pairs", rows: [][]any{{"CHAOS", int64(4), int64(5), int64(0), int64(0), int64(0), int64(0)}}},
		{match: "FROM work_item_dependencies", rows: [][]any{{"00000000-0000-4000-8000-000000000001", int64(4), int64(5), int64(0), int64(3)}}},
	}
}

func readTeamBlockers(t *testing.T, client *fakeClient) (contextfabric.FactProviderResult, error) {
	t.Helper()
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactBlockers)
	return provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
}

// CHAOS-13003: the team blocker edge-row statements held 36-50 MiB on a store
// of 21,000 work items and the prod read stopped at its 64 MiB statement cap
// (ClickHouse 241), so every team answered "read failed". The statements carry
// the blockers' own cap, with headroom over what they were measured at.
func TestTeamBlockerEdgeRowStatementsCarryTheBlockersMemoryCap(t *testing.T) {
	client := &fakeClient{tables: teamBlockerTables()}
	if _, err := readTeamBlockers(t, client); err != nil {
		t.Fatalf("the read failed: %v", err)
	}
	want := fmt.Sprintf("max_memory_usage = %d", devhealthfacts.BlockerStatementMaxMemoryUsage)
	seen := 0
	for _, query := range client.queries {
		if !strings.Contains(query.statement, "uniqExactIf(tgt, is_open)") {
			continue
		}
		seen++
		if !strings.Contains(query.statement, want) {
			t.Errorf("an edge-row statement lacks %q:\n%s", want, query.statement[max(0, len(query.statement)-300):])
		}
	}
	if seen < 2 {
		t.Fatalf("%d edge-row statements seen, want the rollup and the totals", seen)
	}
	if devhealthfacts.BlockerStatementMaxMemoryUsage < 128<<20 {
		t.Fatalf("cap %d is below the 128 MiB the prod read needs headroom over", devhealthfacts.BlockerStatementMaxMemoryUsage)
	}
}

// A statement the server stops at its memory limit is a read that says so, with
// the exception code on the failure, not the generic "read failed".
func TestABlockersReadStoppedAtItsMemoryLimitSaysSo(t *testing.T) {
	tables := teamBlockerTables()
	for index := range tables {
		if tables[index].match == "is_open AND start_unix IS NOT NULL" || tables[index].match == "arrayJoin({team_repo_pairs" {
			tables[index].err = &clickhousedriver.Exception{Code: 241, Name: "DB::Exception", Message: "memory limit exceeded"}
		}
	}
	_, err := readTeamBlockers(t, &fakeClient{tables: tables})
	if err == nil {
		t.Fatal("the read did not fail")
	}
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) {
		t.Fatalf("not a typed failure: %T %v", err, err)
	}
	if failure.Reason != contextfabric.FactReadMemoryLimitReason || failure.SourceCode != 241 {
		t.Fatalf("reason %q code %d; want %q and 241", failure.Reason, failure.SourceCode, contextfabric.FactReadMemoryLimitReason)
	}
}
