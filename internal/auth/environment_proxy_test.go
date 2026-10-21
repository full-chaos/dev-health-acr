package auth

import (
	"context"
	"net/http"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/proxycapture"
)

// The metadata URL must be https, so a proxied fetch shows as a CONNECT to the capture proxy.
func TestASuppliedMetadataClientWithoutATransportNeverDialsTheEnvironmentProxy(t *testing.T) {
	proxycapture.Check(t, func(ctx context.Context) {
		_, err := NewClientMetadataFetcher(&http.Client{}).Fetch(ctx, "https://upstream.invalid/oauth/client.json")
		proxycapture.MustFailWith(err, "fetch_failed")
	}).AssertNothingReached(t)
}
