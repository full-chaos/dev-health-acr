package pgprojection

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CheckpointStore is the production contextfabric.ProjectionCheckpointStore.
// The caller owns database construction; this package never parses or logs
// DSNs (repository convention, internal/storage/AGENTS.md).
type CheckpointStore struct {
	db *sql.DB
}

func NewCheckpointStore(db *sql.DB) (*CheckpointStore, error) {
	if db == nil {
		return nil, errors.New("pgprojection: checkpoint store requires a database")
	}
	return &CheckpointStore{db: db}, nil
}

func (s *CheckpointStore) LoadProjectionCheckpoint(ctx context.Context, orgID, source string) (contextfabric.ProjectionCheckpoint, error) {
	if s == nil || s.db == nil {
		return contextfabric.ProjectionCheckpoint{}, errors.New("pgprojection: checkpoint store is not configured")
	}
	orgID, source = strings.TrimSpace(orgID), strings.TrimSpace(source)
	if orgID == "" || source == "" {
		return contextfabric.ProjectionCheckpoint{}, errors.New("pgprojection: organization and source are required")
	}
	row := s.db.QueryRowContext(ctx, `
SELECT cursor, source_version, backend_watermark, updated_at
FROM acr.context_fabric_projection_checkpoints
WHERE org_id = $1 AND source = $2 AND epoch = 0`, orgID, source)
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: orgID, Source: source}
	err := row.Scan(&checkpoint.Cursor, &checkpoint.SourceVersion, &checkpoint.BackendWatermark, &checkpoint.UpdatedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Zero-value cursor: never projected. This is the sentinel
		// devhealthsource treats as "start a full snapshot".
		return checkpoint, nil
	case err != nil:
		return contextfabric.ProjectionCheckpoint{}, fmt.Errorf("load projection checkpoint: %w", sanitizeError(err))
	default:
		checkpoint.UpdatedAt = checkpoint.UpdatedAt.UTC()
		return checkpoint, nil
	}
}

