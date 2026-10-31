package pginvestigation_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/orgpurge"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation/paritytest"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// retentionBackingTables is the D-22 backing set (plan §2.6) as tables, each
// counted per organization, with the two org-keyed tables retention keeps
// (the usage aggregate, AC-6; the model receipts, lead ruling Q3).
var retentionBackingTables = []string{
	"context_fabric_threads",
	"context_fabric_investigation_submissions",
	"context_fabric_investigation_results",
	"context_fabric_result_feedback",
	"context_fabric_investigation_run_events",
	"context_fabric_clarification_selections",
	"context_fabric_structure_selections",
	"context_fabric_structure_supersession_claims",
}

var retentionKeptTables = []string{"context_fabric_usage_monthly", "context_fabric_model_execution_receipts"}

// The org purge removes the whole backing set and the kept tables too: every
// table above is on its list (lead ruling: an organization purge removes the
// whole set).
func TestOrgPurgeCoversTheRetentionBackingSet(t *testing.T) {
	purged := map[string]bool{}
	for _, table := range orgpurge.PurgedTables() {
		purged[table] = true
	}
	for _, table := range append(append([]string{}, retentionBackingTables...), retentionKeptTables...) {
		require.True(t, purged[table], "the org purge does not remove %s", table)
	}
}

type drillClock struct{ now time.Time }

func (c *drillClock) Now() time.Time { return c.now }

