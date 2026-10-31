package paritytest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// SubmissionStore is what the submission parity suite drives (CHAOS-11912):
// one object that is the result store, its submission store and the engine's
// unsaved marker, as both adapters are.
type SubmissionStore interface {
	contextfabric.InvestigationResultStore
	contextfabric.SubmissionStore
	contextfabric.SubmissionUnsavedMarker
}

// RunSubmissionSuite runs the shared submission behavior table against a
// store, so the memory and PostgreSQL adapters cannot disagree about a state
// transition. newStore must return an EMPTY store per case (a fresh
// database for PostgreSQL): the sweep case counts exactly.
//
// newStore receives the suite's clock: the stores' own reads (Get's
// closed-record rule, CHAOS-11952: a record with no thread closes 24 hours
// after its admission) must run on the same time as the records it admits.
func RunSubmissionSuite(t *testing.T, newStore func(t *testing.T, clock func() time.Time) SubmissionStore) {
	t.Helper()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	owner := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-parity-owner", OrgID: "org-parity-submission"}
	stranger := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-parity-stranger", OrgID: owner.OrgID}
	digest := "sha256:" + fmt.Sprintf("%064x", 11912)
	admit := func(t *testing.T, store SubmissionStore, name string) contextfabric.SubmissionAdmission {
		t.Helper()
		key, err := contextfabric.SubmissionKeyFor(owner, "submission-parity-"+name)
		if err != nil {
			t.Fatal(err)
		}
		admission := contextfabric.SubmissionAdmission{Key: key, RequestDigest: digest, ReservedResultID: "result_parity_" + name, ExpiresAt: now.Add(time.Minute)}
		record, admitted, err := store.AdmitSubmission(context.Background(), admission, now)
		if err != nil || !admitted || record.State != contextfabric.SubmissionRunning || record.ReservedResultID != admission.ReservedResultID {
			t.Fatalf("admit %s = %+v admitted=%t err=%v", name, record, admitted, err)
		}
		return admission
	}
	save := func(t *testing.T, store SubmissionStore, principal storage.Principal, resultID string) {
		t.Helper()
		if err := store.Save(context.Background(), principal, ValidResult(resultID, "question for "+resultID), nil, nil,
			contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
			contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
			contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)); err != nil {
			t.Fatalf("save %s: %v", resultID, err)
		}
	}
	state := func(t *testing.T, store SubmissionStore, key contextfabric.SubmissionKey, at time.Time) contextfabric.SubmissionRecord {
		t.Helper()
		record, err := store.GetSubmission(context.Background(), key, at)
		if err != nil {
			t.Fatalf("get %s: %v", key.SubmissionID, err)
		}
		return record
	}

	t.Run("a repeat admission reads the first record", func(t *testing.T) {
		store := newStore(t, func() time.Time { return now })
		first := admit(t, store, "repeat")
		again := first
		again.ReservedResultID = "result_parity_repeat_other"
		again.RequestDigest = "sha256:" + fmt.Sprintf("%064x", 1)
		record, admitted, err := store.AdmitSubmission(context.Background(), again, now)
		if err != nil || admitted || record.ReservedResultID != first.ReservedResultID || record.RequestDigest != first.RequestDigest {
			t.Fatalf("repeat = %+v admitted=%t err=%v, want the first record", record, admitted, err)
		}
	})

	t.Run("the thread is stored and a repeat keeps the first", func(t *testing.T) {
		store := newStore(t, func() time.Time { return now })
		key, err := contextfabric.SubmissionKeyFor(owner, "submission-parity-thread")
		if err != nil {
			t.Fatal(err)
		}
		admission := contextfabric.SubmissionAdmission{Key: key, RequestDigest: digest, ReservedResultID: "result_parity_thread", ThreadID: "thread-parity-11913", ExpiresAt: now.Add(time.Minute)}
		if record, admitted, err := store.AdmitSubmission(context.Background(), admission, now); err != nil || !admitted || record.ThreadID != "thread-parity-11913" {
			t.Fatalf("admit = %+v admitted=%t err=%v", record, admitted, err)
		}
		again := admission
		again.ThreadID = "thread-parity-other"
		if record, admitted, err := store.AdmitSubmission(context.Background(), again, now); err != nil || admitted || record.ThreadID != "thread-parity-11913" {
			t.Fatalf("repeat = %+v admitted=%t err=%v, want the first thread", record, admitted, err)
		}
		if record := state(t, store, key, now); record.ThreadID != "thread-parity-11913" {
			t.Fatalf("read thread = %q", record.ThreadID)
		}
		bad := admission
		bad.Key.SubmissionID, bad.ReservedResultID, bad.ThreadID = "submission-parity-badthread", "result_parity_badthread", "bad thread"
		if _, _, err := store.AdmitSubmission(context.Background(), bad, now); err == nil {
			t.Fatal("a malformed thread id was admitted")
		}
		none := admission
		none.Key.SubmissionID, none.ReservedResultID, none.ThreadID = "submission-parity-nothread", "result_parity_nothread", ""
		if record, _, err := store.AdmitSubmission(context.Background(), none, now); err != nil || record.ThreadID != "" {
			t.Fatalf("no thread = %+v err=%v", record, err)
		}
	})

	t.Run("save completes the record and binds the owner", func(t *testing.T) {
		store := newStore(t, func() time.Time { return now })
		admission := admit(t, store, "save")
		save(t, store, owner, admission.ReservedResultID)
		record := state(t, store, admission.Key, now)
		if record.State != contextfabric.SubmissionDone || record.ResultID != admission.ReservedResultID {
			t.Fatalf("record = %+v, want done with the reserved id", record)
		}
		stored, err := store.Get(context.Background(), owner, admission.ReservedResultID)
		if err != nil || stored.SubmissionOwner == nil || !stored.SubmissionOwner.Is(owner) || stored.SubmissionOwner.Is(stranger) {
			t.Fatalf("stored owner = %+v err=%v", stored.SubmissionOwner, err)
		}
		save(t, store, owner, "result_parity_unbound")
		if unbound, err := store.Get(context.Background(), owner, "result_parity_unbound"); err != nil || unbound.SubmissionOwner != nil {
			t.Fatalf("a save with no reservation is bound: %+v err=%v", unbound.SubmissionOwner, err)
		}
	})

	t.Run("expiry abandons and a late save still binds and completes", func(t *testing.T) {
		store := newStore(t, func() time.Time { return now })
		admission := admit(t, store, "expiry")
		if record := state(t, store, admission.Key, now.Add(30*time.Second)); record.State != contextfabric.SubmissionRunning {
			t.Fatalf("before expiry = %s", record.State)
		}
		later := now.Add(2 * time.Minute)
		if record := state(t, store, admission.Key, later); record.State != contextfabric.SubmissionAbandoned {
			t.Fatalf("after expiry = %s, want abandoned", record.State)
		}
		save(t, store, owner, admission.ReservedResultID)
		stored, err := store.Get(context.Background(), owner, admission.ReservedResultID)
		if err != nil || stored.SubmissionOwner == nil {
			t.Fatalf("late save unbound: %+v err=%v", stored.SubmissionOwner, err)
		}
		if record := state(t, store, admission.Key, later); record.State != contextfabric.SubmissionDone || record.ResultID != admission.ReservedResultID {
			t.Fatalf("after the late save = %+v, want done", record)
		}
	})

	t.Run("finish: served stored, served unsaved, already final", func(t *testing.T) {
		store := newStore(t, func() time.Time { return now })
		// A reused answer the owner's own earlier submission saved.
		earlier := admit(t, store, "finish-earlier")
		save(t, store, owner, earlier.ReservedResultID)
		reused := admit(t, store, "finish-reused")
		record, completion, err := store.FinishSubmission(context.Background(), reused.Key, earlier.ReservedResultID, now)
		if err != nil || completion != contextfabric.SubmissionCompletionServedStored || record.State != contextfabric.SubmissionDone || record.ResultID != earlier.ReservedResultID {
			t.Fatalf("reused = %+v %s %v", record, completion, err)
		}
		unsaved := admit(t, store, "finish-unsaved")
		record, completion, err = store.FinishSubmission(context.Background(), unsaved.Key, unsaved.ReservedResultID, now)
		if err != nil || completion != contextfabric.SubmissionCompletionServedUnsaved || record.State != contextfabric.SubmissionDoneUnsaved || record.ResultID != "" {
			t.Fatalf("unsaved = %+v %s %v", record, completion, err)
		}
		saved := admit(t, store, "finish-saved")
		save(t, store, owner, saved.ReservedResultID)
		record, completion, err = store.FinishSubmission(context.Background(), saved.Key, saved.ReservedResultID, now)
		if err != nil || completion != contextfabric.SubmissionCompletionAlreadyFinal || record.State != contextfabric.SubmissionDone {
			t.Fatalf("saved = %+v %s %v", record, completion, err)
		}
		abandoned := admit(t, store, "finish-abandoned")
		if _, err := store.AbandonSubmission(context.Background(), abandoned.Key, now); err != nil {
			t.Fatal(err)
		}
		record, completion, err = store.FinishSubmission(context.Background(), abandoned.Key, abandoned.ReservedResultID, now)
		if err != nil || completion != contextfabric.SubmissionCompletionNotRunning || record.State != contextfabric.SubmissionAbandoned {
			t.Fatalf("abandoned = %+v %s %v", record, completion, err)
		}
	})

	// Round-2 F1: a done record names a result bound to the record's own
	// owner, never another principal's result and never an unbound one.
	t.Run("a record is done only with a result its owner owns", func(t *testing.T) {
		store := newStore(t, func() time.Time { return now })
		strangerKey, err := contextfabric.SubmissionKeyFor(stranger, "submission-parity-strangers")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.AdmitSubmission(context.Background(), contextfabric.SubmissionAdmission{Key: strangerKey, RequestDigest: digest, ReservedResultID: "result_parity_strangers", ExpiresAt: now.Add(time.Minute)}, now); err != nil {
			t.Fatal(err)
		}
		save(t, store, stranger, "result_parity_strangers")
		save(t, store, owner, "result_parity_plain")
		for served, name := range map[string]string{"result_parity_strangers": "another principal's result", "result_parity_plain": "an unbound result"} {
			admission := admit(t, store, "foreign-"+served[len("result_parity_"):])
			record, completion, err := store.FinishSubmission(context.Background(), admission.Key, served, now)
			if err != nil || completion != contextfabric.SubmissionCompletionServedUnsaved || record.State != contextfabric.SubmissionDoneUnsaved || record.ResultID != "" {
				t.Fatalf("finish with %s = %+v %s %v, want done_unsaved", name, record, completion, err)
			}
		}
		// A reserved id another principal saved under (unbound to this
		// record's owner) does not complete the record on read; past its
		// expiry the record is abandoned.
		hijacked := admit(t, store, "hijacked")
		save(t, store, stranger, hijacked.ReservedResultID)
		if record := state(t, store, hijacked.Key, now); record.State != contextfabric.SubmissionRunning {
			t.Fatalf("a row this owner does not own completed the record: %+v", record)
		}
		if record := state(t, store, hijacked.Key, now.Add(2*time.Minute)); record.State != contextfabric.SubmissionAbandoned {
			t.Fatalf("past expiry = %+v, want abandoned", record)
		}
	})

	t.Run("unsaved mark: owner only, running only", func(t *testing.T) {
		store := newStore(t, func() time.Time { return now })
		admission := admit(t, store, "mark")
		if marked, err := store.MarkSubmissionUnsaved(context.Background(), stranger, admission.ReservedResultID, now); err != nil || marked {
			t.Fatalf("stranger marked = %t err=%v", marked, err)
		}
		if marked, err := store.MarkSubmissionUnsaved(context.Background(), owner, admission.ReservedResultID, now); err != nil || !marked {
			t.Fatalf("owner marked = %t err=%v", marked, err)
		}
		if record := state(t, store, admission.Key, now); record.State != contextfabric.SubmissionDoneUnsaved {
			t.Fatalf("record = %s", record.State)
		}
		if marked, err := store.MarkSubmissionUnsaved(context.Background(), owner, admission.ReservedResultID, now); err != nil || marked {
			t.Fatalf("a final record was marked again = %t err=%v", marked, err)
		}
	})

	t.Run("abandon leaves a final record final", func(t *testing.T) {
		store := newStore(t, func() time.Time { return now })
		running := admit(t, store, "abandon-running")
		if record, err := store.AbandonSubmission(context.Background(), running.Key, now); err != nil || record.State != contextfabric.SubmissionAbandoned {
			t.Fatalf("abandon = %+v err=%v", record, err)
		}
		done := admit(t, store, "abandon-done")
		save(t, store, owner, done.ReservedResultID)
		if record, err := store.AbandonSubmission(context.Background(), done.Key, now); err != nil || record.State != contextfabric.SubmissionDone {
			t.Fatalf("abandon of a done record = %+v err=%v", record, err)
		}
	})

	t.Run("keys are per owner and unknown keys are not found", func(t *testing.T) {
		store := newStore(t, func() time.Time { return now })
		admission := admit(t, store, "per-owner")
		strangerKey, err := contextfabric.SubmissionKeyFor(stranger, admission.Key.SubmissionID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetSubmission(context.Background(), strangerKey, now); !errors.Is(err, contextfabric.ErrSubmissionNotFound) {
			t.Fatalf("another owner's key = %v, want not found", err)
		}
		unknown := admission.Key
		unknown.SubmissionID = "submission-parity-unknown"
		if _, err := store.GetSubmission(context.Background(), unknown, now); !errors.Is(err, contextfabric.ErrSubmissionNotFound) {
			t.Fatalf("unknown key = %v, want not found", err)
		}
	})

	t.Run("sweep counts what it changes", func(t *testing.T) {
		store := newStore(t, func() time.Time { return now })
		expired := admit(t, store, "sweep-expired")
		_ = admit(t, store, "sweep-live")
		sweep, err := store.SweepSubmissions(context.Background(), now.Add(30*time.Second))
		if err != nil || sweep != (contextfabric.SubmissionSweep{}) {
			t.Fatalf("early sweep = %+v err=%v", sweep, err)
		}
		sweep, err = store.SweepSubmissions(context.Background(), now.Add(2*time.Minute))
		if err != nil || sweep != (contextfabric.SubmissionSweep{Abandoned: 2}) {
			t.Fatalf("late sweep = %+v err=%v, want both running records abandoned", sweep, err)
		}
		if record := state(t, store, expired.Key, now.Add(2*time.Minute)); record.State != contextfabric.SubmissionAbandoned {
			t.Fatalf("expired record = %s", record.State)
		}
	})
}

