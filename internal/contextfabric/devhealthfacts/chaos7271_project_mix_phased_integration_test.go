package devhealthfacts_test

// CHAOS-7271: the project theme-mix reads are phased (scope statement, then one
// statement per column group). Two properties carry the change:
//
//  1. Every phase reads ONE snapshot. A version written after the scope
//     statement (a newer computed_at for a unit it saw, or a unit it did not
//     see) must not reach a later phase.
//  2. The phased read returns the rows the single statement returned, on the
//     prod-shaped table.
//
// The newer version lands ONE millisecond after the fixture's computed_at: the
// same second as the snapshot, the granularity computed_at (DateTime64(3)) is
// bound at. Each case below is paired with a control: the same write, read AFTER it, moves
// the answer, so a pin that "held" because the write changed nothing fails here.

import (
	"context"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/full-chaos/dev-health-go/readers"
)

func TestProjectMixPhasesReadOneSnapshotAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	ids := []string{"linear:proj-1", "linear:proj-2", "linear:proj-3", "linear:proj-4", "linear:proj-5", "linear:proj-6"}
	nativeIDs := []string{"linear:n-p1", "linear:n-p2", "linear:n-p3"}
	window := devhealthfacts.ProjectMixWindow{}
	const nativeLimit = 201

	for _, tc := range []struct {
		name string
		// land writes the version a concurrent writer lands between the phases.
		land func(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string, snapshot time.Time)
	}{
		{"a version one millisecond newer (same second) than a unit the scope saw", func(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string, snapshot time.Time) {
			insertVersionAtMilli(t, ctx, direct, orgID, "wu-1", repoUUID("r1a"), `{"issues":[],"prs":[]}`, snapshot.Add(time.Millisecond))
			insertNativeVersion(t, ctx, direct, orgID, "nu-1", snapshot.Add(time.Millisecond))
		}},
		{"a unit the scope did not see, dated before the snapshot", func(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string, snapshot time.Time) {
			insertVersionAtMilli(t, ctx, direct, orgID, "wu-late", repoUUID("r1a"), `{"issues":[],"prs":[]}`, snapshot.Add(-time.Hour))
			insertNativeVersion(t, ctx, direct, orgID, "nu-late", snapshot.Add(-time.Hour))
			// In the current membership run, so a read AFTER this write does see
			// both units (the control); only the phases' unit set keeps them out.
			for _, id := range []string{"wu-late", "nu-late"} {
				if err := direct.Exec(ctx, `INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id) VALUES (?,?,?,?,?,?,?,?)`,
					orgID, "issue", "ISS-"+id, id, "theme", "feature_delivery", snapshot.Add(-24*time.Hour), "run-2"); err != nil {
					t.Fatalf("insert membership %s: %v", id, err)
				}
			}
		}},
	} {
		t.Run(tc.name+"/roll-up", func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			query, direct := newScopedCHAOS7257Client(t, nil)
			createCHAOS7257Tables(t, ctx, direct)
			const orgID = "org-7271-pin-rollup"
			seedCHAOS7257Parity(t, ctx, direct, orgID)
			baseline := runRollupPhased(t, ctx, query, orgID, ids, window)
			landed := false
			hooked, err := devhealthfacts.RunProjectRollupMix(devhealthfacts.WithProjectMixBetweenPhases(ctx, func() {
				tc.land(t, ctx, direct, orgID, newestVersion(t, ctx, direct, orgID))
				landed = true
			}), query, orgID, ids, window)
			if err != nil || !landed {
				t.Fatalf("hooked read: err %v, hook ran %v", err, landed)
			}
			if err := rollupRowsDiffer(baseline, rollupRowsOf(hooked)); err != nil {
				t.Fatalf("a write between the phases reached a later phase: %v", err)
			}
			if err := rollupRowsDiffer(baseline, runRollupPhased(t, ctx, query, orgID, ids, window)); err == nil {
				t.Fatal("control: the same write read AFTER it left the answer unchanged, so the case above proves nothing")
			}
		})
		t.Run(tc.name+"/native", func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			query, direct := newScopedCHAOS7257Client(t, nil)
			createCHAOS7257Tables(t, ctx, direct)
			const orgID = "org-7271-pin-native"
			seedCHAOS7257Native(t, ctx, direct, orgID)
			baseline := runNativePhased(t, ctx, query, orgID, nativeIDs, window, nativeLimit)
			landed := false
			hooked, err := devhealthfacts.RunProjectNativeMix(devhealthfacts.WithProjectMixBetweenPhases(ctx, func() {
				tc.land(t, ctx, direct, orgID, newestVersion(t, ctx, direct, orgID))
				landed = true
			}), query, orgID, nativeIDs, window, nativeLimit)
			if err != nil || !landed {
				t.Fatalf("hooked read: err %v, hook ran %v", err, landed)
			}
			if err := nativeRowsDiffer(baseline, nativeRowsOf(hooked)); err != nil {
				t.Fatalf("a write between the phases reached a later phase: %v", err)
			}
			if err := nativeRowsDiffer(baseline, runNativePhased(t, ctx, query, orgID, nativeIDs, window, nativeLimit)); err == nil {
				t.Fatal("control: the same write read AFTER it left the answer unchanged, so the case above proves nothing")
			}
		})
	}
}