// CompareAndSwapProjectionCheckpoint advances the durable checkpoint only
// when the row it reads back still matches the cursor the caller last
// observed (expected.Cursor).
//
// It does NOT dispatch on "expected.Cursor == \"\" means no row exists yet":
// that was wrong (CHAOS-3753 codex finding C1) -- a rebuild resets an
// EXISTING row's cursor back to "", so a later replay's expected.Cursor is
// also "" while a real row is present, and INSERT ... ON CONFLICT DO
// NOTHING against that already-existing row silently affects zero rows
// forever, permanently misreporting ErrProjectionConflict. Instead it
// always attempts the UPDATE (which matches an existing row regardless of
// what its cursor value is, including ""); only when that affects zero
// rows does it attempt the INSERT, which can only succeed if no row
// existed at all. Both affecting zero rows is a genuine conflict.
func (s *CheckpointStore) CompareAndSwapProjectionCheckpoint(ctx context.Context, expected, updated contextfabric.ProjectionCheckpoint) error {
	if s == nil || s.db == nil {
		return errors.New("pgprojection: checkpoint store is not configured")
	}
	if expected.OrgID != updated.OrgID || expected.Source != updated.Source || strings.TrimSpace(updated.OrgID) == "" || strings.TrimSpace(updated.Source) == "" {
		return errors.New("pgprojection: checkpoint compare-and-swap organization/source mismatch")
	}
	if updated.UpdatedAt.IsZero() {
		updated.UpdatedAt = time.Now().UTC()
	}
	updateResult, err := s.db.ExecContext(ctx, `
UPDATE acr.context_fabric_projection_checkpoints
SET cursor = $3, source_version = $4, backend_watermark = $5, updated_at = $6
WHERE org_id = $1 AND source = $2 AND epoch = 0 AND cursor = $7`,
		updated.OrgID, updated.Source, updated.Cursor, updated.SourceVersion, updated.BackendWatermark, updated.UpdatedAt, expected.Cursor)
	if err != nil {
		return fmt.Errorf("advance projection checkpoint: %w", sanitizeError(err))
	}
	if rows, err := updateResult.RowsAffected(); err != nil {
		return fmt.Errorf("advance projection checkpoint rows affected: %w", sanitizeError(err))
	} else if rows == 1 {
		return nil
	}
	// No existing row matched (org_id, source, epoch=0, cursor=expected).
	// Either no row exists yet at all (first-ever checkpoint) or a row
	// exists with a different cursor (a genuine conflict). Try the insert;
	// ON CONFLICT DO NOTHING makes a racing first-ever insert -- or a row
	// that turned out to already exist -- lose cleanly rather than corrupt
	// the row.
	insertResult, err := s.db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_projection_checkpoints (org_id, source, epoch, cursor, source_version, backend_watermark, updated_at)
VALUES ($1, $2, 0, $3, $4, $5, $6)
ON CONFLICT (org_id, epoch, source) DO NOTHING`,
		updated.OrgID, updated.Source, updated.Cursor, updated.SourceVersion, updated.BackendWatermark, updated.UpdatedAt)
	if err != nil {
		return fmt.Errorf("advance projection checkpoint: %w", sanitizeError(err))
	}
	rows, err := insertResult.RowsAffected()
	if err != nil {
		return fmt.Errorf("advance projection checkpoint rows affected: %w", sanitizeError(err))
	}
	if rows != 1 {
		return contextfabric.ErrProjectionConflict
	}
	return nil
}

// LoadProjectionCheckpointForEpoch is LoadProjectionCheckpoint's CHAOS-3898
// S2a epoch-scoped counterpart (design brief §3.4): it reads the checkpoint
// set for a SPECIFIC epoch, not only the legacy epoch 0 the interface
// method above is pinned to. This is an ADDITIONAL exported method, not a
// change to contextfabric.ProjectionCheckpointStore's signature -- widening
// that interface would ripple through every existing implementation and
// test fake for a capability nothing in production calls yet (S2a ships
// the lifecycle CAS machinery and its own acceptance tests; wiring the
// projector to actually drive ticks against a build-target epoch's
// checkpoint set is S2/conversion work). Mirrors the optional-capability
// shape pginvestigation.Store already uses for AnswerReuseGate/
// ReuseInvalidator: one concrete type, several typed capabilities, each
// consumed only by the caller that needs it.
func (s *CheckpointStore) LoadProjectionCheckpointForEpoch(ctx context.Context, orgID string, epoch int64, source string) (contextfabric.ProjectionCheckpoint, error) {
	if s == nil || s.db == nil {
		return contextfabric.ProjectionCheckpoint{}, errors.New("pgprojection: checkpoint store is not configured")
	}
	orgID, source = strings.TrimSpace(orgID), strings.TrimSpace(source)
	if orgID == "" || source == "" || epoch < 0 {
		return contextfabric.ProjectionCheckpoint{}, errors.New("pgprojection: organization, non-negative epoch, and source are required")
	}
	row := s.db.QueryRowContext(ctx, `
SELECT cursor, source_version, backend_watermark, updated_at
FROM acr.context_fabric_projection_checkpoints
WHERE org_id = $1 AND source = $2 AND epoch = $3`, orgID, source, epoch)
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: orgID, Source: source, Epoch: epoch}
	err := row.Scan(&checkpoint.Cursor, &checkpoint.SourceVersion, &checkpoint.BackendWatermark, &checkpoint.UpdatedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Zero-value cursor for an epoch with no row yet: exactly the state
		// design brief §3.1 step 2 calls "open the target epoch's OWN
		// checkpoint set" -- there is nothing to reset because nothing was
		// ever written under this epoch, the same sentinel
		// LoadProjectionCheckpoint already uses for "never projected".
		return checkpoint, nil
	case err != nil:
		return contextfabric.ProjectionCheckpoint{}, fmt.Errorf("load projection checkpoint for epoch: %w", sanitizeError(err))
	default:
		checkpoint.UpdatedAt = checkpoint.UpdatedAt.UTC()
		return checkpoint, nil
	}
}

// CompareAndSwapProjectionCheckpointForEpoch is
// CompareAndSwapProjectionCheckpoint's epoch-scoped counterpart. expected
// and updated must carry the SAME (OrgID, Source, Epoch) -- see
// CompareAndSwapProjectionCheckpoint's own doc comment for why the UPDATE
// is always attempted first regardless of expected.Cursor, and why a
// zero-rows UPDATE falls through to an ON-CONFLICT-safe INSERT rather than
// dispatching on whether expected.Cursor is empty.
func (s *CheckpointStore) CompareAndSwapProjectionCheckpointForEpoch(ctx context.Context, expected, updated contextfabric.ProjectionCheckpoint) error {
	if s == nil || s.db == nil {
		return errors.New("pgprojection: checkpoint store is not configured")
	}
	if expected.OrgID != updated.OrgID || expected.Source != updated.Source || expected.Epoch != updated.Epoch ||
		strings.TrimSpace(updated.OrgID) == "" || strings.TrimSpace(updated.Source) == "" || updated.Epoch < 0 {
		return errors.New("pgprojection: checkpoint compare-and-swap organization/epoch/source mismatch")
	}
	if updated.UpdatedAt.IsZero() {
		updated.UpdatedAt = time.Now().UTC()
	}
	updateResult, err := s.db.ExecContext(ctx, `
UPDATE acr.context_fabric_projection_checkpoints
SET cursor = $4, source_version = $5, backend_watermark = $6, updated_at = $7
WHERE org_id = $1 AND source = $2 AND epoch = $3 AND cursor = $8`,
		updated.OrgID, updated.Source, updated.Epoch, updated.Cursor, updated.SourceVersion, updated.BackendWatermark, updated.UpdatedAt, expected.Cursor)
	if err != nil {
		return fmt.Errorf("advance projection checkpoint for epoch: %w", sanitizeError(err))
	}
	if rows, err := updateResult.RowsAffected(); err != nil {
		return fmt.Errorf("advance projection checkpoint for epoch rows affected: %w", sanitizeError(err))
	} else if rows == 1 {
		return nil
	}
	insertResult, err := s.db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_projection_checkpoints (org_id, source, epoch, cursor, source_version, backend_watermark, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (org_id, epoch, source) DO NOTHING`,
		updated.OrgID, updated.Source, updated.Epoch, updated.Cursor, updated.SourceVersion, updated.BackendWatermark, updated.UpdatedAt)
	if err != nil {
		return fmt.Errorf("advance projection checkpoint for epoch: %w", sanitizeError(err))
	}
	rows, err := insertResult.RowsAffected()
	if err != nil {
		return fmt.Errorf("advance projection checkpoint for epoch rows affected: %w", sanitizeError(err))
	}
	if rows != 1 {
		return contextfabric.ErrProjectionConflict
	}
	return nil
}

