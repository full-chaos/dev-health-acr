package modelprovider

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-go/llm"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/proxycapture"
)

func TestTheModelClientsNeverDialTheEnvironmentProxy(t *testing.T) {
	proxycapture.Check(t, func(ctx context.Context) {
		withKey, err := ConfigFromEnv(func(name string) (string, bool) {
			if name == EnvAPIKey {
				return proxycapture.Canary, true
			}
			return "", false
		})
		if err != nil {
			panic(err)
		}
		for _, cfg := range []Config{withKey, {Timeout: withKey.Timeout}} {
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, proxycapture.Target, nil)
			if err != nil {
				panic(err)
			}
			request.Header.Set("X-Canary", proxycapture.Canary)
			if response, err := newModelHTTPClient(cfg).Do(request); err == nil {
				_ = response.Body.Close()
			}
		}
	}).AssertNothingReached(t)
}

// The production path: the clients newLLMClients builds through dev-health-go llm, with the API key the model calls
// carry, for every provider kind. A regression in that dependency that put the environment proxy back fails here.
func TestTheLLMClientsNeverDialTheEnvironmentProxy(t *testing.T) {
	proxycapture.Check(t, func(ctx context.Context) {
		for _, provider := range []string{"", "anthropic", "gemini", "qwen"} {
			clients, err := newLLMClients(Config{Provider: provider, APIKey: llm.NewSecret(proxycapture.Canary), BaseURL: "http://upstream.invalid", Timeout: 5 * time.Second}, "model-a")
			if err != nil {
				panic(err)
			}
			for _, client := range clients {
				_, err = client.Complete(ctx, llm.Request{Prompt: "ping"})
				proxycapture.MustFailWith(err, "endpoint could not be reached")
				_ = client.Close()
			}
		}
	}).AssertNothingReached(t)
}