// The phased reads return what the single statements return, on the prod-shaped
// table, for the current axis and a range, through both mixes. The single
// statements fail the prod byte cap on this table, so this runs uncapped: the
// byte budget is pinned by TestProjectThemeMixFitsTheClickHouseByteBudget...
func TestProjectMixPhasedReadMatchesTheSingleStatementOnTheProdShapedTableAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	query, direct := newScopedCHAOS7257Client(t, func(o *runtimeclickhouse.Options) {
		unlimited := uint64(0)
		o.MaxBytesToRead = &unlimited
		rows := uint(1_000_000)
		o.MaxResultRows = &rows
	})
	createCHAOS7257Tables(t, ctx, direct)
	const orgID = "org-7271-diff"
	shape := seedCHAOS7257ProdShape(t, ctx, direct, orgID, chaos7257Units)
	ids := make([]string, 0, len(shape.projects))
	for _, project := range shape.projects {
		ids = append(ids, "linear:"+project)
	}
	const rowLimit = 201
	for _, w := range []struct {
		name   string
		window devhealthfacts.ProjectMixWindow
	}{
		{"current axis", devhealthfacts.ProjectMixWindow{}},
		{"range over 6 days", devhealthfacts.ProjectMixWindow{Active: true, HasStart: true, Start: shape.at.Add(-6 * 24 * time.Hour), End: shape.at}},
	} {
		t.Run(w.name, func(t *testing.T) {
			single := runRollupStatement(t, ctx, query, "SingleRollup", devhealthfacts.ProjectRollupStatement(w.window), orgID, ids, w.window)
			if len(single) == 0 {
				t.Fatal("the single roll-up statement returned no rows: the fixture proves nothing")
			}
			if err := rollupRowsDiffer(single, runRollupPhased(t, ctx, query, orgID, ids, w.window)); err != nil {
				t.Fatalf("roll-up: %v", err)
			}
			nativeSingle := runNativeStatement(t, ctx, query, "SingleNative", devhealthfacts.ProjectNativeStatement(w.window, rowLimit), orgID, ids, w.window)
			if len(nativeSingle) == 0 {
				t.Fatal("the single native statement returned no rows: the fixture proves nothing")
			}
			if err := nativeRowsDiffer(nativeSingle, runNativePhased(t, ctx, query, orgID, ids, w.window, rowLimit)); err != nil {
				t.Fatalf("native: %v", err)
			}
		})
	}
}

func rollupRowsOf(rows []devhealthfacts.ProjectRollupMixRow) []rollupRow {
	var out []rollupRow
	for _, r := range rows {
		out = append(out, rollupRow{Key: r.ProjectKey, Themes: [5]float64{r.FeatureDelivery, r.Operational, r.Maintenance, r.Quality, r.Risk},
			Bugfix: r.BugfixWeighted, WorkUnits: r.WorkUnits, Repos: r.Repos, Teams: r.Teams, Excluded: r.ExcludedNoRepoLink})
	}
	return out
}

func nativeRowsOf(rows []readers.ProjectThemeMixRow) []nativeRow {
	var out []nativeRow
	for _, r := range rows {
		out = append(out, nativeRow{Key: r.ProjectSubjectKey, Themes: [5]float64{r.FeatureDelivery, r.Operational, r.Maintenance, r.Quality, r.Risk},
			Bugfix: r.BugfixWeighted, WorkUnits: r.WorkUnits, EffortUnits: r.EffortUnits, Spanning: r.SpanningUnits, MultiPlaced: r.MultiPlacedUnits})
	}
	return out
}

// insertVersionAtMilli writes one version of a unit with a computed_at bound at
// the column's own precision (whole milliseconds, fromUnixTimestamp64Milli): a
// time.Time through the driver's positional `?` is rendered in whole seconds
// and would land the version a fraction of a second early. effort 1e6 and
// feature_delivery 1.0 make a version that reached a phase move the mix.
func insertVersionAtMilli(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID, id, repoID, evidence string, computedAt time.Time) {
	t.Helper()
	from := computedAt.Add(-72 * time.Hour).Truncate(time.Second)
	if err := direct.Exec(ctx, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id)
SELECT ?, ?, ?, toUUIDOrNull(?), 1000000.0, map('feature_delivery', 1.0), map(), ?, fromUnixTimestamp64Milli(?, 'UTC'), ?`,
		id, from, from, repoID, evidence, computedAt.UnixMilli(), orgID); err != nil {
		t.Fatalf("insert version of %s: %v", id, err)
	}
}

func insertNativeVersion(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID, id string, computedAt time.Time) {
	t.Helper()
	insertVersionAtMilli(t, ctx, direct, orgID, id, "", `{"issues":["linear:A-1"],"prs":[]}`, computedAt)
}

// newestVersion is the largest computed_at in the table: the snapshot the
// scope statement reads (DateTime64(3), so whole milliseconds).
func newestVersion(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string) time.Time {
	t.Helper()
	var newest time.Time
	if err := direct.QueryRow(ctx, `SELECT max(computed_at) FROM work_unit_investments WHERE org_id = ?`, orgID).Scan(&newest); err != nil {
		t.Fatalf("read newest computed_at: %v", err)
	}
	return newest
}
