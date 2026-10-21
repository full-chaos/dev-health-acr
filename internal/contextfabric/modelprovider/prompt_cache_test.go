package modelprovider

import "testing"

func TestPromptCacheIsOnForThePlatformConfigAndReachesBothRuntimes(t *testing.T) {
	cfg, err := ConfigFromEnv(lookupFrom(map[string]string{EnvAPIKey: "sk-test", EnvFallbackModel: "fb"}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.PromptCache {
		t.Fatal("the deployment's platform config does not enable the prompt cache")
	}
	if !runtimeConfigWithPhrasing(cfg, cfg.Model, nil).PromptCache || !fallbackRuntimeConfig(cfg).PromptCache {
		t.Fatal("PromptCache does not reach the primary and fallback runtime configs")
	}
	if (Config{}).PromptCache {
		t.Fatal("the zero Config enables the prompt cache")
	}
}
