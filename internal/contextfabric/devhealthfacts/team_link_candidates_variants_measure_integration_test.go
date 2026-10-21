package devhealthfacts_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/full-chaos/dev-health-go/readers"
)

const (
	candidateVariantsInsideOrg  = "9c1e4b7a-2f86-4d03-b5a9-8e7d16c3f240"
	candidateVariantsOutsideOrg = "3a8f6d21-c497-4e5b-9f10-6b2e8d4c7a95"
	candidateVariantsInside     = 96_000
	candidateVariantsOutside    = 400_000
	candidateVariantsBatches    = 20
	candidateVariantsReads      = 3
)

// The candidate statement forms are measured on the all-inside link shape
// (96k owned linked issues) and on the 400k outside-links shape, unmerged and
// merged, through the client the hosted API uses. Each form must return the
// served statement's ids exactly (sorted, no repeats, the per-version oracle's
// set); the peak memory, duration and read bytes are logged, not bounded.
func TestTeamLinkCandidateStatementFormsMeasuredOnTheAllInsideShapeOnRealClickHouse(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("memory measurement rows run in the unit legs; the race detector does not change a server peak")
	}
	started := time.Now()
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
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
	tables := []string{"work_items", "work_graph_issue_pr", "team_project_ownership"}
	for _, table := range tables {
		exec(`SYSTEM STOP MERGES ` + table)
	}
	at := ts(2026, 9, 29, 0, 0, 0)
	const teamID, project = "team-owner", "proj-owner"
	links := func(org, prefix, projectID string, count int) {
		for b := 0; b < candidateVariantsBatches; b++ {
			first, n := uint64(b*count/candidateVariantsBatches), uint64((b+1)*count/candidateVariantsBatches-b*count/candidateVariantsBatches)
			exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at, last_synced)
SELECT concat('`+prefix+`-', repeat('x', 96), '-', toString(number)), ?, ?, 'issue', 'open', '', ?, '', 'linear', ?, NULL, ? FROM numbers(?, ?)`,
				repoUUID("repo-filler"), org, at, projectID, at, first, n)
			exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT ?, concat('`+prefix+`-', repeat('x', 96), '-', toString(number)), toUInt32(number + 1000), 1, 'native', '', ?, ? FROM numbers(?, ?)`,
				repoUUID("repo-reached"), at, org, first, n)
		}
	}
	type shape struct {
		label string
		org   string
		want  int
	}
	shapes := []shape{
		{label: fmt.Sprintf("all inside (%d owned linked issues)", candidateVariantsInside), org: candidateVariantsInsideOrg, want: candidateVariantsInside},
		{label: fmt.Sprintf("outside (%d outside links, 1 owned linked issue)", candidateVariantsOutside), org: candidateVariantsOutsideOrg, want: 1},
	}
	for _, s := range shapes {
		exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			project, s.org, "linear", nil, project, uint8(1), "active", "", at)
		// devhealthschema:not-a-production-replica the table names here select or fill what devhealthschema.DDL renders; no schema is defined in this file
		exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			s.org, "linear", teamID, project, nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, at)
	}
	links(candidateVariantsInsideOrg, "owned", project, candidateVariantsInside)
	links(candidateVariantsOutsideOrg, "outside", "", candidateVariantsOutside)
	links(candidateVariantsOutsideOrg, "owned", project, 1)
	t.Logf("MEASURE seeded in %s", time.Since(started).Round(time.Millisecond))

	variants := devhealthfacts.TeamLinkCandidateVariantsForTest()
	read := func(org, reader, statement string) []string {
		t.Helper()
		var ids []string
		if err := readers.QueryOrgScopedNamed(contextpacket.WithStatementReader(ctx, reader), query, "TeamLinkCandidateVariants", statement, org, []string{teamID}, func(row readers.RowScanner) error {
			return row.Scan(&ids)
		}); err != nil {
			t.Fatalf("%s: %v: %s", reader, err, readFailureCause(err))
		}
		return ids
	}
	for _, mode := range []string{"unmerged", "merged"} {
		if mode == "merged" {
			for _, table := range tables {
				exec(`SYSTEM START MERGES ` + table)
				exec(`OPTIMIZE TABLE ` + table + ` FINAL`)
			}
		}
		var active uint64
		if err := direct.QueryRow(ctx, `SELECT count() FROM system.parts WHERE database = currentDatabase() AND table = 'work_items' AND active`).Scan(&active); err != nil {
			t.Fatalf("count active parts: %v", err)
		}
		if mode == "unmerged" && active < candidateVariantsBatches {
			t.Fatalf("%d active work_items parts, want at least %d (the unmerged state was not reached)", active, candidateVariantsBatches)
		}
		if mode == "merged" && active != 1 {
			t.Fatalf("%d active work_items parts, want 1 (the merged state was not reached)", active)
		}
		for _, s := range shapes {
			oracle := slices.Compact(slices.Sorted(slices.Values(read(s.org, "cand-oracle", devhealthfacts.TeamLinkCandidatesPerVersionStatementForTest()))))
			if len(oracle) != s.want {
				t.Fatalf("%s %s: the per-version oracle holds %d distinct ids, want %d", mode, s.label, len(oracle), s.want)
			}
			for _, v := range variants {
				got := slices.Sorted(slices.Values(read(s.org, "cand-"+v.Name, v.Statement)))
				if !slices.Equal(got, oracle) {
					t.Errorf("%s %s: variant %s returned %d entries, not the oracle's %d distinct ids (set changed or repeats)", mode, s.label, v.Name, len(got), len(oracle))
				}
			}
			mark := serverNowMicros(t, ctx, direct)
			for range candidateVariantsReads {
				for _, v := range variants {
					read(s.org, "cand-"+v.Name, v.Statement)
				}
			}
			if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
				t.Fatalf("flush logs (query_log unavailable, measurement did not happen): %v", err)
			}
			for _, v := range variants {
				var lowest, highest, readBytes, durationMax, durationMin, rows, unbound uint64
				var threads string
				if err := direct.QueryRow(ctx, `SELECT min(memory_usage), max(memory_usage), max(read_bytes), max(query_duration_ms), min(query_duration_ms), count(), `+statementthreads.UnboundThreadsCount+`, any(Settings['max_threads']) FROM system.query_log
WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND query_kind = 'Select' AND log_comment = ?`, mark, "cand-"+v.Name).Scan(&lowest, &highest, &readBytes, &durationMax, &durationMin, &rows, &unbound, &threads); err != nil {
					t.Fatalf("read query_log: %v", err)
				}
				if rows != candidateVariantsReads {
					t.Fatalf("%s %s: variant %s logged %d finished statements, want %d (the measurement did not happen)", mode, s.label, v.Name, rows, candidateVariantsReads)
				}
				statementthreads.RequireThreadBound(t, ctx, direct, "team link candidate variant "+v.Name, unbound)
				t.Logf("MEASURE %s %s variant=%s peak_min=%d peak_max=%d (%.1f%% of the API cap) read_bytes=%d duration_ms=%d..%d max_threads=%q entries=%d",
					mode, s.label, v.Name, lowest, highest, 100*float64(lowest)/float64(contextpacket.APIStatementMaxMemoryUsage), readBytes, durationMin, durationMax, threads, s.want)
			}
		}
	}
	t.Logf("MEASURE total %s", time.Since(started).Round(time.Millisecond))
}
