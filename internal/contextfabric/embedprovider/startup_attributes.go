package embedprovider

import (
	"errors"
	"regexp"
)

const startupNameRedacted = "invalid_name"

var startupNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:-]{0,63}$`)

// startupName passes only an identifier-shaped name (no spaces, quotes, "=",
// "@" or other credential punctuation, at most 64 characters); free text from
// the environment is replaced by a fixed marker so it never reaches the log.
func startupName(value string) string {
	if value == "" || startupNamePattern.MatchString(value) {
		return value
	}
	return startupNameRedacted
}

// StartupAttributes returns the startup-log fields that disclose the embed
// provider state the binary will use: names, mode and presence only, never a
// URL, key or secret. Values come from the resolved ConfigFromEnv result, so a
// key supplied through its _FILE variant counts as present. embed_config_valid
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
		"embed_provider", startupName(cfg.Provider),
		"embed_model", startupName(cfg.Model),
		"embed_dimension", cfg.Dimension,
		"embed_key_present", cfg.APIKey != "",
	}
}
