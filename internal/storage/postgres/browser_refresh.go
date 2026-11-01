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

// BrowserRefreshStore keeps browser token families and refresh tokens
// (CHAOS-12124, migration 0062). Every refresh and every family revocation
// takes the family row lock first, so two replicas serialize on one family.
type BrowserRefreshStore struct {
	DB    *sql.DB
	audit *AuditStore
	now   func() time.Time
}

var _ storage.BrowserRefreshStore = (*BrowserRefreshStore)(nil)

// NewBrowserRefreshStore builds the store over the credential database.
func NewBrowserRefreshStore(db *sql.DB, audit *AuditStore) (*BrowserRefreshStore, error) {
	if db == nil || audit == nil {
		return nil, storage.ErrInvalidBrowserRefresh
	}
	return &BrowserRefreshStore{DB: db, audit: audit, now: time.Now}, nil
}

func (s *BrowserRefreshStore) ready(ctx context.Context) error {
	if s == nil || s.DB == nil || s.audit == nil || ctx == nil {
		return storage.ErrInvalidBrowserRefresh
	}
	return ctx.Err()
}

// StartBrowserFamily records a family and its first refresh token.
func (s *BrowserRefreshStore) StartBrowserFamily(ctx context.Context, start storage.BrowserFamilyStart) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	if err := storage.ValidateBrowserFamilyStart(start); err != nil || !uuidPattern.MatchString(start.OrgID) {
		return storage.ErrInvalidBrowserRefresh
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin browser family start: %w", sanitizeDatabaseError(err))
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO acr.browser_token_families (family_id, org_id, client_id, created_at, expires_at)
VALUES ($1, $2::uuid, $3, $4, $5)`, start.FamilyID, start.OrgID, start.ClientID, start.CreatedAt.UTC(), start.ExpiresAt.UTC()); err != nil {
		return fmt.Errorf("insert browser family: %w", sanitizeDatabaseError(err))
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO acr.browser_refresh_tokens (token_hash, family_id, credential_id, created_at)
VALUES ($1, $2, $3, $4)`, start.RefreshTokenHash, start.FamilyID, start.CredentialID, start.CreatedAt.UTC()); err != nil {
		return fmt.Errorf("insert browser refresh token: %w", sanitizeDatabaseError(err))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit browser family start: %w", sanitizeDatabaseError(err))
	}
	return nil
}

type browserFamilyRow struct {
	orgID, clientID string
	expiresAt       time.Time
	revokedAt       sql.NullTime
}

type browserRefreshRow struct {
	familyID, credentialID string
	consumedAt             sql.NullTime
	successorHash          sql.NullString
	seed                   []byte
}

// lockBrowserFamily finds the family of a refresh token and locks the family
// row, then the token row. found is false for an unknown token.
func lockBrowserFamily(ctx context.Context, tx *sql.Tx, tokenHash string) (browserFamilyRow, browserRefreshRow, bool, error) {
	var familyID string
	err := tx.QueryRowContext(ctx, `SELECT family_id FROM acr.browser_refresh_tokens WHERE token_hash = $1`, tokenHash).Scan(&familyID)
	if errors.Is(err, sql.ErrNoRows) {
		return browserFamilyRow{}, browserRefreshRow{}, false, nil
	}
	if err != nil {
		return browserFamilyRow{}, browserRefreshRow{}, false, fmt.Errorf("find browser refresh token: %w", sanitizeDatabaseError(err))
	}
	var family browserFamilyRow
	if err := tx.QueryRowContext(ctx, `
SELECT org_id::text, client_id, expires_at, revoked_at FROM acr.browser_token_families WHERE family_id = $1 FOR UPDATE`, familyID).
		Scan(&family.orgID, &family.clientID, &family.expiresAt, &family.revokedAt); err != nil {
		return browserFamilyRow{}, browserRefreshRow{}, false, fmt.Errorf("lock browser family: %w", sanitizeDatabaseError(err))
	}
	token := browserRefreshRow{familyID: familyID}
	if err := tx.QueryRowContext(ctx, `
SELECT credential_id, consumed_at, successor_hash, rotation_seed FROM acr.browser_refresh_tokens WHERE token_hash = $1 FOR UPDATE`, tokenHash).
		Scan(&token.credentialID, &token.consumedAt, &token.successorHash, &token.seed); err != nil {
		return browserFamilyRow{}, browserRefreshRow{}, false, fmt.Errorf("lock browser refresh token: %w", sanitizeDatabaseError(err))
	}
	return family, token, true, nil
}

