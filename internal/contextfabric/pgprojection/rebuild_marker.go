package pgprojection

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RebuildMarkerStore is the production
// internal/contextfabric/projectionrun.RebuildMarker. Its presence-of-a-row
// contract is documented on migration 0007
// (context_fabric_projection_rebuild_markers) and enforces the CHAOS-3753
// codex finding C2 invariant: no code path may run incremental projection
// against a purged-but-not-reset graph. See
// docs/design/context-fabric-projection-worker.md.
type RebuildMarkerStore struct {
	db *sql.DB
}

func NewRebuildMarkerStore(db *sql.DB) (*RebuildMarkerStore, error) {
	if db == nil {
		return nil, errors.New("pgprojection: rebuild marker store requires a database")
	}
	return &RebuildMarkerStore{db: db}, nil
}

// BeginRebuild marks orgID as having a rebuild in progress. Idempotent: a
// second call while a marker is already present (a resume, or a racing
// concurrent Rebuild call under the same org lock) is a no-op, not an
// error, so crash-recovery can call it unconditionally.
func (s *RebuildMarkerStore) BeginRebuild(ctx context.Context, orgID string) error {
	orgID = strings.TrimSpace(orgID)
	if s == nil || s.db == nil {
		return errors.New("pgprojection: rebuild marker store is not configured")
	}
	if orgID == "" {
		return errors.New("pgprojection: organization is required")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_projection_rebuild_markers (org_id, started_at)
VALUES ($1, $2)
ON CONFLICT (org_id) DO NOTHING`, orgID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("begin rebuild marker: %w", sanitizeError(err))
	}
	return nil
}

// IsRebuildInProgress reports whether orgID currently has a rebuild marker.
func (s *RebuildMarkerStore) IsRebuildInProgress(ctx context.Context, orgID string) (bool, error) {
	orgID = strings.TrimSpace(orgID)
	if s == nil || s.db == nil {
		return false, errors.New("pgprojection: rebuild marker store is not configured")
	}
	if orgID == "" {
		return false, errors.New("pgprojection: organization is required")
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM acr.context_fabric_projection_rebuild_markers WHERE org_id = $1)`, orgID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check rebuild marker: %w", sanitizeError(err))
	}
	return exists, nil
}

// CompleteRebuild clears orgID's rebuild marker. Idempotent: clearing an
// already-absent marker is not an error.
func (s *RebuildMarkerStore) CompleteRebuild(ctx context.Context, orgID string) error {
	orgID = strings.TrimSpace(orgID)
	if s == nil || s.db == nil {
		return errors.New("pgprojection: rebuild marker store is not configured")
	}
	if orgID == "" {
		return errors.New("pgprojection: organization is required")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM acr.context_fabric_projection_rebuild_markers WHERE org_id = $1`, orgID); err != nil {
		return fmt.Errorf("complete rebuild marker: %w", sanitizeError(err))
	}
	return nil
}
