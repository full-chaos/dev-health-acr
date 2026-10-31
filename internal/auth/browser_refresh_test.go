package auth

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12124: the browser refresh grant.

func refreshOnce(t *testing.T, h *oauthHarness, refreshToken string) (OAuthToken, error) {
	t.Helper()
	return h.oauth.Refresh(context.Background(), OAuthRefreshRequest{GrantType: "refresh_token", RefreshToken: refreshToken, ClientID: DevBrowserClientID})
}

func mustRefresh(t *testing.T, h *oauthHarness, refreshToken string) OAuthToken {
	t.Helper()
	token, err := refreshOnce(t, h, refreshToken)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	return token
}

func TestBrowserRefresh_theCodeExchangeReturnsARefreshToken(t *testing.T) {
	h, clientID := browserHarness(t)

	token := browserLogin(t, h, clientID)

	if !ValidRefreshTokenShape(token.RefreshToken) {
		t.Fatalf("refresh token %q does not have the refresh shape", token.RefreshToken[:min(len(token.RefreshToken), 8)])
	}
	// Other clients get none.
	dynamic := h.register(t)
	verifier, challenge := pkce(t)
	plain, err := h.oauth.Exchange(context.Background(), OAuthTokenRequest{
		GrantType: "authorization_code", Code: h.login(t, dynamic, challenge, ""), RedirectURI: testRedirect, ClientID: dynamic, CodeVerifier: verifier,
	})
	if err != nil || plain.RefreshToken != "" {
		t.Fatalf("a dynamic client's exchange: refresh %q, %v; want none", plain.RefreshToken, err)
	}
}

func TestBrowserRefresh_rotatesIntoANewCredentialOfTheSameFamily(t *testing.T) {
	h, clientID := browserHarness(t)
	first := browserLogin(t, h, clientID)
	h.now = h.now.Add(8 * time.Minute)

	second := mustRefresh(t, h, first.RefreshToken)

	a, b := first.Issued.Credential, second.Issued.Credential
	if b.CredentialID == a.CredentialID || second.Issued.Token == first.Issued.Token || second.RefreshToken == first.RefreshToken {
		t.Fatal("the refresh did not issue a new credential and refresh token")
	}
	if b.Provenance.ClientClass != "browser" || b.Provenance.FamilyID != a.Provenance.FamilyID || b.Provenance.SessionID != "sid_1" ||
		b.Provenance.ApprovingUserID != "user_1" || b.Provenance.ClientID != DevBrowserClientID || b.Resource != a.Resource ||
		!b.Provenance.FamilyExpiresAt.Equal(*a.Provenance.FamilyExpiresAt) {
		t.Fatalf("successor provenance = %+v, resource %q; want the family's, unchanged", b.Provenance, b.Resource)
	}
	if !b.ExpiresAt.Equal(h.now.Add(10*time.Minute)) || second.ExpiresIn != 10*time.Minute || second.Outcome != "" {
		t.Fatalf("successor expires %v (in %s, outcome %q), want %v", b.ExpiresAt, second.ExpiresIn, second.Outcome, h.now.Add(10*time.Minute))
	}
	stored, err := h.creds.FindByTokenHash(context.Background(), HashToken(second.Issued.Token))
	if err != nil || stored.CredentialID != b.CredentialID {
		t.Fatalf("the successor access token does not authenticate: %v", err)
	}
	// The consumed token is not a second rotation.
	third := mustRefresh(t, h, second.RefreshToken)
	if third.Issued.Credential.Provenance.FamilyID != a.Provenance.FamilyID {
		t.Fatal("the second rotation left the family")
	}
}

func TestBrowserRefresh_neverExtendsTheFamily(t *testing.T) {
	h, clientID := browserHarness(t)
	first := browserLogin(t, h, clientID)
	familyEnd := *first.Issued.Credential.Provenance.FamilyExpiresAt
	h.now = familyEnd.Add(-4 * time.Minute)

	late := mustRefresh(t, h, first.RefreshToken)

	if !late.Issued.Credential.ExpiresAt.Equal(familyEnd) || late.ExpiresIn != 4*time.Minute {
		t.Fatalf("a refresh 4 minutes before the family end lives until %v (%s), want %v", late.Issued.Credential.ExpiresAt, late.ExpiresIn, familyEnd)
	}
	h.now = familyEnd
	if _, err := refreshOnce(t, h, late.RefreshToken); outcomeOf(err) != oauthvocab.OutcomeFamilyExpired {
		t.Fatalf("a refresh at the family end: %s, want %s", outcomeOf(err), oauthvocab.OutcomeFamilyExpired)
	}
}

