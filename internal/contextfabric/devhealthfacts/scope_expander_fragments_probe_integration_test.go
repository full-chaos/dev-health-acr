package devhealthfacts_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// TestProbeHistoricalFragmentsReadRows reports the rows each fragment of the
// scoped historical statements reads at two organization sizes. It fails on
// purpose so the hosted log carries the numbers.
func TestProbeHistoricalFragmentsReadRows(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	for _, statement := range devhealthschema.DDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	fixture := seedEveryRootFixture(t, ctx, direct, "7d1c9f52-3e68-4a0b-8c47-1b5e2f9a6d30")
	since := time.Now().UTC().Add(-7 * 24 * time.Hour)
	// devhealthschema:not-a-production-replica: test seed over a table the shared DDL creates; it declares no column, type or engine.
	if err := direct.Exec(ctx, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', 'issue-Q', 'linear', '', 'Q', '', '', '', ?, ?, 'linear:issue-Q-add')`,
		fixture.orgID, fixture.shared, since, since); err != nil {
		t.Fatalf("seed transition: %v", err)
	}
	recorder := &recordingQueryClient{inner: query}
	expander := devhealthfacts.NewScopeExpander(recorder)
	run := func(t *testing.T) map[string]uint64 {
		recorder.reset()
		for _, tc := range fixture.cases() {
			if tc.name == "project repositories as of" {
				if _, err := expander.ExpandFactScope(ctx, tc.request); err != nil {
					t.Fatalf("ExpandFactScope: %v: %s", err, readFailureCause(err))
				}
			}
		}
		touch, ok := recorder.find("AS malformed_touches")
		if !ok {
			t.Fatal("the touch statement was not sent")
		}
		out := map[string]uint64{}
		for name, statement := range devhealthfacts.HistoricalFragmentsForTest() {
			for warm := 0; warm < 2; warm++ {
				mark := serverNowMicros(t, ctx, direct)
				rows, err := query.Query(ctx, statement, touch.bindings)
				if err != nil {
					t.Fatalf("%s: %v: %s", name, err, readFailureCause(err))
				}
				for rows.Next() {
				}
				_ = rows.Close()
				out[name] = readRowsOf(t, ctx, direct, "project_membership_transitions", "AS probe_"+name, mark)
			}
		}
		return out
	}
	fillScopeOrganization(t, ctx, direct, fixture, "small", 100_000)
	mergeScopeTables(t, ctx, direct)
	small := run(t)
	fillScopeOrganization(t, ctx, direct, fixture, "large", 200_000)
	mergeScopeTables(t, ctx, direct)
	large := run(t)
	names := make([]string, 0, len(small))
	for name := range small {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Errorf("PROBE9617 %s read_rows small=%d large=%d delta=%d", name, small[name], large[name], large[name]-small[name])
	}
}
