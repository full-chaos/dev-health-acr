// Package pgmodelreceipts is the production Postgres implementation of
// contextfabric.ModelReceiptSink (CHAOS-3775, AC-3775-6; closes drift item
// D16 -- §19.13 confirmed no non-test ModelReceiptSink implementation
// existed anywhere on main). It durably records every model execution
// receipt: success, fallback, invalid_output, rate_limited, and
// unavailable outcomes alike, for every organization's runtime, so usage,
// cost, and the fallback rate are measurable.
//
// Insert-only: this package never updates or deletes a receipt row. A
// receipt is a fact about what happened, not mutable state.
package pgmodelreceipts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Store is the production contextfabric.ModelReceiptSink. The caller owns
// database construction; this package never parses or logs DSNs
// (repository convention, internal/storage/AGENTS.md).
type Store struct {
	db         *sql.DB
	generateID func() (string, error)
}

var _ contextfabric.ModelReceiptSink = (*Store)(nil)

// NewStore builds a Store around a caller-owned *sql.DB.
func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("pgmodelreceipts: store requires a database")
	}
	return &Store{db: db, generateID: generateUUID}, nil
}

// RecordModelExecution persists receipt durably for principal.OrgID.
// AC-3775-5 (CredentialMasked) does not apply here: ModelExecutionReceipt
// carries no credential field at all, by contract (internal/contracts/v1),
// so there is nothing to mask or leak (AC-3770-5/AC-3775-4's "never in a
// receipt" is enforced structurally, not by redaction here).
func (s *Store) RecordModelExecution(ctx context.Context, principal storage.Principal, receipt contextfabric.ModelExecutionReceipt) error {
	if s == nil || s.db == nil || s.generateID == nil {
		return errors.New("pgmodelreceipts: store is not configured")
	}
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return errors.New("pgmodelreceipts: organization is required")
	}
	if err := receipt.Validate(); err != nil {
		return fmt.Errorf("pgmodelreceipts: invalid receipt: %w", err)
	}
	id, err := s.generateID()
	if err != nil {
		return fmt.Errorf("pgmodelreceipts: generate receipt id: %w", err)
	}
	payload, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("pgmodelreceipts: marshal receipt: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_model_execution_receipts
    (receipt_id, org_id, operation, provider, outcome, fallback_used, payload, started_at, completed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		id, orgID, string(receipt.Operation), receipt.Provider, receipt.Outcome, receipt.FallbackUsed,
		payload, receipt.StartedAt, receipt.CompletedAt)
	if err != nil {
		return fmt.Errorf("pgmodelreceipts: record receipt: %w", sanitizeError(err))
	}
	return nil
}

// sanitizeError classifies a raw database error into the sentinel the
// CONSUMING seam actually checks. Unlike pgmodelconfig (whose errors reach
// internal/api/context_fabric_model_config_routes.go, which classifies on
// storage.Err*), a ModelReceiptSink failure reaches
// RuntimeQuestionInterpreter/RuntimeAnswerSynthesizer
// (internal/contextfabric/model_runtime.go) and from there
// internal/api/context_fabric_routes.go's writeContextFabricError, which
// classifies ONLY on contextfabric.Err*/context.*. Wrapping storage.ErrUnavailable
// here (as pgmodelconfig does) would have been invisible to that classifier:
// errors.Is(err, contextfabric.ErrUnavailable) is false for a
// storage.ErrUnavailable-wrapped error, so a transient sink outage would
// fall through to the route's generic 500 internal_error branch instead of
// the declared 503 upstream_unavailable (Codex round-1 finding F2). This
// mirrors internal/contextfabric/pginvestigation.sanitizeError, the other
// store that feeds the same investigation route/engine.
func sanitizeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return fmt.Errorf("%w: %v", contextfabric.ErrUnavailable, err)
}

// generateUUID mirrors internal/storage/postgres.generateUUID: a
// crypto/rand-backed UUIDv4, no external dependency.
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