func TestBrowserRefresh_graceReplaysTheSameUnusedSuccessor(t *testing.T) {
	h, clientID := browserHarness(t)
	first := browserLogin(t, h, clientID)
	second := mustRefresh(t, h, first.RefreshToken)
	before, err := h.creds.List(context.Background(), testOrg)
	if err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(BrowserRefreshGrace)

	replay := mustRefresh(t, h, first.RefreshToken)

	if replay.Issued.Token != second.Issued.Token || replay.RefreshToken != second.RefreshToken || replay.Issued.Credential.CredentialID != second.Issued.Credential.CredentialID {
		t.Fatal("the grace replay did not answer the same successor")
	}
	if replay.Outcome != oauthvocab.OutcomeGraceReplay {
		t.Fatalf("grace replay outcome %q, want %q", replay.Outcome, oauthvocab.OutcomeGraceReplay)
	}
	after, err := h.creds.List(context.Background(), testOrg)
	if err != nil || len(after) != len(before) {
		t.Fatalf("credentials %d → %d after a grace replay, want no new row", len(before), len(after))
	}
	// The successor is still the one to use.
	if _, err := refreshOnce(t, h, second.RefreshToken); err != nil {
		t.Fatalf("the replayed successor no longer refreshes: %v", err)
	}
}

func familyRevoked(t *testing.T, h *oauthHarness, familyID string) bool {
	t.Helper()
	credentials, err := h.creds.List(context.Background(), testOrg)
	if err != nil {
		t.Fatal(err)
	}
	members := 0
	for _, credential := range credentials {
		if credential.Provenance.FamilyID != familyID {
			continue
		}
		members++
		if credential.RevokedAt == nil {
			return false
		}
	}
	return members > 0
}

func TestBrowserRefresh_reuseRevokesTheFamily(t *testing.T) {
	for name, tc := range map[string]struct {
		useSuccessor bool
		wait         time.Duration
	}{
		"successor used, inside the grace":  {true, time.Second},
		"successor unused, after the grace": {false, BrowserRefreshGrace + time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			h, clientID := browserHarness(t)
			first := browserLogin(t, h, clientID)
			second := mustRefresh(t, h, first.RefreshToken)
			latest := second
			if tc.useSuccessor {
				latest = mustRefresh(t, h, second.RefreshToken)
			}
			h.now = h.now.Add(tc.wait)

			_, err := refreshOnce(t, h, first.RefreshToken)

			if outcomeOf(err) != oauthvocab.OutcomeReuseRevoked {
				t.Fatalf("reuse: %s, want %s", outcomeOf(err), oauthvocab.OutcomeReuseRevoked)
			}
			family := first.Issued.Credential.Provenance.FamilyID
			if !familyRevoked(t, h, family) {
				t.Fatal("the reuse left a family credential live")
			}
			if _, err := refreshOnce(t, h, latest.RefreshToken); outcomeOf(err) != oauthvocab.OutcomeFamilyRevoked {
				t.Fatalf("the newest token after a reuse: %s, want %s", outcomeOf(err), oauthvocab.OutcomeFamilyRevoked)
			}
		})
	}
}

func TestBrowserRefresh_revokeByRefreshTokenWorksAfterTheAccessExpired(t *testing.T) {
	h, clientID := browserHarness(t)
	first := browserLogin(t, h, clientID)
	h.now = h.now.Add(11 * time.Minute) // the access token has expired

	revocation, err := h.oauth.RevokeRefreshToken(context.Background(), first.RefreshToken, clientID)

	if err != nil || revocation.Outcome != oauthvocab.OutcomeOK {
		t.Fatalf("family revoke: %+v, %v; want ok", revocation, err)
	}
	if !familyRevoked(t, h, first.Issued.Credential.Provenance.FamilyID) {
		t.Fatal("the family revoke left a credential live")
	}
	if _, err := refreshOnce(t, h, first.RefreshToken); outcomeOf(err) != oauthvocab.OutcomeFamilyRevoked {
		t.Fatalf("refresh after the revoke: %s, want %s", outcomeOf(err), oauthvocab.OutcomeFamilyRevoked)
	}
	again, err := h.oauth.RevokeRefreshToken(context.Background(), first.RefreshToken, clientID)
	if err != nil || again.Outcome != oauthvocab.OutcomeFamilyRevoked {
		t.Fatalf("second revoke: %+v, %v; want family_revoked without an error", again, err)
	}
	unknown, err := h.oauth.RevokeRefreshToken(context.Background(), RefreshTokenPrefix+"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", clientID)
	if err != nil || unknown.Outcome != oauthvocab.OutcomeInvalidGrant {
		t.Fatalf("unknown token revoke: %+v, %v; want invalid_grant without an error", unknown, err)
	}
	if _, err := h.oauth.RevokeRefreshToken(context.Background(), first.Issued.Token, clientID); outcomeOf(err) != oauthvocab.OutcomeUnsupportedTokenType {
		t.Fatalf("an access token at the revocation endpoint: %s, want %s", outcomeOf(err), oauthvocab.OutcomeUnsupportedTokenType)
	}
}

