package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// stubConsentAuthority is a consent authority whose answers a test sets, so
// each clause of the consent state mapping is reached on its own, including
// states the device flow reaches only through a race or a partial failure.
type stubConsentAuthority struct {
	state      OAuthDeviceState
	stateErr   error
	approveErr error
	denyErr    error
	approvals  int
}

func (s *stubConsentAuthority) StartForOAuth(context.Context) (OAuthDeviceAuthorization, error) {
	return OAuthDeviceAuthorization{DeviceCodeHash: storage.HashDeviceCode("device"), ExpiresAt: time.Date(2026, 9, 21, 12, 10, 0, 0, time.UTC)}, nil
}

func (s *stubConsentAuthority) StateForOAuth(context.Context, storage.DeviceCodeHash) (OAuthDeviceState, error) {
	return s.state, s.stateErr
}

func (s *stubConsentAuthority) ApproveForOAuth(context.Context, storage.Principal, storage.DeviceCodeHash, []string) error {
	s.approvals++
	return s.approveErr
}

func (s *stubConsentAuthority) DenyForOAuth(context.Context, storage.Principal, storage.DeviceCodeHash) error {
	return s.denyErr
}

func (s *stubConsentAuthority) RedeemForResource(context.Context, storage.DeviceCodeHash, string, []string) (IssuedCredential, error) {
	return IssuedCredential{}, ErrOAuthDeviceNotApproved
}

func newStubConsent(t *testing.T) (*OAuthService, *stubConsentAuthority, string) {
	t.Helper()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	authority := &stubConsentAuthority{state: OAuthDeviceStatePending}
	service, err := NewOAuthService(memory.NewOAuthStore(clock), authority, OAuthConfig{Issuer: testIssuer, Resources: []string{testResource}, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	client, err := service.Register(context.Background(), OAuthRegistrationRequest{ClientName: "c", RedirectURIs: []string{testRedirect}})
	if err != nil {
		t.Fatal(err)
	}
	_, challenge := pkce(t)
	authorization, err := service.Authorize(context.Background(), OAuthAuthorizeRequest{ResponseType: "code", ClientID: client.ClientID, RedirectURI: testRedirect, CodeChallenge: challenge, CodeChallengeMethod: "S256"})
	if err != nil {
		t.Fatal(err)
	}
	return service, authority, authorization.Handle
}

func TestOAuthConsentMapsEveryAuthorityState(t *testing.T) {
	for _, tc := range []struct {
		state OAuthDeviceState
		want  string
	}{
		{OAuthDeviceStatePending, "nil"},
		{OAuthDeviceStateExpired, oauthvocab.OutcomeExpired},
		{OAuthDeviceStateApproved, oauthvocab.OutcomeAlreadyCompleted},
		{OAuthDeviceStateDenied, oauthvocab.OutcomeAlreadyCompleted},
		{OAuthDeviceStateRedeemed, oauthvocab.OutcomeAlreadyCompleted},
	} {
		service, authority, handle := newStubConsent(t)
		authority.state = tc.state
		if _, _, err := service.ConsentRequest(context.Background(), handle); outcomeOf(err) != tc.want {
			t.Errorf("preview with state %s = %v, want %s", tc.state, err, tc.want)
		}
		if _, err := service.ApproveConsent(context.Background(), handle, webPrincipal([]string{"org/repo"}), []string{"org/repo"}); tc.want != "nil" && outcomeOf(err) != tc.want {
			t.Errorf("approve with state %s = %v, want %s", tc.state, err, tc.want)
		}
		if tc.want != "nil" && authority.approvals != 0 {
			t.Errorf("state %s reached the authority's approval", tc.state)
		}
	}
	service, authority, handle := newStubConsent(t)
	authority.stateErr = errors.New("store down")
	if _, _, err := service.ConsentRequest(context.Background(), handle); !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("state read failure = %v, want ErrOAuthUnavailable", err)
	}
}

func TestOAuthConsentMapsEveryDecisionFailure(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{ErrInvalidDeviceFlow, oauthvocab.OutcomeInvalidRequest},
		{storage.ErrDeviceAuthorizationConflict, oauthvocab.OutcomeAlreadyCompleted},
		{storage.ErrDeviceAuthorizationExpired, oauthvocab.OutcomeExpired},
		{storage.ErrDeviceAuthorizationNotFound, oauthvocab.OutcomeExpired},
	} {
		service, authority, handle := newStubConsent(t)
		authority.approveErr, authority.denyErr = tc.err, tc.err
		if _, err := service.ApproveConsent(context.Background(), handle, webPrincipal([]string{"org/repo"}), []string{"org/repo"}); outcomeOf(err) != tc.want {
			t.Errorf("approve failing with %v = %v, want %s", tc.err, err, tc.want)
		}
		if _, err := service.DenyConsent(context.Background(), handle, webPrincipal([]string{"org/repo"})); outcomeOf(err) != tc.want {
			t.Errorf("deny failing with %v = %v, want %s", tc.err, err, tc.want)
		}
	}
	service, authority, handle := newStubConsent(t)
	authority.approveErr = errors.New("store down")
	if _, err := service.ApproveConsent(context.Background(), handle, webPrincipal([]string{"org/repo"}), []string{"org/repo"}); !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("approval failure = %v, want ErrOAuthUnavailable", err)
	}
}

