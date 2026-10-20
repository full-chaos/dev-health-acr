package devhealthfacts_test

import (
	"context"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/full-chaos/dev-health-go/readers"
)

const (
	candidateVersionsOrg    = "4f7a2c91-8e35-4b06-a1d8-6c09e3b5f472"
	candidateVersionsIssues = 24_000
	candidateVersionsCount  = 20
	// candidateVersionsFloor is the memory a statement may add beyond the
	// slope when only the number of row versions grows.
	candidateVersionsFloor = uint64(2 << 20)
)

// The team link candidates are one entry per distinct issue, not per stored row
// version: with every owned linked issue stored in 20 unmerged versions, the
// candidate statement's peak memory stays at the slope of the single-version
// read.
func TestTeamLinkCandidatesFollowDistinctIssuesNotRowVersionsOnRealClickHouse(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("memory bound rows run in the unit legs; the race detector does not change a server peak")
	}
	ctx := context.Background()
	rawQuery, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	query := statementthreads.API(rawQuery)
	// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
	for _, statement := range devhealthschema.DDL("work_items", "work_graph_issue_pr", "projects", "team_project_ownership") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	org, at := candidateVersionsOrg, ts(2026, 9, 29, 0, 0, 0)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	exec(`SYSTEM STOP MERGES work_items`)
	const teamID, project = "team-owner", "proj-owner"
	exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		project, org, "linear", nil, project, uint8(1), "active", "", at)
	// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		org, "linear", teamID, project, nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, at)
	exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT ?, concat('owned-', repeat('x', 96), '-', toString(number)), toUInt32(number + 1000), 1, 'native', '', ?, ? FROM numbers(?)`,
		repoUUID("repo-reached"), at, org, uint64(candidateVersionsIssues))
	version := func(v int) {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at, last_synced)
SELECT concat('owned-', repeat('x', 96), '-', toString(number)), ?, ?, 'issue', 'open', '', ?, '', 'linear', ?, NULL, addSeconds(?, ?) FROM numbers(?)`,
			repoUUID("repo-filler"), org, at, project, at, v, uint64(candidateVersionsIssues))
	}
	read := func(label string) uint64 {
		t.Helper()
		var lowest uint64
		for i := range 4 {
			mark := serverNowMicros(t, ctx, direct)
			var ids []string
			if err := readers.QueryOrgScopedNamed(ctx, query, "ReadTeamLinkCandidates", devhealthfacts.TeamLinkCandidatesStatementForTest(), org, []string{teamID}, func(row readers.RowScanner) error {
				return row.Scan(&ids)
			}); err != nil {
				t.Fatalf("%s: team link candidates: %v: %s", label, err, readFailureCause(err))
			}
			if len(ids) < candidateVersionsIssues {
				t.Fatalf("%s: %d candidates, want at least %d", label, len(ids), candidateVersionsIssues)
			}
			peak := candidateStatementPeakSince(t, ctx, direct, mark)
			if i == 0 {
				continue
			}
			if lowest == 0 || peak < lowest {
				lowest = peak
			}
		}
		return lowest
	}
	version(0)
	small := read("one version")
	for v := 1; v < candidateVersionsCount; v++ {
		version(v)
	}
	var active uint64
	if err := direct.QueryRow(ctx, `SELECT count() FROM system.parts WHERE database = currentDatabase() AND table = 'work_items' AND active`).Scan(&active); err != nil {
		t.Fatalf("count active parts: %v", err)
	}
	if active < candidateVersionsCount {
		t.Fatalf("%d active work_items parts, want at least %d (the unmerged versions were not kept)", active, candidateVersionsCount)
	}
	large := read("twenty versions")
	t.Logf("VERSIONS team link candidate statement peak %d bytes with one version of %d issues, %d with %d versions (bound %.2fx + %d)", small, candidateVersionsIssues, large, candidateVersionsCount, statementMemorySlope, candidateVersionsFloor)
	if float64(large) > statementMemorySlope*float64(small)+float64(candidateVersionsFloor) {
		t.Errorf("team link candidate statement grew with the row versions: peak %d bytes with %d versions, %d with one, over the %.2fx slope plus %d", large, candidateVersionsCount, small, statementMemorySlope, candidateVersionsFloor)
	}
}

// candidateStatementPeakSince is the peak memory of the team link candidate
// statement after the mark; it must have run at the statement thread count.
func candidateStatementPeakSince(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, markMicros int64) uint64 {
	t.Helper()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (query_log unavailable, measurement did not happen): %v", err)
	}
	var peak, rows, unbound uint64
	if err := direct.QueryRow(ctx, `SELECT max(memory_usage), count(), `+statementthreads.UnboundThreadsCount+` FROM system.query_log
WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND query_kind = 'Select' AND position(query, ?) > 0`, markMicros, teamLinkCandidatesMarker).Scan(&peak, &rows, &unbound); err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	if rows == 0 {
		t.Fatalf("team link candidate statement not logged (the measurement did not happen)")
	}
	statementthreads.RequireThreadBound(t, ctx, direct, "team link candidate statement", unbound)
	return peak
}
