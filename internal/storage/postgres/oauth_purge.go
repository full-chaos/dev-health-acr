package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// liveOAuthCredentialClause is the SQL fragment "credential cc is still
// usable at $3": unrevoked and not past its expiry. A credential is a
// standalone bearer (acr.client_credentials has no client_id column and no
// foreign key to acr.oauth_clients), so the ONLY link from a client to a
// credential it obtained is
//
//	request | device grant .device_code_hash
//	  -> acr.device_authorizations.redeemed_credential_id
//	  -> acr.client_credentials.credential_id
//
// Both purge statements below key on it: the request purge keeps a request
// whose redeemed credential is live, so the link survives for the
// credential's life (30 days, auth.DeviceCredentialLifetime); the client
// purge never deletes a client that link still reaches.
const liveOAuthCredentialClause = `cc.revoked_at IS NULL AND (cc.expires_at IS NULL OR cc.expires_at > $3)`

// expiredOAuthRequestPredicate selects an /authorize request past
// expires_at + grace ($1) whose device authorization did not redeem a
// credential that is live at $3. It is the ONE definition of "eligible": the
// request purge deletes by it and the remaining-eligible probe counts by it,
// so the two cannot drift.
const expiredOAuthRequestPredicate = `
      r.expires_at < $1
      AND NOT EXISTS (
          SELECT 1
          FROM acr.device_authorizations d
          JOIN acr.client_credentials cc ON cc.credential_id = d.redeemed_credential_id
          WHERE d.device_code_hash = r.device_code_hash
            AND ` + liveOAuthCredentialClause + `
      )`

// purgeExpiredOAuthRequestsSQL deletes one bounded batch of the requests
// expiredOAuthRequestPredicate selects.
//
// No table references acr.oauth_authorization_requests, and its only foreign
// key (device_code_hash -> acr.device_authorizations ON DELETE CASCADE) runs
// parent to child, so deleting a request row can never be blocked or cascade
// upward: the device_authorizations row behind it stays until its own
// statement below (expiredDeviceAuthorizationPredicate) takes it.
const purgeExpiredOAuthRequestsSQL = `
WITH expired AS (
    SELECT r.handle_hash FROM acr.oauth_authorization_requests r
    WHERE` + expiredOAuthRequestPredicate + `
    ORDER BY r.expires_at, r.handle_hash
    LIMIT $2
    FOR UPDATE OF r SKIP LOCKED
)
DELETE FROM acr.oauth_authorization_requests r
USING expired
WHERE r.handle_hash = expired.handle_hash`

// idleOAuthClientPredicate selects a dynamic (RFC 7591) client registered
// before $1 that is idle: no request row left (rows are kept while inside the
// purge grace or while backing a live credential), no device grant created
// since $1, and no live credential (at $3) reachable through any device grant
// of theirs. The client_id pattern is the same shape migration 0040 forces on
// every row of the table; it stays in the predicate so a pre-registered
// client, should one ever be added under a different id shape, is never
// deleted here.
const idleOAuthClientPredicate = `
      c.created_at < $1
      AND c.client_id ~ '^acrc_[0-9a-f]{32}$'
      AND NOT EXISTS (
          SELECT 1 FROM acr.oauth_authorization_requests r
          WHERE r.client_id = c.client_id
      )
      AND NOT EXISTS (
          SELECT 1 FROM acr.oauth_device_grants g
          WHERE g.client_id = c.client_id AND g.created_at >= $1
      )
      AND NOT EXISTS (
          SELECT 1
          FROM acr.oauth_device_grants g
          JOIN acr.device_authorizations d ON d.device_code_hash = g.device_code_hash
          JOIN acr.client_credentials cc ON cc.credential_id = d.redeemed_credential_id
          WHERE g.client_id = c.client_id
            AND ` + liveOAuthCredentialClause + `
      )`

// A client is purged in TWO statements of one transaction, never one. The
// first locks the candidates (FOR UPDATE, SKIP LOCKED: a client an /authorize
// or device authorization holds FOR KEY SHARE right now is skipped). The
// second deletes them again only if they are STILL idle, under a fresh
// READ COMMITTED snapshot: a request or device grant committed after the first
// statement's snapshot (and whose insert already released its row lock) is
// seen here, so a client that just became active is kept. A single
// DELETE ... USING (SELECT ... FOR UPDATE) cannot do this: its row recheck
// re-evaluates the locked row only, not the NOT EXISTS subqueries over other
// tables, so it would delete a client a concurrent insert had just used.
const selectIdleOAuthClientsSQL = `
SELECT c.client_id FROM acr.oauth_clients c
WHERE` + idleOAuthClientPredicate + `
ORDER BY c.created_at, c.client_id
LIMIT $2
FOR UPDATE OF c SKIP LOCKED`

