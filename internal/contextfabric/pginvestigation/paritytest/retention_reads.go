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

// SubmissionRecordRemover deletes one submission record directly, as the
// retention job does (CHAOS-11952), so the suite can reach a reserved save
// whose record is gone (design §6 T2) without the job. Each adapter's test
// supplies it; a store without it fails the suite rather than skipping.
type SubmissionRecordRemover interface {
	RemoveSubmissionRecord(key contextfabric.SubmissionKey) error
}

// RunRetentionReadSuite runs the read rules retention depends on
// (CHAOS-11952, plan §2.6, D-22; contract §2 and §6) against a store:
//
//   - a delete closes every read at once (lead ruling Q1: proved, not
//     assumed). A store read of the record returns it with ThreadClosed set,
//     and every route that reads a record (status, attach, cancel, a repeat)
//     refuses on that field;
//   - a record with no thread is a one-turn thread with retention 0 (lead
//     ruling Q2): from its admission + 24 hours its result fails closed and
//     the record reads closed (expired), for its status and a repeat alike;
//   - a result no record names (MCP, Workbench) is out of scope and stays;
//   - T2: a save under a reserved result id whose record is gone is refused
//     and stores nothing, never an unbound row the organization could read.
//
// The rule for a record with no thread is behind the retention switch
// (config.Config.AskDevRetentionEnabled, lead ruling 2): newStore's retention
// argument is that switch, and the suite drives both states. Off, a record
// with no thread keeps today's behaviour; on, it closes at 24 hours.
//
// newStore must return an EMPTY store per case whose own clock is clock.Now.
func RunRetentionReadSuite(t *testing.T, newStore func(t *testing.T, clock func() time.Time, retention bool) ThreadStore) {
	t.Helper()
	start := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	owner := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-retention-owner", OrgID: "org-parity-retention"}
	ctx := context.Background()
	setup := func(t *testing.T) (ThreadStore, *ThreadClock) {
		t.Helper()
		clock := &ThreadClock{now: start}
		return newStore(t, clock.Now, true), clock
	}
	month := contractsv1.ContextFabricThreadRetentionMonth
	admit := func(t *testing.T, store ThreadStore, clock *ThreadClock, threadID, name string, retention *contractsv1.ContextFabricThreadRetentionDays) contextfabric.SubmissionAdmission {
		t.Helper()
		key, err := contextfabric.SubmissionKeyFor(owner, "submission-retention-"+name)
		if err != nil {
			t.Fatal(err)
		}
		admission := contextfabric.SubmissionAdmission{
			Key: key, RequestDigest: "sha256:" + fmt.Sprintf("%064x", 11952), ReservedResultID: "result_retention_" + name,
			ThreadID: threadID, ExpiresAt: clock.now.Add(time.Minute), ThreadRetention: retention,
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
	save := func(ctx context.Context, store ThreadStore, resultID string) error {
		return store.Save(ctx, owner, ValidResult(resultID, "question for "+resultID), nil, nil,
			contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
			contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
			contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation))
	}
	mustSave := func(t *testing.T, store ThreadStore, resultID string) {
		t.Helper()
		if err := save(contextfabric.WithSubmissionReservedResultID(ctx, resultID), store, resultID); err != nil {
			t.Fatalf("save %s: %v", resultID, err)
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

	t.Run("Q1: a delete closes the thread, its turns' results and their status at once", func(t *testing.T) {
		store, clock := setup(t)
		turn := admit(t, store, clock, "thread-retention-delete", "delete", &month)
		mustSave(t, store, turn.ReservedResultID)
		if _, err := store.Get(ctx, owner, turn.ReservedResultID); err != nil {
			t.Fatalf("before the delete the turn's result = %v", err)
		}
		clock.now = clock.now.Add(time.Minute)
		if closed, err := store.CloseThread(ctx, threadKey(t, "thread-retention-delete"), clock.now); err != nil || !closed {
			t.Fatalf("delete = %t %v", closed, err)
		}
		// The same instant, long before expires_at (30 days): no job ran.
		if _, err := store.Get(ctx, owner, turn.ReservedResultID); !errors.Is(err, contextfabric.ErrInvestigationResultNotFound) {
			t.Fatalf("the deleted thread's result by id = %v, want not found", err)
		}
		// The record carries the closure; every route that reads it
		// (status, attach, cancel, a repeat) refuses on ThreadClosed.
		if record, err := store.GetSubmission(ctx, turn.Key, clock.now); err != nil || record.ThreadClosed != contextfabric.ThreadClosedDeleted {
			t.Fatalf("the deleted thread's turn = %+v %v, want closed (deleted)", record, err)
		}
		if _, err := store.GetThread(ctx, threadKey(t, "thread-retention-delete"), nil, 10, clock.now); !errors.Is(err, contextfabric.ErrThreadNotFound) {
			t.Fatalf("the deleted thread = %v, want not found", err)
		}
		threads, _, err := store.ListThreads(ctx, owner.OrgID, threadKey(t, "thread-retention-delete").Owner, nil, 10, clock.now)
		if err != nil || len(threads) != 0 {
			t.Fatalf("the list after the delete = %+v %v, want empty", threads, err)
		}
	})

	t.Run("Q2: a record with no thread is a one-turn thread with retention 0", func(t *testing.T) {
		store, clock := setup(t)
		admitted := clock.now
		lone := admit(t, store, clock, "", "lone", nil)
		clock.now = clock.now.Add(5 * time.Minute)
		mustSave(t, store, lone.ReservedResultID)
		// One second before admission + 24 h: still readable.
		clock.now = admitted.Add(24*time.Hour - time.Second)
		if _, err := store.Get(ctx, owner, lone.ReservedResultID); err != nil {
			t.Fatalf("the lone record's result inside 24 h = %v", err)
		}
		if record, err := store.GetSubmission(ctx, lone.Key, clock.now); err != nil || record.State != contextfabric.SubmissionDone || record.ThreadClosed != "" {
			t.Fatalf("the lone record inside 24 h = %+v %v, want done and open", record, err)
		}
		// From admission + 24 h: closed for every read, before any job runs.
		clock.now = admitted.Add(24 * time.Hour)
		if _, err := store.Get(ctx, owner, lone.ReservedResultID); !errors.Is(err, contextfabric.ErrInvestigationResultNotFound) {
			t.Fatalf("the lone record's result at 24 h = %v, want not found", err)
		}
		if record, err := store.GetSubmission(ctx, lone.Key, clock.now); err != nil || record.ThreadClosed != contextfabric.ThreadClosedExpired {
			t.Fatalf("the lone record at 24 h = %+v %v, want closed (expired)", record, err)
		}
		// A repeat of its id runs nothing while the record lives: the
		// existing record comes back closed, and the route refuses it.
		repeat := lone
		repeat.ExpiresAt = clock.now.Add(time.Minute)
		if again, admittedAgain, err := store.AdmitSubmission(ctx, repeat, clock.now); err != nil || admittedAgain || again.ThreadClosed != contextfabric.ThreadClosedExpired {
			t.Fatalf("a repeat of the expired lone id = %+v admitted=%t err=%v, want the record, closed (expired)", again, admittedAgain, err)
		}
	})

	t.Run("Q2 with retention off: a record with no thread keeps today's behaviour", func(t *testing.T) {
		clock := &ThreadClock{now: start}
		store := newStore(t, clock.Now, false)
		lone := admit(t, store, clock, "", "lone-off", nil)
		mustSave(t, store, lone.ReservedResultID)
		clock.now = start.Add(400 * 24 * time.Hour)
		if _, err := store.Get(ctx, owner, lone.ReservedResultID); err != nil {
			t.Fatalf("the lone record's result a year later with retention off = %v, want readable", err)
		}
		if record, err := store.GetSubmission(ctx, lone.Key, clock.now); err != nil || record.ThreadClosed != "" {
			t.Fatalf("the lone record a year later with retention off = %+v %v, want open", record, err)
		}
	})

	t.Run("Q2 with retention off: a record with no thread keeps a result a closed thread's turn reuses readable", func(t *testing.T) {
		clock := &ThreadClock{now: start}
		store := newStore(t, clock.Now, false)
		lone := admit(t, store, clock, "", "lone-shared", nil)
		mustSave(t, store, lone.ReservedResultID)
		// A later turn of a thread served the lone record's stored result
		// (answer reuse within one owner), then the thread was deleted.
		turn := admit(t, store, clock, "thread-retention-shared", "shared-turn", &month)
		if record, _, err := store.FinishSubmission(ctx, turn.Key, lone.ReservedResultID, clock.now); err != nil || record.ResultID != lone.ReservedResultID {
			t.Fatalf("finish the turn with the lone result = %+v %v", record, err)
		}
		if closed, err := store.CloseThread(ctx, threadKey(t, "thread-retention-shared"), clock.now); err != nil || !closed {
			t.Fatalf("delete = %t %v", closed, err)
		}
		// Off: the record with no thread is live, as on main, so the result
		// it names stays readable however old it is.
		clock.now = start.Add(400 * 24 * time.Hour)
		if _, err := store.Get(ctx, owner, lone.ReservedResultID); err != nil {
			t.Fatalf("the shared result a year later with retention off = %v, want readable", err)
		}
	})

	t.Run("a result no record names (MCP, Workbench) is out of scope and stays", func(t *testing.T) {
		store, clock := setup(t)
		if err := save(ctx, store, "result_retention_unbound"); err != nil {
			t.Fatal(err)
		}
		clock.now = clock.now.Add(400 * 24 * time.Hour)
		if _, err := store.Get(ctx, owner, "result_retention_unbound"); err != nil {
			t.Fatalf("an unbound result a year later = %v", err)
		}
	})

	t.Run("T2: a reserved save whose record is gone is refused and stores nothing", func(t *testing.T) {
		store, clock := setup(t)
		remover, ok := store.(SubmissionRecordRemover)
		if !ok {
			t.Fatal("the store's test adapter must implement SubmissionRecordRemover")
		}
		turn := admit(t, store, clock, "", "fence", nil)
		if err := remover.RemoveSubmissionRecord(turn.Key); err != nil {
			t.Fatal(err)
		}
		err := save(contextfabric.WithSubmissionReservedResultID(ctx, turn.ReservedResultID), store, turn.ReservedResultID)
		if err == nil {
			t.Fatal("a reserved save whose record is gone was stored")
		}
		if _, getErr := store.Get(ctx, owner, turn.ReservedResultID); !errors.Is(getErr, contextfabric.ErrInvestigationResultNotFound) {
			t.Fatalf("after the refused save the id reads %v, want not found (stored unbound)", getErr)
		}
	})
}
