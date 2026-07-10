package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type AuditStore struct {
	DB         *sql.DB
	GenerateID func() (string, error)
}

func NewAuditStore(db *sql.DB) (*AuditStore, error) {
	if db == nil {
		return nil, errors.New("PostgreSQL database is required")
	}
	return &AuditStore{DB: db, GenerateID: generateUUID}, nil
}

func (s *AuditStore) Record(ctx context.Context, event storage.AuditEvent) error {
	id, err := s.GenerateID()
	if err != nil {
		return fmt.Errorf("generate audit id: %w", err)
	}
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return fmt.Errorf("encode audit metadata: %w", err)
	}
	_, err = s.DB.ExecContext(ctx, `
INSERT INTO acr.audit_events (
    audit_event_id, org_id, repo_id, actor_type, actor_id, action,
    resource_type, resource_id, status, request_id, metadata, created_at
) VALUES ($1::uuid, $2::uuid, NULLIF($3, '')::uuid, $4, $5, $6, $7, $8, $9, NULLIF($10, ''), $11::jsonb, $12)`,
		id, event.OrgID, event.RepoID, event.ActorType, event.ActorID, event.Action,
		event.ResourceType, event.ResourceID, event.Status, event.RequestID, string(metadata), event.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

func generateUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}
