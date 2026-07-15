package config

import (
	"errors"
	"strings"
)

func validateWebAssertionConfig(cfg Config) error {
	configured := []string{cfg.WebAssertionIssuer, cfg.WebAssertionAudience, cfg.WebAssertionJWKSFile}
	empty := 0
	for _, value := range configured {
		if strings.TrimSpace(value) == "" {
			empty++
		}
	}
	if empty == 0 || empty == len(configured) {
		return nil
	}
	switch {
	case strings.TrimSpace(cfg.WebAssertionIssuer) == "":
		return errors.New("ACR_WEB_ASSERTION_ISSUER is required when web assertions are configured")
	case strings.TrimSpace(cfg.WebAssertionAudience) == "":
		return errors.New("ACR_WEB_ASSERTION_AUDIENCE is required when web assertions are configured")
	default:
		return errors.New("ACR_WEB_ASSERTION_JWKS_FILE is required when web assertions are configured")
	}
}
