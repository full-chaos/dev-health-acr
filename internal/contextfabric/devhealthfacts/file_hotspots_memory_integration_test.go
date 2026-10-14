package devhealthfacts_test

import (
	"context"
	"fmt"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	fileHotspotMemoryRepos      = 25
	fileHotspotMemorySmallFiles = 200
	fileHotspotMemoryLargeFiles = 4000
	fileHotspotMemoryRuns       = 10
	fileHotspotGranuleRows      = 8192
	fileHotspotMemorySlack      = uint64(2 << 20)
)

type hotspotStatementCost struct {
	peak, readRows uint64
	durationMs     uint64
}

func hotspotStatementCostSince(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, markMicros int64) hotspotStatementCost {
	t.Helper()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (measurement did not happen): %v", err)
	}
	var cost hotspotStatementCost
	var statements uint64
	if err := direct.QueryRow(ctx, `SELECT max(memory_usage), max(read_rows), max(query_duration_ms), count() FROM system.query_log
WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.file_hotspot_daily')) AND position(query, 'groupArraySorted') > 0`, markMicros).
		Scan(&cost.peak, &cost.readRows, &cost.durationMs, &statements); err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	if statements == 0 {
		t.Fatalf("measurement did not happen: no hotspot statement logged; recent:\n%s", recentServerStatements(ctx, direct))
	}
	return cost
}

