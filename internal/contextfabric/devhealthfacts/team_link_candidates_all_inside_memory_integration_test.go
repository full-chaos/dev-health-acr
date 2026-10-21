package devhealthfacts_test

import (
	"context"
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/full-chaos/dev-health-go/readers"
)

const (
	candidateAllInsideOrg     = "6d2b8f40-1c73-4e95-a8b6-0f4e7c9d3a12"
	candidateAllInsideIssues  = 96_000
	candidateAllInsideBatches = 20
	// candidateAllInsideBound is the candidate statement's own bound where
	// every link is inside the owned projects: the sort-key DISTINCT measured
	// 51.55 MB unmerged and 52.02 MB merged at 96k (lowest of three reads),
	// times 1.15 plus 2 MiB. The DISTINCT over work_item_id alone held a set
	// of every id: 89.96 / 93.95 MB.
	candidateAllInsideBound = uint64(59 << 20)
)

func candidateAllInsideWithinBound(peak uint64) bool {
	return peak <= candidateAllInsideBound
}

// Where every link of 96k is inside the owned projects, the team link
// candidate statement returns the per-version statement's ids, each once, and
// its peak stays under its own bound, unmerged and merged, through the client
// the hosted API uses.
func TestTeamLinkCandidateStatementStaysUnderItsBoundWhereEveryLinkIsInsideTheOwnedProjectsOnRealClickHouse(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("memory bound rows run in the unit legs; the race detector does not change a server peak")
	}
	if candidateAllInsideWithinBound(candidateAllInsideBound + 1) {
		t.Fatalf("control: a peak 1 byte over the %d-byte bound passed the candidate row", candidateAllInsideBound)
	}
	ctx := context.Background()
	rawQuery, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	query := statementthreads.API(rawQuery)
	// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
	tables := []string{"work_items", "work_graph_issue_pr", "projects", "team_project_ownership"}
	for _, statement := range devhealthschema.DDL(tables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	for _, table := range tables {
		exec(`SYSTEM STOP MERGES ` + table)
	}
	org, at := candidateAllInsideOrg, ts(2026, 9, 29, 0, 0, 0)
	const teamID, project = "team-owner", "proj-owner"
	exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		project, org, "linear", nil, project, uint8(1), "active", "", at)
	// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		org, "linear", teamID, project, nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, at)
	for b := 0; b < candidateAllInsideBatches; b++ {
		first, n := uint64(b*candidateAllInsideIssues/candidateAllInsideBatches), uint64((b+1)*candidateAllInsideIssues/candidateAllInsideBatches-b*candidateAllInsideIssues/candidateAllInsideBatches)
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at, last_synced)
SELECT concat('owned-', repeat('x', 96), '-', toString(number)), ?, ?, 'issue', 'open', '', ?, '', 'linear', ?, NULL, ? FROM numbers(?, ?)`,
			repoUUID("repo-filler"), org, at, project, at, first, n)
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT ?, concat('owned-', repeat('x', 96), '-', toString(number)), toUInt32(number + 1000), 1, 'native', '', ?, ? FROM numbers(?, ?)`,
			repoUUID("repo-reached"), at, org, first, n)
	}
	read := func(statement string) []string {
		t.Helper()
		var ids []string
		if err := readers.QueryOrgScopedNamed(ctx, query, "ReadTeamLinkCandidates", statement, org, []string{teamID}, func(row readers.RowScanner) error {
			return row.Scan(&ids)
		}); err != nil {
			t.Fatalf("team link candidates: %v: %s", err, readFailureCause(err))
		}
		return ids
	}
	for _, mode := range []string{"unmerged", "merged"} {
		var active uint64
		if mode == "merged" {
			for _, table := range tables {
				exec(`SYSTEM START MERGES ` + table)
				exec(`OPTIMIZE TABLE ` + table + ` FINAL`)
			}
		}
		if err := direct.QueryRow(ctx, `SELECT count() FROM system.parts WHERE database = currentDatabase() AND table = 'work_items' AND active`).Scan(&active); err != nil {
			t.Fatalf("count active parts: %v", err)
		}
		if mode == "unmerged" && active < candidateAllInsideBatches {
			t.Fatalf("%d active work_items parts, want at least %d (the unmerged state was not reached)", active, candidateAllInsideBatches)
		}
		if mode == "merged" && active != 1 {
			t.Fatalf("%d active work_items parts, want 1 (the merged state was not reached)", active)
		}
		oracle := slices.Compact(slices.Sorted(slices.Values(read(devhealthfacts.TeamLinkCandidatesPerVersionStatementForTest()))))
		if len(oracle) != candidateAllInsideIssues {
			t.Fatalf("%s: the per-version oracle holds %d distinct ids, want %d", mode, len(oracle), candidateAllInsideIssues)
		}
		var lowest uint64
		for i := range 4 {
			mark := serverNowMicros(t, ctx, direct)
			got := slices.Sorted(slices.Values(read(devhealthfacts.TeamLinkCandidatesStatementForTest())))
			if !slices.Equal(got, oracle) {
				t.Fatalf("%s: the candidate statement returned %d entries, not the per-version oracle's %d distinct ids, each once", mode, len(got), len(oracle))
			}
			peak := candidateStatementPeakSince(t, ctx, direct, mark)
			if i == 0 {
				continue
			}
			if lowest == 0 || peak < lowest {
				lowest = peak
			}
		}
		t.Logf("ALL-INSIDE %s team link candidate statement over %d issues: peak %d bytes (%.1f%% of the API statement cap; bound %d)",
			mode, candidateAllInsideIssues, lowest, 100*float64(lowest)/float64(contextpacket.APIStatementMaxMemoryUsage), candidateAllInsideBound)
		if !candidateAllInsideWithinBound(lowest) {
			t.Errorf("%s: team link candidate statement peaked at %d bytes where every link is inside the owned projects, want at most %d", mode, lowest, candidateAllInsideBound)
		}
	}
}
