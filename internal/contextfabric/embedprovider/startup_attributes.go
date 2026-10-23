package embedprovider

import (
	"strconv"
)

// StartupAttributes returns the startup-log fields that disclose the embed
// provider state: names, mode and presence only, never a URL, key or secret.
// It reads the raw environment, so it still reports a misconfigured value.
func StartupAttributes(lookup func(string) (string, bool)) []any {
	dimension := 0
	if parsed, err := strconv.Atoi(envString(lookup, EnvDimension, "")); err == nil {
		dimension = parsed
	}
	return []any{
		"embed_provider_configured", Configured(lookup),
		"embed_provider", envString(lookup, EnvProvider, ""),
		"embed_model", envString(lookup, EnvModel, ""),
		"embed_dimension", dimension,
		"embed_key_present", envString(lookup, EnvAPIKey, "") != "" || envString(lookup, EnvAPIKey+"_FILE", "") != "",
	}
}
