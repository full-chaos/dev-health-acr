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

// RunConnectOwnerSuite pins one user's Connects on both adapters. Each Connect
// is its own credential, so its legacy subject differs, and the owner is the
// approving user on the browser surface (CHAOS-12091). So a user's Connects
// share their History and their threads, and an answer of one Connect reused on
// another is stored, not unsaved. Another user and the same user's MCP surface
// share none of it. newStore must return an EMPTY store per case.
func RunConnectOwnerSuite(t *testing.T, newStore func(t *testing.T, clock func() time.Time) ThreadStore) {
	t.Helper()
	start := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	const org = "org-parity-connect"
	connect := func(credentialID, user string, class storage.CredentialClientClass) storage.Principal {
		return storage.Principal{
			AuthenticationMethod: storage.AuthenticationMethodCredential, Subject: credentialID, CredentialID: credentialID, OrgID: org,
			ClientClass: class, ApprovingUserID: user,
		}
	}
	first := connect("cred_connect_first", "user-connect", storage.CredentialClientClassBrowser)
	second := connect("cred_connect_second", "user-connect", storage.CredentialClientClassBrowser)
	otherUser := connect("cred_connect_other", "user-connect-other", storage.CredentialClientClassBrowser)
	mcpSameUser := connect("cred_connect_mcp", "user-connect", storage.CredentialClientClassNativeDevice)
	ctx := context.Background()
	setup := func(t *testing.T) (ThreadStore, *ThreadClock) {
		t.Helper()
		clock := &ThreadClock{now: start}
		return newStore(t, clock.Now), clock
	}
	admit := func(t *testing.T, store ThreadStore, clock *ThreadClock, principal storage.Principal, threadID, name string) contextfabric.SubmissionAdmission {
		t.Helper()
		key, err := contextfabric.SubmissionKeyFor(principal, "submission-connect-"+name)
		if err != nil {
			t.Fatal(err)
		}
		admission := contextfabric.SubmissionAdmission{
			Key: key, RequestDigest: "sha256:" + fmt.Sprintf("%064x", 12471), ReservedResultID: "result_connect_" + name,
			ThreadID: threadID, ExpiresAt: clock.now.Add(time.Minute), Question: "question " + name,
		}
		if record, admitted, err := store.AdmitSubmission(ctx, admission, clock.now); err != nil || !admitted {
			t.Fatalf("admit %s = %+v admitted=%t err=%v", name, record, admitted, err)
		}
		clock.now = clock.now.Add(time.Second)
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
	threads := func(t *testing.T, store ThreadStore, clock *ThreadClock, principal storage.Principal) []string {
		t.Helper()
		owner, err := contextfabric.SubmissionOwnerOf(principal)
		if err != nil {
			t.Fatal(err)
		}
		list, _, err := store.ListThreads(ctx, org, owner, nil, 10, clock.now)
		if err != nil {
			t.Fatalf("list threads: %v", err)
		}
		out := make([]string, 0, len(list))
		for _, thread := range list {
			out = append(out, thread.Key.ThreadID)
		}
		return out
	}
	turns := func(t *testing.T, store ThreadStore, clock *ThreadClock, principal storage.Principal, threadID string) []contextfabric.ThreadTurn {
		t.Helper()
		key, err := contextfabric.ThreadKeyFor(principal, threadID)
		if err != nil {
			t.Fatal(err)
		}
		page, err := store.GetThread(ctx, key, nil, 10, clock.now)
		if err != nil {
			t.Fatalf("read thread as %s: %v", principal.Subject, err)
		}
		return page.Turns
	}

	t.Run("an answer of one Connect reused on another is stored", func(t *testing.T) {
		store, clock := setup(t)
		answered := admit(t, store, clock, first, "", "answered")
		save(t, store, first, answered.ReservedResultID)
		for _, tc := range []struct {
			name       string
			principal  storage.Principal
			served     string
			state      contextfabric.SubmissionState
			resultID   string
			completion contextfabric.SubmissionCompletion
		}{
			{"the same user's second Connect", second, answered.ReservedResultID, contextfabric.SubmissionDone, answered.ReservedResultID, contextfabric.SubmissionCompletionServedStored},
			{"another user", otherUser, answered.ReservedResultID, contextfabric.SubmissionDoneUnsaved, "", contextfabric.SubmissionCompletionServedUnsaved},
			{"the same user's MCP surface", mcpSameUser, answered.ReservedResultID, contextfabric.SubmissionDoneUnsaved, "", contextfabric.SubmissionCompletionServedUnsaved},
		} {
			t.Run(tc.name, func(t *testing.T) {
				reused := admit(t, store, clock, tc.principal, "", "reused-"+tc.principal.Subject)
				record, completion, err := store.FinishSubmission(ctx, reused.Key, tc.served, clock.now)
				if err != nil {
					t.Fatal(err)
				}
				if record.State != tc.state || record.ResultID != tc.resultID || completion != tc.completion {
					t.Fatalf("finish = state %s, result %q, completion %s; want %s, %q, %s", record.State, record.ResultID, completion, tc.state, tc.resultID, tc.completion)
				}
				// The recovery reads what the record names.
				if record.State == contextfabric.SubmissionDone {
					if _, err := store.Get(ctx, tc.principal, record.ResultID); err != nil {
						t.Fatalf("read of the stored answer: %v", err)
					}
				}
			})
		}
	})

	t.Run("a user's Connects share History and a thread", func(t *testing.T) {
		store, clock := setup(t)
		const thread = "thread_12471_shared"
		opening := admit(t, store, clock, first, thread, "opening")
		save(t, store, first, opening.ReservedResultID)

		if got := threads(t, store, clock, second); len(got) != 1 || got[0] != thread {
			t.Fatalf("the second Connect's History = %v, want the first Connect's thread", got)
		}
		if got := turns(t, store, clock, second, thread); len(got) != 1 || got[0].State != contextfabric.SubmissionDone || got[0].ResultID != opening.ReservedResultID {
			t.Fatalf("the second Connect reads the thread's turns %+v, want the first turn done", got)
		}
		// The second Connect continues the thread, and its turn reuses the
		// answer of the first.
		reused := admit(t, store, clock, second, thread, "reused-in-thread")
		if record, _, err := store.FinishSubmission(ctx, reused.Key, opening.ReservedResultID, clock.now); err != nil || record.State != contextfabric.SubmissionDone {
			t.Fatalf("the reused turn = %+v err=%v, want done", record, err)
		}
		for name, principal := range map[string]storage.Principal{"first": first, "second": second} {
			got := turns(t, store, clock, principal, thread)
			if len(got) != 2 {
				t.Fatalf("the %s Connect reads %d turns, want 2: %+v", name, len(got), got)
			}
			if got[1].State != contextfabric.SubmissionDone || got[1].ResultID != opening.ReservedResultID {
				t.Fatalf("the %s Connect reads the reused turn as %+v, want done on the stored answer", name, got[1])
			}
			if list := threads(t, store, clock, principal); len(list) != 1 {
				t.Fatalf("the %s Connect's History = %v, want one thread", name, list)
			}
		}
		for name, principal := range map[string]storage.Principal{"another user": otherUser, "the MCP surface": mcpSameUser} {
			if got := threads(t, store, clock, principal); len(got) != 0 {
				t.Fatalf("%s reads History %v, want none", name, got)
			}
		}
	})

	t.Run("a turn of a second Connect keeps a reused answer alive", func(t *testing.T) {
		store, clock := setup(t)
		const opened, reusing = "thread_12471_opened", "thread_12471_reusing"
		closeThread := func(principal storage.Principal, threadID string) {
			t.Helper()
			key, err := contextfabric.ThreadKeyFor(principal, threadID)
			if err != nil {
				t.Fatal(err)
			}
			if closed, err := store.CloseThread(ctx, key, clock.now); err != nil || !closed {
				t.Fatalf("close %s = %t %v", threadID, closed, err)
			}
		}
		opening := admit(t, store, clock, first, opened, "kept")
		save(t, store, first, opening.ReservedResultID)
		reused := admit(t, store, clock, second, reusing, "keeps")
		if record, _, err := store.FinishSubmission(ctx, reused.Key, opening.ReservedResultID, clock.now); err != nil || record.State != contextfabric.SubmissionDone {
			t.Fatalf("the reused turn = %+v err=%v, want done", record, err)
		}
		closeThread(first, opened)
		if _, err := store.Get(ctx, second, opening.ReservedResultID); err != nil {
			t.Fatalf("the answer a live turn of the second Connect names is hidden: %v", err)
		}
		closeThread(second, reusing)
		if _, err := store.Get(ctx, second, opening.ReservedResultID); !errors.Is(err, contextfabric.ErrInvestigationResultNotFound) {
			t.Fatalf("the answer only closed threads name = %v, want hidden", err)
		}
	})
}