// The file_hotspots statement keeps a bounded sorted array per repository.
// Measured at a small and at a 20x larger run size in both part states of the
// table (merged to one part, and pinned multi-part), in the production form
// (max_threads = 1) and in the default-threads form beside it. The production
// form must hold the slope (large <= 1.15 x small, or within small + 2 MiB),
// and it must read only the rows it needs: about one run per repository,
// never the older runs of the window.
func TestFileHotspotsStatementMemoryMergedAndPinnedMultiPartOnRealClickHouse(t *testing.T) {
	for _, state := range []struct {
		name   string
		pinned bool
	}{{"merged", false}, {"pinned_multipart", true}} {
		t.Run(state.name, func(t *testing.T) {
			ctx := context.Background()
			query, direct := sharedClickHouseFixture(t)
			for _, statement := range devhealthschema.ProjectionDDL("file_hotspot_daily") {
				if err := direct.Exec(ctx, statement); err != nil {
					t.Fatalf("declared projection: %v\n%s", err, statement)
				}
			}
			if state.pinned {
				if err := direct.Exec(ctx, `SYSTEM STOP MERGES file_hotspot_daily`); err != nil {
					t.Fatalf("stop merges: %v", err)
				}
				t.Cleanup(func() { _ = direct.Exec(ctx, `SYSTEM START MERGES file_hotspot_daily`) })
			}
			seed := func(org string, files int) []contextfabric.SubjectRef {
				subjects := make([]contextfabric.SubjectRef, 0, fileHotspotMemoryRepos)
				for r := 0; r < fileHotspotMemoryRepos; r++ {
					repo := repoUUID(fmt.Sprintf("%s-%s-%d", state.name, org, r))
					subjects = append(subjects, repoSubject(repo))
					for run, day := range []string{"2026-10-01", "2026-10-02", "2026-10-03", "2026-10-04", "2026-10-05", "2026-10-06", "2026-10-07", "2026-10-08", "2026-10-09", "2026-10-10"} {
						if err := direct.Exec(ctx, `INSERT INTO file_hotspot_daily (org_id, repo_id, day, file_path, churn_loc_30d, churn_commits_30d, cyclomatic_total, cyclomatic_avg, blame_concentration, risk_score, computed_at)
SELECT ?, toUUID(?), toDate(?), concat('src/dir', toString(number % 50), '/file_', toString(number), '.go'),
	toUInt64(number), toUInt32(number % 40), toUInt32(number % 30), 1.5, 0.3, toFloat64(number % 977) + number / 100000,
	parseDateTimeBestEffort(concat(?, ' 06:00:00'), 'UTC')
FROM numbers(?)`, org, repo, day, day, uint64(files)); err != nil {
							t.Fatalf("seed run %d: %v", run, err)
						}
					}
				}
				return subjects
			}
			small, large := "mem-small-"+state.name, "mem-large-"+state.name
			smallSubjects := seed(small, fileHotspotMemorySmallFiles)
			largeSubjects := seed(large, fileHotspotMemoryLargeFiles)
			if !state.pinned {
				if err := direct.Exec(ctx, `OPTIMIZE TABLE file_hotspot_daily FINAL`); err != nil {
					t.Fatalf("optimize: %v", err)
				}
			}
			provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactFileHotspots)
			measure := func(org string, subjects []contextfabric.SubjectRef) hotspotStatementCost {
				t.Helper()
				mark := serverNowMicros(t, ctx, direct)
				result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: org, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
					Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactFileHotspots, Subjects: subjects,
				})
				if err != nil {
					t.Fatalf("ReadFacts: %v", err)
				}
				if len(result.Facts) != fileHotspotMemoryRepos {
					t.Fatalf("served %d facts, want %d: %s", len(result.Facts), fileHotspotMemoryRepos, result.Reason)
				}
				return hotspotStatementCostSince(t, ctx, direct, mark)
			}
			type form struct{ name, clause string }
			costs := map[string][2]hotspotStatementCost{}
			for _, f := range []form{{"default_threads", ""}, {"max_threads_1", "\nSETTINGS max_threads = 1"}} {
				restore := devhealthfacts.SetFileHotspotSettingsForTest(f.clause)
				measure(small, smallSubjects) // cold read, unmeasured
				costs[f.name] = [2]hotspotStatementCost{measure(small, smallSubjects), measure(large, largeSubjects)}
				restore()
				t.Logf("%s %s: small(files=%d) peak=%d read_rows=%d ms=%d | large(files=%d) peak=%d read_rows=%d ms=%d | read_rows per repository: small=%d large=%d",
					state.name, f.name, fileHotspotMemorySmallFiles, costs[f.name][0].peak, costs[f.name][0].readRows, costs[f.name][0].durationMs,
					fileHotspotMemoryLargeFiles, costs[f.name][1].peak, costs[f.name][1].readRows, costs[f.name][1].durationMs,
					costs[f.name][0].readRows/fileHotspotMemoryRepos, costs[f.name][1].readRows/fileHotspotMemoryRepos)
			}
			prod := costs["max_threads_1"]
			if float64(prod[1].peak) > statementMemorySlope*float64(prod[0].peak) && prod[1].peak > prod[0].peak+fileHotspotMemorySlack {
				t.Errorf("%s: production form grew with the run size: peak %d at the larger size, %d at the smaller", state.name, prod[1].peak, prod[0].peak)
			}
			if prod[1].peak >= statementMemoryCeiling {
				t.Errorf("%s: production form peaked at %d bytes, want under %d", state.name, prod[1].peak, statementMemoryCeiling)
			}
			// Pruning works at granule size: the latest day of a repository is
			// at most ceil(files / granule) + 1 granules. The unpruned read is
			// every run of the history, so the bound is checked where it
			// differs from it (the larger size).
			for index, files := range []uint64{fileHotspotMemorySmallFiles, fileHotspotMemoryLargeFiles} {
				granules := (files+fileHotspotGranuleRows-1)/fileHotspotGranuleRows + 1
				limit := fileHotspotMemoryRepos*granules*fileHotspotGranuleRows + 2000
				whole := fileHotspotMemoryRepos * files * fileHotspotMemoryRuns
				t.Logf("%s files=%d: production read_rows=%d, pruned bound=%d, whole history=%d", state.name, files, prod[index].readRows, limit, whole)
				if limit < whole && prod[index].readRows > limit {
					t.Errorf("%s: production form read %d rows for %d files per run and %d runs of history, want at most %d (latest day at granule size), the whole history is %d: the latest-run discovery or the key-prefix memberships are not pruning",
						state.name, prod[index].readRows, files, fileHotspotMemoryRuns, limit, whole)
				}
			}
			// Control: a statement that cannot prune (the day key hashed, which
			// no key, partition or min-max analysis can invert) must exceed the same bound at the larger size, so the
			// bound is shown to be able to fail.
			{
				const tag = "file-hotspots-control"
				var counted uint64
				if err := direct.QueryRow(ctx, `SELECT sum(churn_loc_30d) FROM file_hotspot_daily WHERE org_id = ? AND cityHash64(toString(day)) = cityHash64('2026-10-10') SETTINGS log_comment = ?, max_threads = 1`, large, tag+state.name).Scan(&counted); err != nil {
					t.Fatalf("control statement: %v", err)
				}
				if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
					t.Fatalf("flush logs: %v", err)
				}
				var controlRows uint64
				if err := direct.QueryRow(ctx, `SELECT max(read_rows) FROM system.query_log WHERE type = 'QueryFinish' AND log_comment = ?`, tag+state.name).Scan(&controlRows); err != nil {
					t.Fatalf("control query_log: %v", err)
				}
				granules := (fileHotspotMemoryLargeFiles+fileHotspotGranuleRows-1)/fileHotspotGranuleRows + 1
				limit := fileHotspotMemoryRepos*granules*fileHotspotGranuleRows + 2000
				t.Logf("%s control (day key hashed): read_rows=%d against the pruned bound %d", state.name, controlRows, limit)
				if controlRows <= uint64(limit) {
					t.Errorf("%s: the non-pruning control read %d rows, within the pruned bound %d: the bound cannot fail", state.name, controlRows, limit)
				}
			}
		})
	}
}
