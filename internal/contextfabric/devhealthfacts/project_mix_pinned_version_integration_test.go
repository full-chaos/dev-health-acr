package devhealthfacts_test

// The project mix scope statement pins each unit to the version it read
// (work_unit_id, computed_at), and every later phase reads exactly those pairs
// (projectMixPinnedFilter). work_unit_investments is
// ReplacingMergeTree(computed_at): a recompute that writes a newer version and a
// merge delete the pinned one. A phase that cannot find a pinned pair must make
// the attempt retry, never answer without the unit.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

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

// The roll-up's repo arms read the same pinned pairs: the same recompute and
// merge must not drop a counted unit from a roll-up row either.
func TestProjectRollupMixKeepsAPinnedUnitThroughARecomputeAndAMergeAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	window := devhealthfacts.ProjectMixWindow{}
	const orgID = "org-12179-pinned-merge-rollup"
	query, direct := newScopedCHAOS7257Client(t, nil)
	createCHAOS7257Tables(t, ctx, direct)
	at := seedCHAOS7257Parity(t, ctx, direct, orgID)
	insertVersionAtMilli(t, ctx, direct, orgID, "wu-pin", repoUUID("r1a"), `{"issues":[],"prs":[]}`, "feature_delivery", at.Add(-time.Hour))
	insertMembership(t, ctx, direct, orgID, "wu-pin", at)
	ids := []string{"linear:proj-1", "linear:proj-2", "linear:proj-3"}
	before := runRollupStatement(t, ctx, query, "SingleRollupBefore", devhealthfacts.ProjectRollupStatement(window), orgID, ids, window)
	var once sync.Once
	got, err := devhealthfacts.RunProjectRollupMix(devhealthfacts.WithProjectMixAfterScope(ctx, func() {
		once.Do(func() {
			insertVersionAtMilli(t, ctx, direct, orgID, "wu-pin", repoUUID("r1a"), `{"issues":[],"prs":[]}`, "feature_delivery", at.Add(-30*time.Minute))
			if err := direct.Exec(ctx, `SYSTEM START MERGES work_unit_investments`); err != nil {
				t.Fatalf("start merges: %v", err)
			}
			if err := direct.Exec(ctx, `OPTIMIZE TABLE work_unit_investments FINAL`); err != nil {
				t.Fatalf("merge: %v", err)
			}
		})
	}), query, orgID, ids, window)
	if err != nil {
		t.Fatal(err)
	}
	var versions uint64
	if err := direct.QueryRow(ctx, `SELECT count() FROM work_unit_investments WHERE org_id = ? AND work_unit_id = 'wu-pin'`, orgID).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	after := runRollupStatement(t, ctx, query, "SingleRollupAfter", devhealthfacts.ProjectRollupStatement(window), orgID, ids, window)
	if versions != 1 {
		t.Fatalf("control: %d physical versions of wu-pin after the merge, want 1 (the pinned one deleted)", versions)
	}
	if diff := rollupRowsDiffer(before, after); diff != nil {
		t.Fatalf("control: the identical recompute changed the single statement's answer: %v", diff)
	}
	if diff := rollupRowsDiffer(after, rollupRowsOf(got)); diff != nil {
		t.Fatalf("the phased roll-up lost the pinned unit after its version was merged away: %v", diff)
	}
}

// A pinned version lost on EVERY attempt exhausts the bounded retry into the
// typed retryable failure, which the provider serves as a contended unavailable
// source, never as an answer short of the unit.
func TestProjectNativeMixFailsContendedWhenAPinnedVersionIsLostOnEveryAttemptAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const orgID = "org-12179-pinned-exhaust"
	query, direct := newScopedCHAOS7257Client(t, nil)
	createCHAOS7257Tables(t, ctx, direct)
	at := seedCHAOS7257Native(t, ctx, direct, orgID)
	insertNativeVersion(t, ctx, direct, orgID, "nu-pin", "feature_delivery", at.Add(-time.Hour))
	insertMembership(t, ctx, direct, orgID, "nu-pin", at)
	if err := direct.Exec(ctx, `SYSTEM START MERGES work_unit_investments`); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	_, err := devhealthfacts.RunProjectNativeMix(devhealthfacts.WithProjectMixAfterScope(ctx, func() {
		attempts++
		insertNativeVersion(t, ctx, direct, orgID, "nu-pin", "feature_delivery", at.Add(time.Duration(attempts)*time.Minute))
		if err := direct.Exec(ctx, `OPTIMIZE TABLE work_unit_investments FINAL`); err != nil {
			t.Fatalf("merge: %v", err)
		}
	}), query, orgID, []string{"linear:n-p1"}, devhealthfacts.ProjectMixWindow{}, 201)
	var contended *devhealthfacts.ProjectMixContendedError
	if !errors.As(err, &contended) || !contended.Retryable() || contended.Attempts != 3 {
		t.Fatalf("err = %v after %d attempt(s), want a retryable *ProjectMixContendedError after 3", err, attempts)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}
