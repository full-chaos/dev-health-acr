package entitlements_test

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/proxycapture"
)

func TestTheEntitlementClientWithoutAProxyURLNeverDialsTheEnvironmentProxy(t *testing.T) {
	proxycapture.Check(t, func(ctx context.Context) {
		dir, err := os.MkdirTemp("", "entitlement-token-")
		if err != nil {
			panic(err)
		}
		defer os.RemoveAll(dir)
		tokenFile := filepath.Join(dir, "token")
		if err := os.WriteFile(tokenFile, []byte(proxycapture.Canary), 0o600); err != nil {
			panic(err)
		}
		base, _ := url.Parse("http://upstream.invalid")
		client, err := entitlements.New(entitlements.Config{
			BaseURL: base, TokenFile: tokenFile, Timeout: time.Second, MaxResponseBytes: 2048,
			PositiveCacheTTL: time.Minute, NegativeCacheTTL: time.Second, CacheCapacity: 2,
		})
		if err != nil {
			panic(err)
		}
		defer client.Close()
		proxycapture.MustAttemptDial(client.Check(ctx))
		_, err = client.HasEntitlement(ctx, "org", "agent_context_runtime")
		proxycapture.MustAttemptDial(err)
	}).AssertNothingReached(t)
}
