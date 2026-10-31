package paritytest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// RetentionStore is what the retention sweep suite drives (CHAOS-11952): the
// result store with its submission, thread, run, feedback and usage sides and
// the retention sweep, as both adapters are one object.
type RetentionStore interface {
	ThreadStore
	contextfabric.RetentionSweeper
	contextfabric.RunStore
	contextfabric.ResultFeedbackStore
	contextfabric.UsageReader
	IsStructureSuperseded(ctx context.Context, orgID, priorResultID string, member contractsv1.ContextFabricStructureNeedKind) (bool, error)
}

// RunRetentionSwitchSuite pins that the job and the read rule for a record
// with no thread are one switch (CHAOS-11952): a store whose read rule is off
// refuses to sweep, and removes nothing. newStore returns a store with the
// read rule off.
func RunRetentionSwitchSuite(t *testing.T, newStore func(t *testing.T) RetentionStore) {
	t.Helper()
	store := newStore(t)
	owner := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-switch", OrgID: "org-parity-switch"}
	key, err := contextfabric.SubmissionKeyFor(owner, "submission-switch-0001")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	admission := contextfabric.SubmissionAdmission{Key: key, RequestDigest: "sha256:" + fmt.Sprintf("%064x", 119522), ReservedResultID: "result_switch_0001", ExpiresAt: at.Add(time.Minute)}
	if _, admitted, err := store.AdmitSubmission(context.Background(), admission, at); err != nil || !admitted {
		t.Fatalf("admit = %t %v", admitted, err)
	}
	if got, err := store.SweepRetention(context.Background(), at.Add(48*time.Hour), 0); !errors.Is(err, contextfabric.ErrRetentionReadRuleOff) || got.Changed() {
		t.Fatalf("a sweep with the read rule off = %+v %v, want ErrRetentionReadRuleOff and nothing removed", got, err)
	}
	if _, err := store.GetSubmission(context.Background(), key, at.Add(48*time.Hour)); err != nil {
		t.Fatalf("the record after the refused sweep = %v, want kept", err)
	}
}

