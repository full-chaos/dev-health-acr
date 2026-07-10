package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

func (s *PacketStore) PurgeExpiredWithAudit(ctx context.Context, before time.Time, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin packet snapshot purge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
WITH expired AS (
    SELECT context_packet_id FROM acr.context_packet_snapshots
    WHERE expires_at <= $1
    ORDER BY expires_at, context_packet_id
    LIMIT $2
    FOR UPDATE SKIP LOCKED
)
DELETE FROM acr.context_packet_snapshots snapshot
USING expired
WHERE snapshot.context_packet_id = expired.context_packet_id
RETURNING snapshot.context_packet_id, snapshot.org_id::text, snapshot.repo_id::text, snapshot.expires_at`, before.UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired packet snapshots: %w", err)
	}
	snapshots := make([]purgedSnapshot, 0, limit)
	for rows.Next() {
		var snapshot purgedSnapshot
		if err := rows.Scan(&snapshot.packetID, &snapshot.orgID, &snapshot.repoID, &snapshot.expiresAt); err != nil {
			return 0, fmt.Errorf("scan purged packet snapshot: %w", err)
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate purged packet snapshots: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close purged packet snapshots: %w", err)
	}
	for _, snapshot := range snapshots {
		if err := insertSnapshotPurgeAudit(ctx, tx, snapshot.packetID, snapshot.orgID, snapshot.repoID, snapshot.expiresAt, before, s.now().UTC()); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit packet snapshot purge: %w", err)
	}
	return len(snapshots), nil
}

type purgedSnapshot struct {
	packetID, orgID, repoID string
	expiresAt               time.Time
}

func insertSnapshotPurgeAudit(ctx context.Context, tx *sql.Tx, packetID, orgID, repoID string, expiresAt, cutoff, createdAt time.Time) error {
	id, err := generateUUID()
	if err != nil {
		return fmt.Errorf("generate packet purge audit id: %w", err)
	}
	metadata, err := json.Marshal(map[string]string{"expires_at": expiresAt.UTC().Format(time.RFC3339Nano), "cutoff": cutoff.UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return fmt.Errorf("encode packet purge audit metadata: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO acr.audit_events (
    audit_event_id, org_id, repo_id, actor_type, actor_id, action,
    resource_type, resource_id, status, request_id, metadata, created_at
) VALUES ($1::uuid, $2::uuid, $3::uuid, 'system', 'retention_worker',
          'context_packet_snapshot_purged', 'context_packet_snapshot', $4,
          'success', NULL, $5::jsonb, $6)`, id, orgID, repoID, packetID, string(metadata), createdAt)
	if err != nil {
		return fmt.Errorf("insert packet purge audit: %w", err)
	}
	return nil
}
