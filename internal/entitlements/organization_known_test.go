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

const knownOrg = "aaaaaaaa-0000-4000-8000-000000000002"

type organizationKnower interface {
	OrganizationKnown(ctx context.Context, orgID string) error
}

func organizationKnown(t *testing.T, client *entitlements.Client) organizationKnower {
	t.Helper()
	knower, ok := any(client).(organizationKnower)
	if !ok {
		t.Fatal("entitlements.Client has no OrganizationKnown")
	}
	return knower
}

func countingResponder(hits *atomic.Int32, status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// Only the coded 404 is "gone"; an uncoded 404, a 5xx and a timeout are the
// unavailable error, never gone.
func TestOrganizationKnownSeparatesTheCodedNotFoundFromAnOutage(t *testing.T) {
	live := `{"schema_version":"acr_entitlement.v1","org_id":"` + knownOrg + `","agent_context_runtime":true}`
	cases := map[string]struct {
		handler http.HandlerFunc
		gone    bool
		reason  string
	}{
		"200":            {respond(http.StatusOK, live), false, ""},
		"coded 404":      {respond(http.StatusNotFound, unknownBody), true, ""},
		"uncoded 404":    {respond(http.StatusNotFound, `{"detail":"Not found"}`), false, "status"},
		"503":            {respond(http.StatusServiceUnavailable, unknownBody), false, "status"},
		"500":            {respond(http.StatusInternalServerError, ``), false, "status"},
		"timeout":        {func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, false, "transport"},
		"200 not schema": {respond(http.StatusOK, `{}`), false, "decode"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			client := serveClient(t, tc.handler, entitlements.Config{Timeout: time.Second})
			err := organizationKnown(t, client).OrganizationKnown(context.Background(), knownOrg)
			switch {
			case name == "200":
				if err != nil {
					t.Fatalf("OrganizationKnown() = %v, want nil", err)
				}
			case tc.gone:
				if !errors.Is(err, entitlements.ErrOrganizationUnknown) {
					t.Fatalf("OrganizationKnown() = %v, want ErrOrganizationUnknown", err)
				}
			default:
				if err == nil || errors.Is(err, entitlements.ErrOrganizationUnknown) {
					t.Fatalf("OrganizationKnown() = %v, want the unavailable error", err)
				}
				if got := entitlements.RejectionReason(err); got != tc.reason {
					t.Fatalf("RejectionReason = %q, want %q", got, tc.reason)
				}
			}
		})
	}
}

func TestOrganizationKnownCachesAKnownOrganizationButNeverAGoneOne(t *testing.T) {
	live := `{"schema_version":"acr_entitlement.v1","org_id":"` + knownOrg + `","agent_context_runtime":true}`
	var liveHits atomic.Int32
	client := serveClient(t, countingResponder(&liveHits, http.StatusOK, live), entitlements.Config{})
	knower := organizationKnown(t, client)
	for range 3 {
		if err := knower.OrganizationKnown(context.Background(), knownOrg); err != nil {
			t.Fatalf("OrganizationKnown() = %v", err)
		}
	}
	if liveHits.Load() != 1 {
		t.Fatalf("a known organization reached Dev Health %d times, want 1 (cached)", liveHits.Load())
	}
	// The same cache answers HasEntitlement.
	if granted, err := client.HasEntitlement(context.Background(), knownOrg, "agent_context_runtime"); err != nil || !granted || liveHits.Load() != 1 {
		t.Fatalf("HasEntitlement after OrganizationKnown = (%v, %v), hits %d; want the cached grant", granted, err, liveHits.Load())
	}

	var goneHits atomic.Int32
	gone := serveClient(t, countingResponder(&goneHits, http.StatusNotFound, unknownBody), entitlements.Config{})
	goneKnower := organizationKnown(t, gone)
	for range 3 {
		if err := goneKnower.OrganizationKnown(context.Background(), unknownOrg); !errors.Is(err, entitlements.ErrOrganizationUnknown) {
			t.Fatalf("OrganizationKnown() = %v, want ErrOrganizationUnknown", err)
		}
	}
	if goneHits.Load() != 3 {
		t.Fatalf("a gone organization reached Dev Health %d times, want 3 (never cached)", goneHits.Load())
	}
}