func retentionCensus(t *testing.T, ctx context.Context, db *sql.DB, orgID string) map[string]int {
	t.Helper()
	census := map[string]int{}
	for _, table := range append(append([]string{}, retentionBackingTables...), retentionKeptTables...) {
		var n int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.`+table+` WHERE org_id = $1`, orgID).Scan(&n), table)
		census[table] = n
	}
	return census
}

func censusLine(census map[string]int) string {
	keys := make([]string, 0, len(census))
	for key := range census {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", strings.TrimPrefix(key, "context_fabric_"), census[key]))
	}
	return strings.Join(parts, " ")
}

// TestRetentionDrillOnBackDatedRows is the I-30 drill (CHAOS-11952): rows
// back-dated through the stores' own producers (admission, save, delete, the
// run event log, feedback; the two selection tables by their row shape),
// one retention pass, and the census of every backing-set table before and
// after. During the pass a second connection reads every removed result after
// each statement of each unit: no read may ever serve one (lead ruling Q4: a
// parent-first order, committed apart from the results, would let it).
func TestRetentionDrillOnBackDatedRows(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	clock := &drillClock{now: start}
	store, err := pginvestigation.NewStore(db, pginvestigation.WithClock(clock.Now), pginvestigation.WithUnthreadedRecordRetention(true))
	require.NoError(t, err)
	reader, err := pginvestigation.NewStore(db, pginvestigation.WithClock(clock.Now), pginvestigation.WithUnthreadedRecordRetention(true))
	require.NoError(t, err)
	owner := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-drill", OrgID: "org-retention-drill"}
	month := contractsv1.ContextFabricThreadRetentionMonth
	limits := contextfabric.SubmissionLimits{MaxRunningPerPrincipal: 100, MaxRunningPerOrg: 100, MonthlyPlatformRunAllowance: 100}

	admit := func(threadID, name string, retention *contractsv1.ContextFabricThreadRetentionDays) contextfabric.SubmissionAdmission {
		key, err := contextfabric.SubmissionKeyFor(owner, "submission-drill-"+name)
		require.NoError(t, err)
		admission := contextfabric.SubmissionAdmission{Key: key, RequestDigest: "sha256:" + fmt.Sprintf("%064x", 30), ReservedResultID: "result_drill_" + name,
			ThreadID: threadID, ExpiresAt: clock.now.Add(time.Minute), ThreadRetention: retention, Limits: limits, PlatformRun: true}
		if threadID != "" {
			admission.Question = "drill question " + name
		}
		_, admitted, err := store.AdmitSubmission(ctx, admission, clock.now)
		require.NoError(t, err)
		require.True(t, admitted, name)
		return admission
	}
	answer := func(admission contextfabric.SubmissionAdmission) {
		require.NoError(t, store.Save(contextfabric.WithSubmissionReservedResultID(ctx, admission.ReservedResultID), owner, paritytest.ValidResult(admission.ReservedResultID, "drill "+admission.ReservedResultID), nil, nil,
			contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
			contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
			contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)))
		appended, err := store.AppendRunEvent(ctx, contextfabric.RunKey{OrgID: owner.OrgID, ReservedResultID: admission.ReservedResultID},
			contextfabric.RunEvent{Seq: 1, Type: contractsv1.ContextFabricInvestigationEventResult, Terminal: &contextfabric.RunTerminal{Kind: contextfabric.RunTerminalResult, ResultID: admission.ReservedResultID}}, 0, clock.now)
		require.NoError(t, err)
		require.True(t, appended)
		_, err = store.SetResultFeedback(ctx, owner, admission.ReservedResultID, contractsv1.ContextFabricResultFeedbackYes, contractsv1.ContextFabricResultFeedbackYes, clock.now)
		require.NoError(t, err)
	}
	selectOn := func(priorResultID, name string) {
		hash := strings.Repeat("a", 64)
		_, err := db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_clarification_selections (selection_id, org_id, captured_at, question_hash, prior_result_id, selected_receipt_id,
    selected_subject_kind, selected_subject_canonical_id, selection_provenance, offered_candidates, pipeline_context)
VALUES ($1, $2, $3, $4, $5, 'receipt_drill', 'repository', 'repo_drill', 'web_assertion', '[]', '{}')`, "selection_clar_"+name, owner.OrgID, clock.now, hash, priorResultID)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_structure_selections (selection_id, org_id, captured_at, question_hash, prior_result_id, member, selected_receipt_id,
    selected_applied_value, accepted, selection_mode, selection_provenance, offered, pipeline_context)
VALUES ($1, $2, $3, $4, $5, 'expected_kind', 'receipt_drill', 'pull_request', true, 'human_panel', 'web_assertion', '[]', '{}')`, "selection_struct_"+name, owner.OrgID, clock.now, hash, priorResultID)
		require.NoError(t, err)
	}
	threadKey := func(threadID string) contextfabric.ThreadKey {
		key, err := contextfabric.ThreadKeyFor(owner, threadID)
		require.NoError(t, err)
		return key
	}

	// A retention-0 conversation of two turns (last turn at +1 h).
	dayOne := admit("thread-drill-day", "day-1", nil)
	answer(dayOne)
	selectOn(dayOne.ReservedResultID, "day-1")
	clock.now = start.Add(time.Hour)
	dayTwo := admit("thread-drill-day", "day-2", nil)
	answer(dayTwo)
	// A retention-30 conversation the user deleted at +2 h.
	clock.now = start.Add(2 * time.Hour)
	deleted := admit("thread-drill-deleted", "deleted", &month)
	answer(deleted)
	require.NoError(t, func() error {
		closed, err := store.CloseThread(ctx, threadKey("thread-drill-deleted"), clock.now)
		if err == nil && !closed {
			return errors.New("not closed")
		}
		return err
	}())
	// A turn with no thread at the start.
	clock.now = start
	lone := admit("", "lone", nil)
	answer(lone)
	// A live retention-30 conversation at +20 h, and an MCP result no record
	// names: both out of this pass.
	clock.now = start.Add(20 * time.Hour)
	live := admit("thread-drill-live", "live", &month)
	answer(live)
	require.NoError(t, store.Save(ctx, owner, paritytest.ValidResult("result_drill_mcp", "drill mcp"), nil, nil,
		contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
		contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
		contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)))

	before := retentionCensus(t, ctx, db, owner.OrgID)
	t.Logf("DRILL before (t0 %s): %s", start.Format(time.RFC3339), censusLine(before))

	// The pass at +25 h: the retention-0 thread expired at +25 h, the lone
	// record at +24 h, the deleted thread is past closed_at.
	sweepAt := start.Add(25 * time.Hour)
	clock.now = sweepAt
	doomed := []string{dayOne.ReservedResultID, dayTwo.ReservedResultID, deleted.ReservedResultID, lone.ReservedResultID}
	var violations []string
	pginvestigation.SetRetentionStepHook(store, func(step string) {
		for _, id := range doomed {
			if _, err := reader.Get(ctx, owner, id); err == nil {
				violations = append(violations, step+": "+id)
			}
		}
	})
	sweep, err := store.SweepRetention(ctx, sweepAt, 0)
	require.NoError(t, err)
	t.Logf("DRILL sweep at %s: %+v", sweepAt.Format(time.RFC3339), sweep)
	require.Empty(t, violations, "a removed result was served mid-unit (Q4)")
	require.Equal(t, contextfabric.RetentionSweep{ThreadsRemoved: 1, ThreadsErased: 1, RecordsRemoved: 1, ResultsRemoved: 4}, sweep)

	after := retentionCensus(t, ctx, db, owner.OrgID)
	t.Logf("DRILL after: %s", censusLine(after))
	want := map[string]int{
		"context_fabric_threads":                       before["context_fabric_threads"] - 1,
		"context_fabric_investigation_submissions":     before["context_fabric_investigation_submissions"] - 3,
		"context_fabric_investigation_results":         before["context_fabric_investigation_results"] - 4,
		"context_fabric_result_feedback":               before["context_fabric_result_feedback"] - 4,
		"context_fabric_investigation_run_events":      before["context_fabric_investigation_run_events"] - 4,
		"context_fabric_clarification_selections":      before["context_fabric_clarification_selections"] - 1,
		"context_fabric_structure_selections":          before["context_fabric_structure_selections"] - 1,
		"context_fabric_structure_supersession_claims": before["context_fabric_structure_supersession_claims"],
		"context_fabric_usage_monthly":                 before["context_fabric_usage_monthly"],
		"context_fabric_model_execution_receipts":      before["context_fabric_model_execution_receipts"],
	}
	require.Equal(t, want, after)
	var platformRuns int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT platform_runs FROM acr.context_fabric_usage_monthly WHERE org_id = $1`, owner.OrgID).Scan(&platformRuns))
	require.Equal(t, int64(5), platformRuns, "the usage aggregate counts every admission after the pass (AC-6)")

	// What stays: the live thread's result, the MCP result, the deleted
	// thread's content-free closure record (one row, its record erased).
	for _, id := range []string{live.ReservedResultID, "result_drill_mcp"} {
		_, err := store.Get(ctx, owner, id)
		require.NoError(t, err, id)
	}
	var question sql.NullString
	var reserved string
	var resultID sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `SELECT question, reserved_result_id, result_id FROM acr.context_fabric_investigation_submissions WHERE org_id = $1 AND submission_id = $2`,
		owner.OrgID, deleted.Key.SubmissionID).Scan(&question, &reserved, &resultID))
	require.False(t, question.Valid, "the erased record keeps no question")
	require.False(t, resultID.Valid, "the erased record keeps no result id")
	require.Equal(t, contextfabric.ErasedReservedResultID(deleted.ReservedResultID), reserved)
	// No row of the organization names a removed result any more.
	for _, id := range doomed {
		for _, column := range []struct{ table, column string }{
			{"context_fabric_investigation_results", "result_id"},
			{"context_fabric_investigation_submissions", "result_id"},
			{"context_fabric_investigation_submissions", "reserved_result_id"},
			{"context_fabric_investigation_run_events", "terminal_result_id"},
			{"context_fabric_result_feedback", "result_id"},
			{"context_fabric_clarification_selections", "prior_result_id"},
			{"context_fabric_structure_selections", "prior_result_id"},
		} {
			var n int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.`+column.table+` WHERE `+column.column+` = $1`, id).Scan(&n))
			require.Zero(t, n, "%s.%s still names %s", column.table, column.column, id)
		}
	}
}

// Codex #1808 r1 (T2 under a race): a reserved save that starts while the
// retention job holds the record it is removing waits for the job's
// transaction, then finds no reservation and is refused, storing nothing.
// Without the reservation row lock the save would read the record from its
// snapshot, insert, and leave a result no record names.
func TestARetentionRemovalRacesAReservedSaveAndTheSaveIsRefused(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	store, err := pginvestigation.NewStore(db, pginvestigation.WithUnthreadedRecordRetention(true))
	require.NoError(t, err)
	saver, err := pginvestigation.NewStore(db)
	require.NoError(t, err)
	owner := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-race", OrgID: "org-retention-race"}
	key, err := contextfabric.SubmissionKeyFor(owner, "submission-race-0001")
	require.NoError(t, err)
	admission := contextfabric.SubmissionAdmission{Key: key, RequestDigest: "sha256:" + fmt.Sprintf("%064x", 31), ReservedResultID: "result_race_0001", ExpiresAt: start.Add(time.Minute)}
	_, admitted, err := store.AdmitSubmission(ctx, admission, start)
	require.NoError(t, err)
	require.True(t, admitted)

	saveDone := make(chan error, 1)
	pginvestigation.SetRetentionStepHook(store, func(step string) {
		if step != "record" {
			return
		}
		// Mid-unit: the record is deleted but not committed. A late save of
		// its reserved id starts now and must wait for the unit.
		go func() {
			saveDone <- saver.Save(contextfabric.WithSubmissionReservedResultID(ctx, admission.ReservedResultID), owner, paritytest.ValidResult(admission.ReservedResultID, "race"), nil, nil,
				contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
				contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
				contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation))
		}()
		select {
		case err := <-saveDone:
			saveDone <- err
		case <-time.After(500 * time.Millisecond):
		}
	})
	sweep, err := store.SweepRetention(ctx, start.Add(48*time.Hour), 0)
	require.NoError(t, err)
	require.Equal(t, 1, sweep.RecordsRemoved)
	select {
	case err := <-saveDone:
		require.ErrorIs(t, err, contextfabric.ErrReservationGone, "the racing save")
	case <-time.After(10 * time.Second):
		t.Fatal("the racing save never returned")
	}
	var n int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_investigation_results WHERE result_id = $1`, admission.ReservedResultID).Scan(&n))
	require.Zero(t, n, "the racing save stored a result no record names")
}

