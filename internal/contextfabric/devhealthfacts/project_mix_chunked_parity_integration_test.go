package devhealthfacts_test

import (
	"context"
	"math"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	clickhousedriverv2 "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// projectMixOneChunk is a raw row count no fixture reaches: every wide-column
// phase runs as one statement.
const projectMixOneChunk = uint64(1) << 40

// Chunking changes which rows one statement reads, never the answer: on the
// prod-shaped table (stale versions, a superseded unit, units without a repo,
// units placed in several projects) the read in chunks of 997 raw rows equals
// the read in one chunk. Counts and the native sums are equal exactly; the
// roll-up bugfix sum adds per-chunk partial sums, so it is equal within the
// parity comparator's 1e-12. Also with a window that holds a quarter of the
// units, spread over the key range, so every chunk's range holds rows the pins
// leave out.
func TestProjectMixChunkedReadEqualsTheOneChunkReadAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, c := range []struct {
		name    string
		quarter bool
	}{{"all units in the window", false}, {"a quarter of the units in the window", true}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			query, direct := newScopedCHAOS7257Client(t, func(o *runtimeclickhouse.Options) {
				unlimited := uint64(0)
				o.MaxBytesToRead = &unlimited
				rows := uint(1_000_000)
				o.MaxResultRows = &rows
			})
			createCHAOS7257Tables(t, ctx, direct)
			const orgID = "org-project-mix-chunked"
			shape := seedCHAOS7257ProdShape(t, ctx, direct, orgID, chaos7257Units)
			window := devhealthfacts.ProjectMixWindow{}
			if c.quarter {
				syncCtx := clickhousedriverv2.Context(ctx, clickhousedriverv2.WithSettings(clickhousedriverv2.Settings{"mutations_sync": 2}))
				if err := direct.Exec(syncCtx, `ALTER TABLE work_unit_investments
UPDATE from_ts = from_ts - INTERVAL 90 DAY, to_ts = to_ts - INTERVAL 90 DAY
WHERE org_id = ? AND cityHash64(work_unit_id) % 4 != 0`, orgID); err != nil {
					t.Fatalf("move three units in four out of the window: %v", err)
				}
				window = devhealthfacts.ProjectMixWindow{Active: true, HasStart: true, Start: shape.at.Add(-30 * 24 * time.Hour), End: shape.at.Add(24 * time.Hour)}
			}
			ids := make([]string, 0, len(shape.projects))
			for _, project := range shape.projects {
				ids = append(ids, "linear:"+project)
			}
			var between atomic.Int64
			chunked := devhealthfacts.WithProjectMixBetweenChunks(devhealthfacts.WithProjectMixChunking(ctx, 0, 997), func() { between.Add(1) })
			whole := devhealthfacts.WithProjectMixBetweenChunks(devhealthfacts.WithProjectMixChunking(ctx, projectMixOneChunk, projectMixOneChunk), func() {
				t.Errorf("a one-chunk read ran a second chunk")
			})

			oneRollup := runRollupPhased(t, whole, query, orgID, ids, window)
			if len(oneRollup) == 0 {
				t.Fatal("the one-chunk roll-up returned no rows: the case proves nothing")
			}
			chunkedRollup := runRollupPhased(t, chunked, query, orgID, ids, window)
			if len(chunkedRollup) != len(oneRollup) {
				t.Fatalf("roll-up: %d rows in chunks, %d in one chunk", len(chunkedRollup), len(oneRollup))
			}
			for i := range oneRollup {
				w, g := oneRollup[i], chunkedRollup[i]
				near := math.Abs(w.Bugfix-g.Bugfix) <= 1e-12*math.Max(1, math.Max(math.Abs(w.Bugfix), math.Abs(g.Bugfix)))
				w.Bugfix, g.Bugfix = 0, 0
				if w != g || !near {
					t.Fatalf("roll-up row %d: one chunk %+v, chunks %+v (bugfix %v vs %v)", i, oneRollup[i], chunkedRollup[i], oneRollup[i].Bugfix, chunkedRollup[i].Bugfix)
				}
			}
			oneNative := runNativePhased(t, whole, query, orgID, ids, window, 201)
			if len(oneNative) == 0 {
				t.Fatal("the one-chunk native read returned no rows: the case proves nothing")
			}
			if chunkedNative := runNativePhased(t, chunked, query, orgID, ids, window, 201); !reflect.DeepEqual(chunkedNative, oneNative) {
				t.Fatalf("native: chunks %+v, one chunk %+v", chunkedNative, oneNative)
			}
			// About 20 chunks per wide-column phase, two phases per mix.
			if n := between.Load(); n < 4*10 {
				t.Fatalf("the chunked reads ran %d chunk switches, want at least 40: the case did not chunk", n)
			}
		})
	}
}

