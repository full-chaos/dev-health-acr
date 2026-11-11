package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// stateOrganizations answers every fresh read with err.
type stateOrganizations struct{ err error }

func (o stateOrganizations) OrganizationKnown(context.Context, string) error    { return o.err }
func (o stateOrganizations) OrganizationKnownNow(context.Context, string) error { return o.err }

// An RFC 8628 device code approved while its organization was live is not
// redeemed once Dev Health answers it as disabled or gone: access_denied with
// the organization outcome, and no credential; an outage is unavailable.
func TestOrganizationDisabled_deviceGrantRedeemIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		err     error
		outcome string
	}{
		"disabled": {ErrOrganizationDisabled, oauthvocab.OutcomeOrganizationDisabled},
		"gone":     {ErrOrganizationGone, oauthvocab.OutcomeOrganizationUnknown},
		"outage":   {errors.New("dev health unavailable"), ""},
	} {
		t.Run(name, func(t *testing.T) {
			h := newOAuthHarness(t)
			clientID := h.register(t)
			started, err := h.oauth.StartDeviceAuthorization(context.Background(), OAuthDeviceAuthorizationRequest{ClientID: clientID})
			if err != nil {
				t.Fatal(err)
			}
			h.approveDeviceGrant(t, started.DeviceCode, []string{"org/repo"})
			h.devices.organizations = stateOrganizations{err: tc.err}
			h.now = h.now.Add(storage.DeviceAuthorizationPollInterval)

			_, err = h.oauth.ExchangeDeviceCode(context.Background(), OAuthDeviceTokenRequest{GrantType: OAuthDeviceCodeGrantType, DeviceCode: started.DeviceCode, ClientID: clientID})

			var refusal *OAuthError
			switch {
			case tc.outcome == "":
				if errors.As(err, &refusal) || !errors.Is(err, ErrOAuthUnavailable) {
					t.Fatalf("redeem in an outage = %v, want ErrOAuthUnavailable", err)
				}
			case !errors.As(err, &refusal) || refusal.Outcome != tc.outcome || refusal.Code != string(DevicePollAccessDenied):
				t.Fatalf("redeem = %v, want access_denied with outcome %s", err, tc.outcome)
			}
			listed, err := h.creds.List(context.Background(), testOrg)
			if err != nil {
				t.Fatal(err)
			}
			if len(listed) != 0 {
				t.Fatalf("credentials = %d, want 0 (nothing issued)", len(listed))
			}
		})
	}
}
