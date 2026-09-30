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
// upward: the device_authorizations row behind it stays (its purge is a
// separate follow-up).
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

// OAuthPurgeResult counts the rows one PurgeExpired call deleted.
type OAuthPurgeResult struct {
	Requests int
	Clients  int
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
// to limit idle dynamic clients (CHAOS-6191). now is the loop's clock;
// requests expired before now-requestGrace and clients registered before
// now-clientIdle are eligible, subject to the live-credential rules above.
// Requests go first so a client whose last request just aged out can be
// collected in the same call. requestGrace must not be shorter than
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
	result.Clients, err = s.purgeIdleOAuthClients(ctx, now.Add(-clientIdle), limit, now)
	if err != nil {
		return result, err
	}
	return result, nil
}

// purgeIdleOAuthClients runs the two-statement client purge (see
// selectIdleOAuthClientsSQL) and returns how many clients it deleted.
func (s *OAuthStore) purgeIdleOAuthClients(ctx context.Context, cutoff time.Time, limit int, now time.Time) (int, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, fmt.Errorf("purge idle oauth clients begin: %w", sanitizeDatabaseError(err))
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, selectIdleOAuthClientsSQL, cutoff, limit, now)
	if err != nil {
		return 0, fmt.Errorf("purge idle oauth clients: %w", sanitizeDatabaseError(err))
	}
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("purge idle oauth clients scan: %w", sanitizeDatabaseError(err))
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("purge idle oauth clients rows: %w", sanitizeDatabaseError(err))
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("purge idle oauth clients close: %w", sanitizeDatabaseError(err))
	}
	if s.afterClientLock != nil {
		s.afterClientLock()
	}
	deleted := int64(0)
	if len(ids) > 0 {
		result, err := tx.ExecContext(ctx, deleteIdleOAuthClientsSQL, cutoff, ids, now)
		if err != nil {
			return 0, fmt.Errorf("purge idle oauth clients delete: %w", sanitizeDatabaseError(err))
		}
		if deleted, err = result.RowsAffected(); err != nil {
			return 0, fmt.Errorf("purge idle oauth clients rows affected: %w", sanitizeDatabaseError(err))
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("purge idle oauth clients commit: %w", sanitizeDatabaseError(err))
	}
	return int(deleted), nil
}

// OAuthPurgeRemaining counts the rows that are STILL purge-eligible, each
// capped at limit+1 (so a value above limit means "more than one batch").
type OAuthPurgeRemaining struct {
	Requests int
	Clients  int
}

// CountPurgeRemaining reports how many authorization requests and idle
// dynamic clients PurgeExpired would still select at now, with the same
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
	return remaining, nil
}