// DeleteEpochCheckpoints removes every checkpoint row for (orgID, epoch) --
// the WHOLE checkpoint set a retired epoch's graph key carried, deleted
// together with its GRAPH.DELETE by the retire executor (design brief
// §3.5's "delete-together promise"). The retire executor's own final-key
// guard (which epoch != the organization's CURRENT active epoch) is the
// safety check that gates this call being reached at all -- this method
// itself only refuses a negative epoch (never legitimately reachable) and
// otherwise deletes whatever epoch it is told to, INCLUDING epoch 0: an
// organization's first-ever flip (0 -> 1) legitimately retires epoch 0's
// checkpoint set exactly like any later abandoned epoch, once epoch 0 is
// no longer the active one.
func (s *CheckpointStore) DeleteEpochCheckpoints(ctx context.Context, orgID string, epoch int64) error {
	if s == nil || s.db == nil {
		return errors.New("pgprojection: checkpoint store is not configured")
	}
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return errors.New("pgprojection: organization is required")
	}
	if epoch < 0 {
		return errors.New("pgprojection: epoch must be non-negative")
	}
	if _, err := s.db.ExecContext(ctx, `
DELETE FROM acr.context_fabric_projection_checkpoints
WHERE org_id = $1 AND epoch = $2`, orgID, epoch); err != nil {
		return fmt.Errorf("delete epoch projection checkpoints: %w", sanitizeError(err))
	}
	return nil
}

func sanitizeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: %v", contextfabric.ErrUnavailable, err)
}
