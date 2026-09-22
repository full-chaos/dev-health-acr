package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// approveDeviceGrant approves the device authorization behind an RFC 8628
// device_code as the web verification page (POST /api/v1/oauth/device_approval)
// would: the SAME approval mechanism a typed-user-code approval or an OAuth
// authorization-code browser consent uses, keyed here by the device code's
// hash rather than a user code, since ApproveForOAuth works off either
// origin's device_authorizations row.
func (h *oauthHarness) approveDeviceGrant(t *testing.T, deviceCode string, repositories []string) {
	t.Helper()
	h.approveDevice(t, storage.HashDeviceCode(deviceCode), repositories)
}

func TestOAuthServiceDeviceGrantFullFlow(t *testing.T) {
	h := newOAuthHarness(t)
	clientID := h.register(t)

	started, err := h.oauth.StartDeviceAuthorization(context.Background(), OAuthDeviceAuthorizationRequest{ClientID: clientID})
	if err != nil {
		t.Fatalf("StartDeviceAuthorization: %v", err)
	}
	if started.DeviceCode == "" || started.UserCode == "" || started.ExpiresIn != storage.DeviceAuthorizationTTL || started.Interval != storage.DeviceAuthorizationPollInterval {
		t.Fatalf("unexpected start: %+v", started)
	}

	// Undecided: authorization_pending.
	_, err = h.oauth.ExchangeDeviceCode(context.Background(), OAuthDeviceTokenRequest{GrantType: OAuthDeviceCodeGrantType, DeviceCode: started.DeviceCode, ClientID: clientID})
	if outcomeOf(err) != oauthvocab.OutcomeAuthorizationPending {
		t.Fatalf("pending poll outcome = %s, want %s", outcomeOf(err), oauthvocab.OutcomeAuthorizationPending)
	}

	// Polled again before the interval elapses: slow_down, with a positive
	// RetryAfter the /token handler turns into a Retry-After header.
	_, err = h.oauth.ExchangeDeviceCode(context.Background(), OAuthDeviceTokenRequest{GrantType: OAuthDeviceCodeGrantType, DeviceCode: started.DeviceCode, ClientID: clientID})
	var slowDown *OAuthError
	if !errors.As(err, &slowDown) || slowDown.Outcome != oauthvocab.OutcomeSlowDown || slowDown.Code != "slow_down" || slowDown.RetryAfter <= 0 {
		t.Fatalf("slow_down poll = %+v, want a positive-RetryAfter slow_down", err)
	}

	h.approveDeviceGrant(t, started.DeviceCode, []string{"org/repo"})

	// The interval still governs polling after approval, on the same clock.
	h.now = h.now.Add(storage.DeviceAuthorizationPollInterval)
	token, err := h.oauth.ExchangeDeviceCode(context.Background(), OAuthDeviceTokenRequest{GrantType: OAuthDeviceCodeGrantType, DeviceCode: started.DeviceCode, ClientID: clientID})
	if err != nil {
		t.Fatalf("ExchangeDeviceCode after approval: %v", err)
	}
	if token.Issued.Token == "" || !IsTokenShapeValid(token.Issued.Token) {
		t.Fatalf("issued token shape invalid: %+v", token)
	}
	if token.Scope != OAuthScope {
		t.Fatalf("token scope = %q, want every requested scope %q", token.Scope, OAuthScope)
	}
	if token.Issued.Credential.RepositoryScopes == nil || len(token.Issued.Credential.RepositoryScopes) != 1 || token.Issued.Credential.RepositoryScopes[0] != "org/repo" {
		t.Fatalf("issued credential repository scopes = %v, want [org/repo]", token.Issued.Credential.RepositoryScopes)
	}
	if token.ClientKind != storage.OAuthClientKindDynamic {
		t.Fatalf("token client kind = %s, want dynamic", token.ClientKind)
	}

	// Redeemed: the record can never be polled again.
	h.now = h.now.Add(storage.DeviceAuthorizationPollInterval)
	_, err = h.oauth.ExchangeDeviceCode(context.Background(), OAuthDeviceTokenRequest{GrantType: OAuthDeviceCodeGrantType, DeviceCode: started.DeviceCode, ClientID: clientID})
	if outcomeOf(err) != oauthvocab.OutcomeInvalidGrant {
		t.Fatalf("re-poll after redemption outcome = %s, want %s", outcomeOf(err), oauthvocab.OutcomeInvalidGrant)
	}
}

