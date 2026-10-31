package devhealthfacts_test

// The project mix consistent-read guard (projectMixMaxAttempts) retries a read
// when an org-wide count+cityHash64 digest of an input table moves between the
// first and the last statement of an attempt, and fails "contended" after three.
// Live ops sync rewrites rows all the time without changing what they say (a new
// last_synced on the same content), writes rows of projects the read does not
// ask for, and merges parts in the background. Each cell below makes one such
// change, proves with a control that it changes no answer (the single statement
// reads the same rows before and after it), and requires the read to answer.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

const contendedZeroRepo = "00000000-0000-0000-0000-000000000000"

func TestProjectMixReadAnswersThroughWritesThatChangeNoAnswerAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	window := devhealthfacts.ProjectMixWindow{}
	nativeIDs := []string{"linear:n-p1", "linear:n-p2", "linear:n-p3"}

	// requireNativeAnswers runs the native mix with hook between its phases on
	// every attempt, checks the control (the single statement reads the same
	// before and after the writes) and requires an answer equal to it.
	requireNativeAnswers := func(t *testing.T, orgID string, write func(ctx context.Context, direct clickhousedriver.Conn, at time.Time, attempt int)) {
		t.Helper()
		ctx := context.Background()
		query, direct := newScopedCHAOS7257Client(t, nil)
		createCHAOS7257Tables(t, ctx, direct)
		at := seedCHAOS7257Native(t, ctx, direct, orgID)
		before := runNativeStatement(t, ctx, query, "SingleNativeBefore", devhealthfacts.ProjectNativeStatement(window, 201), orgID, nativeIDs, window)
		attempts := 0
		got, err := devhealthfacts.RunProjectNativeMix(devhealthfacts.WithProjectMixBetweenPhases(ctx, func() {
			attempts++
			write(ctx, direct, at, attempts)
		}), query, orgID, nativeIDs, window, 201)
		after := runNativeStatement(t, ctx, query, "SingleNativeAfter", devhealthfacts.ProjectNativeStatement(window, 201), orgID, nativeIDs, window)
		if diff := nativeRowsDiffer(before, after); diff != nil {
			t.Fatalf("control: the writes changed the single statement's answer (%v), so this cell does not hold a no-change write", diff)
		}
		var contended *devhealthfacts.ProjectMixContendedError
		if errors.As(err, &contended) {
			t.Fatalf("the read failed %v after %d attempt(s), though no write changed its answer", err, attempts)
		}
		if err != nil {
			t.Fatal(err)
		}
		if diff := nativeRowsDiffer(before, nativeRowsOf(got)); diff != nil {
			t.Fatalf("the answer differs from the single statement's: %v", diff)
		}
	}

	t.Run("a re-sync of a read work item with the same content", func(t *testing.T) {
		t.Parallel()
		orgID := "org-12177-resync-native"
		requireNativeAnswers(t, orgID, func(ctx context.Context, direct clickhousedriver.Conn, at time.Time, attempt int) {
			if err := direct.Exec(ctx, `INSERT INTO work_items (repo_id, work_item_id, provider, title, type, status, project_key, project_id, native_team_key, project_name, created_at, updated_at, completed_at, parent_id, url, last_synced, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				contendedZeroRepo, "linear:A-1", "linear", "title", "issue", "open", "", "n-p1", "", "", at, at, nil, "", "", at.Add(time.Duration(attempt)*time.Hour), orgID); err != nil {
				t.Fatalf("re-sync work item: %v", err)
			}
		})
	})
	t.Run("a new work item of a project the read does not ask for", func(t *testing.T) {
		t.Parallel()
		orgID := "org-12177-unrelated-native"
		requireNativeAnswers(t, orgID, func(ctx context.Context, direct clickhousedriver.Conn, at time.Time, attempt int) {
			if err := direct.Exec(ctx, `INSERT INTO work_items (repo_id, work_item_id, provider, title, type, status, project_key, project_id, native_team_key, project_name, created_at, updated_at, completed_at, parent_id, url, last_synced, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				contendedZeroRepo, fmt.Sprintf("linear:Z-%d", attempt), "linear", "title", "issue", "open", "", "n-p9", "", "", at, at, nil, "", "", at, orgID); err != nil {
				t.Fatalf("unrelated work item: %v", err)
			}
		})
	})
	t.Run("a background merge with no write at all", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		const orgID = "org-12177-merge-native"
		query, direct := newScopedCHAOS7257Client(t, nil)
		createCHAOS7257Tables(t, ctx, direct)
		at := seedCHAOS7257Native(t, ctx, direct, orgID)
		// A second physical version of A-1 with the same content (an earlier sync),
		// in its own part: a merge collapses the two, which changes the raw row count.
		// Merges stay stopped until the hook, so none runs before the read's baseline.
		if err := direct.Exec(ctx, `SYSTEM STOP MERGES work_items`); err != nil {
			t.Fatal(err)
		}
		if err := direct.Exec(ctx, `INSERT INTO work_items (repo_id, work_item_id, provider, title, type, status, project_key, project_id, native_team_key, project_name, created_at, updated_at, completed_at, parent_id, url, last_synced, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			contendedZeroRepo, "linear:A-1", "linear", "title", "issue", "open", "", "n-p1", "", "", at, at, nil, "", "", at.Add(-time.Hour), orgID); err != nil {
			t.Fatalf("earlier version: %v", err)
		}
		before := runNativeStatement(t, ctx, query, "SingleNativeBefore", devhealthfacts.ProjectNativeStatement(window, 201), orgID, nativeIDs, window)
		var physicalBefore, physicalAfter uint64
		if err := direct.QueryRow(ctx, `SELECT count() FROM work_items WHERE org_id = ?`, orgID).Scan(&physicalBefore); err != nil {
			t.Fatal(err)
		}
		attempts := 0
		_, err := devhealthfacts.RunProjectNativeMix(devhealthfacts.WithProjectMixBetweenPhases(ctx, func() {
			attempts++
			if attempts > 1 {
				return
			}
			if err := direct.Exec(ctx, `SYSTEM START MERGES work_items`); err != nil {
				t.Fatalf("start merges: %v", err)
			}
			if err := direct.Exec(ctx, `OPTIMIZE TABLE work_items FINAL`); err != nil {
				t.Fatalf("merge: %v", err)
			}
		}), query, orgID, nativeIDs, window, 201)
		if err != nil {
			t.Fatal(err)
		}
		if err := direct.QueryRow(ctx, `SELECT count() FROM work_items WHERE org_id = ?`, orgID).Scan(&physicalAfter); err != nil {
			t.Fatal(err)
		}
		after := runNativeStatement(t, ctx, query, "SingleNativeAfter", devhealthfacts.ProjectNativeStatement(window, 201), orgID, nativeIDs, window)
		if physicalAfter >= physicalBefore {
			t.Fatalf("control: the merge left %d physical rows of %d, so it did not collapse a version", physicalAfter, physicalBefore)
		}
		if diff := nativeRowsDiffer(before, after); diff != nil {
			t.Fatalf("control: the merge changed the single statement's answer: %v", diff)
		}
		if attempts != 1 {
			t.Fatalf("the read took %d attempts after a merge alone (%d -> %d physical rows, no write), want 1", attempts, physicalBefore, physicalAfter)
		}
	})
	t.Run("a re-sync of a repository with the same name (roll-up)", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		const orgID = "org-12177-resync-rollup"
		query, direct := newScopedCHAOS7257Client(t, nil)
		createCHAOS7257Tables(t, ctx, direct)
		at := seedCHAOS7257Parity(t, ctx, direct, orgID)
		ids := []string{"linear:proj-1", "linear:proj-2", "linear:proj-3"}
		before := runRollupStatement(t, ctx, query, "SingleRollupBefore", devhealthfacts.ProjectRollupStatement(window), orgID, ids, window)
		attempts := 0
		got, err := devhealthfacts.RunProjectRollupMix(devhealthfacts.WithProjectMixBetweenPhases(ctx, func() {
			attempts++
			if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
				repoUUID("r1a"), orgID, "acme/r1a", "github", at.Add(time.Duration(attempts)*time.Hour)); err != nil {
				t.Fatalf("re-sync repo: %v", err)
			}
		}), query, orgID, ids, window)
		after := runRollupStatement(t, ctx, query, "SingleRollupAfter", devhealthfacts.ProjectRollupStatement(window), orgID, ids, window)
		if diff := rollupRowsDiffer(before, after); diff != nil {
			t.Fatalf("control: the re-sync changed the single statement's answer (%v), so this cell does not hold a no-change write", diff)
		}
		var contended *devhealthfacts.ProjectMixContendedError
		if errors.As(err, &contended) {
			t.Fatalf("the roll-up read failed %v after %d attempt(s), though no write changed its answer", err, attempts)
		}
		if err != nil {
			t.Fatal(err)
		}
		if diff := rollupRowsDiffer(before, rollupRowsOf(got)); diff != nil {
			t.Fatalf("the answer differs from the single statement's: %v", diff)
		}
	})
}

// The scope statement pins each unit to the version it read (work_unit_id,
// computed_at), and the later phases read exactly that version. A recompute that
// writes the same content under a newer computed_at, followed by a merge, deletes
// the pinned version (work_unit_investments is ReplacingMergeTree(computed_at)).
// The unit must still be in the answer: the single statement reads the newer,
// identical version and counts it.
func TestProjectNativeMixKeepsAPinnedUnitThroughARecomputeAndAMergeAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	window := devhealthfacts.ProjectMixWindow{}
	const orgID = "org-12177-pinned-merge"
	query, direct := newScopedCHAOS7257Client(t, nil)
	createCHAOS7257Tables(t, ctx, direct)
	at := seedCHAOS7257Native(t, ctx, direct, orgID)
	insertNativeVersion(t, ctx, direct, orgID, "nu-pin", "feature_delivery", at.Add(-time.Hour))
	insertMembership(t, ctx, direct, orgID, "nu-pin", at)
	ids := []string{"linear:n-p1", "linear:n-p2", "linear:n-p3"}
	before := runNativeStatement(t, ctx, query, "SingleNativeBefore", devhealthfacts.ProjectNativeStatement(window, 201), orgID, ids, window)
	var once sync.Once
	got, err := devhealthfacts.RunProjectNativeMix(devhealthfacts.WithProjectMixAfterScope(ctx, func() {
		once.Do(func() {
			insertNativeVersion(t, ctx, direct, orgID, "nu-pin", "feature_delivery", at.Add(-30*time.Minute))
			if err := direct.Exec(ctx, `SYSTEM START MERGES work_unit_investments`); err != nil {
				t.Fatalf("start merges: %v", err)
			}
			if err := direct.Exec(ctx, `OPTIMIZE TABLE work_unit_investments FINAL`); err != nil {
				t.Fatalf("merge: %v", err)
			}
		})
	}), query, orgID, ids, window, 201)
	if err != nil {
		t.Fatal(err)
	}
	var versions uint64
	if err := direct.QueryRow(ctx, `SELECT count() FROM work_unit_investments WHERE org_id = ? AND work_unit_id = 'nu-pin'`, orgID).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	after := runNativeStatement(t, ctx, query, "SingleNativeAfter", devhealthfacts.ProjectNativeStatement(window, 201), orgID, ids, window)
	if versions != 1 {
		t.Fatalf("control: %d physical versions of nu-pin after the merge, want 1 (the pinned one deleted)", versions)
	}
	if diff := nativeRowsDiffer(before, after); diff != nil {
		t.Fatalf("control: the identical recompute changed the single statement's answer: %v", diff)
	}
	if diff := nativeRowsDiffer(after, nativeRowsOf(got)); diff != nil {
		t.Fatalf("the phased answer lost the pinned unit after its version was merged away: %v\nwant %+v\ngot  %+v", diff, after, nativeRowsOf(got))
	}
}