func TestBrowserRefresh_selfRevokeRevokesTheFamily(t *testing.T) {
	h, clientID := browserHarness(t)
	first := browserLogin(t, h, clientID)
	second := mustRefresh(t, h, first.RefreshToken)
	service, err := NewService(h.creds, ServiceOptions{Now: func() time.Time { return h.now }})
	if err != nil {
		t.Fatal(err)
	}
	credential := second.Issued.Credential

	_, err = service.RevokeSelf(context.Background(), storage.Principal{
		AuthenticationMethod: storage.AuthenticationMethodCredential, Subject: credential.CredentialID, CredentialID: credential.CredentialID,
		OrgID: credential.OrgID, RepositoryScopes: credential.RepositoryScopes, Permissions: credential.Scopes,
	})

	if err != nil {
		t.Fatal(err)
	}
	if !familyRevoked(t, h, credential.Provenance.FamilyID) {
		t.Fatal("a self-revoke left a family credential live")
	}
	if _, err := refreshOnce(t, h, second.RefreshToken); outcomeOf(err) != oauthvocab.OutcomeFamilyRevoked {
		t.Fatalf("refresh after a self-revoke: %s, want %s", outcomeOf(err), oauthvocab.OutcomeFamilyRevoked)
	}
}

func TestBrowserRefresh_onlyTheBrowserClientsOwnFamily(t *testing.T) {
	const otherBrowser = "https://www.example.test/acr/client-metadata.json"
	h := newOAuthHarnessWith(t, func(cfg *OAuthConfig) {
		cfg.Resources = []string{testResource, testIssuer}
		cfg.DevBrowserRedirectURIs = []string{testRedirect}
		cfg.BrowserClients = []string{otherBrowser}
	})
	h.sessionID = "sid_1"
	first := browserLogin(t, h, DevBrowserClientID)
	dynamic := h.register(t)
	for name, tc := range map[string]struct{ client, want string }{
		"another browser client": {otherBrowser, oauthvocab.OutcomeClientMismatch},
		"a dynamic client":       {dynamic, oauthvocab.OutcomeUnauthorizedClient},
	} {
		_, err := h.oauth.Refresh(context.Background(), OAuthRefreshRequest{GrantType: "refresh_token", RefreshToken: first.RefreshToken, ClientID: tc.client})
		if outcomeOf(err) != tc.want {
			t.Errorf("%s: %s, want %s", name, outcomeOf(err), tc.want)
		}
	}
	// The family is untouched: its own client still refreshes.
	if _, err := refreshOnce(t, h, first.RefreshToken); err != nil {
		t.Fatalf("the family's own client after refused attempts: %v", err)
	}
	// With no browser client the grant does not exist.
	off := newOAuthHarness(t)
	_, err := off.oauth.Refresh(context.Background(), OAuthRefreshRequest{GrantType: "refresh_token", RefreshToken: first.RefreshToken, ClientID: DevBrowserClientID})
	if outcomeOf(err) != oauthvocab.OutcomeUnsupportedGrantType || off.oauth.RefreshGrantSupported() {
		t.Fatalf("refresh with the class off: %s, want %s", outcomeOf(err), oauthvocab.OutcomeUnsupportedGrantType)
	}
}

func TestBrowserRefresh_successorDerivation(t *testing.T) {
	presented := RefreshTokenPrefix + "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	seedA, seedB := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	accessA, refreshA := deriveRefreshSuccessor(presented, seedA)
	accessA2, refreshA2 := deriveRefreshSuccessor(presented, seedA)
	accessB, refreshB := deriveRefreshSuccessor(presented, seedB)
	accessC, _ := deriveRefreshSuccessor(RefreshTokenPrefix+"CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC", seedA)
	if accessA != accessA2 || refreshA != refreshA2 {
		t.Fatal("the derivation is not deterministic")
	}
	if accessA == accessB || refreshA == refreshB || accessA == accessC {
		t.Fatal("the derivation ignores the seed or the presented token")
	}
	if !IsTokenShapeValid(accessA) || !ValidRefreshTokenShape(refreshA) {
		t.Fatal("derived tokens do not have their shapes")
	}
}

// A store whose grace replay names a successor the presented token does not
// derive is refused, never answered.
func TestBrowserRefresh_aMismatchedReplayIsNotAnswered(t *testing.T) {
	service, err := NewBrowserRefreshService(fixedRefreshStore{outcome: storage.BrowserRefreshOutcome{
		Decision: storage.BrowserRefreshGraceReplay, Seed: bytes.Repeat([]byte{3}, 32), SuccessorTokenHash: "other", SuccessorRefreshHash: "other",
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Refresh(context.Background(), RefreshTokenPrefix+"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", DevBrowserClientID)

	if !errors.Is(err, errRefreshDerivation) {
		t.Fatalf("a mismatched replay: %v, want errRefreshDerivation", err)
	}
}

type fixedRefreshStore struct{ outcome storage.BrowserRefreshOutcome }

func (fixedRefreshStore) StartBrowserFamily(context.Context, storage.BrowserFamilyStart) error {
	return nil
}
func (s fixedRefreshStore) RotateBrowserRefresh(context.Context, storage.BrowserRefreshRotation) (storage.BrowserRefreshOutcome, error) {
	return s.outcome, nil
}
func (fixedRefreshStore) RevokeBrowserFamilyByToken(context.Context, string, string, string, time.Time) (storage.BrowserFamilyRevocationOutcome, error) {
	return storage.BrowserFamilyRevocationOutcome{}, nil
}
