package auth

import (
	"context"
	"strings"
	"testing"
)

// familyRevokeReasons returns the family revoke reason every credential_revoked
// audit row carries: family_revoke_reason, or the "browser family <reason>" text
// of the credential that started the revocation.
func familyRevokeReasons(h *oauthHarness) []any {
	var reasons []any
	for _, event := range h.audit.Events() {
		if event.Action != "credential_revoked" {
			continue
		}
		if reason, ok := event.Metadata["family_revoke_reason"]; ok {
			reasons = append(reasons, reason)
			continue
		}
		text, _ := event.Metadata["reason"].(string)
		reasons = append(reasons, strings.TrimPrefix(text, "browser family "))
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
