package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/internal/credentiallifecycle"
)

// RetentionDeleteGateSQL gates every retention delete of credentials (the
// workload credential purge and the ended browser credential purge, alias cc):
// a revoked row (legacy or not) goes as before; an unrevoked row waits until
// the tick has written its expired event. No credential is deleted before its
// terminal event.
const RetentionDeleteGateSQL = `(cc.revoked_at IS NOT NULL OR EXISTS (
          SELECT 1 FROM acr.credential_lifecycle_outbox o
          WHERE o.aggregate_id = cc.credential_id AND o.event_type = 'acr_credential.expired'))`

// The tick's two selects. Each locks the rows it takes (SKIP LOCKED: a row a
// revoke or another pod holds is left for a later tick) and reads the outbox
// for the row's state. $1 is the tick's one now, $2 the batch limit.
const (
	// LifecycleTickExpiredSQL: unrevoked credentials past their expiry whose
	// aggregate has no terminal row (an expired or revoked one).
	LifecycleTickExpiredSQL = `SELECT ` + credentialColumnList + ` FROM acr.client_credentials cc
WHERE cc.revoked_at IS NULL AND cc.expires_at IS NOT NULL AND cc.expires_at <= $1
  AND NOT EXISTS (SELECT 1 FROM acr.credential_lifecycle_outbox o
                  WHERE o.aggregate_id = cc.credential_id
                    AND o.event_type IN ('acr_credential.expired', 'acr_credential.revoked'))
ORDER BY cc.expires_at, cc.credential_id
LIMIT $2
FOR UPDATE OF cc SKIP LOCKED`
	// LifecycleTickFirstTouchSQL: live credentials the stream has never seen.
	LifecycleTickFirstTouchSQL = `SELECT ` + credentialColumnList + ` FROM acr.client_credentials cc
WHERE cc.revoked_at IS NULL AND (cc.expires_at IS NULL OR cc.expires_at > $1)
  AND NOT EXISTS (SELECT 1 FROM acr.credential_lifecycle_outbox o WHERE o.aggregate_id = cc.credential_id)
ORDER BY cc.created_at, cc.credential_id
LIMIT $2
FOR UPDATE OF cc SKIP LOCKED`
)

// LifecycleTick writes the lifecycle events no request writes: the expired
// event of a credential that reached its expiry, and the revision 1 of a live
// credential the stream predates.
type LifecycleTick struct {
	db    *sql.DB
	audit *AuditStore
}

// LifecycleTickResult counts one tick. Full reports that either batch reached
// its limit, so more rows wait for the next tick.
type LifecycleTickResult struct {
	Expired    int
	FirstTouch int
	Full       bool
	// Skipped counts the credentials the event mapper refuses (an empty scope
	// list, written by hand): no event describes them, so the tick leaves them
	// and names them every run; SkippedIDs are their ids.
	Skipped    int
	SkippedIDs []string
}

func NewCredentialLifecycleTick(db *sql.DB, audit *AuditStore) (*LifecycleTick, error) {
	if db == nil || audit == nil || audit.GenerateID == nil {
		return nil, storage.ErrInvalidCredentialLifecycle
	}
	return &LifecycleTick{db: db, audit: audit}, nil
}

// Tick runs both selects in one transaction at one now. Any failure rolls the
// whole tick back: the rows of the same tick commit together or not at all.
func (t *LifecycleTick) Tick(ctx context.Context, now time.Time, limit int) (LifecycleTickResult, error) {
	if t == nil || t.db == nil || now.IsZero() || limit <= 0 {
		return LifecycleTickResult{}, storage.ErrInvalidCredentialLifecycle
	}
	now = now.UTC()
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return LifecycleTickResult{}, fmt.Errorf("begin credential lifecycle tick: %w", sanitizeDatabaseError(err))
	}
	defer func() { _ = tx.Rollback() }()
	sink := t.audit.lifecycleSink(tx)
	expiredRows, err := readCredentialsFor(ctx, tx, LifecycleTickExpiredSQL, now, limit)
	if err != nil {
		return LifecycleTickResult{}, fmt.Errorf("select expired credentials: %w", err)
	}
	var skipped []string
	writtenExpired := 0
	for _, row := range expiredRows {
		skip, err := appendTickExpired(ctx, sink, row, now)
		if err != nil {
			return LifecycleTickResult{}, fmt.Errorf("record credential lifecycle event: %w", err)
		}
		if skip {
			skipped = append(skipped, row.CredentialID)
			continue
		}
		writtenExpired++
	}
	touchRows, err := readCredentialsFor(ctx, tx, LifecycleTickFirstTouchSQL, now, limit)
	if err != nil {
		return LifecycleTickResult{}, fmt.Errorf("select untouched credentials: %w", err)
	}
	writtenTouch := 0
	for _, row := range touchRows {
		skip, err := ensureTickFirstTouch(ctx, sink, row)
		if err != nil {
			return LifecycleTickResult{}, fmt.Errorf("record credential lifecycle event: %w", err)
		}
		if skip {
			skipped = append(skipped, row.CredentialID)
			continue
		}
		writtenTouch++
	}
	if err := tx.Commit(); err != nil {
		return LifecycleTickResult{}, fmt.Errorf("commit credential lifecycle tick: %w", sanitizeDatabaseError(err))
	}
	return LifecycleTickResult{
		Expired: writtenExpired, FirstTouch: writtenTouch,
		Full:    len(expiredRows) >= limit || len(touchRows) >= limit,
		Skipped: len(skipped), SkippedIDs: skipped,
	}, nil
}

func readCredentialsFor(ctx context.Context, tx *sql.Tx, statement string, now time.Time, limit int) ([]contractsv1.ClientCredential, error) {
	result, err := tx.QueryContext(ctx, statement, now, limit)
	if err != nil {
		return nil, sanitizeDatabaseError(err)
	}
	defer result.Close()
	var rows []contractsv1.ClientCredential
	for result.Next() {
		credential, err := scanCredential(result)
		if err != nil {
			return nil, sanitizeDatabaseError(err)
		}
		rows = append(rows, credential)
	}
	if err := result.Err(); err != nil {
		return nil, sanitizeDatabaseError(err)
	}
	return rows, nil
}

// appendTickExpired writes a credential's expired event (its first touch first).
// A mapper refusal (the credential cannot be described: an empty scope list)
// happens before any SQL of that row and leaves the transaction healthy, so the
// row is skipped; a database error, or a revision conflict, is returned and
// rolls the tick back.
func appendTickExpired(ctx context.Context, sink credentiallifecycle.Sink, row contractsv1.ClientCredential, now time.Time) (skipped bool, err error) {
	facts := credentiallifecycle.Facts{Kind: credentiallifecycle.KindExpired, Credential: row, At: now, Actor: credentiallifecycle.Actor{Type: credentiallifecycle.ActorSystem}}
	return tickWriteSkippingRefusals(credentiallifecycle.Append(ctx, sink, facts))
}

// ensureTickFirstTouch writes a live credential's revision 1, skipping a
// credential the mapper refuses like appendTickExpired.
func ensureTickFirstTouch(ctx context.Context, sink credentiallifecycle.Sink, row contractsv1.ClientCredential) (skipped bool, err error) {
	return tickWriteSkippingRefusals(credentiallifecycle.EnsureIssued(ctx, sink, row))
}

func tickWriteSkippingRefusals(err error) (bool, error) {
	if errors.Is(err, credentiallifecycle.ErrInvalidEvent) {
		return true, nil
	}
	return false, err
}
