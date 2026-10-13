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

func TestScopeProbeUnmerged(t *testing.T) {
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
	fixture := seedEveryRootFixture(t, ctx, direct, "7a1c4e92-6b3d-4f05-8e27-2d9b5c1f0a63")
	recorder := &recordingQueryClient{inner: query}
	expander := devhealthfacts.NewScopeExpander(recorder)
	var out strings.Builder
	peak := func(label, statement string, bindings []capturedStatement) {
		var peaks []uint64
		for attempt := 0; attempt < 3; attempt++ {
			mark := serverNowMicros(t, ctx, direct)
			rows, err := query.Query(ctx, statement, bindings[0].bindings)
			if err != nil {
				fmt.Fprintf(&out, "PROBE %s err=%v\n", label, err)
				return
			}
			for rows.Next() {
			}
			_ = rows.Close()
			first, _, _ := strings.Cut(strings.TrimSpace(statement), "\n")
			if len(first) > 40 {
				first = first[:40]
			}
			p := uint64(0)
			for _, table := range []string{"work_items", "work_graph_issue_pr", "git_pull_requests", "projects"} {
				p = max(p, peakOf(t, ctx, direct, table, first, mark))
			}
			peaks = append(peaks, p)
		}
		fmt.Fprintf(&out, "PROBE %s %v\n", label, peaks)
	}
	parts := func(label string) {
		rows, err := query.Query(ctx, "SELECT table, toString(count()), toString(sum(rows)) FROM system.parts WHERE active AND database = currentDatabase() AND table IN ('work_items', 'work_graph_issue_pr', 'git_pull_requests') GROUP BY table ORDER BY table", nil)
		if err != nil {
			fmt.Fprintf(&out, "PARTS %s err=%v\n", label, err)
			return
		}
		for rows.Next() {
			var a, b, c string
			_ = rows.Scan(&a, &b, &c)
			fmt.Fprintf(&out, "PARTS %s %s parts=%s rows=%s\n", label, a, b, c)
		}
		_ = rows.Close()
	}
	probe := func(label string) {
		parts(label)
		for _, f := range devhealthfacts.ScopeProbeFragmentsForTest(fixture.orgID) {
			peak(label+" "+f.Name, f.SQL, []capturedStatement{{bindings: f.Bindings}})
		}
		recorder.reset()
		for _, tc := range fixture.cases() {
			if _, err := expander.ExpandFactScope(ctx, tc.request); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
		}
		for _, frag := range []string{"min(p.id), argMax(l.provenance, l.link_rank)", "AS link_state"} {
			captured, ok := recorder.find(frag)
			if !ok {
				t.Fatalf("%s not sent", frag)
			}
			peak(label+" stmt "+frag, captured.statement, []capturedStatement{captured})
			peak(label+" stmt threads1 "+frag, captured.statement+"\nSETTINGS max_threads = 1", []capturedStatement{captured})
		}
	}
	fillScopeOrganization(t, ctx, direct, fixture, "small", 100_000)
	probe("small-unmerged")
	mergeScopeTables(t, ctx, direct)
	probe("small-merged")
	fillScopeOrganization(t, ctx, direct, fixture, "large", 200_000)
	probe("large-unmerged")
	mergeScopeTables(t, ctx, direct)
	probe("large-merged")
	t.Errorf("PROBE OUTPUT\n%s", out.String())
}
