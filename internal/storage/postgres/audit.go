package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type AuditStore struct {
	DB         *sql.DB
	GenerateID func() (string, error)
	mu         sync.Mutex
	lifecycle  *credentialStore
}

func (s *AuditStore) bindLifecycle(store *credentialStore) error {
	if s == nil || store == nil {
		return errors.New("credential and audit stores are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lifecycle != nil {
		return storage.ErrConflict
	}
	s.lifecycle = store
	return nil
}

func NewAuditStore(db *sql.DB) (*AuditStore, error) {
	if db == nil {
		return nil, errors.New("PostgreSQL database is required")
	}
	return &AuditStore{DB: db, GenerateID: generateUUID}, nil
}

func (s *AuditStore) Record(ctx context.Context, event storage.AuditEvent) error {
	if s == nil || s.DB == nil || s.GenerateID == nil || storage.IsNil(ctx) {
		return storage.ErrInvalidCredentialLifecycle
	}
	if storage.IsCredentialLifecycleAuditAction(event.Action) {
		return storage.ErrInvalidCredentialInput
	}
	return s.record(ctx, s.DB, event)
}

func (s *AuditStore) record(ctx context.Context, executor execer, event storage.AuditEvent) error {
	if s == nil || s.GenerateID == nil || storage.IsNil(executor) || ctx == nil {
		return storage.ErrInvalidCredentialLifecycle
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := s.GenerateID()
	if err != nil {
		return fmt.Errorf("generate audit id: %w", err)
	}
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return fmt.Errorf("encode audit metadata: %w", err)
	}
	_, err = executor.ExecContext(ctx, `
INSERT INTO acr.audit_events (
    audit_event_id, org_id, repo_id, actor_type, actor_id, action,
    resource_type, resource_id, status, request_id, metadata, created_at
) VALUES ($1::uuid, $2::uuid, NULLIF($3, '')::uuid, $4, $5, $6, $7, $8, $9, NULLIF($10, ''), $11::jsonb, $12)`,
		id, event.OrgID, event.RepoID, event.ActorType, event.ActorID, event.Action,
		event.ResourceType, event.ResourceID, event.Status, event.RequestID, string(metadata), event.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert audit event: %w", sanitizeDatabaseError(err))
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
