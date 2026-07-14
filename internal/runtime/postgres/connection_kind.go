package postgres

import (
	"errors"
	"strings"
)

// ConnectionKind declares whether a PostgreSQL runtime DSN is connected to
// directly or through a PgBouncer pooler.
type ConnectionKind string

const (
	ConnectionKindDirect    ConnectionKind = "direct"
	ConnectionKindPgBouncer ConnectionKind = "pgbouncer"
)

// ParseConnectionKind parses an ACR_POSTGRES_CONNECTION_KIND value.
func ParseConnectionKind(raw string) (ConnectionKind, error) {
	switch strings.TrimSpace(raw) {
	case string(ConnectionKindDirect):
		return ConnectionKindDirect, nil
	case string(ConnectionKindPgBouncer):
		return ConnectionKindPgBouncer, nil
	default:
		return "", errors.New("ACR_POSTGRES_CONNECTION_KIND must be direct or pgbouncer")
	}
}

// ValidateConnectionKind rejects PostgreSQL connection configurations where
// the declared connection kind contradicts the presence of a PgBouncer
// administration DSN. Every entry point that opens a runtime PostgreSQL
// connection (the hosted server, the credential CLI, and the migration CLI)
// applies this check consistently so a PgBouncer deployment is never
// silently treated as a direct connection, or vice versa: "direct" must not
// carry an administration DSN, and "pgbouncer" must supply one so the
// session-mode probe in Open runs.
func ValidateConnectionKind(kind ConnectionKind, poolerAdminDSN string) error {
	hasPoolerAdmin := strings.TrimSpace(poolerAdminDSN) != ""
	switch kind {
	case ConnectionKindDirect:
		if hasPoolerAdmin {
			return errors.New("ACR_POSTGRES_CONNECTION_KIND=direct must not configure a PgBouncer administration DSN")
		}
	case ConnectionKindPgBouncer:
		if !hasPoolerAdmin {
			return errors.New("ACR_POSTGRES_CONNECTION_KIND=pgbouncer requires a PgBouncer administration DSN for the session-mode probe")
		}
	default:
		return errors.New("ACR_POSTGRES_CONNECTION_KIND must be direct or pgbouncer")
	}
	return nil
}
