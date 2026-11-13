package auth

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// familyRevokeReasons returns the family_revoke_reason of every
// credential_revoked audit row, the form revokeBrowserFamilyTx writes in
// Postgres: "<none>" for a row without it.
func familyRevokeReasons(h *oauthHarness) []any {
	var reasons []any
	for _, event := range h.audit.Events() {
		if event.Action != "credential_revoked" {
			continue
		}
		reason, ok := event.Metadata["family_revoke_reason"]
		if !ok {
			reason = "<none>"
		}
		reasons = append(reasons, reason)
	}
	return reasons
}

func requireFamilyReasons(t *testing.T, h *oauthHarness, want string, count int) {
	t.Helper()
	got := familyRevokeReasons(h)
	if len(got) != count {
		t.Fatalf("credential_revoked rows = %d (%v), want %d", len(got), got, count)
	}
	for _, reason := range got {
		if reason != want {
			t.Fatalf("family revoke reason = %v, want %q", reason, want)
		}
	}
}

func TestBrowserRefresh_aGoneOrganizationRevokesTheFamilyAsOrgDeprovisioned(t *testing.T) {
	h, clientID := browserHarness(t)
	login := browserLogin(t, h, clientID)
	h.oauth.refresh.orgs = &fakeOrgPresence{gone: map[string]bool{login.Issued.Credential.OrgID: true}}

	_, err := refreshOnce(t, h, login.RefreshToken)

	if err == nil || !strings.Contains(err.Error(), "organization_unknown") {
		t.Fatalf("refresh of a gone organization: %v, want invalid_grant (organization_unknown)", err)
	}
	requireFamilyReasons(t, h, "org_deprovisioned", 1)
}

func TestBrowserRefresh_signOutStillRecordsSignOut(t *testing.T) {
	h, clientID := browserHarness(t)
	login := browserLogin(t, h, clientID)

	if _, err := h.oauth.refresh.RevokeFamily(context.Background(), login.RefreshToken, clientID); err != nil {
		t.Fatal(err)
	}

	requireFamilyReasons(t, h, "sign_out", 1)
}

func TestBrowserRefresh_reuseStillRecordsReuse(t *testing.T) {
	h, clientID := browserHarness(t)
	first := browserLogin(t, h, clientID)
	mustRefresh(t, h, first.RefreshToken)
	h.now = h.now.Add(BrowserRefreshGrace + 1)

	_, err := refreshOnce(t, h, first.RefreshToken)

	if err == nil || !strings.Contains(err.Error(), "reuse_revoked") {
		t.Fatalf("reuse: %v, want invalid_grant (reuse_revoked)", err)
	}
	requireFamilyReasons(t, h, "reuse", 2)
}

func TestBrowserRefresh_aSecondRevokeKeepsTheFirstReason(t *testing.T) {
	h, clientID := browserHarness(t)
	login := browserLogin(t, h, clientID)
	if _, err := h.oauth.refresh.RevokeFamily(context.Background(), login.RefreshToken, clientID); err != nil {
		t.Fatal(err)
	}
	h.oauth.refresh.orgs = &fakeOrgPresence{gone: map[string]bool{login.Issued.Credential.OrgID: true}}

	_, _ = refreshOnce(t, h, login.RefreshToken)

	requireFamilyReasons(t, h, "sign_out", 1)
}

func TestBrowserRefresh_theStoreRefusesAReasonOutsideTheClosedSet(t *testing.T) {
	for _, reason := range []storage.BrowserFamilyRevokeReason{"", "other", "SIGN_OUT", "sign_out ", "browser family sign_out", "revoked"} {
		t.Run(string(reason), func(t *testing.T) {
			h, clientID := browserHarness(t)
			login := browserLogin(t, h, clientID)

			_, err := h.refresh.RevokeBrowserFamilyByToken(context.Background(), HashRefreshToken(login.RefreshToken), clientID, browserRefreshActor, reason, h.now)

			if !errors.Is(err, storage.ErrInvalidBrowserRefresh) {
				t.Fatalf("reason %q: %v, want ErrInvalidBrowserRefresh", reason, err)
			}
			if got := familyRevokeReasons(h); len(got) != 0 {
				t.Fatalf("reason %q wrote revocations %v, want none", reason, got)
			}
			stored, err := h.creds.FindByTokenHash(context.Background(), HashToken(login.Issued.Token))
			if err != nil || stored.RevokedAt != nil {
				t.Fatalf("reason %q: credential revoked %v, %v; want live", reason, stored.RevokedAt, err)
			}
		})
	}
}

