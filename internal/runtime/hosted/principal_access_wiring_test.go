package hosted

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
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
	remote, err := newEntitlement(config.Config{Environment: "production", DevHealthEntitlementURL: "https://ops.example.test", DevHealthEntitlementTokenFile: tokenFile}, entitlementDeps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	if principalAccessReader(remote) == nil {
		t.Fatal("remote entitlement mode composes no per-call access reader: user-class calls would not be re-checked")
	}
	local, err := newEntitlement(config.Config{Environment: "development"}, entitlementDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if principalAccessReader(local) != nil {
		t.Fatal("local entitlement mode composed an access reader")
	}
}

// rowOnlyEntitlement is an entitlement provider without the access read.
type rowOnlyEntitlement struct{ events *[]string }

func (rowOnlyEntitlement) HasEntitlement(context.Context, string, string) (bool, error) {
	return true, nil
}
func (rowOnlyEntitlement) Check(context.Context) error { return nil }
func (e rowOnlyEntitlement) Close() error {
	*e.events = append(*e.events, "entitlement.close")
	return nil
}

// open composes the provider's access read into the runtime the API serves
// from. In remote entitlement mode a provider without one is refused at
// startup (fail closed: user-class calls would not be re-checked); only
// local entitlement mode (development and test) runs without one.
func TestOpenComposesTheAccessReadAndRefusesToStartWithoutOneInRemoteMode(t *testing.T) {
	events := []string{}
	request := testBuildRequest(t, &events, "")
	provider := &fakeEntitlement{events: &events}
	request.factories.newEntitlement = func(config.Config, entitlementDeps) (entitlementChecker, error) { return provider, nil }
	runtime, err := open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got := runtime.Dependencies.Runtime.PrincipalAccess; any(got) != any(provider) {
		t.Errorf("composed access reader = %v, want the entitlement provider", got)
	}
	if closeErr := runtime.Close(); closeErr != nil {
		t.Error(closeErr)
	}

	events = nil
	request = testBuildRequest(t, &events, "")
	request.factories.newEntitlement = func(config.Config, entitlementDeps) (entitlementChecker, error) {
		return rowOnlyEntitlement{events: &events}, nil
	}
	if request.config.EntitlementMode() != config.EntitlementModeRemote {
		t.Fatal("precondition: the test configuration is remote entitlement mode")
	}
	if runtime, err := open(context.Background(), request); err == nil || !strings.Contains(err.Error(), "principal access read") {
		if runtime != nil {
			_ = runtime.Close()
		}
		t.Fatalf("remote mode without an access read: open err = %v, want a refusal naming the principal access read", err)
	}
	if !slices.Contains(events, "entitlement.close") {
		t.Fatalf("the refused start did not close the entitlement provider: %v", events)
	}

	events = nil
	request = testBuildRequest(t, &events, "")
	request.config.DevHealthEntitlementURL, request.config.DevHealthEntitlementTokenFile = "", ""
	if request.config.EntitlementMode() != config.EntitlementModeLocal {
		t.Fatal("precondition: the test configuration is local entitlement mode")
	}
	request.factories.newEntitlement = func(config.Config, entitlementDeps) (entitlementChecker, error) {
		return rowOnlyEntitlement{events: &events}, nil
	}
	runtime, err = open(context.Background(), request)
	if err != nil {
		t.Fatalf("local mode without an access read: open err = %v, want it to start", err)
	}
	if runtime.Dependencies.Runtime.PrincipalAccess != nil {
		t.Errorf("local mode composed access reader = %v, want nil", runtime.Dependencies.Runtime.PrincipalAccess)
	}
	if closeErr := runtime.Close(); closeErr != nil {
		t.Error(closeErr)
	}
}
