package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12140: the purge of ended browser token families. One browser login
// makes one family (acr.browser_token_families, migration 0062); every refresh
// adds a refresh row and a browser-class credential row (acr.client_credentials
// with family_id). Once a family has ended -- its absolute end (expires_at) or
// its revocation is older than the grace -- nothing can use those rows again:
// a refresh on an ended family is refused, and every member credential is
// expired or revoked. This purge deletes the member credentials first, then
// the families (their refresh rows go by ON DELETE CASCADE).
//
// Two rows are never deleted here:
//
//   - A member credential of a family that has NOT ended: a refresh reads the
//     presented token's credential (lockedCredential) to copy its provenance,
//     so deleting a dead member of a live family would break that family's
//     next refresh. A member revoked on its own (an older release) is kept
//     until its family ends; the refresh that finds it ends the family.
//   - A credential still named by acr.device_authorizations.redeemed_credential_id:
//     a family's first credential comes from the code exchange, which redeems
//     a device authorization, and that foreign key (migration 0004) has no ON
//     DELETE action. Deleting it would fail the whole batch with a foreign-key
//     violation on every tick. The OAuth purge deletes that device
//     authorization after ACR_OAUTH_REQUEST_PURGE_GRACE; this purge then takes
//     the credential by its own family_expires_at, family row or not.

// browserCredentialUnpinnedClause is "credential cc is not named by a device
// authorization", the one foreign key that points at acr.client_credentials.
// ix on redeemed_credential_id: its UNIQUE constraint (migration 0004).
const browserCredentialUnpinnedClause = `NOT EXISTS (
          SELECT 1 FROM acr.device_authorizations d
          WHERE d.redeemed_credential_id = cc.credential_id
      )`

// endedBrowserCredentialPredicate selects a browser-class credential ($1 is
// the cutoff, now - grace; $3 is now) that is dead at now, whose family ended
// before the cutoff, and that no device authorization names. The family has
// ended when the family end the credential carries (family_expires_at, copied
// from the family at issue and never changed) is before the cutoff, or when
// its family row was revoked before the cutoff. The first form needs no family
// row, so it also takes a credential whose family row is already gone (a
// first credential that a device authorization kept past its family). family_id
// IS NOT NULL is implied by the class (migration 0058's shape check); it is
// written out so the partial index ix_acr_client_credentials_family applies.
// The dead-at-now clause is implied too (a member never outlives its family
// end, and a family revocation revokes every member); it stays so that a
// member that is somehow still live is never deleted.
const endedBrowserCredentialPredicate = `
      cc.client_class = 'browser'
      AND cc.family_id IS NOT NULL
      AND (cc.revoked_at IS NOT NULL OR cc.expires_at <= $3)
      AND (
          cc.family_expires_at < $1
          OR EXISTS (
              SELECT 1 FROM acr.browser_token_families f
              WHERE f.family_id = cc.family_id AND f.org_id = cc.org_id AND f.revoked_at < $1
          )
      )
      AND ` + browserCredentialUnpinnedClause

// purgeEndedBrowserCredentialsSQL deletes one bounded batch of the credentials
// endedBrowserCredentialPredicate selects. SKIP LOCKED: a row another
// transaction holds is left for a later tick. The predicate only becomes more
// true over time for a given row (a family end never moves, a revocation is
// never undone, and a device authorization only ever names the credential its
// own redemption just inserted), so the row recheck under the lock needs no
// fresh look at the other tables.
const purgeEndedBrowserCredentialsSQL = `
WITH ended AS (
    SELECT cc.credential_id FROM acr.client_credentials cc
    WHERE` + endedBrowserCredentialPredicate + `
    ORDER BY cc.family_expires_at, cc.credential_id
    LIMIT $2
    FOR UPDATE OF cc SKIP LOCKED
)
DELETE FROM acr.client_credentials cc
USING ended
WHERE cc.credential_id = ended.credential_id`

// endedBrowserFamilyPredicate selects a family ($1 is the cutoff) that ended
// before the cutoff and has no member credential left except the ones a device
// authorization keeps. A member the credential statement could still delete
// keeps the family for a later tick (the credential batch was full), so a
// revoked family's members are always taken through their family row first.
// The member lookup uses ix_acr_client_credentials_family (org_id, family_id).
const endedBrowserFamilyPredicate = `
      (f.expires_at < $1 OR f.revoked_at < $1)
      AND NOT EXISTS (
          SELECT 1 FROM acr.client_credentials cc
          WHERE cc.org_id = f.org_id AND cc.family_id = f.family_id
            AND ` + browserCredentialUnpinnedClause + `
      )`

// purgeEndedBrowserFamiliesSQL deletes one bounded batch of the families
// endedBrowserFamilyPredicate selects; their refresh rows go by cascade. A
// refresh or a revocation locks the family row first, so SKIP LOCKED skips a
// family one of them holds, and once this statement holds a family they wait
// for it. No member credential can be added to an ended family: a refresh
// refuses it under the same lock.
const purgeEndedBrowserFamiliesSQL = `
WITH ended AS (
    SELECT f.family_id FROM acr.browser_token_families f
    WHERE` + endedBrowserFamilyPredicate + `
    ORDER BY f.expires_at, f.family_id
    LIMIT $2
    FOR UPDATE OF f SKIP LOCKED
)
DELETE FROM acr.browser_token_families f
USING ended
WHERE f.family_id = ended.family_id`