func TestOAuthServiceDeviceGrantDenied(t *testing.T) {
	h := newOAuthHarness(t)
	clientID := h.register(t)
	started, err := h.oauth.StartDeviceAuthorization(context.Background(), OAuthDeviceAuthorizationRequest{ClientID: clientID})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.devices.DenyForOAuth(context.Background(), webPrincipal([]string{"org/repo"}), storage.HashDeviceCode(started.DeviceCode)); err != nil {
		t.Fatal(err)
	}
	_, err = h.oauth.ExchangeDeviceCode(context.Background(), OAuthDeviceTokenRequest{GrantType: OAuthDeviceCodeGrantType, DeviceCode: started.DeviceCode, ClientID: clientID})
	if outcomeOf(err) != oauthvocab.OutcomeAccessDenied {
		t.Fatalf("poll after deny outcome = %s, want %s", outcomeOf(err), oauthvocab.OutcomeAccessDenied)
	}
}

func TestOAuthServiceDeviceGrantExpires(t *testing.T) {
	h := newOAuthHarness(t)
	clientID := h.register(t)
	started, err := h.oauth.StartDeviceAuthorization(context.Background(), OAuthDeviceAuthorizationRequest{ClientID: clientID})
	if err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(storage.DeviceAuthorizationTTL + time.Second)
	_, err = h.oauth.ExchangeDeviceCode(context.Background(), OAuthDeviceTokenRequest{GrantType: OAuthDeviceCodeGrantType, DeviceCode: started.DeviceCode, ClientID: clientID})
	if outcomeOf(err) != oauthvocab.OutcomeExpired {
		t.Fatalf("poll after expiry outcome = %s, want %s", outcomeOf(err), oauthvocab.OutcomeExpired)
	}
}

func TestOAuthServiceDeviceGrantClientMismatch(t *testing.T) {
	h := newOAuthHarness(t)
	clientID := h.register(t)
	other, err := h.oauth.Register(context.Background(), OAuthRegistrationRequest{ClientName: "other", RedirectURIs: []string{testRedirect}})
	if err != nil {
		t.Fatal(err)
	}
	started, err := h.oauth.StartDeviceAuthorization(context.Background(), OAuthDeviceAuthorizationRequest{ClientID: clientID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.oauth.ExchangeDeviceCode(context.Background(), OAuthDeviceTokenRequest{GrantType: OAuthDeviceCodeGrantType, DeviceCode: started.DeviceCode, ClientID: other.ClientID})
	if outcomeOf(err) != oauthvocab.OutcomeClientMismatch {
		t.Fatalf("poll with the wrong client outcome = %s, want %s", outcomeOf(err), oauthvocab.OutcomeClientMismatch)
	}
}

func TestOAuthServiceStartDeviceAuthorizationRefusals(t *testing.T) {
	h := newOAuthHarness(t)
	clientID := h.register(t)
	for name, tc := range map[string]struct {
		request      OAuthDeviceAuthorizationRequest
		wantOutcome  string
		wantRedirect bool
	}{
		"unknown client":       {request: OAuthDeviceAuthorizationRequest{ClientID: "acrc_unknown"}, wantOutcome: oauthvocab.OutcomeInvalidClient},
		"unsupported scope":    {request: OAuthDeviceAuthorizationRequest{ClientID: clientID, Scope: "not_a_scope"}, wantOutcome: oauthvocab.OutcomeInvalidScope},
		"unsupported resource": {request: OAuthDeviceAuthorizationRequest{ClientID: clientID, Resource: "https://other.example.test/mcp"}, wantOutcome: oauthvocab.OutcomeInvalidTarget},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := h.oauth.StartDeviceAuthorization(context.Background(), tc.request)
			if outcomeOf(err) != tc.wantOutcome {
				t.Fatalf("outcome = %s, want %s", outcomeOf(err), tc.wantOutcome)
			}
			var oauthErr *OAuthError
			if errors.As(err, &oauthErr) && oauthErr.Redirectable {
				t.Fatal("StartDeviceAuthorization refusal must never be redirectable: RFC 8628 has no redirect step")
			}
		})
	}
}

