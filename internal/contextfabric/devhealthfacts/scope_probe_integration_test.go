package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

func TestScopeProbeFixedCost(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	for _, statement := range devhealthschema.DDL(append(append([]string{}, sharedClickHouseTables...), "project_membership_transitions", "work_item_dependencies")...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	fixture := seedEveryRootFixture(t, ctx, direct, "5e2b9c71-3d4a-4f80-9b6e-1a7c3e9d2f45")
	recorder := &recordingQueryClient{inner: query}
	expander := devhealthfacts.NewScopeExpander(recorder)
	var out strings.Builder
	run := func(label, statement string, bindings any) {
		mark := serverNowMicros(t, ctx, direct)
		var err error
		switch b := bindings.(type) {
		case []devhealthfacts.ScopeProbeFragment:
			_ = b
		}
		captured := capturedStatement{statement: statement}
		if bs, ok := bindings.([]capturedStatement); ok && len(bs) > 0 {
			captured.bindings = bs[0].bindings
		}
		rows, qerr := query.Query(ctx, captured.statement, captured.bindings)
		if qerr == nil {
			for rows.Next() {
			}
			err = rows.Err()
			_ = rows.Close()
		} else {
			err = qerr
		}
		first, _, _ := strings.Cut(strings.TrimSpace(statement), "\n")
		if len(first) > 40 {
			first = first[:40]
		}
		peak := peakOf(t, ctx, direct, "work_items", first, mark)
		if peak == 0 {
			peak = peakOf(t, ctx, direct, "team_repo_ownership", first, mark)
		}
		if peak == 0 {
			peak = peakOf(t, ctx, direct, "projects", first, mark)
		}
		if peak == 0 {
			peak = peakOf(t, ctx, direct, "work_item_team_attributions", first, mark)
		}
		fmt.Fprintf(&out, "PROBE %s peak=%d err=%v\n", label, peak, err)
	}
	cases := fixture.cases()
	probeSize := func(size string) {
		for _, fragment := range devhealthfacts.ScopeProbeFragmentsForTest(fixture.orgID) {
			for attempt := 0; attempt < 2; attempt++ {
				run(size+" frag "+fragment.Name, fragment.SQL, []capturedStatement{{bindings: fragment.Bindings}})
			}
		}
		recorder.reset()
		for _, tc := range cases {
			if _, err := expander.ExpandFactScope(ctx, tc.request); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
		}
		for _, target := range []struct{ label, fragment string }{
			{"teamRepositories", "argMin(source, source_rank) AS best_source"},
			{"teamWorkItems", "min(a.team_id) AS origin_id"},
			{"projectWorkItems", "p.id)) AS origin_id"},
			{"projectRepositories", "min(p.id), argMax(l.provenance, l.link_rank)"},
		} {
			captured, ok := recorder.find(target.fragment)
			if !ok {
				t.Fatalf("%s not sent", target.label)
			}
			body := captured.statement
			if at := strings.LastIndex(body, "\nSETTINGS "); at >= 0 {
				body = body[:at]
			}
			for _, variant := range []struct{ name, settings string }{
				{"served", ""},
				{"threads1", "\nSETTINGS max_threads = 1"},
				{"threads2", "\nSETTINGS max_threads = 2"},
				{"block8192", "\nSETTINGS max_block_size = 8192"},
				{"threads1block8192", "\nSETTINGS max_threads = 1, max_block_size = 8192"},
				{"nohashprealloc", "\nSETTINGS collect_hash_table_stats_during_joins = 0, collect_hash_table_stats_during_aggregation = 0"},
			} {
				for attempt := 0; attempt < 2; attempt++ {
					run(size+" stmt "+target.label+" "+variant.name, body+variant.settings, []capturedStatement{{bindings: captured.bindings}})
				}
			}
			if size == "small" {
				rows, err := query.Query(ctx, "EXPLAIN PIPELINE "+body, captured.bindings)
				if err == nil {
					fmt.Fprintf(&out, "PIPELINE %s\n", target.label)
					for rows.Next() {
						var line string
						_ = rows.Scan(&line)
						fmt.Fprintf(&out, "  %s\n", line)
					}
					_ = rows.Close()
				} else {
					fmt.Fprintf(&out, "PIPELINE %s err=%v\n", target.label, err)
				}
			}
		}
		rows, err := query.Query(ctx, "SELECT toString(getSetting('max_threads')), toString(getSetting('max_block_size')), toString(getSetting('max_memory_usage')), version()", nil)
		if err == nil {
			for rows.Next() {
				var a, b, c, d string
				_ = rows.Scan(&a, &b, &c, &d)
				fmt.Fprintf(&out, "SETTINGS max_threads=%s max_block_size=%s max_memory_usage=%s version=%s\n", a, b, c, d)
			}
			_ = rows.Close()
		}
	}
	fillScopeOrganization(t, ctx, direct, fixture, "small", 100_000)
	probeSize("small")
	fillScopeOrganization(t, ctx, direct, fixture, "large", 200_000)
	probeSize("large")
	t.Errorf("PROBE OUTPUT\n%s", out.String())
}
