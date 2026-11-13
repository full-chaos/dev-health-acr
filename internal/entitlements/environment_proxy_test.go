package entitlements_test

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/proxycapture"
)

func TestTheEntitlementClientWithoutAProxyURLNeverDialsTheEnvironmentProxy(t *testing.T) {
	proxycapture.Check(t, func(ctx context.Context) {
		base, _ := url.Parse("http://upstream.invalid")
		client, err := entitlements.New(entitlements.Config{
			BaseURL: base, Workload: &fakeTokenSource{token: proxycapture.Canary}, Timeout: time.Second, MaxResponseBytes: 2048,
			PositiveCacheTTL: time.Minute, NegativeCacheTTL: time.Second, CacheCapacity: 2,
		})
		if err != nil {
			panic(err)
		}
		defer client.Close()
		proxycapture.MustFailWith(client.Check(ctx), "entitlement service unavailable")
		_, err = client.HasEntitlement(ctx, "org", "agent_context_runtime")
		proxycapture.MustFailWith(err, "entitlement service unavailable")
	}).AssertNothingReached(t)
}