const deleteIdleOAuthClientsSQL = `
DELETE FROM acr.oauth_clients c
WHERE c.client_id = ANY($2::text[])
  AND` + idleOAuthClientPredicate

// expiredDeviceAuthorizationPredicate selects a device authorization (CHAOS-7229)
// that no flow can still use and nothing still needs:
//
//   - it is past expires_at + grace ($1). Eligibility is TIME-based, never
//     state-based: state 'expired' is written lazily, only when a poll, approve
//     or redeem touches the row (expireDeviceAuthorization), and an /authorize
//     request's raw device code is discarded, so an abandoned row stays
//     'pending' forever; a state filter would skip almost every row.
//   - its redeemed credential, if any, is not live at $3 (the link the request
//     and client purges use, see liveOAuthCredentialClause): a device
//     authorization backing a live credential is kept for the credential's life.
//   - every row that ON DELETE CASCADE would take with it is already past its
//     own retention: a request still inside the request grace, or a device grant
//     created inside the window ($1 is the grace cutoff, and the grace is not
//     shorter than the client idle window, so this also keeps every grant the
//     client purge still counts as activity). The cascade must not shorten
//     what the request purge and the client purge rely on.
//
// The predicate only becomes MORE true over time for a given row: a dead
// credential never becomes live again (rollbackCredentialRotation revokes a
// successor, it never revives a source), and a request or device grant is only
// ever created against the device authorization its own flow just created.
const expiredDeviceAuthorizationPredicate = `
      d.expires_at < $1
      AND NOT EXISTS (
          SELECT 1 FROM acr.client_credentials cc
          WHERE cc.credential_id = d.redeemed_credential_id
            AND ` + liveOAuthCredentialClause + `
      )
      AND NOT EXISTS (
          SELECT 1 FROM acr.oauth_authorization_requests r
          WHERE r.device_code_hash = d.device_code_hash AND r.expires_at >= $1
      )
      AND NOT EXISTS (
          SELECT 1 FROM acr.oauth_device_grants g
          WHERE g.device_code_hash = d.device_code_hash AND g.created_at >= $1
      )`

// Device authorizations are purged in the same two statements of one
// transaction as clients (lock the candidates, SKIP LOCKED, then delete them
// only if they are STILL eligible under a fresh snapshot), for the same reason:
// the predicate reads other tables, and a row a concurrent flow just attached to
// or locked must be kept, not deleted on a stale first look. Deleting a row
// cascades to its acr.oauth_authorization_requests and acr.oauth_device_grants
// rows (both ON DELETE CASCADE), which the predicate has already required to be
// past their own retention.
const selectExpiredDeviceAuthorizationsSQL = `
SELECT d.device_code_hash FROM acr.device_authorizations d
WHERE` + expiredDeviceAuthorizationPredicate + `
ORDER BY d.expires_at, d.device_code_hash
LIMIT $2
FOR UPDATE OF d SKIP LOCKED`

const deleteExpiredDeviceAuthorizationsSQL = `
DELETE FROM acr.device_authorizations d
WHERE d.device_code_hash = ANY($2::text[])
  AND` + expiredDeviceAuthorizationPredicate

// The remaining-eligible probes (CHAOS-7249) count, WITHOUT locking, the rows
// the two purge statements' own predicates still select, stopping at $2. A LIMIT
// caps the rows a statement RETURNS, not the rows it EXAMINES: what keeps the
// probes and the purge statements off the whole table is the index each reads in
// order, ix_acr_oauth_authorization_requests_expiry (migration 0040) for
// requests and ix_acr_oauth_clients_created (migration 0045) for clients. Rows
// younger than the cutoff (every registration inside the idle window, which is
// what unauthenticated registration grows) are never read; the work grows with
// the rows past the cutoff that are still KEPT (a request backing a live
// credential, a client older than the window that is still in use), not with the
// table. TestOAuthPurgeStatements_readABoundedNumberOfBuffers pins this.
const countExpiredOAuthRequestsSQL = `
SELECT count(*) FROM (
    SELECT 1 FROM acr.oauth_authorization_requests r
    WHERE` + expiredOAuthRequestPredicate + `
    LIMIT $2
) eligible`

