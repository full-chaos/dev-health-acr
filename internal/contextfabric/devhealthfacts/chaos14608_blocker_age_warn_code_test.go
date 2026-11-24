package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-14608: when the age statement throws, the counts are still served and
// the WARN names which ClickHouse limit stopped it (the exception code and its
// limit class), never the exception text.
func TestCHAOS14608FailedAgeStatementWarnCarriesTheClickHouseCodeAndLimitClass(t *testing.T) {
	const secret = "Code: 241. DB::Exception: tenant-secret-query-text"
	for _, cell := range []struct {
		name      string
		err       error
		wantCode  string
		wantClass string
	}{
		{"memory", &clickhousedriver.Exception{Code: 241, Name: "DB::Exception", Message: secret}, "clickhouse_code=241", "limit_class=memory"},
		{"wrapped memory", fmt.Errorf("query: %w", &clickhousedriver.Exception{Code: 241, Name: "DB::Exception", Message: secret}), "clickhouse_code=241", "limit_class=memory"},
		{"execution time", &clickhousedriver.Exception{Code: 159, Name: "DB::Exception", Message: secret}, "clickhouse_code=159", "limit_class=execution_time"},
		{"rows to read", &clickhousedriver.Exception{Code: 158, Name: "DB::Exception", Message: secret}, "clickhouse_code=158", "limit_class=rows_to_read"},
		{"bytes to read", &clickhousedriver.Exception{Code: 307, Name: "DB::Exception", Message: secret}, "clickhouse_code=307", "limit_class=bytes_to_read"},
		{"result rows", &clickhousedriver.Exception{Code: 396, Name: "DB::Exception", Message: secret}, "clickhouse_code=396", "limit_class=result_rows"},
		{"another code", &clickhousedriver.Exception{Code: 60, Name: "DB::Exception", Message: secret}, "clickhouse_code=60", "limit_class=other"},
	} {
		teamTables := []fakeTable{
			{match: "GROUP BY team_id, repo_key", rows: [][]any{{"CHAOS", "00000000-0000-4000-8000-000000000001", "acme/repo"}}},
			{match: "is_open AND start_unix IS NOT NULL", err: cell.err},
			{match: "SELECT toInt64(uniqExact(if(", rows: [][]any{{int64(0)}}},
			{match: "arrayJoin({team_repo_pairs", rows: [][]any{{"CHAOS", int64(4), int64(5), int64(0), int64(0), int64(0), int64(0)}}},
			{match: "FROM work_item_dependencies", rows: [][]any{{"00000000-0000-4000-8000-000000000001", int64(4), int64(5), int64(0), int64(3)}}},
		}
		projectTables := []fakeTable{
			{match: "is_open = 1 AND start_unix IS NOT NULL", err: cell.err},
			{match: "uniqExactIf(tgt, is_open = 1)", rows: [][]any{{"linear:proj-1", int64(2), int64(3), int64(1), int64(0)}}},
		}
		for _, subject := range []struct {
			kind     string
			tables   []fakeTable
			subject  contextfabric.SubjectRef
			wantBlkd int64
		}{
			{"team", teamTables, teamSubject("CHAOS"), 4},
			{"project", projectTables, projectSubject("linear", "proj-1"), 2},
		} {
			logs := captureSlog(t)
			provider := findProvider(t, devhealthfacts.NewProviders(&fakeClient{tables: subject.tables}), contextfabric.FactBlockers)
			result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{subject.subject},
			})
			if err != nil {
				t.Fatalf("%s %s: a failed age statement failed the read: %v", cell.name, subject.kind, err)
			}
			fields := factFor(t, result, subject.subject).Fields
			wantInt(t, fields, "blocked_work_items_current", subject.wantBlkd)
			if _, has := fields["oldest_blocked_age_days"]; has {
				t.Errorf("%s %s: an age is served after its statement failed", cell.name, subject.kind)
			}
			var warn string
			for _, line := range strings.Split(logs.String(), "\n") {
				if strings.Contains(line, "devhealthfacts.blocker_age_read_failed") {
					warn = line
				}
			}
			if warn == "" {
				t.Fatalf("%s %s: no blocker_age_read_failed line:\n%s", cell.name, subject.kind, logs.String())
			}
			if !strings.Contains(warn, "subject_kind="+subject.kind) || !strings.Contains(warn, cell.wantCode) || !strings.Contains(warn, cell.wantClass) {
				t.Errorf("WANT %s %s: the WARN carries %s and %s, got: %s", cell.name, subject.kind, cell.wantCode, cell.wantClass, warn)
			}
			if strings.Contains(logs.String(), "tenant-secret-query-text") {
				t.Errorf("%s %s: the exception text reached the log: %s", cell.name, subject.kind, warn)
			}
		}
	}
}

// An age statement that fails without a ClickHouse exception (a driver or
// network error) carries no code and no limit class.
func TestCHAOS14608FailedAgeStatementWithoutAClickHouseCodeLogsNoCode(t *testing.T) {
	logs := captureSlog(t)
	tables := []fakeTable{
		{match: "is_open = 1 AND start_unix IS NOT NULL", err: fmt.Errorf("read: connection reset")},
		{match: "uniqExactIf(tgt, is_open = 1)", rows: [][]any{{"linear:proj-1", int64(2), int64(3), int64(1), int64(0)}}},
	}
	provider := findProvider(t, devhealthfacts.NewProviders(&fakeClient{tables: tables}), contextfabric.FactBlockers)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("a failed age statement failed the read: %v", err)
	}
	wantInt(t, factFor(t, result, projectSubject("linear", "proj-1")).Fields, "blocked_work_items_current", 2)
	if !strings.Contains(logs.String(), "devhealthfacts.blocker_age_read_failed") {
		t.Fatalf("no blocker_age_read_failed line:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "clickhouse_code=") || strings.Contains(logs.String(), "limit_class=") {
		t.Errorf("a failure without a ClickHouse code logged a code or class:\n%s", logs.String())
	}
}