func TestBrowserRefresh_everyReasonOfTheClosedSetRevokesWithItself(t *testing.T) {
	for _, reason := range storage.BrowserFamilyRevokeReasonVocabulary() {
		t.Run(string(reason), func(t *testing.T) {
			h, clientID := browserHarness(t)
			login := browserLogin(t, h, clientID)

			out, err := h.refresh.RevokeBrowserFamilyByToken(context.Background(), HashRefreshToken(login.RefreshToken), clientID, browserRefreshActor, reason, h.now)

			if err != nil || out.Decision != storage.BrowserFamilyRevokedNow {
				t.Fatalf("reason %q: %v, %v; want revoked", reason, out.Decision, err)
			}
			requireFamilyReasons(t, h, string(reason), 1)
		})
	}
}

// Parity with postgres (browser_family_revoke_reason_integration_test.go): a
// family of two credentials, revoked for any reason, has every member carry it.
func TestBrowserRefresh_everyMemberOfARevokedFamilyCarriesTheReason(t *testing.T) {
	for _, reason := range storage.BrowserFamilyRevokeReasonVocabulary() {
		t.Run(string(reason), func(t *testing.T) {
			h, clientID := browserHarness(t)
			login := browserLogin(t, h, clientID)
			rotated := mustRefresh(t, h, login.RefreshToken)

			out, err := h.refresh.RevokeBrowserFamilyByToken(context.Background(), HashRefreshToken(rotated.RefreshToken), clientID, browserRefreshActor, reason, h.now)

			if err != nil || out.Decision != storage.BrowserFamilyRevokedNow || out.RevokedCredentials != 2 {
				t.Fatalf("reason %q: %+v, %v; want revoked with 2 credentials", reason, out, err)
			}
			requireFamilyReasons(t, h, string(reason), 2)
		})
	}
}

// A direct revoke of one browser credential (self-revoke or the operator
// command): its siblings carry member_revoked, the initiating credential
// carries the operator's text and no family reason, as in Postgres
// (credentials_audit.go revokeBrowserFamilyTx runs after the member's own row).
func TestBrowserRefresh_aDirectRevokeNamesTheFamilyReasonOnSiblingsOnly(t *testing.T) {
	h, clientID := browserHarness(t)
	login := browserLogin(t, h, clientID)
	rotated := mustRefresh(t, h, login.RefreshToken)
	initiator := rotated.Issued.Credential

	_, err := h.creds.RevokeCredential(context.Background(), storage.CredentialRevocationInput{
		OrgID: initiator.OrgID, CredentialID: initiator.CredentialID, ActorID: "operator_1", Reason: "operator said so",
	})

	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]map[string]any{}
	for _, event := range h.audit.Events() {
		if event.Action == "credential_revoked" {
			seen[event.ResourceID] = event.Metadata
		}
	}
	if len(seen) != 2 {
		t.Fatalf("credential_revoked rows = %v, want 2", seen)
	}
	first, sibling := seen[initiator.CredentialID], seen[login.Issued.Credential.CredentialID]
	if first["reason"] != "operator said so" || first["family_revoke_reason"] != nil {
		t.Fatalf("initiating credential row = %v, want the operator reason and no family_revoke_reason", first)
	}
	if sibling["family_revoke_reason"] != "member_revoked" || sibling["reason"] != nil {
		t.Fatalf("sibling row = %v, want family_revoke_reason member_revoked and no reason", sibling)
	}
}

// No-change pin: a family revoke that carries no revocation details (here
// sign-out) writes the row form it always wrote: actor user, the family fields
// and nothing else.
func TestBrowserRefresh_signOutRowFormIsUnchanged(t *testing.T) {
	h, clientID := browserHarness(t)
	login := browserLogin(t, h, clientID)

	if _, err := h.oauth.refresh.RevokeFamily(context.Background(), login.RefreshToken, clientID); err != nil {
		t.Fatal(err)
	}

	var rows int
	for _, event := range h.audit.Events() {
		if event.Action != "credential_revoked" {
			continue
		}
		rows++
		want := map[string]any{"family_id": login.Issued.Credential.Provenance.FamilyID, "family_revoke_reason": "sign_out"}
		if event.ActorType != "user" || !reflect.DeepEqual(event.Metadata, want) {
			t.Fatalf("sign-out row = actor %q metadata %v; want actor user metadata %v", event.ActorType, event.Metadata, want)
		}
	}
	if rows != 1 {
		t.Fatalf("credential_revoked rows = %d, want 1", rows)
	}
}