const countIdleOAuthClientsSQL = `
SELECT count(*) FROM (
    SELECT 1 FROM acr.oauth_clients c
    WHERE` + idleOAuthClientPredicate + `
    LIMIT $2
) eligible`

const countExpiredDeviceAuthorizationsSQL = `
SELECT count(*) FROM (
    SELECT 1 FROM acr.device_authorizations d
    WHERE` + expiredDeviceAuthorizationPredicate + `
    LIMIT $2
) eligible`

// OAuthPurgeResult counts the rows one PurgeExpired call deleted.
// DeviceAuthorizations counts device_authorizations rows only; the request and
// device grant rows their deletion cascades to are not counted.
type OAuthPurgeResult struct {
	Requests             int
	Clients              int
	DeviceAuthorizations int
}

// validateOAuthPurgeWindows refuses the windows PurgeExpired and
// CountPurgeRemaining must never run with (see PurgeExpired).
func validateOAuthPurgeWindows(requestGrace, clientIdle time.Duration) error {
	if requestGrace <= 0 || clientIdle <= 0 || requestGrace < clientIdle {
		return storage.ErrInvalidOAuthClient
	}
	return nil
}

// PurgeExpired deletes up to limit expired authorization requests, then up
// to limit idle dynamic clients (CHAOS-6191), then up to limit expired device
// authorizations (CHAOS-7229). now is the loop's clock; requests and device
// authorizations expired before now-requestGrace and clients registered before
// now-clientIdle are eligible, subject to the live-credential rules above.
// Requests go first so a client whose last request just aged out can be
// collected in the same call; device authorizations go last, after both
// statements that read the rows their deletion cascades to. requestGrace must not be shorter than
// clientIdle: request rows are the only record of a client's last request, so
// a shorter grace would judge "idle for clientIdle" on rows already purged
// (a client used inside the idle window would lose its request rows, then the
// client). Both must be positive.
func (s *OAuthStore) PurgeExpired(ctx context.Context, now time.Time, requestGrace, clientIdle time.Duration, limit int) (OAuthPurgeResult, error) {
	if err := s.ready(ctx); err != nil {
		return OAuthPurgeResult{}, err
	}
	if limit <= 0 {
		return OAuthPurgeResult{}, nil
	}
	if err := validateOAuthPurgeWindows(requestGrace, clientIdle); err != nil {
		return OAuthPurgeResult{}, err
	}
	now = now.UTC()
	var result OAuthPurgeResult
	requests, err := s.DB.ExecContext(ctx, purgeExpiredOAuthRequestsSQL, now.Add(-requestGrace), limit, now)
	if err != nil {
		return result, fmt.Errorf("purge expired oauth authorization requests: %w", sanitizeDatabaseError(err))
	}
	deleted, err := requests.RowsAffected()
	if err != nil {
		return result, fmt.Errorf("purge expired oauth authorization requests rows affected: %w", sanitizeDatabaseError(err))
	}
	result.Requests = int(deleted)
	result.Clients, err = s.purgeLockedThenRechecked(ctx, "idle oauth clients", selectIdleOAuthClientsSQL, deleteIdleOAuthClientsSQL, now.Add(-clientIdle), limit, now, s.afterClientLock)
	if err != nil {
		return result, err
	}
	result.DeviceAuthorizations, err = s.purgeLockedThenRechecked(ctx, "expired device authorizations", selectExpiredDeviceAuthorizationsSQL, deleteExpiredDeviceAuthorizationsSQL, now.Add(-requestGrace), limit, now, s.afterDeviceAuthorizationLock)
	if err != nil {
		return result, err
	}
	return result, nil
}

// oauthPurgeLockTimeout bounds how long one purge transaction waits for a row
// lock it cannot skip (see purgeLockedThenRechecked).
const oauthPurgeLockTimeout = 5 * time.Second