func TestOAuthServiceExchangeDeviceCodeRefusals(t *testing.T) {
	h := newOAuthHarness(t)
	clientID := h.register(t)
	for name, request := range map[string]OAuthDeviceTokenRequest{
		"wrong grant type":      {GrantType: "authorization_code", DeviceCode: "x", ClientID: clientID},
		"empty device code":     {GrantType: OAuthDeviceCodeGrantType, DeviceCode: "", ClientID: clientID},
		"malformed device code": {GrantType: OAuthDeviceCodeGrantType, DeviceCode: "not-base64url-shaped-or-right-length", ClientID: clientID},
		"unknown device code":   {GrantType: OAuthDeviceCodeGrantType, DeviceCode: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ClientID: clientID},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := h.oauth.ExchangeDeviceCode(context.Background(), request)
			if err == nil {
				t.Fatal("want a refusal")
			}
		})
	}
	wantGrantType := outcomeOf(func() error {
		_, err := h.oauth.ExchangeDeviceCode(context.Background(), OAuthDeviceTokenRequest{GrantType: "authorization_code", DeviceCode: "x", ClientID: clientID})
		return err
	}())
	if wantGrantType != oauthvocab.OutcomeUnsupportedGrantType {
		t.Fatalf("wrong grant type outcome = %s, want %s", wantGrantType, oauthvocab.OutcomeUnsupportedGrantType)
	}
}

// TestDeviceFlowServicePollRefusesAnOAuthDeviceGrant pins the CHAOS-6233
// structural fix (three prior review rounds each found a way a SEPARATE
// pre-check disagreed with Poll's own normalization/lookup): the conflict
// check now lives INSIDE Poll itself, on the already-normalized,
// already-resolved device code, so there is exactly one normalization and
// one lookup for both the legacy and the OAuth-aware redemption paths.
func TestDeviceFlowServicePollRefusesAnOAuthDeviceGrant(t *testing.T) {
	h := newOAuthHarness(t)
	clientID := h.register(t)
	started, err := h.oauth.StartDeviceAuthorization(context.Background(), OAuthDeviceAuthorizationRequest{ClientID: clientID, Scope: ScopeContextRead})
	if err != nil {
		t.Fatal(err)
	}

	// The guard fires before the record's state is even considered: an
	// UNDECIDED OAuth device grant is refused the same as an approved one --
	// Poll must never leak "this is a real, pending OAuth device code" to the
	// legacy caller either.
	_, err = h.devices.Poll(context.Background(), started.DeviceCode)
	if !errors.Is(err, ErrOAuthDeviceGrantConflict) {
		t.Fatalf("Poll on a pending OAuth device grant: err = %v, want ErrOAuthDeviceGrantConflict", err)
	}
	var pollError *DevicePollError
	if !errors.As(err, &pollError) || pollError.Kind != DevicePollInvalidGrant {
		t.Fatalf("Poll on a pending OAuth device grant: pollError = %+v, want Kind=invalid_grant", pollError)
	}

	h.approveDeviceGrant(t, started.DeviceCode, []string{"org/repo"})
	h.now = h.now.Add(storage.DeviceAuthorizationPollInterval)
	_, err = h.devices.Poll(context.Background(), started.DeviceCode)
	if !errors.Is(err, ErrOAuthDeviceGrantConflict) {
		t.Fatalf("Poll on an approved OAuth device grant: err = %v, want ErrOAuthDeviceGrantConflict", err)
	}
}

