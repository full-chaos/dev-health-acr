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
func RunSubmissionSuite(t *testing.T, newStore func(t *testing.T) SubmissionStore) {
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
		store := newStore(t)
		first := admit(t, store, "repeat")
		again := first
		again.ReservedResultID = "result_parity_repeat_other"
		again.RequestDigest = "sha256:" + fmt.Sprintf("%064x", 1)
		record, admitted, err := store.AdmitSubmission(context.Background(), again, now)
		if err != nil || admitted || record.ReservedResultID != first.ReservedResultID || record.RequestDigest != first.RequestDigest {
			t.Fatalf("repeat = %+v admitted=%t err=%v, want the first record", record, admitted, err)
		}
	})

	t.Run("save completes the record and binds the owner", func(t *testing.T) {
		store := newStore(t)
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
		store := newStore(t)
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
		store := newStore(t)
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
		store := newStore(t)
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
		store := newStore(t)
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
		store := newStore(t)
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
		store := newStore(t)
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
		store := newStore(t)
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
