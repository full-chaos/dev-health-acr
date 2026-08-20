// Package pgstructurepriors is the production Postgres implementation of
// contextfabric.PriorStore (CHAOS-3977 P5, design brief §3.3): the
// versioned, immutable, org-scoped Bridge prior store's read AND write
// sides. Unlike pgstructureselection (P4's capture-only sink), this
// package legitimately owns BOTH: curation (structurepriorcuration) and
// cmd/acr-projector's own "priors" operator subcommand are its write
// callers, and Engine's own runtime consultation (via
// contextfabric.NewPriorConsultant, wrapping Store) is its read caller.
//
// CAS discipline for the active-version pointer mirrors pglifecycle.Store
// exactly (that package's own header comment): every flip/rollback is
//
//	UPDATE acr.context_fabric_structure_prior_pointer
//	SET active_version = $new, previous_version = $old, ...
//	WHERE org_id = $1 AND active_version IS NOT DISTINCT FROM $expected
//
// so exactly one concurrent transition wins any race; the loser observes
// zero rows affected and gets contextfabric.ErrPriorPointerConflict.
package pgstructurepriors

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Telemetry is the write-side signal sink (design brief §3.4's
// cf_prior_flip_cas_conflict, folded into the SAME PriorDegradationState
// enum contextfabric's own read-side telemetry uses -- see
// contextfabric.PriorDegradationFlipCASConflict's own doc comment for why
// one vocabulary, not two).
type Telemetry interface {
	RecordFlipCASConflict(ctx context.Context, orgID string)
}

type noopTelemetry struct{}

func (noopTelemetry) RecordFlipCASConflict(context.Context, string) {}

// Store is the production contextfabric.PriorStore, plus the write-side
// methods curation and the operator CLI need. The caller owns database
// construction; this package never parses or logs DSNs (repository
// convention, internal/storage/AGENTS.md).
type Store struct {
	db        *sql.DB
	Telemetry Telemetry
}

func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("pgstructurepriors: store requires a database")
	}
	return &Store{db: db}, nil
}

func (s *Store) telemetry() Telemetry {
	if s.Telemetry != nil {
		return s.Telemetry
	}
	return noopTelemetry{}
}

var _ contextfabric.PriorStore = (*Store)(nil)

// entryJSON mirrors contextfabric.StructurePriorEntry's OWN persisted
// shape, one array element inside context_fabric_structure_priors.entries
// -- EntryID/QuestionHash/Version are NOT duplicated inside each element
// (Version is the row's own column; EntryID/QuestionHash are, but kept
// here for self-description and future-proofing an entries[] extraction
// query). Revoked is NEVER stored here -- it is computed at READ time from
// acr.context_fabric_structure_prior_revocations, never persisted onto the
// immutable snapshot itself (a later revocation must never require
// rewriting an already-published version).
type entryJSON struct {
	EntryID             string `json:"entry_id"`
	QuestionHash        string `json:"question_hash"`
	Member              string `json:"member"`
	Value               string `json:"value"`
	Kind                string `json:"kind,omitempty"`
	PatternID           string `json:"pattern_id,omitempty"`
	SupportHumanPanel   int    `json:"support_human_panel"`
	SupportAgentReceipt int    `json:"support_agent_receipt"`
	SupportConsensus    int    `json:"support_consensus"`
	Rank                int    `json:"rank"`
}

func toEntryJSON(e contextfabric.StructurePriorEntry) entryJSON {
	return entryJSON{
		EntryID: e.EntryID, QuestionHash: e.QuestionHash, Member: string(e.Member), Value: e.Value,
		Kind: string(e.Kind), PatternID: e.PatternID,
		SupportHumanPanel: e.SupportHumanPanel, SupportAgentReceipt: e.SupportAgentReceipt, SupportConsensus: e.SupportConsensus,
		Rank: e.Rank,
	}
}

