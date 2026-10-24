package embedprovider

import "errors"

const startupNameUnlisted = "unlisted"

// The startup line names a provider or model only when it equals an entry of
// these closed lists, which are owned by this code: provider kinds from the
// package doc in config.go, model ids from config.go and provider_test.go. A
// value outside the lists is never logged, not even in part.
var (
	startupKnownProviders = map[string]bool{"lmstudio": true, "ollama": true, "tei": true, "vllm": true, "openai": true}
	startupKnownModels    = map[string]bool{
		"qwen3-embedding": true, "embeddinggemma": true, "nomic-embed-text": true,
		"text-embedding-3-small": true, "text-embedding-3-large": true, "text-embedding-nomic-embed-text-v1.5": true,
	}
)

func startupName(value string, known map[string]bool) string {
	if value == "" || known[value] {
		return value
	}
	return startupNameUnlisted
}

// StartupAttributes returns the startup-log fields that disclose the embed
// provider state the binary will use: names, mode and presence only, never a
// URL, key or secret. Values come from the resolved ConfigFromEnv result, so a
// key supplied through its _FILE variant counts as present. The query
// timeout, batch timeout and transport retry count are the values in force
// (0 when unconfigured or rejected: no value is in force). embed_config_valid
// is false when a base URL is set but the configuration is rejected.
func StartupAttributes(lookup func(string) (string, bool)) []any {
	cfg, err := ConfigFromEnv(lookup)
	valid := true
	switch {
	case errors.Is(err, ErrNotConfigured):
		cfg = Config{}
	case err != nil:
		valid = false
		cfg = Config{
			Provider:  envString(lookup, EnvProvider, ""),
			Model:     envString(lookup, EnvModel, ""),
			Dimension: 0,
		}
	}
	return []any{
		"embed_provider_configured", Configured(lookup),
		"embed_config_valid", valid,
		"embed_provider", startupName(cfg.Provider, startupKnownProviders),
		"embed_model_configured", cfg.Model != "",
		"embed_model", startupName(cfg.Model, startupKnownModels),
		"embed_dimension", cfg.Dimension,
		"embed_key_present", cfg.APIKey != "",
		"embed_query_timeout_ms", cfg.Timeout.Milliseconds(),
		"embed_max_transport_retries", cfg.MaxTransportRetries,
		"embed_batch_timeout_ms", cfg.BatchTimeout.Milliseconds(),
	}
}
