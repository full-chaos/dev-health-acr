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
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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
			insertVersionAtMilli(t, ctx, direct, orgID, "wu-1", repoUUID("r1a"), `{"issues":[],"prs":[]}`, 1_000_000, snapshot.Add(time.Millisecond))
			insertNativeVersion(t, ctx, direct, orgID, "nu-1", snapshot.Add(time.Millisecond))
		}},
		{"a unit the scope did not see, dated before the snapshot", func(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string, snapshot time.Time) {
			insertVersionAtMilli(t, ctx, direct, orgID, "wu-late", repoUUID("r1a"), `{"issues":[],"prs":[]}`, 1_000_000, snapshot.Add(-time.Hour))
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
// feature_delivery 1.0 make a version that reached a phase move the mix (effort
// is a parameter so a pinned unit can start small).
func insertVersionAtMilli(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID, id, repoID, evidence string, effort float64, computedAt time.Time) {
	t.Helper()
	from := computedAt.Add(-72 * time.Hour).Truncate(time.Second)
	if err := direct.Exec(ctx, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id)
SELECT ?, ?, ?, toUUIDOrNull(?), ?, map('feature_delivery', 1.0), map(), ?, fromUnixTimestamp64Milli(?, 'UTC'), ?`,
		id, from, from, repoID, effort, evidence, computedAt.UnixMilli(), orgID); err != nil {
		t.Fatalf("insert version of %s: %v", id, err)
	}
}

func insertNativeVersion(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID, id string, computedAt time.Time) {
	t.Helper()
	insertVersionAtMilli(t, ctx, direct, orgID, id, "", `{"issues":["linear:A-1"],"prs":[]}`, 1_000_000, computedAt)
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

func insertMembership(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID, id string, at time.Time) {
	t.Helper()
	if err := direct.Exec(ctx, `INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id) VALUES (?,?,?,?,?,?,?,?)`,
		orgID, "issue", "ISS-"+id, id, "theme", "feature_delivery", at.Add(-24*time.Hour), "run-2"); err != nil {
		t.Fatalf("insert membership %s: %v", id, err)
	}
}

// A version dated AFTER a selected unit's own version but BEFORE the newest
// version phase 0 saw must not replace the selected one. A bound of "at or
// before the newest computed_at" lets it in: the pin is the exact (unit,
// version) pair. Both mixes; the control reads after the write and must move.
func TestProjectMixPhasesReadTheSelectedVersionNotABackdatedOneAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	window := devhealthfacts.ProjectMixWindow{}
	t.Run("roll-up", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		query, direct := newScopedCHAOS7257Client(t, nil)
		createCHAOS7257Tables(t, ctx, direct)
		const orgID = "org-7271-backdated-rollup"
		at := seedCHAOS7257Parity(t, ctx, direct, orgID)
		insertVersionAtMilli(t, ctx, direct, orgID, "wu-pin", repoUUID("r1a"), `{"issues":[],"prs":[]}`, 7, at.Add(-time.Hour))
		insertMembership(t, ctx, direct, orgID, "wu-pin", at)
		ids := []string{"linear:proj-1", "linear:proj-2", "linear:proj-3"}
		baseline := runRollupPhased(t, ctx, query, orgID, ids, window)
		hooked, err := devhealthfacts.RunProjectRollupMix(devhealthfacts.WithProjectMixBetweenPhases(ctx, func() {
			insertVersionAtMilli(t, ctx, direct, orgID, "wu-pin", repoUUID("r1a"), `{"issues":[],"prs":[]}`, 1_000_000, at.Add(-30*time.Minute))
		}), query, orgID, ids, window)
		if err != nil {
			t.Fatal(err)
		}
		if err := rollupRowsDiffer(baseline, rollupRowsOf(hooked)); err != nil {
			t.Fatalf("a backdated version reached a later phase: %v", err)
		}
		if err := rollupRowsDiffer(baseline, runRollupPhased(t, ctx, query, orgID, ids, window)); err == nil {
			t.Fatal("control: the same write read AFTER it left the answer unchanged, so the case above proves nothing")
		}
	})
	t.Run("native", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		query, direct := newScopedCHAOS7257Client(t, nil)
		createCHAOS7257Tables(t, ctx, direct)
		const orgID = "org-7271-backdated-native"
		at := seedCHAOS7257Native(t, ctx, direct, orgID)
		insertVersionAtMilli(t, ctx, direct, orgID, "nu-pin", "", `{"issues":["linear:A-1"],"prs":[]}`, 7, at.Add(-time.Hour))
		insertMembership(t, ctx, direct, orgID, "nu-pin", at)
		ids := []string{"linear:n-p1", "linear:n-p2", "linear:n-p3"}
		baseline := runNativePhased(t, ctx, query, orgID, ids, window, 201)
		hooked, err := devhealthfacts.RunProjectNativeMix(devhealthfacts.WithProjectMixBetweenPhases(ctx, func() {
			insertNativeVersion(t, ctx, direct, orgID, "nu-pin", at.Add(-30*time.Minute))
		}), query, orgID, ids, window, 201)
		if err != nil {
			t.Fatal(err)
		}
		if err := nativeRowsDiffer(baseline, nativeRowsOf(hooked)); err != nil {
			t.Fatalf("a backdated version reached a later phase: %v", err)
		}
		if err := nativeRowsDiffer(baseline, runNativePhased(t, ctx, query, orgID, ids, window, 201)); err == nil {
			t.Fatal("control: the same write read AFTER it left the answer unchanged, so the case above proves nothing")
		}
	})
}

// The native read keeps the old statement's ORDER BY project_key LIMIT n: with
// two projects reached and a limit of one, the first by key is returned.
func TestProjectNativePhasedReadHonoursTheRowLimitAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	query, direct := newScopedCHAOS7257Client(t, nil)
	createCHAOS7257Tables(t, ctx, direct)
	const orgID = "org-7271-limit"
	seedCHAOS7257Native(t, ctx, direct, orgID)
	ids := []string{"linear:n-p1", "linear:n-p2", "linear:n-p3"}
	window := devhealthfacts.ProjectMixWindow{}
	oracle := runNativeStatement(t, ctx, query, "OracleProjectNative", devhealthfacts.OracleProjectNativeStatement(window, 1), orgID, ids, window)
	got := runNativePhased(t, ctx, query, orgID, ids, window, 1)
	if len(oracle) != 1 || len(got) != 1 {
		t.Fatalf("limit 1: oracle %d rows, phased %d rows, want 1 each", len(oracle), len(got))
	}
	if err := nativeRowsDiffer(oracle, got); err != nil {
		t.Fatal(err)
	}
}

// A write to an input the phases read besides the unit versions (team
// attributions, work items) must never produce an answer that mixes the state
// before it with the state after it. The write below moves a unit's values AND
// the attribution (roll-up) / project placement (native) in one step: the phased
// read must equal the single statement run before the write or the single
// statement run after it. Never a blend. The control: the two single-statement
// answers differ.
func TestProjectMixPhasesNeverBlendTwoInputStatesAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	window := devhealthfacts.ProjectMixWindow{}
	t.Run("roll-up", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		query, direct := newScopedCHAOS7257Client(t, nil)
		createCHAOS7257Tables(t, ctx, direct)
		const orgID = "org-7271-blend-rollup"
		at := seedCHAOS7257Parity(t, ctx, direct, orgID)
		insertVersionAtMilli(t, ctx, direct, orgID, "wu-pin", repoUUID("r1a"), `{"issues":[],"prs":[]}`, 7, at.Add(-time.Hour))
		insertMembership(t, ctx, direct, orgID, "wu-pin", at)
		ids := []string{"linear:proj-1", "linear:proj-2", "linear:proj-3"}
		before := runRollupStatement(t, ctx, query, "SingleRollupBefore", devhealthfacts.ProjectRollupStatement(window), orgID, ids, window)
		var once sync.Once
		hooked, err := devhealthfacts.RunProjectRollupMix(devhealthfacts.WithProjectMixBetweenPhases(ctx, func() {
			once.Do(func() {
				insertVersionAtMilli(t, ctx, direct, orgID, "wu-pin", repoUUID("r1a"), `{"issues":[],"prs":[]}`, 1_000_000, at.Add(-30*time.Minute))
				if err := direct.Exec(ctx, `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, team_name, source, is_primary, confidence, computed_at)
SELECT org_id, repo_id, work_item_id, 'team-2', 'team-2', source, is_primary, confidence, now64(3) FROM work_item_team_attributions WHERE org_id = ?`, orgID); err != nil {
					t.Fatalf("move attributions: %v", err)
				}
			})
		}), query, orgID, ids, window)
		if err != nil {
			t.Fatal(err)
		}
		after := runRollupStatement(t, ctx, query, "SingleRollupAfter", devhealthfacts.ProjectRollupStatement(window), orgID, ids, window)
		if rollupRowsDiffer(before, after) == nil {
			t.Fatal("control: the single statement answers before and after the write are equal, so the case proves nothing")
		}
		got := rollupRowsOf(hooked)
		if rollupRowsDiffer(before, got) != nil && rollupRowsDiffer(after, got) != nil {
			t.Fatalf("the phased answer is neither the state before the write nor the state after it:\nbefore %+v\nafter  %+v\ngot    %+v", before, after, got)
		}
	})
	t.Run("native", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		query, direct := newScopedCHAOS7257Client(t, nil)
		createCHAOS7257Tables(t, ctx, direct)
		const orgID = "org-7271-blend-native"
		at := seedCHAOS7257Native(t, ctx, direct, orgID)
		insertVersionAtMilli(t, ctx, direct, orgID, "nu-pin", "", `{"issues":["linear:A-1"],"prs":[]}`, 7, at.Add(-time.Hour))
		insertMembership(t, ctx, direct, orgID, "nu-pin", at)
		ids := []string{"linear:n-p1", "linear:n-p2", "linear:n-p3"}
		before := runNativeStatement(t, ctx, query, "SingleNativeBefore", devhealthfacts.ProjectNativeStatement(window, 201), orgID, ids, window)
		var once sync.Once
		hooked, err := devhealthfacts.RunProjectNativeMix(devhealthfacts.WithProjectMixBetweenPhases(ctx, func() {
			once.Do(func() {
				insertNativeVersion(t, ctx, direct, orgID, "nu-pin", at.Add(-30*time.Minute))
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
		after := runNativeStatement(t, ctx, query, "SingleNativeAfter", devhealthfacts.ProjectNativeStatement(window, 201), orgID, ids, window)
		if nativeRowsDiffer(before, after) == nil {
			t.Fatal("control: the single statement answers before and after the write are equal, so the case proves nothing")
		}
		got := nativeRowsOf(hooked)
		if nativeRowsDiffer(before, got) != nil && nativeRowsDiffer(after, got) != nil {
			t.Fatalf("the phased answer is neither the state before the write nor the state after it:\nbefore %+v\nafter  %+v\ngot    %+v", before, after, got)
		}
	})
}

// A read whose inputs move on EVERY attempt fails with the typed retryable
// class, which the provider answers as an unavailable source with a "contended"
// reason (never a blend, never a generic failure), and which reports through the
// instrumentation hook: one event per retry, one error event for the terminal
// failure.
func TestProjectNativePhasedReadFailsContendedWhenItsInputsMoveOnEveryAttemptAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	query, direct := newScopedCHAOS7257Client(t, nil)
	createCHAOS7257Tables(t, ctx, direct)
	const orgID = "org-7271-exhaust"
	at := seedCHAOS7257Native(t, ctx, direct, orgID)
	attempts := 0
	hook := func() {
		attempts++
		if err := direct.Exec(ctx, `INSERT INTO work_items (repo_id, work_item_id, provider, title, type, status, project_key, project_id, native_team_key, project_name, created_at, updated_at, completed_at, parent_id, url, last_synced, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			"00000000-0000-0000-0000-000000000000", "linear:A-1", "linear", "title", "issue", "open", "", "n-p1", "", "", at, at, nil, "", "", at.Add(time.Duration(attempts)*time.Hour), orgID); err != nil {
			t.Fatalf("write work item: %v", err)
		}
	}

	// The typed class, straight from the read.
	_, err := devhealthfacts.RunProjectNativeMix(devhealthfacts.WithProjectMixBetweenPhases(ctx, hook), query, orgID, []string{"linear:n-p1"}, devhealthfacts.ProjectMixWindow{}, 201)
	var contended *devhealthfacts.ProjectMixContendedError
	if !errors.As(err, &contended) || !contended.Retryable() || contended.Attempts != 3 {
		t.Fatalf("err = %v, want a retryable *ProjectMixContendedError after 3 attempts", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3 (the bounded retry)", attempts)
	}

	// Through the provider: an unavailable source with a contended reason, and
	// the retry path visible in the instrumentation.
	// (attempts keeps counting: each write must be newer than the last one.)
	rec := &mixEventRecorder{}
	provider := findProvider(t, devhealthfacts.NewInstrumentedProviders(query, rec), contextfabric.FactInvestment)
	_, err = provider.ReadFacts(devhealthfacts.WithProjectMixBetweenPhases(ctx, hook), storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment,
		Subjects: []contextfabric.SubjectRef{projectSubject("linear", "n-p1")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable || !strings.Contains(failure.Reason, "contended") {
		t.Fatalf("ReadFacts err = %#v, want a FactReadFailure with State unavailable and a contended reason", err)
	}
	if got := rec.count("ProjectMixInputsRetry"); got != 2 {
		t.Errorf("retry events = %d, want 2 (attempts 1 and 2 retry; attempt 3 is terminal)", got)
	}
	if got := rec.errCount("ProjectMixContended"); got != 1 {
		t.Errorf("terminal contended error events = %d, want 1", got)
	}
}

// mixEventRecorder is a readers.Instrumentation that keeps every event.
type mixEventRecorder struct {
	mu     sync.Mutex
	events []mixEvent
}

type mixEvent struct {
	reader string
	err    error
}

func (r *mixEventRecorder) StartQuery(ctx context.Context, reader string, _ bool) (context.Context, func(error)) {
	idx := -1
	r.mu.Lock()
	r.events = append(r.events, mixEvent{reader: reader})
	idx = len(r.events) - 1
	r.mu.Unlock()
	return ctx, func(err error) {
		r.mu.Lock()
		r.events[idx].err = err
		r.mu.Unlock()
	}
}

func (r *mixEventRecorder) count(reader string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e.reader == reader {
			n++
		}
	}
	return n
}

func (r *mixEventRecorder) errCount(reader string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e.reader == reader && e.err != nil {
			n++
		}
	}
	return n
}