// RotateBrowserRefresh decides one refresh under the family's row lock.
func (s *BrowserRefreshStore) RotateBrowserRefresh(ctx context.Context, rotation storage.BrowserRefreshRotation) (storage.BrowserRefreshOutcome, error) {
	if err := s.ready(ctx); err != nil {
		return storage.BrowserRefreshOutcome{}, err
	}
	if err := storage.ValidateBrowserRefreshRotation(rotation); err != nil {
		return storage.BrowserRefreshOutcome{}, err
	}
	now := rotation.Now.UTC()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return storage.BrowserRefreshOutcome{}, fmt.Errorf("begin browser refresh: %w", sanitizeDatabaseError(err))
	}
	defer func() { _ = tx.Rollback() }()
	family, token, found, err := lockBrowserFamily(ctx, tx, rotation.PresentedHash)
	if err != nil {
		return storage.BrowserRefreshOutcome{}, err
	}
	if !found {
		return storage.BrowserRefreshOutcome{Decision: storage.BrowserRefreshUnknownToken}, nil
	}
	outcome := storage.BrowserRefreshOutcome{OrgID: family.orgID, FamilyID: token.familyID}
	switch {
	case family.clientID != rotation.ClientID:
		outcome.Decision = storage.BrowserRefreshClientMismatch
		return outcome, nil
	case family.revokedAt.Valid:
		outcome.Decision = storage.BrowserRefreshFamilyRevoked
		return outcome, nil
	case !now.Before(family.expiresAt):
		outcome.Decision = storage.BrowserRefreshFamilyExpired
		return outcome, nil
	}
	source, err := lockedCredential(ctx, tx, family.orgID, token.credentialID)
	if err != nil {
		return storage.BrowserRefreshOutcome{}, err
	}
	if source.RevokedAt != nil {
		// A member credential revoked outside the family path (an older
		// release) ends the family.
		revoked, err := s.revokeBrowserFamilyTx(ctx, tx, family.orgID, token.familyID, "member_revoked", rotation.ActorID, now)
		if err != nil {
			return storage.BrowserRefreshOutcome{}, err
		}
		outcome.Decision, outcome.RevokedCredentials = storage.BrowserRefreshFamilyRevoked, revoked
		return outcome, s.commit(tx, "browser refresh")
	}
	if !token.consumedAt.Valid {
		successor, err := s.rotateBrowserRefreshTx(ctx, tx, rotation, source, family, token, now)
		if err != nil {
			return storage.BrowserRefreshOutcome{}, err
		}
		outcome.Decision, outcome.Credential, outcome.Seed = storage.BrowserRefreshRotated, successor, append([]byte(nil), rotation.Seed...)
		outcome.SuccessorTokenHash, outcome.SuccessorRefreshHash = rotation.SuccessorTokenHash, rotation.SuccessorRefresh
		return outcome, s.commit(tx, "browser refresh")
	}
	replay, ok, err := s.graceReplayTx(ctx, tx, family, token, rotation.Grace, now)
	if err != nil {
		return storage.BrowserRefreshOutcome{}, err
	}
	if ok {
		replay.OrgID, replay.FamilyID = outcome.OrgID, outcome.FamilyID
		return replay, s.commit(tx, "browser refresh")
	}
	revoked, err := s.revokeBrowserFamilyTx(ctx, tx, family.orgID, token.familyID, "reuse", rotation.ActorID, now)
	if err != nil {
		return storage.BrowserRefreshOutcome{}, err
	}
	outcome.Decision, outcome.RevokedCredentials = storage.BrowserRefreshReuseRevoked, revoked
	return outcome, s.commit(tx, "browser refresh")
}

func (s *BrowserRefreshStore) commit(tx *sql.Tx, operation string) error {
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s: %w", operation, sanitizeDatabaseError(err))
	}
	return nil
}