// GetActive implements contextfabric.PriorStore: read the org's active-
// version pointer, load that version's entries, and mark each one's
// Revoked flag from acr.context_fabric_structure_prior_revocations.
func (s *Store) GetActive(ctx context.Context, orgID string) (contextfabric.StructurePriorSet, bool, contextfabric.PriorDegradationState, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return contextfabric.StructurePriorSet{}, false, contextfabric.PriorDegradationNone, errors.New("pgstructurepriors: organization is required")
	}
	var activeVersion sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
SELECT active_version FROM acr.context_fabric_structure_prior_pointer WHERE org_id = $1`, orgID).Scan(&activeVersion)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !activeVersion.Valid) {
		// No pointer row, or a pointer explicitly set to NULL (whole-version
		// revocation-to-nothing) -- §3.7 cold start either way.
		return contextfabric.StructurePriorSet{}, false, contextfabric.PriorDegradationNone, nil
	}
	if err != nil {
		return contextfabric.StructurePriorSet{}, false, contextfabric.PriorDegradationNone, fmt.Errorf("pgstructurepriors: read active pointer: %w", sanitize(err))
	}

	var (
		entriesRaw   []byte
		watermark    string
		curationRule string
	)
	err = s.db.QueryRowContext(ctx, `
SELECT entries, created_from_watermark, curation_rule_version
FROM acr.context_fabric_structure_priors
WHERE org_id = $1 AND version = $2`, orgID, activeVersion.Int64).Scan(&entriesRaw, &watermark, &curationRule)
	if errors.Is(err, sql.ErrNoRows) {
		// The pointer names a version this table does not have -- a retire
		// outran its grace, or a data-integrity fault (design brief §3.4).
		return contextfabric.StructurePriorSet{}, false, contextfabric.PriorDegradationPointerDangling, nil
	}
	if err != nil {
		return contextfabric.StructurePriorSet{}, false, contextfabric.PriorDegradationNone, fmt.Errorf("pgstructurepriors: read active version: %w", sanitize(err))
	}

	var raw []entryJSON
	if err := json.Unmarshal(entriesRaw, &raw); err != nil {
		return contextfabric.StructurePriorSet{}, false, contextfabric.PriorDegradationNone, fmt.Errorf("pgstructurepriors: decode entries: %w", err)
	}

	revoked, err := s.revokedEntryIDs(ctx, orgID)
	if err != nil {
		return contextfabric.StructurePriorSet{}, false, contextfabric.PriorDegradationNone, err
	}

	entries := make([]contextfabric.StructurePriorEntry, 0, len(raw))
	for _, r := range raw {
		entries = append(entries, contextfabric.StructurePriorEntry{
			EntryID: r.EntryID, QuestionHash: r.QuestionHash, Version: activeVersion.Int64,
			Member: contractsv1.ContextFabricStructureNeedKind(r.Member),
			Value:  r.Value, Kind: contractsv1.ContextFabricSubjectKind(r.Kind), PatternID: r.PatternID,
			SupportHumanPanel: r.SupportHumanPanel, SupportAgentReceipt: r.SupportAgentReceipt, SupportConsensus: r.SupportConsensus,
			Rank: r.Rank, Revoked: revoked[r.EntryID],
		})
	}
	return contextfabric.StructurePriorSet{
		OrgID: orgID, Version: activeVersion.Int64, Entries: entries,
		CreatedFromWatermark: watermark, CurationRuleVersion: curationRule,
	}, true, contextfabric.PriorDegradationNone, nil
}

func (s *Store) revokedEntryIDs(ctx context.Context, orgID string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT entry_id FROM acr.context_fabric_structure_prior_revocations WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, fmt.Errorf("pgstructurepriors: read revocations: %w", sanitize(err))
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("pgstructurepriors: scan revocation: %w", sanitize(err))
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgstructurepriors: read revocations: %w", sanitize(err))
	}
	return out, nil
}

// PublishVersion is curation's own write entry point (structurepriorcuration.Curate,
// the sole production caller): INSERT a new immutable snapshot at
// max(version)+1 for orgID. Never touches the active-version pointer --
// publishing a version and ACTIVATING it are deliberately separate
// operations (DP8(a): a flip is a human-ratified operation; publishing is
// not a flip). Serialized per-org via a Postgres advisory transaction lock
// (curation is expected to run at most one concurrent instance per org
// regardless -- this is belt, not the sole guard).
func (s *Store) PublishVersion(ctx context.Context, orgID string, entries []contextfabric.StructurePriorEntry, createdFromWatermark, curationRuleVersion string) (version int64, err error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return 0, errors.New("pgstructurepriors: organization is required")
	}
	if strings.TrimSpace(curationRuleVersion) == "" {
		return 0, errors.New("pgstructurepriors: curation rule version is required")
	}
	payload := make([]entryJSON, 0, len(entries))
	for _, e := range entries {
		payload = append(payload, toEntryJSON(e))
	}
	entriesJSON, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("pgstructurepriors: encode entries: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("pgstructurepriors: begin publish: %w", sanitize(err))
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "structure_priors:"+orgID); err != nil {
		return 0, fmt.Errorf("pgstructurepriors: acquire publish lock: %w", sanitize(err))
	}
	row := tx.QueryRowContext(ctx, `
