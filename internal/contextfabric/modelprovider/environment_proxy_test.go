package modelprovider

import (
	"context"
	"net/http"
	"testing"

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