// rotateBrowserRefreshTx issues the successor credential (provenance copied
// from the presented token's credential, life bounded by the family end) and
// refresh token, and consumes the presented token.
func (s *BrowserRefreshStore) rotateBrowserRefreshTx(ctx context.Context, tx *sql.Tx, rotation storage.BrowserRefreshRotation, source contractsv1.ClientCredential, family browserFamilyRow, token browserRefreshRow, now time.Time) (contractsv1.ClientCredential, error) {
	input := storage.BrowserRefreshSuccessorInput(rotation, source, family.expiresAt, now)
	successor, err := credentialFromNormalizedCreate(input, now)
	if err != nil {
		return contractsv1.ClientCredential{}, err
	}
	record := storage.CredentialRecord{
		Metadata: successor, TokenHash: input.TokenHash, CreatedBy: input.ActorID,
		IssuanceProvenance: input.IssuanceProvenance,
	}
	if err := insertCredential(ctx, tx, record); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	event := credentialCreatedEvent(record)
	if event.Metadata == nil {
		event.Metadata = map[string]any{}
	}
	event.Metadata["browser_refresh"] = string(storage.BrowserRefreshRotated)
	event.Metadata["family_id"] = token.familyID
	if err := s.audit.record(ctx, tx, event); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	if err := s.audit.appendLifecycle(ctx, tx, credentiallifecycle.Facts{Kind: credentiallifecycle.CreateKind(input), Credential: successor, At: now, Actor: credentiallifecycle.CreateActor(input)}); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO acr.browser_refresh_tokens (token_hash, family_id, credential_id, created_at)
VALUES ($1, $2, $3, $4)`, rotation.SuccessorRefresh, token.familyID, rotation.SuccessorCredential, now); err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("insert successor refresh token: %w", sanitizeDatabaseError(err))
	}
	result, err := tx.ExecContext(ctx, `
UPDATE acr.browser_refresh_tokens SET consumed_at = $2, successor_hash = $3, rotation_seed = $4
WHERE token_hash = $1 AND consumed_at IS NULL`, rotation.PresentedHash, now, rotation.SuccessorRefresh, rotation.Seed)
	if err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("consume refresh token: %w", sanitizeDatabaseError(err))
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return contractsv1.ClientCredential{}, storage.ErrConflict
	}
	return successor, nil
}

// graceReplayTx answers a consumed token again when it was consumed inside
// the grace window and its successor is still unused and live.
func (s *BrowserRefreshStore) graceReplayTx(ctx context.Context, tx *sql.Tx, family browserFamilyRow, token browserRefreshRow, grace time.Duration, now time.Time) (storage.BrowserRefreshOutcome, bool, error) {
	if !token.successorHash.Valid || len(token.seed) != 32 || !storage.BrowserRefreshInGrace(token.consumedAt.Time, now, grace) {
		return storage.BrowserRefreshOutcome{}, false, nil
	}
	var successorCredentialID string
	var successorConsumed sql.NullTime
	err := tx.QueryRowContext(ctx, `
SELECT credential_id, consumed_at FROM acr.browser_refresh_tokens WHERE token_hash = $1 FOR UPDATE`, token.successorHash.String).
		Scan(&successorCredentialID, &successorConsumed)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.BrowserRefreshOutcome{}, false, nil
	}
	if err != nil {
		return storage.BrowserRefreshOutcome{}, false, fmt.Errorf("lock successor refresh token: %w", sanitizeDatabaseError(err))
	}
	if successorConsumed.Valid {
		return storage.BrowserRefreshOutcome{}, false, nil
	}
	successor, err := lockedCredential(ctx, tx, family.orgID, successorCredentialID)
	if err != nil {
		return storage.BrowserRefreshOutcome{}, false, err
	}
	if successor.RevokedAt != nil {
		return storage.BrowserRefreshOutcome{}, false, nil
	}
	var successorTokenHash string
	if err := tx.QueryRowContext(ctx, `SELECT token_hash FROM acr.client_credentials WHERE credential_id = $1`, successorCredentialID).Scan(&successorTokenHash); err != nil {
		return storage.BrowserRefreshOutcome{}, false, fmt.Errorf("read successor token hash: %w", sanitizeDatabaseError(err))
	}
	return storage.BrowserRefreshOutcome{
		Decision: storage.BrowserRefreshGraceReplay, Credential: successor, Seed: append([]byte(nil), token.seed...),
		SuccessorTokenHash: successorTokenHash, SuccessorRefreshHash: token.successorHash.String,
	}, true, nil
}

// revokeBrowserFamilyTx revokes a family row (once) and every member
// credential still live, writing one credential_revoked audit row each. It
// returns the number of credentials it revoked.
func (s *BrowserRefreshStore) revokeBrowserFamilyTx(ctx context.Context, tx *sql.Tx, orgID, familyID, reason, actorID string, now time.Time) (int, error) {
	// The refresh paths (member_revoked, reuse, sign_out) act as the refresh grant:
	// a user actor named by the grant's actor id, as their audit rows record.
	return revokeBrowserFamilyTx(ctx, tx, s.audit, orgID, familyID, reason, actorID, credentiallifecycle.Actor{Type: credentiallifecycle.ActorUser, Ref: actorID}, now)
}

// revokeBrowserFamilyTx writes every sibling event with the one actor the caller
// mapped: nothing here rebuilds an actor from the actor id.
func revokeBrowserFamilyTx(ctx context.Context, tx *sql.Tx, audit *AuditStore, orgID, familyID, reason, actorID string, actor credentiallifecycle.Actor, now time.Time) (int, error) {
	if _, err := tx.ExecContext(ctx, `
UPDATE acr.browser_token_families SET revoked_at = $2, revoked_reason = $3
WHERE family_id = $1 AND revoked_at IS NULL`, familyID, now, reason); err != nil {
		return 0, fmt.Errorf("revoke browser family: %w", sanitizeDatabaseError(err))
	}
	rows, err := tx.QueryContext(ctx, `
SELECT credential_id, name, token_prefix, org_id, repository_scopes, scopes,
       created_at, expires_at, revoked_at, last_used_at, workload_binding_id, resource,
       client_class, approving_user_id, client_id, session_id, family_id, family_expires_at
FROM acr.client_credentials
WHERE org_id = $1::uuid AND family_id = $2 AND revoked_at IS NULL
ORDER BY credential_id
FOR UPDATE`, orgID, familyID)
	if err != nil {
		return 0, fmt.Errorf("lock browser family credentials: %w", sanitizeDatabaseError(err))
	}
	var revoked []contractsv1.ClientCredential
	for rows.Next() {
		member, err := scanCredential(rows)
		if err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan revoked family credential: %w", sanitizeDatabaseError(err))
		}
		revoked = append(revoked, member)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close revoked family credentials: %w", sanitizeDatabaseError(err))
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("revoked family credentials: %w", sanitizeDatabaseError(err))
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE acr.client_credentials SET revoked_at = $3
WHERE org_id = $1::uuid AND family_id = $2 AND revoked_at IS NULL`, orgID, familyID, now); err != nil {
		return 0, fmt.Errorf("revoke browser family credentials: %w", sanitizeDatabaseError(err))
	}
	for _, member := range revoked {
		event := storage.AuditEvent{
			OrgID: orgID, ActorType: "user", ActorID: actorID,
			Action: storage.AuditActionCredentialRevoked, ResourceType: "acr_credential", ResourceID: member.CredentialID,
			Status: "success", CreatedAt: now, Metadata: map[string]any{"family_id": familyID, "family_revoke_reason": reason},
		}
		if err := audit.record(ctx, tx, event); err != nil {
			return 0, err
		}
		if err := audit.appendLifecycle(ctx, tx, credentiallifecycle.Facts{Kind: credentiallifecycle.KindRevoked, Credential: member, At: now, Actor: actor}); err != nil {
			return 0, err
		}
	}
	return len(revoked), nil
}

