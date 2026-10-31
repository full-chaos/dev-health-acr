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

// ThreadStore is what the thread parity suite drives (CHAOS-11948): the
// result store, its submission store and its thread store, as both adapters
// are one object.
type ThreadStore interface {
	SubmissionStore
	contextfabric.ThreadStore
}

// ThreadTurnCountSeeder sets a thread row's turn count directly, so the
// suite reaches the 1,000,000-turn bound (design §6 T7) without that many
// admissions. Each adapter's test supplies it; a store without it fails the
// suite rather than skipping the case.
type ThreadTurnCountSeeder interface {
	SeedThreadTurnCount(key contextfabric.ThreadKey, count int) error
}

// ThreadClock is the suite's clock: the stores' own (Get's closed-thread
// rule reads it) and the one the suite passes to every method.
type ThreadClock struct{ now time.Time }

// Now returns the clock's time.
func (c *ThreadClock) Now() time.Time { return c.now }

// RunThreadSuite runs the shared thread behavior table against a store, so
// the memory and PostgreSQL adapters cannot disagree about a thread. newStore
// must return an EMPTY store per case whose own clock is clock.Now.
func RunThreadSuite(t *testing.T, newStore func(t *testing.T, clock func() time.Time) ThreadStore) {
	t.Helper()
	start := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	owner := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-thread-owner", OrgID: "org-parity-thread"}
	stranger := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-thread-stranger", OrgID: owner.OrgID}
	ctx := context.Background()
	setup := func(t *testing.T) (ThreadStore, *ThreadClock) {
		t.Helper()
		clock := &ThreadClock{now: start}
		return newStore(t, clock.Now), clock
	}
	month := contractsv1.ContextFabricThreadRetentionMonth
	day := contractsv1.ContextFabricThreadRetentionDay
	turn := func(t *testing.T, store ThreadStore, clock *ThreadClock, principal storage.Principal, threadID, name string, retention *contractsv1.ContextFabricThreadRetentionDays) contextfabric.SubmissionAdmission {
		t.Helper()
		key, err := contextfabric.SubmissionKeyFor(principal, "submission-thread-"+name)
		if err != nil {
			t.Fatal(err)
		}
		admission := contextfabric.SubmissionAdmission{
			Key: key, RequestDigest: "sha256:" + fmt.Sprintf("%064x", 11948), ReservedResultID: "result_thread_" + name,
			ThreadID: threadID, ExpiresAt: clock.now.Add(time.Minute), Question: "question " + name, ThreadRetention: retention,
		}
		record, admitted, err := store.AdmitSubmission(ctx, admission, clock.now)
		if err != nil || !admitted {
			t.Fatalf("admit %s = %+v admitted=%t err=%v", name, record, admitted, err)
		}
		return admission
	}
	save := func(t *testing.T, store ThreadStore, principal storage.Principal, resultID string) {
		t.Helper()
		if err := store.Save(ctx, principal, ValidResult(resultID, "question for "+resultID), nil, nil,
			contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
			contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
			contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)); err != nil {
			t.Fatalf("save %s: %v", resultID, err)
		}
	}
	threadKey := func(t *testing.T, principal storage.Principal, threadID string) contextfabric.ThreadKey {
		t.Helper()
		key, err := contextfabric.ThreadKeyFor(principal, threadID)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	ownerOf := func(t *testing.T, principal storage.Principal) contextfabric.SubmissionOwner {
		t.Helper()
		o, err := contextfabric.SubmissionOwnerOf(principal)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}

	t.Run("turns build the thread row: times, count, retention by the latest naming turn", func(t *testing.T) {
		store, clock := setup(t)
		turn(t, store, clock, owner, "thread-parity-rows", "rows-1", nil)
		clock.now = start.Add(time.Minute)
		turn(t, store, clock, owner, "thread-parity-rows", "rows-2", &month)
		clock.now = start.Add(2 * time.Minute)
		turn(t, store, clock, owner, "thread-parity-rows", "rows-3", nil)
		page, err := store.GetThread(ctx, threadKey(t, owner, "thread-parity-rows"), nil, 100, clock.now)
		if err != nil {
			t.Fatal(err)
		}
		thread := page.Thread
		if thread.TurnCount != 3 || !thread.CreatedAt.Equal(start) || !thread.LastTurnAt.Equal(start.Add(2*time.Minute)) || thread.RetentionDays != month ||
			!thread.ExpiresAt.Equal(start.Add(2*time.Minute).AddDate(0, 0, 30)) || thread.ClosedAt != nil {
			t.Fatalf("thread = %+v", thread)
		}
		if len(page.Turns) != 3 || page.EarlierTurns || page.Turns[0].Question != "question rows-1" || page.Turns[2].SubmissionID != "submission-thread-rows-3" {
			t.Fatalf("turns = %+v earlier=%t", page.Turns, page.EarlierTurns)
		}
		// A turn admitted with an EARLIER clock (another replica's) that
		// names 0 does not override the later turn's 30.
		clock.now = start.Add(30 * time.Second)
		turn(t, store, clock, owner, "thread-parity-rows", "rows-early", &day)
		page, err = store.GetThread(ctx, threadKey(t, owner, "thread-parity-rows"), nil, 100, clock.now)
		if err != nil || page.Thread.RetentionDays != month || page.Thread.TurnCount != 4 || !page.Thread.LastTurnAt.Equal(start.Add(2*time.Minute)) {
			t.Fatalf("after an out-of-order turn: %+v err=%v", page.Thread, err)
		}
		if page.Turns[1].SubmissionID != "submission-thread-rows-early" {
			t.Fatalf("turn order = %+v", page.Turns)
		}
	})

	t.Run("a thread with no naming turn keeps retention 0 and expires 24 hours after its last turn", func(t *testing.T) {
		store, clock := setup(t)
		turn(t, store, clock, owner, "thread-parity-zero", "zero-1", nil)
		key := threadKey(t, owner, "thread-parity-zero")
		page, err := store.GetThread(ctx, key, nil, 100, clock.now)
		if err != nil || page.Thread.RetentionDays != day || !page.Thread.ExpiresAt.Equal(start.Add(24*time.Hour)) {
			t.Fatalf("thread = %+v err=%v", page.Thread, err)
		}
		clock.now = start.Add(24*time.Hour - time.Second)
		if _, err := store.GetThread(ctx, key, nil, 100, clock.now); err != nil {
			t.Fatalf("one second before the expiry: %v", err)
		}
		clock.now = start.Add(24 * time.Hour)
		if _, err := store.GetThread(ctx, key, nil, 100, clock.now); !errors.Is(err, contextfabric.ErrThreadNotFound) {
			t.Fatalf("at the expiry = %v, want not found", err)
		}
		threads, _, err := store.ListThreads(ctx, owner.OrgID, ownerOf(t, owner), nil, 50, clock.now)
		if err != nil || len(threads) != 0 {
			t.Fatalf("list after expiry = %+v err=%v", threads, err)
		}
		next := contextfabric.SubmissionAdmission{}
		key2, _ := contextfabric.SubmissionKeyFor(owner, "submission-thread-zero-late")
		next = contextfabric.SubmissionAdmission{Key: key2, RequestDigest: "sha256:" + fmt.Sprintf("%064x", 11948), ReservedResultID: "result_thread_zero_late", ThreadID: "thread-parity-zero", ExpiresAt: clock.now.Add(time.Minute)}
		var closed *contextfabric.ThreadClosedError
		if _, _, err := store.AdmitSubmission(ctx, next, clock.now); !errors.As(err, &closed) || closed.Reason != contextfabric.ThreadClosedExpired {
			t.Fatalf("a turn of an expired thread = %v, want thread closed (expired)", err)
		}
	})

	t.Run("another principal's same thread id is another thread", func(t *testing.T) {
		store, clock := setup(t)
		turn(t, store, clock, owner, "thread-parity-shared", "shared-owner", nil)
		turn(t, store, clock, stranger, "thread-parity-shared", "shared-stranger", nil)
		page, err := store.GetThread(ctx, threadKey(t, owner, "thread-parity-shared"), nil, 100, clock.now)
		if err != nil || page.Thread.TurnCount != 1 || len(page.Turns) != 1 || page.Turns[0].SubmissionID != "submission-thread-shared-owner" {
			t.Fatalf("owner's thread = %+v err=%v", page, err)
		}
		if closed, err := store.CloseThread(ctx, threadKey(t, stranger, "thread-parity-shared"), clock.now); err != nil || !closed {
			t.Fatalf("stranger closes its own = %t %v", closed, err)
		}
		if _, err := store.GetThread(ctx, threadKey(t, owner, "thread-parity-shared"), nil, 100, clock.now); err != nil {
			t.Fatalf("the stranger's delete closed the owner's thread: %v", err)
		}
	})

	// CHAOS-12148: the organization is part of the thread key. A principal of
	// another organization with the owner's method, subject and thread id
	// never joins, reads, lists or closes the owner's thread.
	t.Run("another organization's principal with the same method, subject and thread id is another thread", func(t *testing.T) {
		store, clock := setup(t)
		otherOrg := storage.Principal{AuthenticationMethod: owner.AuthenticationMethod, Subject: owner.Subject, OrgID: owner.OrgID + "-other"}
		turn(t, store, clock, owner, "thread-parity-cross-org", "cross-org-owner", &month)
		clock.now = clock.now.Add(time.Second)
		turn(t, store, clock, otherOrg, "thread-parity-cross-org", "cross-org-other", nil)
		// The owner's thread holds its own turn only, with its question and
		// its retention.
		page, err := store.GetThread(ctx, threadKey(t, owner, "thread-parity-cross-org"), nil, 100, clock.now)
		if err != nil || page.Thread.TurnCount != 1 || len(page.Turns) != 1 || page.Turns[0].SubmissionID != "submission-thread-cross-org-owner" ||
			page.Turns[0].Question != "question cross-org-owner" || page.Thread.RetentionDays != month {
			t.Fatalf("owner's thread = %+v err=%v", page, err)
		}
		// The other organization's principal has its own thread: one turn, its own.
		other, err := store.GetThread(ctx, threadKey(t, otherOrg, "thread-parity-cross-org"), nil, 100, clock.now)
		if err != nil || other.Thread.TurnCount != 1 || len(other.Turns) != 1 || other.Turns[0].SubmissionID != "submission-thread-cross-org-other" || other.Thread.RetentionDays != day {
			t.Fatalf("other organization's thread = %+v err=%v", other, err)
		}
		// Each list names its own thread only.
		for _, reader := range []storage.Principal{owner, otherOrg} {
			threads, _, err := store.ListThreads(ctx, reader.OrgID, ownerOf(t, reader), nil, 10, clock.now)
			if err != nil || len(threads) != 1 || threads[0].Key.OrgID != reader.OrgID || threads[0].TurnCount != 1 {
				t.Fatalf("list for %s = %+v err=%v", reader.OrgID, threads, err)
			}
		}
		// The other organization's delete closes its own thread only: the
		// owner's stays open and keeps its question.
		if closed, err := store.CloseThread(ctx, threadKey(t, otherOrg, "thread-parity-cross-org"), clock.now); err != nil || !closed {
			t.Fatalf("the other organization closes its own = %t %v", closed, err)
		}
		page, err = store.GetThread(ctx, threadKey(t, owner, "thread-parity-cross-org"), nil, 100, clock.now)
		if err != nil || page.Thread.ClosedAt != nil || len(page.Turns) != 1 || page.Turns[0].Question != "question cross-org-owner" {
			t.Fatalf("the other organization's delete reached the owner's thread: %+v err=%v", page, err)
		}
		// And the owner's delete leaves the other organization's (already
		// closed) thread as it was, without reopening or touching it.
		if closed, err := store.CloseThread(ctx, threadKey(t, owner, "thread-parity-cross-org"), clock.now); err != nil || !closed {
			t.Fatalf("owner closes its own = %t %v", closed, err)
		}
		if closed, err := store.CloseThread(ctx, threadKey(t, otherOrg, "thread-parity-cross-org"), clock.now); err != nil || closed {
			t.Fatalf("a second close by the other organization = %t %v, want nothing to close", closed, err)
		}
	})

	t.Run("pages hold the latest turns and the cursor reads the earlier ones", func(t *testing.T) {
		store, clock := setup(t)
		for i := range 5 {
			clock.now = start.Add(time.Duration(i) * time.Minute)
			turn(t, store, clock, owner, "thread-parity-pages", fmt.Sprintf("page-%d", i), nil)
		}
		key := threadKey(t, owner, "thread-parity-pages")
		page, err := store.GetThread(ctx, key, nil, 2, clock.now)
		if err != nil || len(page.Turns) != 2 || !page.EarlierTurns || page.Turns[0].SubmissionID != "submission-thread-page-3" || page.Turns[1].SubmissionID != "submission-thread-page-4" {
			t.Fatalf("latest page = %+v err=%v", page, err)
		}
		cursor := contextfabric.ThreadTurnCursor{AdmittedAt: page.Turns[0].AdmittedAt, SubmissionID: page.Turns[0].SubmissionID}
		page, err = store.GetThread(ctx, key, &cursor, 2, clock.now)
		if err != nil || len(page.Turns) != 2 || !page.EarlierTurns || page.Turns[0].SubmissionID != "submission-thread-page-1" {
			t.Fatalf("second page = %+v err=%v", page, err)
		}
		cursor = contextfabric.ThreadTurnCursor{AdmittedAt: page.Turns[0].AdmittedAt, SubmissionID: page.Turns[0].SubmissionID}
		page, err = store.GetThread(ctx, key, &cursor, 2, clock.now)
		if err != nil || len(page.Turns) != 1 || page.EarlierTurns || page.Turns[0].SubmissionID != "submission-thread-page-0" {
			t.Fatalf("first page = %+v err=%v", page, err)
		}
	})

	t.Run("the list holds open threads, newest first, in pages", func(t *testing.T) {
		store, clock := setup(t)
		for i, id := range []string{"thread-parity-list-a", "thread-parity-list-b", "thread-parity-list-c"} {
			clock.now = start.Add(time.Duration(i) * time.Minute)
			turn(t, store, clock, owner, id, fmt.Sprintf("list-%d", i), nil)
		}
		turn(t, store, clock, stranger, "thread-parity-list-s", "list-stranger", nil)
		if closed, err := store.CloseThread(ctx, threadKey(t, owner, "thread-parity-list-b"), clock.now); err != nil || !closed {
			t.Fatalf("close b = %t %v", closed, err)
		}
		threads, more, err := store.ListThreads(ctx, owner.OrgID, ownerOf(t, owner), nil, 1, clock.now)
		if err != nil || !more || len(threads) != 1 || threads[0].Key.ThreadID != "thread-parity-list-c" {
			t.Fatalf("first page = %+v more=%t err=%v", threads, more, err)
		}
		after := contextfabric.ThreadListCursor{LastTurnAt: threads[0].LastTurnAt, ThreadID: threads[0].Key.ThreadID}
		threads, more, err = store.ListThreads(ctx, owner.OrgID, ownerOf(t, owner), &after, 1, clock.now)
		if err != nil || more || len(threads) != 1 || threads[0].Key.ThreadID != "thread-parity-list-a" {
			t.Fatalf("second page = %+v more=%t err=%v", threads, more, err)
		}
	})

	// CHAOS-12235: each list item names the question of its thread's TRUE
	// first turn, in turn order (admitted_at, then submission id), whatever
	// a thread read's page serves; absent when that turn recorded none; and
	// another principal's turns never reach it.
	t.Run("the list names each thread's first question from its true first turn", func(t *testing.T) {
		listedIn := func(t *testing.T, store ThreadStore, clock *ThreadClock, principal storage.Principal) map[string]string {
			t.Helper()
			threads, _, err := store.ListThreads(ctx, principal.OrgID, ownerOf(t, principal), nil, contractsv1.ContextFabricThreadListMaxLimit, clock.now)
			if err != nil {
				t.Fatal(err)
			}
			out := map[string]string{}
			for _, thread := range threads {
				if _, twice := out[thread.Key.ThreadID]; twice {
					t.Fatalf("thread %s is listed twice", thread.Key.ThreadID)
				}
				out[thread.Key.ThreadID] = thread.FirstQuestion
			}
			return out
		}
		// A long thread: more turns than one read serves (its own store, so
		// its running turns do not meet the principal's admission limit).
		longStore, longClock := setup(t)
		long := contractsv1.ContextFabricThreadMaxServedTurns + 5
		for i := range long {
			longClock.now = start.Add(time.Duration(i+1) * time.Minute)
			turn(t, longStore, longClock, owner, "thread-parity-first-long", fmt.Sprintf("first-long-%03d", i), nil)
		}
		page, err := longStore.GetThread(ctx, threadKey(t, owner, "thread-parity-first-long"), nil, contractsv1.ContextFabricThreadMaxServedTurns, longClock.now)
		if err != nil || !page.EarlierTurns || page.Turns[0].Question == "question first-long-000" {
			t.Fatalf("fixture: the latest page must not hold the first turn: %+v err=%v", page.Turns[:1], err)
		}
		if got := listedIn(t, longStore, longClock, owner)["thread-parity-first-long"]; got != "question first-long-000" {
			t.Errorf("long thread first question = %q, want the true first turn's, not the served page's", got)
		}

		store, clock := setup(t)
		listed := func(t *testing.T, principal storage.Principal) map[string]string {
			t.Helper()
			return listedIn(t, store, clock, principal)
		}
		// Turn order is admitted_at, not admission order nor submission id: a
		// turn admitted later with an earlier clock (another replica's), and
		// with the larger submission id, is the first.
		clock.now = start.Add(2 * time.Minute)
		turn(t, store, clock, owner, "thread-parity-first-order", "first-order-a-later-clock", nil)
		clock.now = start.Add(time.Minute)
		turn(t, store, clock, owner, "thread-parity-first-order", "first-order-z-earlier-clock", nil)
		// A first turn that recorded no question: the field is absent, never
		// the next turn's question.
		clock.now = start.Add(3 * time.Minute)
		key, _ := contextfabric.SubmissionKeyFor(owner, "submission-thread-first-legacy-1")
		if _, admitted, err := store.AdmitSubmission(ctx, contextfabric.SubmissionAdmission{
			Key: key, RequestDigest: "sha256:" + fmt.Sprintf("%064x", 12235), ReservedResultID: "result_thread_first_legacy_1",
			ThreadID: "thread-parity-first-legacy", ExpiresAt: clock.now.Add(time.Minute),
		}, clock.now); err != nil || !admitted {
			t.Fatalf("admit a turn with no question: admitted=%t err=%v", admitted, err)
		}
		clock.now = start.Add(4 * time.Minute)
		turn(t, store, clock, owner, "thread-parity-first-legacy", "first-legacy-2", nil)
		// The same thread id under another principal is another thread.
		clock.now = start.Add(5 * time.Minute)
		turn(t, store, clock, stranger, "thread-parity-first-order", "first-stranger", nil)
		clock.now = start.Add(10 * time.Minute)

		got := listed(t, owner)
		want := map[string]string{
			"thread-parity-first-order":  "question first-order-z-earlier-clock",
			"thread-parity-first-legacy": "",
		}
		for id, question := range want {
			if q, ok := got[id]; !ok || q != question {
				t.Errorf("%s first question = %q (listed %t), want %q", id, q, ok, question)
			}
		}
		if got := listed(t, stranger); got["thread-parity-first-order"] != "question first-stranger" {
			t.Errorf("the other principal's thread first question = %q, want its own first turn's", got["thread-parity-first-order"])
		}

		// A deleted thread is not listed, and its turns' questions are gone.
		if closed, err := store.CloseThread(ctx, threadKey(t, owner, "thread-parity-first-order"), clock.now); err != nil || !closed {
			t.Fatalf("close = %t %v", closed, err)
		}
		if q, ok := listed(t, owner)["thread-parity-first-order"]; ok {
			t.Errorf("a deleted thread is listed with first question %q", q)
		}
		record, err := store.GetSubmission(ctx, contextfabric.SubmissionKey{OrgID: owner.OrgID, Owner: ownerOf(t, owner), SubmissionID: "submission-thread-first-order-z-earlier-clock"}, clock.now)
		if err != nil || record.Question != "" {
			t.Errorf("the deleted thread's first turn = %+v err=%v, want its question erased", record, err)
		}
		if got := listed(t, stranger); got["thread-parity-first-order"] != "question first-stranger" {
			t.Errorf("deleting one principal's thread changed the other's first question to %q", got["thread-parity-first-order"])
		}
	})

	t.Run("delete closes the thread, erases its questions and refuses its turns", func(t *testing.T) {
		store, clock := setup(t)
		first := turn(t, store, clock, owner, "thread-parity-delete", "delete-1", &month)
		key := threadKey(t, owner, "thread-parity-delete")
		if closed, err := store.CloseThread(ctx, key, clock.now); err != nil || !closed {
			t.Fatalf("close = %t %v", closed, err)
		}
		if closed, err := store.CloseThread(ctx, key, clock.now); err != nil || closed {
			t.Fatalf("a second close = %t %v, want nothing to close", closed, err)
		}
		if _, err := store.GetThread(ctx, key, nil, 100, clock.now); !errors.Is(err, contextfabric.ErrThreadNotFound) {
			t.Fatalf("read after delete = %v", err)
		}
		record, err := store.GetSubmission(ctx, first.Key, clock.now)
		if err != nil || record.Question != "" || record.ThreadClosed != contextfabric.ThreadClosedDeleted {
			t.Fatalf("the turn's record = %+v err=%v, want its question erased and its thread closed", record, err)
		}
		// A repeat of the turn's key reads the record (the API refuses it).
		again, admitted, err := store.AdmitSubmission(ctx, first, clock.now)
		if err != nil || admitted || again.ThreadClosed != contextfabric.ThreadClosedDeleted {
			t.Fatalf("repeat = %+v admitted=%t err=%v", again, admitted, err)
		}
		// A new turn naming the deleted thread is refused, and writes nothing.
		key2, _ := contextfabric.SubmissionKeyFor(owner, "submission-thread-delete-2")
		next := contextfabric.SubmissionAdmission{Key: key2, RequestDigest: first.RequestDigest, ReservedResultID: "result_thread_delete_2", ThreadID: "thread-parity-delete", ExpiresAt: clock.now.Add(time.Minute)}
		var closed *contextfabric.ThreadClosedError
		if _, _, err := store.AdmitSubmission(ctx, next, clock.now); !errors.As(err, &closed) || closed.Reason != contextfabric.ThreadClosedDeleted {
			t.Fatalf("a new turn of a deleted thread = %v", err)
		}
		if _, err := store.GetSubmission(ctx, key2, clock.now); !errors.Is(err, contextfabric.ErrSubmissionNotFound) {
			t.Fatalf("the refused turn was written: %v", err)
		}
		// Closing an unknown thread writes nothing.
		if closed, err := store.CloseThread(ctx, threadKey(t, owner, "thread-parity-unknown"), clock.now); err != nil || closed {
			t.Fatalf("close unknown = %t %v", closed, err)
		}
	})

	t.Run("a result only closed turns name is not served; one a live record names is", func(t *testing.T) {
		store, clock := setup(t)
		a := turn(t, store, clock, owner, "thread-parity-hide-a", "hide-a", &month)
		save(t, store, owner, a.ReservedResultID)
		b := turn(t, store, clock, owner, "thread-parity-hide-b", "hide-b", &month)
		// Turn b served a's stored result (answer reuse): b's record names it.
		if record, completion, err := store.FinishSubmission(ctx, b.Key, a.ReservedResultID, clock.now); err != nil || record.ResultID != a.ReservedResultID || completion != contextfabric.SubmissionCompletionServedStored {
			t.Fatalf("finish b with a's result = %+v %s %v", record, completion, err)
		}
		if closed, err := store.CloseThread(ctx, threadKey(t, owner, "thread-parity-hide-a"), clock.now); err != nil || !closed {
			t.Fatalf("close a = %t %v", closed, err)
		}
		if _, err := store.Get(ctx, owner, a.ReservedResultID); err != nil {
			t.Fatalf("a result a live thread's turn names is hidden: %v", err)
		}
		if closed, err := store.CloseThread(ctx, threadKey(t, owner, "thread-parity-hide-b"), clock.now); err != nil || !closed {
			t.Fatalf("close b = %t %v", closed, err)
		}
		if _, err := store.Get(ctx, owner, a.ReservedResultID); !errors.Is(err, contextfabric.ErrInvestigationResultNotFound) {
			t.Fatalf("a result only closed turns name = %v, want not found", err)
		}
		// A result no thread turn names is unaffected.
		save(t, store, owner, "result_thread_unthreaded")
		if _, err := store.Get(ctx, owner, "result_thread_unthreaded"); err != nil {
			t.Fatalf("an unthreaded result: %v", err)
		}
	})

	// CHAOS-12091 on threads: the owner is the approving user on a surface.
	credential := func(id, user string, class storage.CredentialClientClass) storage.Principal {
		return storage.Principal{AuthenticationMethod: storage.AuthenticationMethodCredential, Subject: id, OrgID: owner.OrgID, ApprovingUserID: user, ClientClass: class}
	}
	t.Run("another credential of the same user on the same surface reaches the user's thread", func(t *testing.T) {
		store, clock := setup(t)
		// The web session of the user, then a browser credential the user
		// approved: another legacy pair (method and subject), one canonical
		// owner (the user on the browser).
		before := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-thread-rotating", OrgID: owner.OrgID}
		after := credential("cred-thread-rot-b", "user-thread-rotating", storage.CredentialClientClassBrowser)
		first := turn(t, store, clock, before, "thread-parity-rotate", "rotate-1", nil)
		clock.now = clock.now.Add(time.Second)
		second := turn(t, store, clock, after, "thread-parity-rotate", "rotate-2", &month)
		save(t, store, after, second.ReservedResultID)
		for _, reader := range []storage.Principal{before, after} {
			page, err := store.GetThread(ctx, threadKey(t, reader, "thread-parity-rotate"), nil, 100, clock.now)
			if err != nil || page.Thread.TurnCount != 2 || len(page.Turns) != 2 || page.Thread.RetentionDays != month ||
				page.Turns[0].SubmissionID != first.Key.SubmissionID || page.Turns[1].SubmissionID != second.Key.SubmissionID {
				t.Fatalf("read by %s = %+v err=%v", reader.Subject, page, err)
			}
			threads, _, err := store.ListThreads(ctx, owner.OrgID, ownerOf(t, reader), nil, 10, clock.now)
			if err != nil || len(threads) != 1 || threads[0].TurnCount != 2 {
				t.Fatalf("list by %s = %+v err=%v", reader.Subject, threads, err)
			}
		}
		if closed, err := store.CloseThread(ctx, threadKey(t, after, "thread-parity-rotate"), clock.now); err != nil || !closed {
			t.Fatalf("close by the rotated credential = %t %v", closed, err)
		}
		if _, err := store.GetThread(ctx, threadKey(t, before, "thread-parity-rotate"), nil, 100, clock.now); !errors.Is(err, contextfabric.ErrThreadNotFound) {
			t.Fatalf("read by the earlier credential after the delete = %v", err)
		}
		// T6: the rotated credential's turn belongs to the closed thread, so
		// its result is hidden too.
		if _, err := store.Get(ctx, after, second.ReservedResultID); !errors.Is(err, contextfabric.ErrInvestigationResultNotFound) {
			t.Fatalf("the rotated turn's result after the delete = %v, want not found", err)
		}
		for _, admitted := range []contextfabric.SubmissionAdmission{first, second} {
			record, err := store.GetSubmission(ctx, admitted.Key, clock.now)
			if err != nil || record.Question != "" || record.ThreadClosed != contextfabric.ThreadClosedDeleted {
				t.Fatalf("turn %s after the delete = %+v err=%v", admitted.Key.SubmissionID, record, err)
			}
		}
	})

	t.Run("a user's MCP credential and browser are different threads (decision 5b)", func(t *testing.T) {
		store, clock := setup(t)
		browser := credential("cred-thread-5b-browser", "user-thread-5b", storage.CredentialClientClassBrowser)
		mcp := credential("cred-thread-5b-mcp", "user-thread-5b", storage.CredentialClientClassOAuthUser)
		turn(t, store, clock, browser, "thread-parity-5b", "5b-browser", nil)
		turn(t, store, clock, mcp, "thread-parity-5b", "5b-mcp", nil)
		for _, tc := range []struct {
			reader storage.Principal
			want   string
		}{{browser, "submission-thread-5b-browser"}, {mcp, "submission-thread-5b-mcp"}} {
			page, err := store.GetThread(ctx, threadKey(t, tc.reader, "thread-parity-5b"), nil, 100, clock.now)
			if err != nil || page.Thread.TurnCount != 1 || len(page.Turns) != 1 || page.Turns[0].SubmissionID != tc.want {
				t.Fatalf("read by %s = %+v err=%v", tc.reader.Subject, page, err)
			}
		}
		if closed, err := store.CloseThread(ctx, threadKey(t, mcp, "thread-parity-5b"), clock.now); err != nil || !closed {
			t.Fatalf("close by MCP = %t %v", closed, err)
		}
		page, err := store.GetThread(ctx, threadKey(t, browser, "thread-parity-5b"), nil, 100, clock.now)
		if err != nil || len(page.Turns) != 1 || page.Turns[0].Question != "question 5b-browser" {
			t.Fatalf("the browser thread after the MCP delete = %+v err=%v", page, err)
		}
	})

	t.Run("a full thread refuses new turns only: its turns, results and repeats are served", func(t *testing.T) {
		store, clock := setup(t)
		seeder, ok := store.(ThreadTurnCountSeeder)
		if !ok {
			t.Fatalf("the store %T cannot seed a turn count: the full-thread case cannot run", store)
		}
		first := turn(t, store, clock, owner, "thread-parity-full", "full-1", &month)
		save(t, store, owner, first.ReservedResultID)
		key := threadKey(t, owner, "thread-parity-full")
		if err := seeder.SeedThreadTurnCount(key, contractsv1.ContextFabricThreadMaxTurnCount); err != nil {
			t.Fatalf("seed the turn count: %v", err)
		}
		// T7: a new turn is refused as full, before any write.
		key2, _ := contextfabric.SubmissionKeyFor(owner, "submission-thread-full-2")
		next := contextfabric.SubmissionAdmission{Key: key2, RequestDigest: first.RequestDigest, ReservedResultID: "result_thread_full_2", ThreadID: "thread-parity-full", ExpiresAt: clock.now.Add(time.Minute), Question: "one too many"}
		var closed *contextfabric.ThreadClosedError
		if _, _, err := store.AdmitSubmission(ctx, next, clock.now); !errors.As(err, &closed) || closed.Reason != contextfabric.ThreadClosedFull {
			t.Fatalf("a turn past the bound = %v, want thread_closed full", err)
		}
		if _, err := store.GetSubmission(ctx, key2, clock.now); !errors.Is(err, contextfabric.ErrSubmissionNotFound) {
			t.Fatalf("the refused turn was written: %v", err)
		}
		// T6 names only delete and expiry: a full thread is not closed.
		page, err := store.GetThread(ctx, key, nil, 100, clock.now)
		if err != nil || page.Thread.TurnCount != contractsv1.ContextFabricThreadMaxTurnCount || len(page.Turns) != 1 || page.Turns[0].Question != "question full-1" {
			t.Fatalf("read of a full thread = %+v err=%v", page, err)
		}
		if threads, _, err := store.ListThreads(ctx, owner.OrgID, ownerOf(t, owner), nil, 10, clock.now); err != nil || len(threads) != 1 || threads[0].Key.ThreadID != "thread-parity-full" {
			t.Fatalf("list with a full thread = %+v err=%v", threads, err)
		}
		if _, err := store.Get(ctx, owner, first.ReservedResultID); err != nil {
			t.Fatalf("a full thread's result is hidden: %v", err)
		}
		record, err := store.GetSubmission(ctx, first.Key, clock.now)
		if err != nil || record.ThreadClosed != "" || record.Question != "question full-1" {
			t.Fatalf("a full thread's turn record = %+v err=%v", record, err)
		}
		again, admitted, err := store.AdmitSubmission(ctx, first, clock.now)
		if err != nil || admitted || again.ThreadClosed != "" {
			t.Fatalf("a repeat of a full thread's key = %+v admitted=%t err=%v, want a replay", again, admitted, err)
		}
	})

	t.Run("a result a submission with no thread names stays served after the thread closes", func(t *testing.T) {
		store, clock := setup(t)
		a := turn(t, store, clock, owner, "thread-parity-unthreaded", "unthreaded-a", &month)
		save(t, store, owner, a.ReservedResultID)
		// A submission outside any conversation served a's stored result.
		plain := turn(t, store, clock, owner, "", "unthreaded-plain", nil)
		if record, completion, err := store.FinishSubmission(ctx, plain.Key, a.ReservedResultID, clock.now); err != nil || record.ResultID != a.ReservedResultID || completion != contextfabric.SubmissionCompletionServedStored {
			t.Fatalf("finish the plain submission with a's result = %+v %s %v", record, completion, err)
		}
		if closed, err := store.CloseThread(ctx, threadKey(t, owner, "thread-parity-unthreaded"), clock.now); err != nil || !closed {
			t.Fatalf("close = %t %v", closed, err)
		}
		if _, err := store.Get(ctx, owner, a.ReservedResultID); err != nil {
			t.Fatalf("a result a submission with no thread names is hidden: %v", err)
		}
	})

	t.Run("an expired thread hides its results too", func(t *testing.T) {
		store, clock := setup(t)
		a := turn(t, store, clock, owner, "thread-parity-expire", "expire-1", nil)
		save(t, store, owner, a.ReservedResultID)
		if _, err := store.Get(ctx, owner, a.ReservedResultID); err != nil {
			t.Fatalf("before the expiry: %v", err)
		}
		clock.now = start.Add(24 * time.Hour)
		if _, err := store.Get(ctx, owner, a.ReservedResultID); !errors.Is(err, contextfabric.ErrInvestigationResultNotFound) {
			t.Fatalf("after the expiry = %v, want not found", err)
		}
		if record, err := store.GetSubmission(ctx, a.Key, clock.now); err != nil || record.ThreadClosed != contextfabric.ThreadClosedExpired {
			t.Fatalf("record after expiry = %+v err=%v", record, err)
		}
	})

	t.Run("a save under a deleted thread's reservation still binds the owner", func(t *testing.T) {
		store, clock := setup(t)
		a := turn(t, store, clock, owner, "thread-parity-fence", "fence-1", &month)
		if closed, err := store.CloseThread(ctx, threadKey(t, owner, "thread-parity-fence"), clock.now); err != nil || !closed {
			t.Fatalf("close = %t %v", closed, err)
		}
		// The run was in flight; its save lands after the delete (T2).
		save(t, store, owner, a.ReservedResultID)
		record, err := store.GetSubmission(ctx, a.Key, clock.now)
		if err != nil || record.State != contextfabric.SubmissionDone {
			t.Fatalf("record = %+v err=%v, want done", record, err)
		}
		// Bound, and hidden: the stranger and the owner both get not found.
		for _, reader := range []storage.Principal{owner, stranger} {
			if _, err := store.Get(ctx, reader, a.ReservedResultID); !errors.Is(err, contextfabric.ErrInvestigationResultNotFound) {
				t.Fatalf("%s reads a deleted thread's late result: %v", reader.Subject, err)
			}
		}
	})
}
