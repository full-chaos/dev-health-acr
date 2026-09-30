package postgres

import (
	"context"
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

// purgeExpiredOAuthRequestsSQL deletes one bounded batch of /authorize
// requests past expires_at + grace ($1), except a request whose
// device authorization redeemed a credential that is live at $3.
//
// No table references acr.oauth_authorization_requests, and its only foreign
// key (device_code_hash -> acr.device_authorizations ON DELETE CASCADE) runs
// parent to child, so deleting a request row can never be blocked or cascade
// upward: the device_authorizations row behind it stays (its purge is a
// separate follow-up).
const purgeExpiredOAuthRequestsSQL = `
WITH expired AS (
    SELECT r.handle_hash FROM acr.oauth_authorization_requests r
    WHERE r.expires_at < $1
      AND NOT EXISTS (
          SELECT 1
          FROM acr.device_authorizations d
          JOIN acr.client_credentials cc ON cc.credential_id = d.redeemed_credential_id
          WHERE d.device_code_hash = r.device_code_hash
            AND ` + liveOAuthCredentialClause + `
      )
    ORDER BY r.expires_at, r.handle_hash
    LIMIT $2
    FOR UPDATE OF r SKIP LOCKED
)
DELETE FROM acr.oauth_authorization_requests r
USING expired
WHERE r.handle_hash = expired.handle_hash`

// purgeIdleOAuthClientsSQL deletes one bounded batch of dynamic (RFC 7591)
// clients registered before $1 that are idle: no request row left (rows are
// kept while inside the purge grace or while backing a live credential), no
// device grant created since $1, and no live credential (at $3) reachable
// through any device grant of theirs. The client_id pattern is the same
// shape migration 0040 forces on every row of the table; it stays in the
// predicate so a pre-registered client, should one ever be added under a
// different id shape, is never deleted here.
const purgeIdleOAuthClientsSQL = `
WITH idle AS (
    SELECT c.client_id FROM acr.oauth_clients c
    WHERE c.created_at < $1
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
      )
    ORDER BY c.created_at, c.client_id
    LIMIT $2
    FOR UPDATE OF c SKIP LOCKED
)
DELETE FROM acr.oauth_clients c
USING idle
WHERE c.client_id = idle.client_id`

// OAuthPurgeResult counts the rows one PurgeExpired call deleted.
type OAuthPurgeResult struct {
	Requests int
	Clients  int
}

// PurgeExpired deletes up to limit expired authorization requests, then up
// to limit idle dynamic clients (CHAOS-6191). now is the loop's clock;
// requests expired before now-requestGrace and clients registered before
// now-clientIdle are eligible, subject to the live-credential rules on the
// two statements above. Requests go first so a client whose last request just
// aged out can be collected in the same call.
func (s *OAuthStore) PurgeExpired(ctx context.Context, now time.Time, requestGrace, clientIdle time.Duration, limit int) (OAuthPurgeResult, error) {
	if err := s.ready(ctx); err != nil {
		return OAuthPurgeResult{}, err
	}
	if limit <= 0 {
		return OAuthPurgeResult{}, nil
	}
	if requestGrace <= 0 || clientIdle < requestGrace {
		return OAuthPurgeResult{}, storage.ErrInvalidOAuthClient
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
	clients, err := s.DB.ExecContext(ctx, purgeIdleOAuthClientsSQL, now.Add(-clientIdle), limit, now)
	if err != nil {
		return result, fmt.Errorf("purge idle oauth clients: %w", sanitizeDatabaseError(err))
	}
	deleted, err = clients.RowsAffected()
	if err != nil {
		return result, fmt.Errorf("purge idle oauth clients rows affected: %w", sanitizeDatabaseError(err))
	}
	result.Clients = int(deleted)
	return result, nil
}