// A write that lands between two chunks of a chunked phase moves the closing
// digests, so the read starts over: the answer is the state before the write or
// the state after it, never a blend. The control: the two single-statement
// answers differ.
func TestProjectMixChunksNeverBlendTwoInputStatesAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	window := devhealthfacts.ProjectMixWindow{}
	t.Run("roll-up", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		query, direct := newScopedCHAOS7257Client(t, nil)
		createCHAOS7257Tables(t, ctx, direct)
		const orgID = "org-project-mix-chunk-blend-rollup"
		at := seedCHAOS7257Parity(t, ctx, direct, orgID)
		insertVersionAtMilli(t, ctx, direct, orgID, "wu-pin", repoUUID("r1a"), `{"issues":[],"prs":[]}`, "feature_delivery", at.Add(-time.Hour))
		insertMembership(t, ctx, direct, orgID, "wu-pin", at)
		ids := []string{"linear:proj-1", "linear:proj-2", "linear:proj-3"}
		before := runRollupStatement(t, ctx, query, "SingleRollupBefore", devhealthfacts.ProjectRollupStatement(window), orgID, ids, window)
		var once sync.Once
		fired := false
		hooked, err := devhealthfacts.RunProjectRollupMix(devhealthfacts.WithProjectMixBetweenChunks(devhealthfacts.WithProjectMixChunking(ctx, 0, 1), func() {
			once.Do(func() {
				fired = true
				insertVersionAtMilli(t, ctx, direct, orgID, "wu-pin", repoUUID("r1a"), `{"issues":[],"prs":[]}`, "risk", at.Add(-30*time.Minute))
				if err := direct.Exec(ctx, `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, team_name, source, is_primary, confidence, computed_at)
SELECT org_id, repo_id, work_item_id, 'team-2', 'team-2', source, is_primary, confidence, now64(3) FROM work_item_team_attributions WHERE org_id = ?`, orgID); err != nil {
					t.Fatalf("move attributions: %v", err)
				}
			})
		}), query, orgID, ids, window)
		if err != nil {
			t.Fatal(err)
		}
		if !fired {
			t.Fatal("no chunk switch happened: the case proves nothing")
		}
		after := runRollupStatement(t, ctx, query, "SingleRollupAfter", devhealthfacts.ProjectRollupStatement(window), orgID, ids, window)
		if rollupRowsDiffer(before, after) == nil {
			t.Fatal("control: the single statement answers before and after the write are equal, so the case proves nothing")
		}
		got := rollupRowsOf(hooked)
		if rollupRowsDiffer(before, got) != nil && rollupRowsDiffer(after, got) != nil {
			t.Fatalf("the chunked answer is neither the state before the write nor the state after it:\nbefore %+v\nafter  %+v\ngot    %+v", before, after, got)
		}
	})
	t.Run("native", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		query, direct := newScopedCHAOS7257Client(t, nil)
		createCHAOS7257Tables(t, ctx, direct)
		const orgID = "org-project-mix-chunk-blend-native"
		at := seedCHAOS7257Native(t, ctx, direct, orgID)
		insertVersionAtMilli(t, ctx, direct, orgID, "nu-pin", "", `{"issues":["linear:A-1"],"prs":[]}`, "feature_delivery", at.Add(-time.Hour))
		insertMembership(t, ctx, direct, orgID, "nu-pin", at)
		ids := []string{"linear:n-p1", "linear:n-p2", "linear:n-p3"}
		before := runNativeStatement(t, ctx, query, "SingleNativeBefore", devhealthfacts.ProjectNativeStatement(window, 201), orgID, ids, window)
		var once sync.Once
		fired := false
		hooked, err := devhealthfacts.RunProjectNativeMix(devhealthfacts.WithProjectMixBetweenChunks(devhealthfacts.WithProjectMixChunking(ctx, 0, 1), func() {
			once.Do(func() {
				fired = true
				insertNativeVersion(t, ctx, direct, orgID, "nu-pin", "risk", at.Add(-30*time.Minute))
				// linear:A-1 moves from n-p1 to n-p2 (a newer work_items version).
				if err := direct.Exec(ctx, `INSERT INTO work_items (repo_id, work_item_id, provider, title, type, status, project_key, project_id, native_team_key, project_name, created_at, updated_at, completed_at, parent_id, url, last_synced, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
					"00000000-0000-0000-0000-000000000000", "linear:A-1", "linear", "title", "issue", "open", "", "n-p2", "", "", at, at, nil, "", "", at.Add(time.Hour), orgID); err != nil {
					t.Fatalf("move work item: %v", err)
				}
			})
		}), query, orgID, ids, window, 201)
		if err != nil {
			t.Fatal(err)
		}
		if !fired {
			t.Fatal("no chunk switch happened: the case proves nothing")
		}
		after := runNativeStatement(t, ctx, query, "SingleNativeAfter", devhealthfacts.ProjectNativeStatement(window, 201), orgID, ids, window)
		if nativeRowsDiffer(before, after) == nil {
			t.Fatal("control: the single statement answers before and after the write are equal, so the case proves nothing")
		}
		got := nativeRowsOf(hooked)
		if nativeRowsDiffer(before, got) != nil && nativeRowsDiffer(after, got) != nil {
			t.Fatalf("the chunked answer is neither the state before the write nor the state after it:\nbefore %+v\nafter  %+v\ngot    %+v", before, after, got)
		}
	})
}
