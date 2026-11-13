package entitlements_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

const disabledBody = `{"detail":"Not found","code":"acr_organization_disabled"}`

// The fresh read has three positive answers; every other answer is the
// unavailable error, and a disabled organization is never "unknown".
func TestOrganizationStateNowSeparatesLiveDisabledUnknownAndOutage(t *testing.T) {
	cases := map[string]struct {
		handler     http.HandlerFunc
		state       entitlements.OrganizationState
		unavailable bool
	}{
		"200":                 {respond(http.StatusOK, `{}`), entitlements.OrganizationLive, false},
		"disabled 404":        {respond(http.StatusNotFound, disabledBody), entitlements.OrganizationDisabled, false},
		"unknown 404":         {respond(http.StatusNotFound, unknownBody), entitlements.OrganizationUnknown, false},
		"uncoded 404":         {respond(http.StatusNotFound, `{"detail":"Not found"}`), 0, true},
		"other coded 404":     {respond(http.StatusNotFound, `{"code":"acr_organization_suspended"}`), 0, true},
		"503 with the code":   {respond(http.StatusServiceUnavailable, disabledBody), 0, true},
		"401 with the code":   {respond(http.StatusUnauthorized, disabledBody), 0, true},
		"timeout":             {abortAfterClientGivesUp, 0, true},
		"404 body not JSON":   {respond(http.StatusNotFound, `<html></html>`), 0, true},
		"404 code not string": {respond(http.StatusNotFound, `{"code":7}`), 0, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			client := serveClient(t, tc.handler, entitlements.Config{Timeout: time.Second})

			state, err := client.OrganizationStateNow(context.Background(), knownOrg)

			if tc.unavailable {
				if err == nil {
					t.Fatalf("OrganizationStateNow() = (%v, nil), want the unavailable error", state)
				}
				if gone, err := client.OrganizationUnknown(context.Background(), knownOrg); err == nil || gone {
					t.Fatalf("OrganizationUnknown() = (%v, %v), want (false, unavailable)", gone, err)
				}
				return
			}
			if err != nil || state != tc.state {
				t.Fatalf("OrganizationStateNow() = (%v, %v), want (%v, nil)", state, err, tc.state)
			}
			gone, err := client.OrganizationUnknown(context.Background(), knownOrg)
			if err != nil || gone != (tc.state == entitlements.OrganizationUnknown) {
				t.Fatalf("OrganizationUnknown() = (%v, %v), want (%v, nil)", gone, err, tc.state == entitlements.OrganizationUnknown)
			}
		})
	}
}

// The cached read answers a disabled organization with its own error, and
// never caches it: a reactivation is seen on the next call.
func TestOrganizationKnownAnswersDisabledAndNeverCachesIt(t *testing.T) {
	var hits atomic.Int32
	client := serveClient(t, countingResponder(&hits, http.StatusNotFound, disabledBody), entitlements.Config{})
	for range 3 {
		err := client.OrganizationKnown(context.Background(), knownOrg)
		if !errors.Is(err, entitlements.ErrOrganizationDisabled) || errors.Is(err, entitlements.ErrOrganizationUnknown) {
			t.Fatalf("OrganizationKnown() = %v, want ErrOrganizationDisabled only", err)
		}
	}
	if hits.Load() != 3 {
		t.Fatalf("a disabled organization reached Dev Health %d times, want 3 (never cached)", hits.Load())
	}
	if granted, err := client.HasEntitlement(context.Background(), knownOrg, "agent_context_runtime"); granted || !errors.Is(err, entitlements.ErrOrganizationDisabled) {
		t.Fatalf("HasEntitlement() = (%v, %v), want (false, ErrOrganizationDisabled)", granted, err)
	}
}