// The input digests (not a max-timestamp mark) are what detect a change. Each case
// below writes to an input of the phased read while it runs, in a place a
// max(timestamp) mark or a late baseline would miss, together with a unit version
// that moves the values. The phased answer must equal the single statement run
// before the writes or the one run after them, never a blend. Control: the two
// single-statement answers differ.
//
//	backdated      the input write is dated BELOW the table's existing maximum
//	after baseline the write lands between the baseline digests and the scope read
//	after scope    an ownership change lands between the scope read and the link read
const (
	mixWriteBackdated     = "backdated"
	mixWriteAfterBaseline = "after_baseline"
	mixWriteAfterScope    = "after_scope"
)

// rollupInputWrites lands what a concurrent writer does to the roll-up's inputs:
// a newer version of wu-pin (moves the theme values) and, as kind says, an
// attribution for a work item no unit had one for, dated below the table's
// maximum (an insert a max(computed_at) mark does not see), or a project
// ownership row.
func rollupInputWrites(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string, at time.Time, kind string) {
	t.Helper()
	insertVersionAtMilli(t, ctx, direct, orgID, "wu-pin", repoUUID("r1a"), `{"issues":[],"prs":[]}`, 1_000_000, at.Add(-30*time.Minute))
	if kind == mixWriteAfterScope {
		if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", "team-2", "proj-1", nil, "native", at.Add(-100*24*time.Hour), nil, at); err != nil {
			t.Fatalf("insert ownership: %v", err)
		}
		return
	}
	if err := direct.Exec(ctx, `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, team_name, source, is_primary, confidence, computed_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, repoUUID("r1a"), "ghpr:acme/r1a#9999", "team-1", "team-1", "linked_issue", uint8(1), "high", at.Add(-2*time.Hour)); err != nil {
		t.Fatalf("insert attribution: %v", err)
	}
}

// nativeInputWrites: a newer version of nu-pin and a transition that moves
// linear:A-1 to n-p2, dated (last_synced) BELOW the table's maximum.
func nativeInputWrites(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string, at time.Time) {
	t.Helper()
	insertNativeVersion(t, ctx, direct, orgID, "nu-pin", at.Add(-30*time.Minute))
	if err := direct.Exec(ctx, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, nil, "00000000-0000-0000-0000-000000000000", "work_item", "linear:A-1", "linear", "n-p1", "n-p2", "", "", "", at.Add(time.Hour), at.Add(-2*time.Hour), "ev-backdated"); err != nil {
		t.Fatalf("insert transition: %v", err)
	}
}

func TestProjectMixInputWritesNeverBlendAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	window := devhealthfacts.ProjectMixWindow{}
	install := func(kind string) func(context.Context, func()) context.Context {
		return func(ctx context.Context, hook func()) context.Context {
			switch kind {
			case mixWriteAfterBaseline:
				return devhealthfacts.WithProjectMixAfterBaseline(ctx, hook)
			case mixWriteAfterScope:
				return devhealthfacts.WithProjectMixAfterScope(ctx, hook)
			}
			return devhealthfacts.WithProjectMixBetweenPhases(ctx, hook)
		}
	}
	for _, kind := range []string{mixWriteBackdated, mixWriteAfterBaseline, mixWriteAfterScope} {
		t.Run(kind+"/roll-up", func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			query, direct := newScopedCHAOS7257Client(t, nil)
			createCHAOS7257Tables(t, ctx, direct)
			orgID := "org-mix-digest-rollup-" + strings.ReplaceAll(kind, "_", "-")
			at := seedCHAOS7257Parity(t, ctx, direct, orgID)
			insertVersionAtMilli(t, ctx, direct, orgID, "wu-pin", repoUUID("r1a"), `{"issues":[],"prs":[]}`, 7, at.Add(-time.Hour))
			insertMembership(t, ctx, direct, orgID, "wu-pin", at)
			// A unit without a repository whose only evidence is a pull request no
			// attribution exists for: the attribution written below is the only
			// thing that gives it a team.
			insertVersionAtMilli(t, ctx, direct, orgID, "wu-bk", "", fmt.Sprintf(`{"issues":[],"prs":[%q]}`, repoUUID("r1a")+"#pr9999"), 3, at.Add(-time.Hour))
			insertMembership(t, ctx, direct, orgID, "wu-bk", at)
			ids := []string{"linear:proj-1", "linear:proj-2", "linear:proj-3"}
			before := runRollupStatement(t, ctx, query, "SingleRollupBefore", devhealthfacts.ProjectRollupStatement(window), orgID, ids, window)
			var once sync.Once
			hooked, err := devhealthfacts.RunProjectRollupMix(install(kind)(ctx, func() {
				once.Do(func() { rollupInputWrites(t, ctx, direct, orgID, at, kind) })
			}), query, orgID, ids, window)
			if err != nil {
				t.Fatal(err)
			}
			after := runRollupStatement(t, ctx, query, "SingleRollupAfter", devhealthfacts.ProjectRollupStatement(window), orgID, ids, window)
			if rollupRowsDiffer(before, after) == nil {
				t.Fatal("control: the single statement answers before and after the writes are equal, so the case proves nothing")
			}
			got := rollupRowsOf(hooked)
			if rollupRowsDiffer(before, got) != nil && rollupRowsDiffer(after, got) != nil {
				t.Fatalf("the phased answer is neither the state before the writes nor the state after them:\nbefore %+v\nafter  %+v\ngot    %+v", before, after, got)
			}
		})
		if kind == mixWriteAfterScope {
			continue // an ownership change only concerns the roll-up's link capture
		}
		t.Run(kind+"/native", func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			query, direct := newScopedCHAOS7257Client(t, nil)
			createCHAOS7257Tables(t, ctx, direct)
			orgID := "org-mix-digest-native-" + strings.ReplaceAll(kind, "_", "-")
			at := seedCHAOS7257Native(t, ctx, direct, orgID)
			insertVersionAtMilli(t, ctx, direct, orgID, "nu-pin", "", `{"issues":["linear:A-1"],"prs":[]}`, 7, at.Add(-time.Hour))
			insertMembership(t, ctx, direct, orgID, "nu-pin", at)
			ids := []string{"linear:n-p1", "linear:n-p2", "linear:n-p3"}
			before := runNativeStatement(t, ctx, query, "SingleNativeBefore", devhealthfacts.ProjectNativeStatement(window, 201), orgID, ids, window)
			var once sync.Once
			hooked, err := devhealthfacts.RunProjectNativeMix(install(kind)(ctx, func() {
				once.Do(func() { nativeInputWrites(t, ctx, direct, orgID, at) })
			}), query, orgID, ids, window, 201)
			if err != nil {
				t.Fatal(err)
			}
			after := runNativeStatement(t, ctx, query, "SingleNativeAfter", devhealthfacts.ProjectNativeStatement(window, 201), orgID, ids, window)
			if nativeRowsDiffer(before, after) == nil {
				t.Fatal("control: the single statement answers before and after the writes are equal, so the case proves nothing")
			}
			got := nativeRowsOf(hooked)
			if nativeRowsDiffer(before, got) != nil && nativeRowsDiffer(after, got) != nil {
				t.Fatalf("the phased answer is neither the state before the writes nor the state after them:\nbefore %+v\nafter  %+v\ngot    %+v", before, after, got)
			}
		})
	}
}