// TestDeviceFlowServicePollUnaffectedByLegacyDeviceCodes confirms the
// CHAOS-6233 guard is additive: a device code from the pre-existing
// typed-user-code flow (Start, no associated storage.OAuthDeviceGrant row)
// polls exactly as it always has.
func TestDeviceFlowServicePollUnaffectedByLegacyDeviceCodes(t *testing.T) {
	h := newOAuthHarness(t)
	started, err := h.devices.Start(context.Background(), DeviceAuthorizationHints{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.devices.Poll(context.Background(), started.DeviceCode)
	if !errors.Is(err, ErrDeviceAuthorizationPending) {
		t.Fatalf("Poll on a legacy device code: err = %v, want ErrDeviceAuthorizationPending", err)
	}
}

// TestDeviceFlowServicePollFailsClosedOnDeviceGrantLookupError pins the
// fail-closed half of the CHAOS-6233 guard at its new home inside Poll.
func TestDeviceFlowServicePollFailsClosedOnDeviceGrantLookupError(t *testing.T) {
	h := newOAuthHarness(t)
	clientID := h.register(t)
	started, err := h.oauth.StartDeviceAuthorization(context.Background(), OAuthDeviceAuthorizationRequest{ClientID: clientID})
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return h.now }
	service, err := NewService(h.creds, ServiceOptions{Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	failing, err := NewDeviceFlowService(h.store, service, DeviceFlowOptions{Now: clock, OAuthDeviceGrants: failingDeviceGrantLookup{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = failing.Poll(context.Background(), started.DeviceCode)
	if !errors.Is(err, ErrOAuthDeviceGrantLookupUnavailable) {
		t.Fatalf("Poll on a lookup failure: err = %v, want ErrOAuthDeviceGrantLookupUnavailable", err)
	}
}

// failingDeviceGrantLookup makes every GetDeviceGrant call fail with a
// generic (non-ErrNotFound) error.
type failingDeviceGrantLookup struct{}

var errDeviceGrantLookupDown = errors.New("device grant lookup unavailable")

func (failingDeviceGrantLookup) GetDeviceGrant(context.Context, storage.DeviceCodeHash) (storage.OAuthDeviceGrant, error) {
	return storage.OAuthDeviceGrant{}, errDeviceGrantLookupDown
}

// TestNewDeviceFlowServiceRequiresOAuthDeviceGrants pins the construction-time
// half of the CHAOS-6233 guard: a caller cannot end up with the guard
// silently skipped by forgetting to wire OAuthDeviceGrants. A deployment
// with no OAuth login at all must say so explicitly (NoOAuthDeviceGrants{}),
// never by leaving the field unset -- the one thing this test proves an
// unset (nil, or a nil-holding-interface) field can no longer do is
// construct successfully.
func TestNewDeviceFlowServiceRequiresOAuthDeviceGrants(t *testing.T) {
	h := newOAuthHarness(t)
	clock := func() time.Time { return h.now }
	service, err := NewService(h.creds, ServiceOptions{Now: clock})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := NewDeviceFlowService(h.store, service, DeviceFlowOptions{Now: clock}); !errors.Is(err, ErrInvalidDeviceFlow) {
		t.Fatalf("NewDeviceFlowService with OAuthDeviceGrants unset: err = %v, want ErrInvalidDeviceFlow", err)
	}

	// A typed nil satisfying the interface (the shape a wiring bug would
	// actually produce -- an *memory.OAuthStore left nil, not a bare Go
	// nil) must be refused the same way: an interface holding a typed nil
	// is itself non-nil and would slip past a bare `== nil` check.
	var typedNilStore *failingDeviceGrantLookupPointer
	if _, err := NewDeviceFlowService(h.store, service, DeviceFlowOptions{Now: clock, OAuthDeviceGrants: typedNilStore}); !errors.Is(err, ErrInvalidDeviceFlow) {
		t.Fatalf("NewDeviceFlowService with a typed-nil OAuthDeviceGrants: err = %v, want ErrInvalidDeviceFlow", err)
	}

	if _, err := NewDeviceFlowService(h.store, service, DeviceFlowOptions{Now: clock, OAuthDeviceGrants: NoOAuthDeviceGrants{}}); err != nil {
		t.Fatalf("NewDeviceFlowService with the explicit NoOAuthDeviceGrants{} opt-out: %v", err)
	}
}

// failingDeviceGrantLookupPointer exists only so
// TestNewDeviceFlowServiceRequiresOAuthDeviceGrants can construct a typed nil
// pointer that satisfies OAuthDeviceGrantLookup.
type failingDeviceGrantLookupPointer struct{}

func (*failingDeviceGrantLookupPointer) GetDeviceGrant(context.Context, storage.DeviceCodeHash) (storage.OAuthDeviceGrant, error) {
	return storage.OAuthDeviceGrant{}, storage.ErrNotFound
}
