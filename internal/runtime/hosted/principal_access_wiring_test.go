package hosted

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

// CHAOS-12092: the hosted remote entitlement provider is the per-call access
// reader, so every user-class call is re-checked live; the local development
// provider is not, and the read is off there (NewApp warns). If the remote
// client stopped satisfying the reader, the read would turn off silently in
// production, so this pins the composed state, not only the type assertion.
func TestTheRemoteEntitlementClientIsThePerCallAccessReader(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("svc_acr_test_token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	remote, err := newEntitlement(config.Config{Environment: "production", DevHealthEntitlementURL: "https://ops.example.test", DevHealthEntitlementTokenFile: tokenFile})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	if principalAccessReader(remote) == nil {
		t.Fatal("remote entitlement mode composes no per-call access reader: user-class calls would not be re-checked")
	}
	local, err := newEntitlement(config.Config{Environment: "development"})
	if err != nil {
		t.Fatal(err)
	}
	if principalAccessReader(local) != nil {
		t.Fatal("local entitlement mode composed an access reader")
	}
}

// readingEntitlement is the test entitlement provider that also serves the
// principal access read.
type readingEntitlement struct{ fakeEntitlement }

func (readingEntitlement) PrincipalAccess(context.Context, string, string, string) (entitlements.PrincipalAccess, error) {
	return entitlements.PrincipalAccess{}, nil
}

// open composes the provider's access read into the runtime the API serves
// from, and composes none for a provider without one.
func TestOpenComposesTheEntitlementProvidersAccessRead(t *testing.T) {
	for _, reads := range []bool{true, false} {
		events := []string{}
		request := testBuildRequest(t, &events, "")
		var provider entitlementChecker = &fakeEntitlement{events: &events}
		if reads {
			provider = &readingEntitlement{fakeEntitlement{events: &events}}
		}
		request.factories.newEntitlement = func(config.Config) (entitlementChecker, error) { return provider, nil }
		runtime, err := open(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		got := runtime.Dependencies.Runtime.PrincipalAccess
		if reads && any(got) != any(provider) {
			t.Errorf("composed access reader = %v, want the entitlement provider", got)
		}
		if !reads && got != nil {
			t.Errorf("composed access reader = %v for a provider without one, want nil", got)
		}
		if closeErr := runtime.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}
}