// LimitsStore is a SubmissionStore that also reads the usage aggregate.
type LimitsStore interface {
	SubmissionStore
	contextfabric.UsageReader
}

// RunSubmissionLimitsSuite runs the shared admission-limit table
// (CHAOS-11914) against a store. newStore must return an EMPTY store per
// case.
func RunSubmissionLimitsSuite(t *testing.T, newStore func(t *testing.T) LimitsStore) {
	t.Helper()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	org := "org-parity-limits"
	principal := func(subject string) storage.Principal {
		return storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: subject, OrgID: org}
	}
	limits := contextfabric.SubmissionLimits{MaxRunningPerPrincipal: 2, MaxRunningPerOrg: 4, MonthlyPlatformRunAllowance: 3}
	digest := "sha256:" + fmt.Sprintf("%064x", 11914)
	admission := func(t *testing.T, who storage.Principal, name string, platform bool) contextfabric.SubmissionAdmission {
		t.Helper()
		key, err := contextfabric.SubmissionKeyFor(who, "submission-limits-"+name)
		if err != nil {
			t.Fatal(err)
		}
		return contextfabric.SubmissionAdmission{Key: key, RequestDigest: digest, ReservedResultID: "result_limits_" + name, ExpiresAt: now.Add(time.Minute), Limits: limits, PlatformRun: platform}
	}
	admit := func(t *testing.T, store LimitsStore, a contextfabric.SubmissionAdmission) {
		t.Helper()
		if _, admitted, err := store.AdmitSubmission(context.Background(), a, now); err != nil || !admitted {
			t.Fatalf("admit %s = admitted %t, err %v", a.Key.SubmissionID, admitted, err)
		}
	}
	refusedAs := func(t *testing.T, store LimitsStore, a contextfabric.SubmissionAdmission, want contextfabric.SubmissionRefusal, count int64) {
		t.Helper()
		_, admitted, err := store.AdmitSubmission(context.Background(), a, now)
		var refused *contextfabric.SubmissionRefusedError
		if admitted || !errors.As(err, &refused) || refused.Refusal != want || refused.Count != count || refused.RetryAfter <= 0 {
			t.Fatalf("admit %s = admitted %t, err %v; want refused %s at %d", a.Key.SubmissionID, admitted, err, want, count)
		}
		// Nothing was written.
		if _, err := store.GetSubmission(context.Background(), a.Key, now); !errors.Is(err, contextfabric.ErrSubmissionNotFound) {
			t.Fatalf("a refused admission left a record: %v", err)
		}
	}
	usage := func(t *testing.T, store LimitsStore) int64 {
		t.Helper()
		got, found, err := store.ReadUsageMonthly(context.Background(), org, now)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			return 0
		}
		if got.CostMicroUSD != nil {
			t.Fatalf("cost is not metered, but the aggregate carries %d", *got.CostMicroUSD)
		}
		return got.PlatformRuns
	}

	t.Run("principal limit, then org limit", func(t *testing.T) {
		store := newStore(t)
		a, b, c := principal("user-limits-a"), principal("user-limits-b"), principal("user-limits-c")
		admit(t, store, admission(t, a, "a1", false))
		admit(t, store, admission(t, a, "a2", false))
		refusedAs(t, store, admission(t, a, "a3", false), contextfabric.SubmissionRefusedPrincipalConcurrency, 2)
		admit(t, store, admission(t, b, "b1", false))
		admit(t, store, admission(t, b, "b2", false))
		refusedAs(t, store, admission(t, c, "c1", false), contextfabric.SubmissionRefusedOrgConcurrency, 4)
		// A repeat of a running key at the limit is a repeat, never a refusal.
		if record, admitted, err := store.AdmitSubmission(context.Background(), admission(t, a, "a1", false), now); err != nil || admitted || record.State != contextfabric.SubmissionRunning {
			t.Fatalf("repeat at the limit = %+v admitted %t err %v", record, admitted, err)
		}
		// A run that ends frees its slot.
		if _, err := store.AbandonSubmission(context.Background(), admission(t, b, "b1", false).Key, now); err != nil {
			t.Fatal(err)
		}
		admit(t, store, admission(t, c, "c2", false))
	})

	t.Run("expired and stored runs do not count", func(t *testing.T) {
		store := newStore(t)
		a := principal("user-limits-expiry")
		expired := admission(t, a, "e1", false)
		expired.ExpiresAt = now.Add(time.Second)
		if _, _, err := store.AdmitSubmission(context.Background(), expired, now); err != nil {
			t.Fatal(err)
		}
		saved := admission(t, a, "e2", false)
		admit(t, store, saved)
		later := now.Add(2 * time.Second)
		third := admission(t, a, "e3", false)
		third.ExpiresAt = later.Add(time.Minute)
		if _, admitted, err := store.AdmitSubmission(context.Background(), third, later); err != nil || !admitted {
			t.Fatalf("past one expiry = admitted %t err %v", admitted, err)
		}
		fourth := admission(t, a, "e4", false)
		fourth.ExpiresAt = later.Add(time.Minute)
		if _, admitted, err := store.AdmitSubmission(context.Background(), fourth, later); admitted || err == nil {
			t.Fatalf("e2 and e3 are live: fourth admitted %t err %v", admitted, err)
		}
		if err := store.Save(context.Background(), a, ValidResult(saved.ReservedResultID, "q"), nil, nil,
			contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
			contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
			contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)); err != nil {
			t.Fatal(err)
		}
		if _, admitted, err := store.AdmitSubmission(context.Background(), fourth, later); err != nil || !admitted {
			t.Fatalf("after e2's save = admitted %t err %v", admitted, err)
		}
	})

	t.Run("monthly allowance counts platform runs only", func(t *testing.T) {
		store := newStore(t)
		// Concurrency is not what this case measures: every run ends.
		end := func(a contextfabric.SubmissionAdmission) {
			if _, err := store.AbandonSubmission(context.Background(), a.Key, now); err != nil {
				t.Fatal(err)
			}
		}
		for index := 0; index < 3; index++ {
			a := admission(t, principal(fmt.Sprintf("user-allow-%d", index)), fmt.Sprintf("p%d", index), true)
			admit(t, store, a)
			end(a)
		}
		if got := usage(t, store); got != 3 {
			t.Fatalf("usage = %d, want 3", got)
		}
		refusedAs(t, store, admission(t, principal("user-allow-x"), "p3", true), contextfabric.SubmissionRefusedAllowanceExhausted, 3)
		if got := usage(t, store); got != 3 {
			t.Fatalf("a refused admission counted: usage = %d", got)
		}
		// A run on the organization's own model counts to concurrency only.
		own := admission(t, principal("user-allow-own"), "own", false)
		admit(t, store, own)
		if got := usage(t, store); got != 3 {
			t.Fatalf("an own-model run counted: usage = %d", got)
		}
		// A repeat of an admitted platform key is not counted again.
		repeat := admission(t, principal("user-allow-0"), "p0", true)
		if _, admitted, err := store.AdmitSubmission(context.Background(), repeat, now); err != nil || admitted {
			t.Fatalf("repeat = admitted %t err %v", admitted, err)
		}
		if got := usage(t, store); got != 3 {
			t.Fatalf("a repeat counted: usage = %d", got)
		}
		// The next month starts at zero.
		next := admission(t, principal("user-allow-next"), "next", true)
		nextMonth := time.Date(2026, 11, 1, 0, 0, 1, 0, time.UTC)
		next.ExpiresAt = nextMonth.Add(time.Minute)
		if _, admitted, err := store.AdmitSubmission(context.Background(), next, nextMonth); err != nil || !admitted {
			t.Fatalf("next month = admitted %t err %v", admitted, err)
		}
	})
}