// RunRetentionSweepSuite runs the retention job's store table (CHAOS-11952,
// plan §2.6, D-22; contract §2 and §6) against a store, on back-dated rows
// written by the stores' own producers (admission, save, delete, event log,
// feedback). newStore must return an EMPTY store per case whose own clock is
// clock.Now.
func RunRetentionSweepSuite(t *testing.T, newStore func(t *testing.T, clock func() time.Time) RetentionStore) {
	t.Helper()
	start := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	owner := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-sweep-owner", OrgID: "org-parity-sweep"}
	ctx := context.Background()
	month := contractsv1.ContextFabricThreadRetentionMonth
	limits := contextfabric.SubmissionLimits{MaxRunningPerPrincipal: 100, MaxRunningPerOrg: 100, MonthlyPlatformRunAllowance: 100}
	setup := func(t *testing.T) (RetentionStore, *ThreadClock) {
		t.Helper()
		clock := &ThreadClock{now: start}
		return newStore(t, clock.Now), clock
	}
	admit := func(t *testing.T, store RetentionStore, clock *ThreadClock, threadID, name string, retention *contractsv1.ContextFabricThreadRetentionDays) contextfabric.SubmissionAdmission {
		t.Helper()
		key, err := contextfabric.SubmissionKeyFor(owner, "submission-sweep-"+name)
		if err != nil {
			t.Fatal(err)
		}
		admission := contextfabric.SubmissionAdmission{
			Key: key, RequestDigest: "sha256:" + fmt.Sprintf("%064x", 119520), ReservedResultID: "result_sweep_" + name,
			ThreadID: threadID, ExpiresAt: clock.now.Add(time.Minute), ThreadRetention: retention,
			Limits: limits, PlatformRun: true,
		}
		if threadID != "" {
			admission.Question = "question " + name
		}
		record, admitted, err := store.AdmitSubmission(ctx, admission, clock.now)
		if err != nil || !admitted {
			t.Fatalf("admit %s = %+v admitted=%t err=%v", name, record, admitted, err)
		}
		return admission
	}
	saveResult := func(t *testing.T, store RetentionStore, result contextfabric.InvestigationResult) {
		t.Helper()
		if err := store.Save(contextfabric.WithSubmissionReservedResultID(ctx, result.ResultID), owner, result, nil, nil,
			contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
			contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
			contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)); err != nil {
			t.Fatalf("save %s: %v", result.ResultID, err)
		}
	}
	// answer is one turn's whole backing set from its producers: the saved
	// result, the run's terminal event naming it, and the owner's feedback.
	answer := func(t *testing.T, store RetentionStore, clock *ThreadClock, admission contextfabric.SubmissionAdmission) {
		t.Helper()
		saveResult(t, store, ValidResult(admission.ReservedResultID, "question for "+admission.ReservedResultID))
		run := contextfabric.RunKey{OrgID: owner.OrgID, ReservedResultID: admission.ReservedResultID}
		if appended, err := store.AppendRunEvent(ctx, run, contextfabric.RunEvent{Seq: 1, Type: contractsv1.ContextFabricInvestigationEventResult,
			Terminal: &contextfabric.RunTerminal{Kind: contextfabric.RunTerminalResult, ResultID: admission.ReservedResultID}}, 0, clock.now); err != nil || !appended {
			t.Fatalf("append the terminal of %s = %t %v", admission.ReservedResultID, appended, err)
		}
		if _, err := store.SetResultFeedback(ctx, owner, admission.ReservedResultID, contractsv1.ContextFabricResultFeedbackYes, contractsv1.ContextFabricResultFeedbackNo, clock.now); err != nil {
			t.Fatalf("feedback on %s: %v", admission.ReservedResultID, err)
		}
	}
	threadKey := func(t *testing.T, threadID string) contextfabric.ThreadKey {
		t.Helper()
		key, err := contextfabric.ThreadKeyFor(owner, threadID)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	sweep := func(t *testing.T, store RetentionStore, at time.Time) contextfabric.RetentionSweep {
		t.Helper()
		got, err := store.SweepRetention(ctx, at, 0)
		if err != nil {
			t.Fatalf("sweep at %s: %v", at, err)
		}
		return got
	}
	// gone asserts a turn's backing set is gone: no result, no event log,
	// no record. Read past the read rules: the stores' own reads, with a
	// clock before any expiry, so only a missing row can answer not found.
	resultGone := func(t *testing.T, store RetentionStore, resultID string) {
		t.Helper()
		if _, err := store.Get(ctx, owner, resultID); !errors.Is(err, contextfabric.ErrInvestigationResultNotFound) {
			t.Fatalf("result %s after the sweep = %v, want gone", resultID, err)
		}
		if terminal, err := store.RunTerminalOf(ctx, contextfabric.RunKey{OrgID: owner.OrgID, ReservedResultID: resultID}); err != nil || terminal != nil {
			t.Fatalf("event log of %s after the sweep = %+v %v, want gone", resultID, terminal, err)
		}
	}
	recordGone := func(t *testing.T, store RetentionStore, key contextfabric.SubmissionKey, at time.Time) {
		t.Helper()
		if record, err := store.GetSubmission(ctx, key, at); !errors.Is(err, contextfabric.ErrSubmissionNotFound) {
			t.Fatalf("record %s after the sweep = %+v %v, want gone", key.SubmissionID, record, err)
		}
	}
	usage := func(t *testing.T, store RetentionStore) int64 {
		t.Helper()
		got, found, err := store.ReadUsageMonthly(ctx, owner.OrgID, start)
		if err != nil || !found {
			t.Fatalf("usage = %+v found=%t %v", got, found, err)
		}
		return got.PlatformRuns
	}

	t.Run("an expired retention-0 thread goes whole; usage stays (AC-6)", func(t *testing.T) {
		store, clock := setup(t)
		first := admit(t, store, clock, "thread-sweep-day", "day-1", nil)
		answer(t, store, clock, first)
		clock.now = clock.now.Add(time.Hour)
		second := admit(t, store, clock, "thread-sweep-day", "day-2", nil)
		answer(t, store, clock, second)
		lastTurn := clock.now
		before := usage(t, store)
		// Inside the window nothing goes.
		if got := sweep(t, store, lastTurn.Add(24*time.Hour-time.Second)); got.Changed() {
			t.Fatalf("a sweep inside the window = %+v, want nothing", got)
		}
		got := sweep(t, store, lastTurn.Add(24*time.Hour))
		if got.ThreadsRemoved != 1 || got.ResultsRemoved != 2 || got.Fenced != 0 {
			t.Fatalf("the sweep at expiry = %+v, want 1 thread and 2 results", got)
		}
		clock.now = start
		for _, admission := range []contextfabric.SubmissionAdmission{first, second} {
			resultGone(t, store, admission.ReservedResultID)
			recordGone(t, store, admission.Key, start)
		}
		if _, err := store.GetThread(ctx, threadKey(t, "thread-sweep-day"), nil, 10, start); !errors.Is(err, contextfabric.ErrThreadNotFound) {
			t.Fatalf("the thread after the sweep = %v, want gone", err)
		}
		if after := usage(t, store); after != before {
			t.Fatalf("usage after the sweep = %d, want %d (the aggregate survives)", after, before)
		}
		// The ids are unknown now: the thread id starts a new thread.
		clock.now = lastTurn.Add(25 * time.Hour)
		admit(t, store, clock, "thread-sweep-day", "day-3", nil)
	})

	t.Run("a retention-30 thread stays a month", func(t *testing.T) {
		store, clock := setup(t)
		turn := admit(t, store, clock, "thread-sweep-month", "month-1", &month)
		answer(t, store, clock, turn)
		if got := sweep(t, store, start.Add(29*24*time.Hour)); got.Changed() {
			t.Fatalf("a sweep at 29 days = %+v, want nothing", got)
		}
		if got := sweep(t, store, start.Add(30*24*time.Hour)); got.ThreadsRemoved != 1 || got.ResultsRemoved != 1 {
			t.Fatalf("the sweep at 30 days = %+v, want the thread and its result", got)
		}
		resultGone(t, store, turn.ReservedResultID)
	})

	t.Run("Q1: a deleted thread's content goes on the next pass; its closure record stays until expiry (T1)", func(t *testing.T) {
		store, clock := setup(t)
		turn := admit(t, store, clock, "thread-sweep-deleted", "deleted-1", &month)
		answer(t, store, clock, turn)
		clock.now = clock.now.Add(time.Minute)
		if closed, err := store.CloseThread(ctx, threadKey(t, "thread-sweep-deleted"), clock.now); err != nil || !closed {
			t.Fatalf("delete = %t %v", closed, err)
		}
		deletedAt := clock.now
		got := sweep(t, store, deletedAt.Add(time.Second))
		if got.ThreadsErased != 1 || got.ResultsRemoved != 1 || got.ThreadsRemoved != 0 {
			t.Fatalf("the pass after the delete = %+v, want the content of 1 thread", got)
		}
		clock.now = deletedAt
		resultGone(t, store, turn.ReservedResultID)
		if feedback, err := store.GetResultFeedback(ctx, owner, turn.ReservedResultID, deletedAt); err == nil && feedback.Correct != "" {
			t.Fatalf("feedback on the erased result = %+v", feedback)
		}
		// The closure record: the id and digest, no result id, no question;
		// a repeat comes back closed and a new turn is refused.
		record, err := store.GetSubmission(ctx, turn.Key, deletedAt)
		if err != nil || record.ThreadClosed != contextfabric.ThreadClosedDeleted || record.ResultID != "" || record.Question != "" ||
			record.RequestDigest != turn.RequestDigest || !contextfabric.IsErasedReservedResultID(record.ReservedResultID) || record.ReservedResultID != contextfabric.ErasedReservedResultID(turn.ReservedResultID) {
			t.Fatalf("the closure record = %+v %v", record, err)
		}
		repeat := turn
		repeat.ExpiresAt = deletedAt.Add(time.Minute)
		if again, admitted, err := store.AdmitSubmission(ctx, repeat, deletedAt); err != nil || admitted || again.ThreadClosed != contextfabric.ThreadClosedDeleted {
			t.Fatalf("a repeat of the erased turn = %+v admitted=%t %v, want the closed record", again, admitted, err)
		}
		next := repeat
		next.Key.SubmissionID, next.ReservedResultID = "submission-sweep-deleted-2", "result_sweep_deleted_2"
		var closed *contextfabric.ThreadClosedError
		if _, _, err := store.AdmitSubmission(ctx, next, deletedAt); !errors.As(err, &closed) || closed.Reason != contextfabric.ThreadClosedDeleted {
			t.Fatalf("a new turn on the erased thread = %v, want thread_closed (deleted)", err)
		}
		// A late save of the erased reservation stores nothing (T2).
		if err := store.Save(contextfabric.WithSubmissionReservedResultID(ctx, turn.ReservedResultID), owner, ValidResult(turn.ReservedResultID, "late"), nil, nil,
			contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
			contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
			contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)); !errors.Is(err, contextfabric.ErrReservationGone) {
			t.Fatalf("a late save of the erased reservation = %v, want ErrReservationGone", err)
		}
		// A second pass changes nothing; at expiry the closure goes.
		if again := sweep(t, store, deletedAt.Add(2*time.Second)); again.Changed() {
			t.Fatalf("a second pass = %+v, want nothing", again)
		}
		if final := sweep(t, store, start.Add(30*24*time.Hour)); final.ThreadsRemoved != 1 || final.ResultsRemoved != 0 {
			t.Fatalf("the pass at expiry = %+v, want the closure record", final)
		}
		recordGone(t, store, turn.Key, start)
	})

	t.Run("Q2: a record with no thread goes with its result 24 hours after its admission", func(t *testing.T) {
		store, clock := setup(t)
		lone := admit(t, store, clock, "", "lone", nil)
		answer(t, store, clock, lone)
		before := usage(t, store)
		if got := sweep(t, store, start.Add(24*time.Hour-time.Second)); got.Changed() {
			t.Fatalf("a sweep inside the window = %+v, want nothing", got)
		}
		if got := sweep(t, store, start.Add(24*time.Hour)); got.RecordsRemoved != 1 || got.ResultsRemoved != 1 {
			t.Fatalf("the sweep at 24 hours = %+v, want the record and its result", got)
		}
		resultGone(t, store, lone.ReservedResultID)
		recordGone(t, store, lone.Key, start)
		if after := usage(t, store); after != before {
			t.Fatalf("usage after the sweep = %d, want %d", after, before)
		}
	})

	t.Run("T2: a record that can still save fences its unit", func(t *testing.T) {
		store, clock := setup(t)
		running := admit(t, store, clock, "thread-sweep-fence", "fence-1", nil)
		clock.now = clock.now.Add(time.Minute)
		if closed, err := store.CloseThread(ctx, threadKey(t, "thread-sweep-fence"), clock.now); err != nil || !closed {
			t.Fatalf("delete = %t %v", closed, err)
		}
		// Still running (its deadline is a minute out): fenced.
		if got := sweep(t, store, clock.now); got.Fenced != 1 || got.Changed() {
			t.Fatalf("a sweep of a running record's thread = %+v, want fenced", got)
		}
		// Abandoned at its deadline; held for the save window.
		deadline := running.ExpiresAt
		if record, err := store.GetSubmission(ctx, running.Key, deadline.Add(time.Second)); err != nil || record.State != contextfabric.SubmissionAbandoned {
			t.Fatalf("past its deadline = %+v %v, want abandoned", record, err)
		}
		if got := sweep(t, store, deadline.Add(contextfabric.RetentionSaveWindow-time.Second)); got.Fenced != 1 || got.Changed() {
			t.Fatalf("a sweep inside the save window = %+v, want fenced", got)
		}
		if got := sweep(t, store, deadline.Add(contextfabric.RetentionSaveWindow)); got.ThreadsErased != 1 {
			t.Fatalf("a sweep after the save window = %+v, want the thread erased", got)
		}
	})

	t.Run("T2: a record with no thread whose run can still save is fenced past its window", func(t *testing.T) {
		store, clock := setup(t)
		key, err := contextfabric.SubmissionKeyFor(owner, "submission-sweep-lone-long")
		if err != nil {
			t.Fatal(err)
		}
		// A run deadline beyond the retention-0 window: the record is due
		// at 24 hours but can still save until its deadline + the window.
		long := contextfabric.SubmissionAdmission{Key: key, RequestDigest: "sha256:" + fmt.Sprintf("%064x", 119521), ReservedResultID: "result_sweep_lone_long",
			ExpiresAt: start.Add(30 * time.Hour), Limits: limits}
		if _, admitted, err := store.AdmitSubmission(ctx, long, clock.now); err != nil || !admitted {
			t.Fatalf("admit = %t %v", admitted, err)
		}
		if got := sweep(t, store, start.Add(25*time.Hour)); got.Fenced != 1 || got.Changed() {
			t.Fatalf("a sweep while its run can save = %+v, want fenced", got)
		}
		if got := sweep(t, store, start.Add(31*time.Hour)); got.RecordsRemoved != 1 {
			t.Fatalf("a sweep after its save window = %+v, want the record", got)
		}
	})

	t.Run("T3: a result a live thread's turn names stays", func(t *testing.T) {
		store, clock := setup(t)
		a := admit(t, store, clock, "thread-sweep-share-a", "share-a", nil)
		answer(t, store, clock, a)
		b := admit(t, store, clock, "thread-sweep-share-b", "share-b", &month)
		if record, completion, err := store.FinishSubmission(ctx, b.Key, a.ReservedResultID, clock.now); err != nil || record.ResultID != a.ReservedResultID || completion != contextfabric.SubmissionCompletionServedStored {
			t.Fatalf("finish b with a's result = %+v %s %v", record, completion, err)
		}
		got := sweep(t, store, start.Add(24*time.Hour))
		if got.ThreadsRemoved != 1 || got.ResultsRemoved != 0 {
			t.Fatalf("the sweep of a's thread = %+v, want the thread and no result", got)
		}
		clock.now = start.Add(24 * time.Hour)
		if _, err := store.Get(ctx, owner, a.ReservedResultID); err != nil {
			t.Fatalf("a result the live thread b names after a went = %v", err)
		}
		if final := sweep(t, store, start.Add(30*24*time.Hour)); final.ThreadsRemoved != 1 || final.ResultsRemoved != 1 {
			t.Fatalf("the sweep of b's thread = %+v, want the thread and the shared result", final)
		}
		clock.now = start
		resultGone(t, store, a.ReservedResultID)
	})

	t.Run("a result no record names, and its claims, stay; a removed result's claims go", func(t *testing.T) {
		store, clock := setup(t)
		if err := store.Save(ctx, owner, ValidResult("result_sweep_unbound", "unbound"), nil, nil,
			contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
			contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
			contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)); err != nil {
			t.Fatal(err)
		}
		prior := admit(t, store, clock, "", "prior", nil)
		saveResult(t, store, ValidResult(prior.ReservedResultID, "prior"))
		// A later turn redeems a receipt of the prior result: a claim keyed
		// by the prior result.
		clock.now = clock.now.Add(time.Minute)
		later := admit(t, store, clock, "", "later", nil)
		saveResult(t, store, ClaimBearingResult(later.ReservedResultID, "later", prior.ReservedResultID, "receipt_sweep_prior"))
		if superseded, err := store.IsStructureSuperseded(ctx, owner.OrgID, prior.ReservedResultID, contractsv1.ContextFabricStructureNeedExpectedKind); err != nil || !superseded {
			t.Fatalf("the claim before the sweep = %t %v", superseded, err)
		}
		got := sweep(t, store, start.Add(24*time.Hour+time.Minute))
		if got.RecordsRemoved != 2 || got.ResultsRemoved != 2 {
			t.Fatalf("the sweep = %+v, want both records and both results", got)
		}
		if superseded, err := store.IsStructureSuperseded(ctx, owner.OrgID, prior.ReservedResultID, contractsv1.ContextFabricStructureNeedExpectedKind); err != nil || superseded {
			t.Fatalf("the removed prior's claim after the sweep = %t %v, want gone", superseded, err)
		}
		clock.now = start.Add(400 * 24 * time.Hour)
		if got := sweep(t, store, clock.now); got.Changed() {
			t.Fatalf("a sweep a year later = %+v, want nothing", got)
		}
		if _, err := store.Get(ctx, owner, "result_sweep_unbound"); err != nil {
			t.Fatalf("an unbound result a year later = %v", err)
		}
	})

	t.Run("a pass handles at most limit units, oldest first", func(t *testing.T) {
		store, clock := setup(t)
		var admissions []contextfabric.SubmissionAdmission
		for i := 0; i < 3; i++ {
			admissions = append(admissions, admit(t, store, clock, "", fmt.Sprintf("batch-%d", i), nil))
			clock.now = clock.now.Add(time.Minute)
		}
		got, err := store.SweepRetention(ctx, start.Add(48*time.Hour), 2)
		if err != nil || got.RecordsRemoved != 2 {
			t.Fatalf("a pass with limit 2 = %+v %v, want 2 records", got, err)
		}
		recordGone(t, store, admissions[0].Key, start)
		recordGone(t, store, admissions[1].Key, start)
		if _, err := store.GetSubmission(ctx, admissions[2].Key, start); err != nil {
			t.Fatalf("the newest record after a limited pass = %v, want kept", err)
		}
	})
}