// Even when the authority would approve again (a second replica racing the
// first), the request carries one code: the issued code closes the handle,
// and a lost race to attach the code is already_completed, never a second
// code and never a retryable failure.
func TestOAuthConsentIssuesOneCodeWhateverTheAuthoritySays(t *testing.T) {
	service, authority, handle := newStubConsent(t)
	first, err := service.ApproveConsent(context.Background(), handle, webPrincipal([]string{"org/repo"}), []string{"org/repo"})
	if err != nil || !strings.Contains(first.RedirectURL, "code=") {
		t.Fatalf("first approval = %+v, %v", first, err)
	}
	authority.state = OAuthDeviceStatePending
	if _, err := service.ApproveConsent(context.Background(), handle, webPrincipal([]string{"org/repo"}), []string{"org/repo"}); outcomeOf(err) != oauthvocab.OutcomeAlreadyCompleted {
		t.Fatalf("second approval with a pending authority = %v, want already_completed", err)
	}
	if _, _, err := service.ConsentRequest(context.Background(), handle); outcomeOf(err) != oauthvocab.OutcomeAlreadyCompleted {
		t.Fatalf("preview after the code = %v, want already_completed", err)
	}
}

func TestOAuthConsentLostCodeRaceIsAlreadyCompleted(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	authority := &stubConsentAuthority{state: OAuthDeviceStatePending}
	store := &racingOAuthStore{OAuthStore: memory.NewOAuthStore(clock)}
	service, err := NewOAuthService(store, authority, OAuthConfig{Issuer: testIssuer, Resources: []string{testResource}, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	client, err := service.Register(context.Background(), OAuthRegistrationRequest{ClientName: "c", RedirectURIs: []string{testRedirect}})
	if err != nil {
		t.Fatal(err)
	}
	_, challenge := pkce(t)
	authorization, err := service.Authorize(context.Background(), OAuthAuthorizeRequest{ResponseType: "code", ClientID: client.ClientID, RedirectURI: testRedirect, CodeChallenge: challenge, CodeChallengeMethod: "S256"})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := service.ApproveConsent(context.Background(), authorization.Handle, webPrincipal([]string{"org/repo"}), []string{"org/repo"})
	if outcomeOf(err) != oauthvocab.OutcomeAlreadyCompleted || decision.RedirectURL != "" {
		t.Fatalf("lost code race = %+v, %v, want already_completed and no redirect", decision, err)
	}
}

// racingOAuthStore loses every code attachment to another replica.
type racingOAuthStore struct{ storage.OAuthStore }

func (racingOAuthStore) IssueAuthorizationCode(context.Context, storage.OAuthSecretHash, storage.OAuthSecretHash, time.Time) (storage.OAuthAuthorizationRequest, error) {
	return storage.OAuthAuthorizationRequest{}, storage.ErrConflict
}