INSERT INTO acr.context_fabric_structure_priors (org_id, version, entries, created_from_watermark, curation_rule_version)
SELECT $1, COALESCE(MAX(version), 0) + 1, $2::jsonb, $3, $4
FROM acr.context_fabric_structure_priors WHERE org_id = $1
RETURNING version`, orgID, string(entriesJSON), createdFromWatermark, curationRuleVersion)
	if err := row.Scan(&version); err != nil {
		return 0, fmt.Errorf("pgstructurepriors: publish version: %w", sanitize(err))
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("pgstructurepriors: commit publish: %w", sanitize(err))
	}
	return version, nil
}

// versionExists checks acr.context_fabric_structure_priors for (orgID,
// version) -- FlipActiveVersion's own pre-CAS guard, so this store can
// never itself create a PriorDegradationPointerDangling condition.
func (s *Store) versionExists(ctx context.Context, orgID string, version int64) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM acr.context_fabric_structure_priors WHERE org_id = $1 AND version = $2)`, orgID, version).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("pgstructurepriors: check version existence: %w", sanitize(err))
	}
	return exists, nil
}

// FlipActiveVersion is DP8(a)'s own operation: a HUMAN-RATIFIED pointer
// move, never called from any automatic path in this repository --
// cmd/acr-projector's "priors flip" subcommand is the sole caller.
// expectedCurrent is the caller's own belief of the CURRENT active_version
// (nil means "I believe there is none yet"); the CAS refuses
// (ErrPriorPointerConflict) if that belief is stale.
func (s *Store) FlipActiveVersion(ctx context.Context, orgID string, expectedCurrent, newVersion *int64, ratifiedBy string) error {
	orgID = strings.TrimSpace(orgID)
	ratifiedBy = strings.TrimSpace(ratifiedBy)
	if orgID == "" || ratifiedBy == "" || newVersion == nil {
		return errors.New("pgstructurepriors: organization, ratified-by, and a target version are required")
	}
	exists, err := s.versionExists(ctx, orgID, *newVersion)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: org %s has no version %d", contextfabric.ErrPriorVersionNotFound, orgID, *newVersion)
	}
	now := time.Now().UTC()
	row := s.db.QueryRowContext(ctx, `
INSERT INTO acr.context_fabric_structure_prior_pointer (org_id, active_version, previous_version, ratified_by, updated_at)
VALUES ($1, $3, NULL, $4, $5)
ON CONFLICT (org_id) DO UPDATE SET
    previous_version = acr.context_fabric_structure_prior_pointer.active_version,
    active_version = $3, ratified_by = $4, updated_at = $5
WHERE acr.context_fabric_structure_prior_pointer.active_version IS NOT DISTINCT FROM $2
RETURNING active_version`,
		orgID, expectedCurrent, *newVersion, ratifiedBy, now)
	var got int64
	if err := row.Scan(&got); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.telemetry().RecordFlipCASConflict(ctx, orgID)
			return fmt.Errorf("%w: org %s active version changed since read", contextfabric.ErrPriorPointerConflict, orgID)
		}
		return fmt.Errorf("pgstructurepriors: flip active version: %w", sanitize(err))
	}
	return s.recordPointerHistory(ctx, orgID, expectedCurrent, newVersion, ratifiedBy, now)
}

