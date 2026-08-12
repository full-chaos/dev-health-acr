package projectionrun

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrOrgLocked means another process currently holds the organization's
// projection lock. It is expected, not exceptional: the coordinator simply
// skips that organization for this tick and retries on the next one.
var ErrOrgLocked = errors.New("projectionrun: organization is locked by another projector")

// OrgLocker enforces the CHAOS-3753 amendment: at most one in-flight
// ApplyProjectionBatch pass per organization, across every source and every
// acr-projector replica. Lock must not block indefinitely -- see
// PostgresOrgLocker's use of pg_try_advisory_lock, not pg_advisory_lock.
type OrgLocker interface {
	Lock(ctx context.Context, orgID string) (unlock func() error, err error)
}

// NoopOrgLocker performs no cross-process locking. It exists for tests and
// for single-replica deployments that accept the Coordinator's in-process
// mutex (see coordinator.go) as sufficient; production composition should
// use PostgresOrgLocker instead -- see docs/design/context-fabric-projection-worker.md.
type NoopOrgLocker struct{}

func (NoopOrgLocker) Lock(context.Context, string) (func() error, error) {
	return func() error { return nil }, nil
}

// advisoryLockClassID namespaces this package's PostgreSQL advisory locks so
// they cannot collide with unrelated locks elsewhere in the process (e.g.
// migrations/postgres.Runner's own fixed advisory lock ID). It has no
// meaning beyond "the CHAOS-3753 per-organization projection lock".
const advisoryLockClassID = 20260812

// PostgresOrgLocker is the production OrgLocker: a session-level PostgreSQL
// advisory lock keyed by the organization ID, held for the duration of one
// organization's full multi-source projection pass. It works across every
// acr-projector replica because it lives in the database, not in process
// memory -- see docs/design/context-fabric-projection-worker.md for why
// that's required instead of an in-process mutex plus replicas: 1.
type PostgresOrgLocker struct {
	db *sql.DB
}

func NewPostgresOrgLocker(db *sql.DB) (*PostgresOrgLocker, error) {
	if db == nil {
		return nil, errors.New("projectionrun: postgres org locker requires a database")
	}
	return &PostgresOrgLocker{db: db}, nil
}

// Lock acquires the org's advisory lock non-blockingly (pg_try_advisory_lock,
// never pg_advisory_lock): if another replica holds it, Lock returns
// ErrOrgLocked immediately rather than blocking a worker goroutine until
// that replica finishes. The lock is session-scoped, so it is held on a
// single pinned *sql.Conn from acquisition until unlock.
func (l *PostgresOrgLocker) Lock(ctx context.Context, orgID string) (func() error, error) {
	if l == nil || l.db == nil {
		return nil, errors.New("projectionrun: postgres org locker is not configured")
	}
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire org lock connection: %w", err)
	}
	var acquired bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1, hashtext($2))`, advisoryLockClassID, orgID).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("acquire org lock: %w", err)
	}
	if !acquired {
		_ = conn.Close()
		return nil, ErrOrgLocked
	}
	return func() error {
		defer conn.Close()
		_, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1, hashtext($2))`, advisoryLockClassID, orgID)
		return err
	}, nil
}
