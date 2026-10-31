package config

import (
	"errors"
	"strings"
)

// AuthWorkloadConfig is acr's side of the auth-service workload-token exchange as a client (CHAOS-11195). The zero
// value is off. It names the exchange and one subject credential file; internal/authworkload builds the client.
type AuthWorkloadConfig struct {
	// ExchangeURL (ACR_AUTH_WORKLOAD_EXCHANGE_URL) is auth-service's origin.
	ExchangeURL string
	// AllowPlainHTTP (ACR_AUTH_WORKLOAD_ALLOW_PLAIN_HTTP, default false) admits an http ExchangeURL.
	AllowPlainHTTP bool
	// Issuer (ACR_AUTH_WORKLOAD_ISSUER) is the token issuer URL every workload token must carry.
	Issuer string
	// SubjectFile (ACR_AUTH_WORKLOAD_SUBJECT_FILE) is a projected ServiceAccount token (Kubernetes);
	// BootstrapFile (ACR_AUTH_WORKLOAD_BOOTSTRAP_FILE) is a Compose bootstrap credential. Exactly one.
	SubjectFile, BootstrapFile string
}

// Enabled reports whether any auth workload setting is present: the client is then built, and a partial
// configuration is a start-up error.
func (a AuthWorkloadConfig) Enabled() bool {
	return a.ExchangeURL != "" || a.Issuer != "" || a.SubjectFile != "" || a.BootstrapFile != "" || a.AllowPlainHTTP
}

// Validate refuses a partial configuration. Errors name the variable, never its value.
func (a AuthWorkloadConfig) Validate() error {
	if !a.Enabled() {
		return nil
	}
	if a.ExchangeURL == "" {
		return errors.New("ACR_AUTH_WORKLOAD_EXCHANGE_URL is required when the auth workload client is configured")
	}
	if a.Issuer == "" {
		return errors.New("ACR_AUTH_WORKLOAD_ISSUER is required when the auth workload client is configured")
	}
	switch {
	case a.SubjectFile == "" && a.BootstrapFile == "":
		return errors.New("ACR_AUTH_WORKLOAD_SUBJECT_FILE or ACR_AUTH_WORKLOAD_BOOTSTRAP_FILE is required when the auth workload client is configured")
	case a.SubjectFile != "" && a.BootstrapFile != "":
		return errors.New("ACR_AUTH_WORKLOAD_SUBJECT_FILE and ACR_AUTH_WORKLOAD_BOOTSTRAP_FILE are mutually exclusive")
	}
	return nil
}

// LoadAuthWorkload reads and validates the ACR_AUTH_WORKLOAD_* variables. Load calls it too, so a command that
// needs only the client reads it the way acr-api does.
func LoadAuthWorkload(lookup func(string) (string, bool)) (AuthWorkloadConfig, error) {
	return authWorkloadValue(lookup)
}

func authWorkloadValue(lookup lookupEnv) (AuthWorkloadConfig, error) {
	plain, err := boolValue(lookup, "ACR_AUTH_WORKLOAD_ALLOW_PLAIN_HTTP", false)
	if err != nil {
		return AuthWorkloadConfig{}, err
	}
	cfg := AuthWorkloadConfig{
		ExchangeURL:    strings.TrimSpace(stringValue(lookup, "ACR_AUTH_WORKLOAD_EXCHANGE_URL", "")),
		AllowPlainHTTP: plain,
		Issuer:         strings.TrimSpace(stringValue(lookup, "ACR_AUTH_WORKLOAD_ISSUER", "")),
		SubjectFile:    strings.TrimSpace(stringValue(lookup, "ACR_AUTH_WORKLOAD_SUBJECT_FILE", "")),
		BootstrapFile:  strings.TrimSpace(stringValue(lookup, "ACR_AUTH_WORKLOAD_BOOTSTRAP_FILE", "")),
	}
	if err := cfg.Validate(); err != nil {
		return AuthWorkloadConfig{}, err
	}
	return cfg, nil
}
