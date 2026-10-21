package embedprovider

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/proxycapture"
)

// The production embedder: the openai-go client New builds, with the embed API key, sending one batch.
func TestTheEmbedderNeverDialsTheEnvironmentProxy(t *testing.T) {
	proxycapture.Check(t, func(ctx context.Context) {
		cfg := testConfig("http://upstream.invalid/v1")
		cfg.APIKey, cfg.AllowNoCredential = proxycapture.Canary, false
		embedder, err := New(cfg)
		if err != nil {
			panic(err)
		}
		_, err = embedder.Embed(ctx, []string{"ping"})
		proxycapture.MustAttemptDial(err)
	}).AssertNothingReached(t)
}
