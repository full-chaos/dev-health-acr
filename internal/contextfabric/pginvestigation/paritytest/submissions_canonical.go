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

// RunSubmissionCanonicalOwnerSuite pins the canonical submission owner
// (CHAOS-12091) on both adapters: a user's rotated credential is the same
// owner as the credential it replaced -- it repeats, reads, completes and
// counts against its user's key -- while another surface of the same user, an
// operator credential and another user are not. newStore must return an
// EMPTY store per case.
func RunSubmissionCanonicalOwnerSuite(t *testing.T, newStore func(t *testing.T) SubmissionStore) {
	t.Helper()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	org := "org-parity-canonical"
	device := func(credentialID, user string) storage.Principal {
		return storage.Principal{
			AuthenticationMethod: storage.AuthenticationMethodCredential, Subject: credentialID, CredentialID: credentialID, OrgID: org,
			ClientClass: storage.CredentialClientClassNativeDevice, ApprovingUserID: user,
		}
	}
	first, rotated := device("cred_parity_first", "user-parity-device"), device("cred_parity_rotated", "user-parity-device")
	otherUser := device("cred_parity_other", "user-parity-other")
	browserSameUser := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-parity-device", OrgID: org}
	operator := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodCredential, Subject: "cred_parity_operator", CredentialID: "cred_parity_operator", OrgID: org,
		ClientClass: storage.CredentialClientClassOrgBearer}
	digest := "sha256:" + fmt.Sprintf("%064x", 12091)
	admission := func(t *testing.T, who storage.Principal, name string, limits contextfabric.SubmissionLimits) contextfabric.SubmissionAdmission {
		t.Helper()
		key, err := contextfabric.SubmissionKeyFor(who, "submission-canonical-"+name)
		if err != nil {
			t.Fatal(err)
		}
		return contextfabric.SubmissionAdmission{Key: key, RequestDigest: digest, ReservedResultID: "result_canonical_" + name + "_" + who.Subject, ExpiresAt: now.Add(time.Minute), Limits: limits}
	}
	keyFor := func(t *testing.T, who storage.Principal, name string) contextfabric.SubmissionKey {
		t.Helper()
		key, err := contextfabric.SubmissionKeyFor(who, "submission-canonical-"+name)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}

	t.Run("a rotated credential repeats, reads and completes its user's record", func(t *testing.T) {
		store := newStore(t)
		original := admission(t, first, "rotate", contextfabric.SubmissionLimits{})
		if _, admitted, err := store.AdmitSubmission(context.Background(), original, now); err != nil || !admitted {
			t.Fatalf("first admission admitted=%t err=%v", admitted, err)
		}
		repeat := admission(t, rotated, "rotate", contextfabric.SubmissionLimits{})
		record, admitted, err := store.AdmitSubmission(context.Background(), repeat, now)
		if err != nil || admitted || record.ReservedResultID != original.ReservedResultID {
			t.Fatalf("the rotated credential's admission = %+v admitted=%t err=%v, want the first record (no second run)", record, admitted, err)
		}
		if record, err := store.GetSubmission(context.Background(), keyFor(t, rotated, "rotate"), now); err != nil || record.ReservedResultID != original.ReservedResultID {
			t.Fatalf("the rotated credential's read = %+v err=%v", record, err)
		}
		// The run saves under the rotated credential (a refresh mid-run): the
		// record completes and the result is its user's.
		save := func(principal storage.Principal, resultID string) error {
			return store.Save(context.Background(), principal, ValidResult(resultID, "question for "+resultID), nil, nil,
				contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
				contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
				contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation))
		}
		if err := save(rotated, original.ReservedResultID); err != nil {
			t.Fatal(err)
		}
		if record, err := store.GetSubmission(context.Background(), keyFor(t, first, "rotate"), now); err != nil || record.State != contextfabric.SubmissionDone || record.ResultID != original.ReservedResultID {
			t.Fatalf("after the rotated save, the first credential reads %+v err=%v, want done", record, err)
		}
		stored, err := store.Get(context.Background(), rotated, original.ReservedResultID)
		if err != nil || stored.SubmissionOwner == nil {
			t.Fatalf("get = %+v err=%v", stored, err)
		}
		for name, tc := range map[string]struct {
			who  storage.Principal
			want bool
		}{
			"the first credential":     {first, true},
			"the rotated credential":   {rotated, true},
			"the same user on browser": {browserSameUser, false},
			"another user":             {otherUser, false},
			"an operator credential":   {operator, false},
		} {
			if got := stored.SubmissionOwner.Is(tc.who); got != tc.want {
				t.Errorf("result owner Is(%s) = %t, want %t", name, got, tc.want)
			}
		}
	})

	t.Run("another surface, user or operator reads nothing of it", func(t *testing.T) {
		store := newStore(t)
		original := admission(t, first, "apart", contextfabric.SubmissionLimits{})
		if _, admitted, err := store.AdmitSubmission(context.Background(), original, now); err != nil || !admitted {
			t.Fatalf("admitted=%t err=%v", admitted, err)
		}
		for name, who := range map[string]storage.Principal{"browser": browserSameUser, "other user": otherUser, "operator": operator} {
			if _, err := store.GetSubmission(context.Background(), keyFor(t, who, "apart"), now); !errors.Is(err, contextfabric.ErrSubmissionNotFound) {
				t.Errorf("%s read err = %v, want not found", name, err)
			}
		}
		// The same submission id by the same user on the browser is its own
		// record: the surfaces stay apart.
		browser := admission(t, browserSameUser, "apart", contextfabric.SubmissionLimits{})
		if _, admitted, err := store.AdmitSubmission(context.Background(), browser, now); err != nil || !admitted {
			t.Fatalf("browser admission admitted=%t err=%v, want its own record", admitted, err)
		}
	})

	t.Run("a user's credentials count to one principal limit", func(t *testing.T) {
		store := newStore(t)
		limits := contextfabric.SubmissionLimits{MaxRunningPerPrincipal: 1, MaxRunningPerOrg: 10, MonthlyPlatformRunAllowance: 10}
		if _, admitted, err := store.AdmitSubmission(context.Background(), admission(t, first, "limit-a", limits), now); err != nil || !admitted {
			t.Fatalf("first run admitted=%t err=%v", admitted, err)
		}
		_, admitted, err := store.AdmitSubmission(context.Background(), admission(t, rotated, "limit-b", limits), now)
		var refused *contextfabric.SubmissionRefusedError
		if admitted || !errors.As(err, &refused) || refused.Refusal != contextfabric.SubmissionRefusedPrincipalConcurrency || refused.Count != 1 {
			t.Fatalf("the rotated credential's second run = admitted %t err %v, want refused at the principal limit (count 1)", admitted, err)
		}
		if _, admitted, err := store.AdmitSubmission(context.Background(), admission(t, otherUser, "limit-c", limits), now); err != nil || !admitted {
			t.Fatalf("another user's run admitted=%t err=%v, want admitted", admitted, err)
		}
	})

	t.Run("the unsaved marker finds the record by its user", func(t *testing.T) {
		store := newStore(t)
		original := admission(t, first, "unsaved", contextfabric.SubmissionLimits{})
		if _, admitted, err := store.AdmitSubmission(context.Background(), original, now); err != nil || !admitted {
			t.Fatalf("admitted=%t err=%v", admitted, err)
		}
		if marked, err := store.MarkSubmissionUnsaved(context.Background(), browserSameUser, original.ReservedResultID, now); err != nil || marked {
			t.Fatalf("browser marker marked=%t err=%v, want not marked", marked, err)
		}
		if marked, err := store.MarkSubmissionUnsaved(context.Background(), rotated, original.ReservedResultID, now); err != nil || !marked {
			t.Fatalf("rotated marker marked=%t err=%v, want marked", marked, err)
		}
	})
}
