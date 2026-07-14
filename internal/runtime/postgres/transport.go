package postgres

import (
	"crypto/tls"
	"errors"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
)

func ValidateDSNTransport(dsn string, allowInsecure bool) error {
	parsed, err := pgx.ParseConfig(dsn)
	if err != nil {
		return errors.New("invalid PostgreSQL configuration")
	}
	if allowInsecure {
		return nil
	}
	if !verifiedEndpoint(parsed.Host, parsed.TLSConfig) {
		return errors.New("PostgreSQL DSN must use verified TLS")
	}
	for _, fallback := range parsed.Fallbacks {
		if fallback == nil || !verifiedEndpoint(fallback.Host, fallback.TLSConfig) {
			return errors.New("PostgreSQL DSN must use verified TLS")
		}
	}
	return nil
}

func verifiedEndpoint(host string, configuration *tls.Config) bool {
	return filepath.IsAbs(host) || verifiedTLS(configuration)
}

func verifiedTLS(configuration *tls.Config) bool {
	return configuration != nil && !configuration.InsecureSkipVerify && strings.TrimSpace(configuration.ServerName) != ""
}
