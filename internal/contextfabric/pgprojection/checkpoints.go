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
WHERE org_id = $1 AND source = $2`, orgID, source)
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
WHERE org_id = $1 AND source = $2 AND cursor = $7`,
		updated.OrgID, updated.Source, updated.Cursor, updated.SourceVersion, updated.BackendWatermark, updated.UpdatedAt, expected.Cursor)
	if err != nil {
		return fmt.Errorf("advance projection checkpoint: %w", sanitizeError(err))
	}
	if rows, err := updateResult.RowsAffected(); err != nil {
		return fmt.Errorf("advance projection checkpoint rows affected: %w", sanitizeError(err))
	} else if rows == 1 {
		return nil
	}
	// No existing row matched (org_id, source, cursor=expected). Either no
	// row exists yet at all (first-ever checkpoint) or a row exists with a
	// different cursor (a genuine conflict). Try the insert; ON CONFLICT DO
	// NOTHING makes a racing first-ever insert -- or a row that turned out
	// to already exist -- lose cleanly rather than corrupt the row.
	insertResult, err := s.db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_projection_checkpoints (org_id, source, cursor, source_version, backend_watermark, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (org_id, source) DO NOTHING`,
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

func sanitizeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: %v", contextfabric.ErrUnavailable, err)
}