// RollbackActiveVersion is DP8(a)'s own rollback operation: point back at
// the previous_version the last flip recorded. Refuses (ErrPriorVersionNotFound)
// when there is nothing to roll back to.
func (s *Store) RollbackActiveVersion(ctx context.Context, orgID, ratifiedBy string) error {
	orgID = strings.TrimSpace(orgID)
	ratifiedBy = strings.TrimSpace(ratifiedBy)
	if orgID == "" || ratifiedBy == "" {
		return errors.New("pgstructurepriors: organization and ratified-by are required")
	}
	var current, previous sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
SELECT active_version, previous_version FROM acr.context_fabric_structure_prior_pointer WHERE org_id = $1`, orgID).Scan(&current, &previous)
	if errors.Is(err, sql.ErrNoRows) || !previous.Valid {
		return fmt.Errorf("%w: org %s has no previous version to roll back to", contextfabric.ErrPriorVersionNotFound, orgID)
	}
	if err != nil {
		return fmt.Errorf("pgstructurepriors: read pointer for rollback: %w", sanitize(err))
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
UPDATE acr.context_fabric_structure_prior_pointer
SET active_version = $3, previous_version = $2, ratified_by = $4, updated_at = $5
WHERE org_id = $1 AND active_version IS NOT DISTINCT FROM $2`,
		orgID, current, previous.Int64, ratifiedBy, now)
	if err != nil {
		return fmt.Errorf("pgstructurepriors: rollback: %w", sanitize(err))
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		s.telemetry().RecordFlipCASConflict(ctx, orgID)
		return fmt.Errorf("%w: org %s active version changed since read", contextfabric.ErrPriorPointerConflict, orgID)
	}
	var currentPtr *int64
	if current.Valid {
		v := current.Int64
		currentPtr = &v
	}
	prev := previous.Int64
	return s.recordPointerHistory(ctx, orgID, currentPtr, &prev, ratifiedBy, now)
}

func (s *Store) recordPointerHistory(ctx context.Context, orgID string, from, to *int64, ratifiedBy string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_structure_prior_pointer_history (org_id, from_version, to_version, ratified_by, ratified_at)
VALUES ($1, $2, $3, $4, $5)`, orgID, from, to, ratifiedBy, at)
	if err != nil {
		return fmt.Errorf("pgstructurepriors: record pointer history: %w", sanitize(err))
	}
	return nil
}

// RevokeEntry is DP8(a)-adjacent (a per-entry kill, not a version flip, but
// still an operator action -- ratifiedBy is required for the same
// accountability reason). Idempotent: revoking an already-revoked entry is
// a no-op, never an error.
func (s *Store) RevokeEntry(ctx context.Context, orgID, entryID, ratifiedBy string) error {
	orgID, entryID, ratifiedBy = strings.TrimSpace(orgID), strings.TrimSpace(entryID), strings.TrimSpace(ratifiedBy)
	if orgID == "" || entryID == "" || ratifiedBy == "" {
		return errors.New("pgstructurepriors: organization, entry id, and ratified-by are required")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_structure_prior_revocations (org_id, entry_id, revoked_by, revoked_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (org_id, entry_id) DO NOTHING`, orgID, entryID, ratifiedBy, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("pgstructurepriors: revoke entry: %w", sanitize(err))
	}
	return nil
}

func sanitize(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: %v", contextfabric.ErrUnavailable, err)
}