// CHAOS-12214: a pass that fails partway returns the counts of the units it
// had already committed, with the error, so the retention loop's failed line
// says what was removed. Here two records with no thread are due; the pass is
// cancelled inside the second unit, before its commit.
func TestARetentionPassThatFailsPartwayReturnsWhatItCommitted(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	store, err := pginvestigation.NewStore(db, pginvestigation.WithUnthreadedRecordRetention(true))
	require.NoError(t, err)
	owner := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-partial", OrgID: "org-retention-partial"}
	for i, name := range []string{"one", "two"} {
		key, err := contextfabric.SubmissionKeyFor(owner, "submission-partial-"+name)
		require.NoError(t, err)
		admission := contextfabric.SubmissionAdmission{Key: key, RequestDigest: "sha256:" + fmt.Sprintf("%064x", 40+i), ReservedResultID: "result_partial_" + name, ExpiresAt: start.Add(time.Minute)}
		_, admitted, err := store.AdmitSubmission(ctx, admission, start)
		require.NoError(t, err)
		require.True(t, admitted, name)
	}

	sweepCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	units := 0
	pginvestigation.SetRetentionStepHook(store, func(step string) {
		if step != "record" {
			return
		}
		units++
		if units == 2 {
			cancel() // inside the second unit, before its commit
		}
	})
	sweep, err := store.SweepRetention(sweepCtx, start.Add(25*time.Hour), 0)
	require.Error(t, err, "the pass was cancelled inside its second unit")
	require.Equal(t, 2, units, "the pass did not reach its second unit")
	require.Equal(t, contextfabric.RetentionSweep{RecordsRemoved: 1}, sweep, "a failed pass must return the units it committed")
	var left int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_investigation_submissions WHERE org_id = $1`, owner.OrgID).Scan(&left))
	require.Equal(t, 1, left, "the first unit committed and the second rolled back")
}
