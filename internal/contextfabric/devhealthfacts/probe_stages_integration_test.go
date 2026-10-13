package devhealthfacts_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

func TestProbeRequestedSplitStagesOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	_, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	type size struct {
		org                        string
		units, prs, commits, edges int
	}
	sizes := []size{
		{org: "org-mem-large", units: 15000, prs: 3, commits: 10, edges: 10},
		{org: "org-mem-many-small", units: 3000, prs: 14},
		{org: "org-mem-many-refs", units: 12000, prs: 14},
	}
	for _, s := range sizes {
		seedSplitMemoryOrg(t, ctx, direct, s.org, s.units, s.prs, s.commits, s.edges)
	}
	ids := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		ids = append(ids, "'"+repoUUID(fmt.Sprintf("mem-%d", i))+"'")
	}
	stages := devhealthfacts.ProbeRequestedStages()
	names := make([]string, 0, len(stages))
	for n := range stages {
		names = append(names, n)
	}
	sort.Strings(names)
	variants := map[string]clickhouse.Settings{
		"default":  {},
		"threads1": {"max_threads": 1},
		"block8k":  {"max_block_size": 8192},
	}
	var report strings.Builder
	for _, s := range sizes {
		fmt.Fprintf(&report, "\n== %s units=%d prs=%d\n", s.org, s.units, s.prs)
		for _, name := range names {
			for _, v := range []string{"default", "threads1", "block8k"} {
				if v != "default" && name != "z_full" && name != "a1_agg" {
					continue
				}
				tag := s.org + "/" + name + "/" + v
				settings := clickhouse.Settings{"log_comment": tag}
				for k, val := range variants[v] {
					settings[k] = val
				}
				for i := 0; i < 2; i++ {
					qctx := clickhouse.Context(ctx, clickhouse.WithParameters(clickhouse.Parameters{"org_id": s.org, "ids": "[" + strings.Join(ids, ",") + "]"}), clickhouse.WithSettings(settings))
					rows, err := direct.Query(qctx, stages[name])
					if err != nil {
						t.Fatalf("%s: %v", tag, err)
					}
					for rows.Next() {
					}
					if err := rows.Err(); err != nil {
						t.Fatalf("%s: %v", tag, err)
					}
					rows.Close()
				}
				r := probeLogged(t, ctx, direct, "log_comment = ?", tag)
				line := fmt.Sprintf("%-18s %-9s mem=%7.2f MiB read=%7.2f MiB", name, v, float64(r.memory)/(1<<20), float64(r.readBytes)/(1<<20))
				keys := make([]string, 0, len(r.events))
				for k := range r.events {
					if strings.HasPrefix(k, "Arena") || strings.HasPrefix(k, "Join") || strings.Contains(k, "TwoLevel") {
						keys = append(keys, k)
					}
				}
				sort.Strings(keys)
				for _, k := range keys {
					line += fmt.Sprintf(" %s=%d", k, r.events[k])
				}
				t.Log(s.org + " " + line)
				report.WriteString(line + "\n")
			}
		}
	}
	t.Errorf("probe report (intentional failure):%s", report.String())
}

type probeRow struct {
	memory, readBytes, readRows uint64
	events                      map[string]uint64
}

func probeLogged(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, where string, args ...any) probeRow {
	t.Helper()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatal(err)
	}
	var r probeRow
	err := direct.QueryRow(ctx, `SELECT memory_usage, read_bytes, read_rows, ProfileEvents
FROM system.query_log WHERE type = 'QueryFinish' AND query_kind = 'Select' AND `+where+`
ORDER BY event_time_microseconds DESC LIMIT 1`, args...).Scan(&r.memory, &r.readBytes, &r.readRows, &r.events)
	if err != nil {
		t.Fatalf("query_log: %v\n%s", err, recentServerStatements(ctx, direct))
	}
	return r
}
