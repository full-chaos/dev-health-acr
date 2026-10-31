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

// FeedbackStore is what the feedback parity suite drives (CHAOS-11950): the
// result, submission and thread stores and the feedback store, as both
// adapters are one object.
type FeedbackStore interface {
	ThreadStore
	contextfabric.ResultFeedbackStore
}

// RunResultFeedbackSuite runs the shared feedback behavior table against a
// store, so the memory and PostgreSQL adapters cannot disagree about who
// reaches a result's feedback. newStore must return an EMPTY store per case
// whose own clock is clock.
func RunResultFeedbackSuite(t *testing.T, newStore func(t *testing.T, clock func() time.Time) FeedbackStore) {
	t.Helper()
	start := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	owner := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-feedback-owner", OrgID: "org-parity-feedback"}
	stranger := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-feedback-stranger", OrgID: owner.OrgID}
	// Another organization's principal with the owner's method and subject.
	otherOrg := storage.Principal{AuthenticationMethod: owner.AuthenticationMethod, Subject: owner.Subject, OrgID: "org-parity-feedback-other"}
	credential := func(id, user string, class storage.CredentialClientClass) storage.Principal {
		return storage.Principal{AuthenticationMethod: storage.AuthenticationMethodCredential, Subject: id, OrgID: owner.OrgID, ApprovingUserID: user, ClientClass: class}
	}
	yes, no, unset := contractsv1.ContextFabricResultFeedbackYes, contractsv1.ContextFabricResultFeedbackNo, contractsv1.ContextFabricResultFeedbackUnset
	ctx := context.Background()
	setup := func(t *testing.T) (FeedbackStore, *ThreadClock) {
		t.Helper()
		clock := &ThreadClock{now: start}
		return newStore(t, clock.Now), clock
	}
	// answered admits a submission of principal (in threadID when it is
	// not empty) and saves its result, so the result row is bound to
	// principal as its submission owner.
	answered := func(t *testing.T, store FeedbackStore, clock *ThreadClock, principal storage.Principal, threadID, name string) string {
		t.Helper()
		key, err := contextfabric.SubmissionKeyFor(principal, "submission-feedback-"+name)
		if err != nil {
			t.Fatal(err)
		}
		resultID := "result_feedback_" + name
		admission := contextfabric.SubmissionAdmission{
			Key: key, RequestDigest: "sha256:" + fmt.Sprintf("%064x", 11950), ReservedResultID: resultID,
			ThreadID: threadID, ExpiresAt: clock.now.Add(time.Minute),
		}
		if threadID != "" {
			admission.Question = "question " + name
		}
		if record, admitted, err := store.AdmitSubmission(ctx, admission, clock.now); err != nil || !admitted {
			t.Fatalf("admit %s = %+v admitted=%t err=%v", name, record, admitted, err)
		}
		saveResult(t, store, principal, resultID)
		return resultID
	}
	notFound := func(t *testing.T, what string, err error) {
		t.Helper()
		if !errors.Is(err, contextfabric.ErrInvestigationResultNotFound) {
			t.Fatalf("%s = %v, want not found", what, err)
		}
	}
	want := func(t *testing.T, what string, got contextfabric.ResultFeedback, err error, correct, useful contractsv1.ContextFabricResultFeedbackValue, at time.Time) {
		t.Helper()
		if err != nil || got.Correct != correct || got.Useful != useful || !got.UpdatedAt.Equal(at) {
			t.Fatalf("%s = %+v err=%v, want correct=%s useful=%s updated_at=%s", what, got, err, correct, useful, at)
		}
	}

	t.Run("the owner reads unset, sets both flags, reads them back, and clears them", func(t *testing.T) {
		store, clock := setup(t)
		resultID := answered(t, store, clock, owner, "", "own")
		got, err := store.GetResultFeedback(ctx, owner, resultID, clock.now)
		want(t, "never set", got, err, unset, unset, time.Time{})
		clock.now = start.Add(time.Minute)
		got, err = store.SetResultFeedback(ctx, owner, resultID, yes, no, clock.now)
		want(t, "set", got, err, yes, no, clock.now)
		got, err = store.GetResultFeedback(ctx, owner, resultID, clock.now)
		want(t, "read back", got, err, yes, no, start.Add(time.Minute))
		clock.now = start.Add(2 * time.Minute)
		got, err = store.SetResultFeedback(ctx, owner, resultID, unset, unset, clock.now)
		want(t, "cleared", got, err, unset, unset, start.Add(2*time.Minute))
		got, err = store.GetResultFeedback(ctx, owner, resultID, clock.now)
		want(t, "cleared, read back", got, err, unset, unset, start.Add(2*time.Minute))
	})

	t.Run("updated_at never moves back on a set from a clock that is behind", func(t *testing.T) {
		store, clock := setup(t)
		resultID := answered(t, store, clock, owner, "", "clock")
		clock.now = start.Add(time.Hour)
		if _, err := store.SetResultFeedback(ctx, owner, resultID, yes, yes, clock.now); err != nil {
			t.Fatal(err)
		}
		got, err := store.SetResultFeedback(ctx, owner, resultID, no, yes, start.Add(time.Minute))
		want(t, "set behind", got, err, no, yes, start.Add(time.Hour))
	})

	t.Run("another principal of the organization, another organization's principal and an unknown id reach nothing", func(t *testing.T) {
		store, clock := setup(t)
		resultID := answered(t, store, clock, owner, "", "isolated")
		if _, err := store.SetResultFeedback(ctx, owner, resultID, yes, yes, clock.now); err != nil {
			t.Fatal(err)
		}
		for name, reader := range map[string]storage.Principal{"stranger": stranger, "other organization": otherOrg} {
			_, err := store.GetResultFeedback(ctx, reader, resultID, clock.now)
			notFound(t, name+" get", err)
			_, err = store.SetResultFeedback(ctx, reader, resultID, no, no, clock.now)
			notFound(t, name+" set", err)
		}
		_, err := store.GetResultFeedback(ctx, owner, "result_feedback_unknown", clock.now)
		notFound(t, "unknown get", err)
		_, err = store.SetResultFeedback(ctx, owner, "result_feedback_unknown", yes, yes, clock.now)
		notFound(t, "unknown set", err)
		got, err := store.GetResultFeedback(ctx, owner, resultID, clock.now)
		want(t, "the owner's feedback after the refused sets", got, err, yes, yes, start)
	})

	t.Run("a result that completed no submission takes no feedback", func(t *testing.T) {
		store, clock := setup(t)
		saveResult(t, store, owner, "result_feedback_unowned")
		if _, err := store.Get(ctx, owner, "result_feedback_unowned"); err != nil {
			t.Fatalf("the unowned result is readable by id: %v", err)
		}
		_, err := store.GetResultFeedback(ctx, owner, "result_feedback_unowned", clock.now)
		notFound(t, "unowned get", err)
		_, err = store.SetResultFeedback(ctx, owner, "result_feedback_unowned", yes, yes, clock.now)
		notFound(t, "unowned set", err)
	})

	t.Run("another credential of the same user on the same surface reaches the feedback; the user's MCP surface does not", func(t *testing.T) {
		store, clock := setup(t)
		session := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-feedback-rotating", OrgID: owner.OrgID}
		browser := credential("cred-feedback-browser", "user-feedback-rotating", storage.CredentialClientClassBrowser)
		mcp := credential("cred-feedback-mcp", "user-feedback-rotating", storage.CredentialClientClassOAuthUser)
		resultID := answered(t, store, clock, session, "", "rotating")
		got, err := store.SetResultFeedback(ctx, browser, resultID, no, yes, clock.now)
		want(t, "browser credential set", got, err, no, yes, start)
		got, err = store.GetResultFeedback(ctx, session, resultID, clock.now)
		want(t, "web session read", got, err, no, yes, start)
		_, err = store.GetResultFeedback(ctx, mcp, resultID, clock.now)
		notFound(t, "MCP credential get", err)
		_, err = store.SetResultFeedback(ctx, mcp, resultID, yes, yes, clock.now)
		notFound(t, "MCP credential set", err)
	})

	t.Run("a result only a closed thread names takes no feedback; a live thread's result does", func(t *testing.T) {
		store, clock := setup(t)
		deleted := answered(t, store, clock, owner, "thread-feedback-deleted", "deleted")
		live := answered(t, store, clock, owner, "thread-feedback-live", "live")
		if _, err := store.SetResultFeedback(ctx, owner, deleted, yes, yes, clock.now); err != nil {
			t.Fatalf("set before the delete: %v", err)
		}
		key, err := contextfabric.ThreadKeyFor(owner, "thread-feedback-deleted")
		if err != nil {
			t.Fatal(err)
		}
		if closed, err := store.CloseThread(ctx, key, clock.now); err != nil || !closed {
			t.Fatalf("close = %t %v", closed, err)
		}
		_, err = store.GetResultFeedback(ctx, owner, deleted, clock.now)
		notFound(t, "deleted thread's result get", err)
		_, err = store.SetResultFeedback(ctx, owner, deleted, no, no, clock.now)
		notFound(t, "deleted thread's result set", err)
		got, err := store.SetResultFeedback(ctx, owner, live, yes, no, clock.now)
		want(t, "live thread's result", got, err, yes, no, start)
		// Expiry closes the live thread too: retention 0 keeps it 24 hours
		// after its last turn.
		clock.now = start.Add(24 * time.Hour)
		_, err = store.GetResultFeedback(ctx, owner, live, clock.now)
		notFound(t, "expired thread's result get", err)
	})

	t.Run("a flag outside the closed vocabulary is refused, not stored", func(t *testing.T) {
		store, clock := setup(t)
		resultID := answered(t, store, clock, owner, "", "invalid")
		_, err := store.SetResultFeedback(ctx, owner, resultID, "maybe", yes, clock.now)
		if err == nil || errors.Is(err, contextfabric.ErrInvestigationResultNotFound) {
			t.Fatalf("an invalid flag = %v, want a refusal that is not not-found", err)
		}
		got, err := store.GetResultFeedback(ctx, owner, resultID, clock.now)
		want(t, "after the refusal", got, err, unset, unset, time.Time{})
	})
}

// saveResult saves a valid result under resultID; it completes principal's
// submission when one reserved resultID.
func saveResult(t *testing.T, store SubmissionStore, principal storage.Principal, resultID string) {
	t.Helper()
	if err := store.Save(context.Background(), principal, ValidResult(resultID, "question for "+resultID), nil, nil,
		contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
		contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
		contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)); err != nil {
		t.Fatalf("save %s: %v", resultID, err)
	}
}