// RevokeBrowserFamilyByToken revokes the family a refresh token belongs to,
// for the client that holds it (sign-out; it works when every access token of
// the family has expired).
func (s *BrowserRefreshStore) RevokeBrowserFamilyByToken(ctx context.Context, presentedHash, clientID, actorID string, now time.Time) (storage.BrowserFamilyRevocationOutcome, error) {
	if err := s.ready(ctx); err != nil {
		return storage.BrowserFamilyRevocationOutcome{}, err
	}
	if presentedHash == "" || clientID == "" || actorID == "" || now.IsZero() {
		return storage.BrowserFamilyRevocationOutcome{}, storage.ErrInvalidBrowserRefresh
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return storage.BrowserFamilyRevocationOutcome{}, fmt.Errorf("begin browser family revoke: %w", sanitizeDatabaseError(err))
	}
	defer func() { _ = tx.Rollback() }()
	family, token, found, err := lockBrowserFamily(ctx, tx, presentedHash)
	if err != nil {
		return storage.BrowserFamilyRevocationOutcome{}, err
	}
	if !found {
		return storage.BrowserFamilyRevocationOutcome{Decision: storage.BrowserRefreshUnknownToken}, nil
	}
	outcome := storage.BrowserFamilyRevocationOutcome{OrgID: family.orgID, FamilyID: token.familyID}
	switch {
	case family.clientID != clientID:
		outcome.Decision = storage.BrowserRefreshClientMismatch
		return outcome, nil
	case family.revokedAt.Valid:
		outcome.Decision = storage.BrowserRefreshFamilyRevoked
		return outcome, nil
	}
	revoked, err := s.revokeBrowserFamilyTx(ctx, tx, family.orgID, token.familyID, "sign_out", actorID, now.UTC())
	if err != nil {
		return storage.BrowserFamilyRevocationOutcome{}, err
	}
	outcome.Decision, outcome.RevokedCredentials = storage.BrowserFamilyRevokedNow, revoked
	return outcome, s.commit(tx, "browser family revoke")
}