// purgeLockedThenRechecked runs a two-statement purge in one READ COMMITTED
// transaction and returns how many rows it deleted: selectSQL locks up to limit
// candidates (FOR UPDATE ... SKIP LOCKED, so a row a concurrent flow holds is
// skipped, never waited on), then deleteSQL deletes them again only if they are
// STILL eligible, under a fresh snapshot. Both take ($1 cutoff, $2 limit-or-ids,
// $3 now). afterLock is a test seam, nil in production, called between the two.
// The DELETE runs even when nothing was locked (it then matches no row), so the
// startup purge exercises the DELETE privilege on an empty table too: a role
// missing it fails there, not only on the first tick that finds a candidate.
func (s *OAuthStore) purgeLockedThenRechecked(ctx context.Context, what, selectSQL, deleteSQL string, cutoff time.Time, limit int, now time.Time, afterLock func()) (int, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, fmt.Errorf("purge %s begin: %w", what, sanitizeDatabaseError(err))
	}
	defer func() { _ = tx.Rollback() }()
	// SKIP LOCKED keeps the select from waiting, but the delete can still wait:
	// deleting a device authorization cascades to its request and device grant
	// rows, and another transaction may hold one of those. Bound that wait so a
	// stuck lock fails the tick (the loop's redacted warning, retried next
	// tick) instead of hanging the loop while it holds its candidates.
	lockTimeout := oauthPurgeLockTimeout
	if s.purgeLockTimeout > 0 {
		lockTimeout = s.purgeLockTimeout
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL lock_timeout = '%dms'", lockTimeout.Milliseconds())); err != nil {
		return 0, fmt.Errorf("purge %s lock timeout: %w", what, sanitizeDatabaseError(err))
	}
	rows, err := tx.QueryContext(ctx, selectSQL, cutoff, limit, now)
	if err != nil {
		return 0, fmt.Errorf("purge %s: %w", what, sanitizeDatabaseError(err))
	}
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("purge %s scan: %w", what, sanitizeDatabaseError(err))
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("purge %s rows: %w", what, sanitizeDatabaseError(err))
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("purge %s close: %w", what, sanitizeDatabaseError(err))
	}
	if afterLock != nil {
		afterLock()
	}
	result, err := tx.ExecContext(ctx, deleteSQL, cutoff, ids, now)
	if err != nil {
		return 0, fmt.Errorf("purge %s delete: %w", what, sanitizeDatabaseError(err))
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("purge %s rows affected: %w", what, sanitizeDatabaseError(err))
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("purge %s commit: %w", what, sanitizeDatabaseError(err))
	}
	return int(deleted), nil
}

// OAuthPurgeRemaining counts the rows that are STILL purge-eligible, each
// capped at limit+1 (so a value above limit means "more than one batch").
type OAuthPurgeRemaining struct {
	Requests             int
	Clients              int
	DeviceAuthorizations int
}

// CountPurgeRemaining reports how many authorization requests, idle dynamic
// clients and expired device authorizations PurgeExpired would still select at now, with the same
// windows (CHAOS-7249). Called right after a PurgeExpired, it is what the
// purge tick logs next to the deleted counts: deleted=0 with remaining=0 is an
// empty tick, deleted=0 with remaining>0 is a tick that skipped rows it should
// have taken (rows held by a concurrent flow, or a broken delete), and
// remaining>0 after a full batch is an ordinary backlog. It takes no lock and
// stops after limit+1 eligible rows per table, reading in index order (see the
// probe statements above for what bounds its work). It shares the purge
// statements' predicates, so it cannot see a rule that is wrong in the
// predicate itself; it sees any way the DELETE fails to take what the
// predicate selects.
func (s *OAuthStore) CountPurgeRemaining(ctx context.Context, now time.Time, requestGrace, clientIdle time.Duration, limit int) (OAuthPurgeRemaining, error) {
	if err := s.ready(ctx); err != nil {
		return OAuthPurgeRemaining{}, err
	}
	if limit <= 0 {
		return OAuthPurgeRemaining{}, nil
	}
	if err := validateOAuthPurgeWindows(requestGrace, clientIdle); err != nil {
		return OAuthPurgeRemaining{}, err
	}
	now = now.UTC()
	var remaining OAuthPurgeRemaining
	if err := s.DB.QueryRowContext(ctx, countExpiredOAuthRequestsSQL, now.Add(-requestGrace), limit+1, now).Scan(&remaining.Requests); err != nil {
		return OAuthPurgeRemaining{}, fmt.Errorf("count remaining oauth authorization requests: %w", sanitizeDatabaseError(err))
	}
	if err := s.DB.QueryRowContext(ctx, countIdleOAuthClientsSQL, now.Add(-clientIdle), limit+1, now).Scan(&remaining.Clients); err != nil {
		return OAuthPurgeRemaining{}, fmt.Errorf("count remaining idle oauth clients: %w", sanitizeDatabaseError(err))
	}
	if err := s.DB.QueryRowContext(ctx, countExpiredDeviceAuthorizationsSQL, now.Add(-requestGrace), limit+1, now).Scan(&remaining.DeviceAuthorizations); err != nil {
		return OAuthPurgeRemaining{}, fmt.Errorf("count remaining expired device authorizations: %w", sanitizeDatabaseError(err))
	}
	return remaining, nil
}