// The remaining-eligible probes count, without locking, the rows the two
// statements' own predicates still select, stopping at $2.
const countEndedBrowserCredentialsSQL = `
SELECT count(*) FROM (
    SELECT 1 FROM acr.client_credentials cc
    WHERE` + endedBrowserCredentialPredicate + `
    LIMIT $2
) eligible`

const countEndedBrowserFamiliesSQL = `
SELECT count(*) FROM (
    SELECT 1 FROM acr.browser_token_families f
    WHERE` + endedBrowserFamilyPredicate + `
    LIMIT $2
) eligible`

// browserFamilyPurgeLockTimeout bounds how long one purge statement waits for
// a lock it cannot skip (a refresh row the family delete cascades to).
const browserFamilyPurgeLockTimeout = 5 * time.Second

// BrowserFamilyPurgeResult counts the rows one PurgeEndedFamilies call
// deleted. Families counts family rows only; the refresh rows their deletion
// cascades to are not counted.
type BrowserFamilyPurgeResult struct {
	Credentials int
	Families    int
}

// BrowserFamilyPurgeRemaining counts the rows that are STILL purge-eligible,
// each capped at limit+1 (a value above limit means "more than one batch").
type BrowserFamilyPurgeRemaining struct {
	Credentials int
	Families    int
}

// PurgeEndedFamilies deletes up to limit member credentials of families that
// ended before now-grace, then up to limit such families. Credentials go
// first, so a family whose last members just went is deleted in the same call.
// grace must be positive.
func (s *BrowserRefreshStore) PurgeEndedFamilies(ctx context.Context, now time.Time, grace time.Duration, limit int) (BrowserFamilyPurgeResult, error) {
	if err := s.ready(ctx); err != nil {
		return BrowserFamilyPurgeResult{}, err
	}
	if grace <= 0 {
		return BrowserFamilyPurgeResult{}, storage.ErrInvalidBrowserRefresh
	}
	if limit <= 0 {
		return BrowserFamilyPurgeResult{}, nil
	}
	now = now.UTC()
	cutoff := now.Add(-grace)
	var result BrowserFamilyPurgeResult
	var err error
	if result.Credentials, err = s.purgeBatch(ctx, "ended browser credentials", purgeEndedBrowserCredentialsSQL, cutoff, limit, now); err != nil {
		return result, err
	}
	if result.Families, err = s.purgeBatch(ctx, "ended browser families", purgeEndedBrowserFamiliesSQL, cutoff, limit); err != nil {
		return result, err
	}
	return result, nil
}

// purgeBatch runs one bounded purge statement in its own transaction under a
// lock timeout, so a stuck lock fails the tick (the loop's redacted warning,
// retried next tick) instead of hanging the loop.
func (s *BrowserRefreshStore) purgeBatch(ctx context.Context, what, statement string, args ...any) (int, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, fmt.Errorf("purge %s begin: %w", what, sanitizeDatabaseError(err))
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL lock_timeout = '%dms'", browserFamilyPurgeLockTimeout.Milliseconds())); err != nil {
		return 0, fmt.Errorf("purge %s lock timeout: %w", what, sanitizeDatabaseError(err))
	}
	result, err := tx.ExecContext(ctx, statement, args...)
	if err != nil {
		return 0, fmt.Errorf("purge %s: %w", what, sanitizeDatabaseError(err))
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

// CountEndedFamiliesRemaining reports how many member credentials and families
// PurgeEndedFamilies would still select at now with the same grace. Called
// right after a purge, it is what the purge tick logs next to the deleted
// counts: deleted 0 with remaining 0 is an empty tick, deleted 0 with
// remaining > 0 is a tick that skipped rows it should have taken, and
// remaining > 0 after a full batch is an ordinary backlog. It shares the purge
// statements' predicates, so it sees a delete that fails to take what the
// predicate selects, not a rule that is wrong in the predicate itself.
func (s *BrowserRefreshStore) CountEndedFamiliesRemaining(ctx context.Context, now time.Time, grace time.Duration, limit int) (BrowserFamilyPurgeRemaining, error) {
	if err := s.ready(ctx); err != nil {
		return BrowserFamilyPurgeRemaining{}, err
	}
	if grace <= 0 {
		return BrowserFamilyPurgeRemaining{}, storage.ErrInvalidBrowserRefresh
	}
	if limit <= 0 {
		return BrowserFamilyPurgeRemaining{}, nil
	}
	now = now.UTC()
	cutoff := now.Add(-grace)
	var remaining BrowserFamilyPurgeRemaining
	if err := s.DB.QueryRowContext(ctx, countEndedBrowserCredentialsSQL, cutoff, limit+1, now).Scan(&remaining.Credentials); err != nil {
		return BrowserFamilyPurgeRemaining{}, fmt.Errorf("count remaining ended browser credentials: %w", sanitizeDatabaseError(err))
	}
	if err := s.DB.QueryRowContext(ctx, countEndedBrowserFamiliesSQL, cutoff, limit+1).Scan(&remaining.Families); err != nil {
		return BrowserFamilyPurgeRemaining{}, fmt.Errorf("count remaining ended browser families: %w", sanitizeDatabaseError(err))
	}
	return remaining, nil
}
