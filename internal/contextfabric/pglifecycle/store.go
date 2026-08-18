// Package pglifecycle is the production Postgres implementation of
// contextfabric.GraphLifecycleStore (CHAOS-3898 S2a, design brief v4.1
// §3.1/§3.5): the per-org graph lifecycle CAS state machine, its durable
// per-epoch retire records, and per-source build completion tracking.
//
// CAS discipline, matching pgprojection.CheckpointStore's own convention
// (see that package's CompareAndSwapProjectionCheckpoint doc comment): every
// mutating query is a single UPDATE (or, for the two-table transitions --
// Rollback and BeginRetire -- a single transaction) whose WHERE clause
// names the caller's expected pre-transition state, RETURNING the new row.
// Zero rows returned means the row no longer matched -- exactly one
// concurrent transition ever wins a race (design brief §3.5); the loser
// gets contextfabric.ErrLifecycleConflict and must re-read before retrying.
// A request that is structurally illegal regardless of race (rollback
// outside grace, a flip whose required sources have not all completed, a
// begin_retire before the grace deadline without force) gets
// contextfabric.ErrLifecycleTransitionRefused instead -- retrying the exact
// same call can never succeed.
package pglifecycle

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// Store is the production contextfabric.GraphLifecycleStore. The caller
// owns database construction; this package never parses or logs DSNs
// (repository convention, internal/storage/AGENTS.md).
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("pglifecycle: store requires a database")
	}
	return &Store{db: db}, nil
}

var _ contextfabric.GraphLifecycleStore = (*Store)(nil)

func (s *Store) Get(ctx context.Context, orgID string) (contextfabric.OrgGraphLifecycle, bool, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return contextfabric.OrgGraphLifecycle{}, false, errors.New("pglifecycle: organization is required")
	}
	row := s.db.QueryRowContext(ctx, `
SELECT active_epoch, last_allocated_epoch, status, target_epoch, grace_epoch, COALESCE(required_sources, '[]'::jsonb), grace_deadline, updated_at
FROM acr.context_fabric_graph_lifecycle
WHERE org_id = $1`, orgID)
	result, err := scanLifecycle(row, orgID)
	if errors.Is(err, sql.ErrNoRows) {
		return contextfabric.OrgGraphLifecycle{}, false, nil
	}
	if err != nil {
		return contextfabric.OrgGraphLifecycle{}, false, fmt.Errorf("pglifecycle: read lifecycle row: %w", sanitize(err))
	}
	return result, true, nil
}

// BeginBuild is the serving -> building CAS transition (design brief §3.1
// step 1-2). An absent row is treated as an implicit LifecycleStatusServing
// row at epoch 0/0 -- the INSERT branch below covers exactly that case; the
// ON CONFLICT DO UPDATE branch covers an existing row, refusing (via its
// WHERE clause) unless that row's status is 'serving'.
func (s *Store) BeginBuild(ctx context.Context, orgID string, requiredSources []string, now time.Time) (contextfabric.OrgGraphLifecycle, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return contextfabric.OrgGraphLifecycle{}, errors.New("pglifecycle: organization is required")
	}
	if len(requiredSources) == 0 {
		return contextfabric.OrgGraphLifecycle{}, errors.New("pglifecycle: at least one required source is required")
	}
	requiredJSON, err := json.Marshal(requiredSources)
	if err != nil {
		return contextfabric.OrgGraphLifecycle{}, fmt.Errorf("pglifecycle: encode required sources: %w", err)
	}
	now = now.UTC()
	row := s.db.QueryRowContext(ctx, `
INSERT INTO acr.context_fabric_graph_lifecycle
    (org_id, active_epoch, last_allocated_epoch, status, target_epoch, grace_epoch, required_sources, grace_deadline, updated_at)
VALUES ($1, 0, 1, 'building', 1, NULL, $2::jsonb, NULL, $3)
ON CONFLICT (org_id) DO UPDATE SET
    last_allocated_epoch = acr.context_fabric_graph_lifecycle.last_allocated_epoch + 1,
    status = 'building',
    target_epoch = acr.context_fabric_graph_lifecycle.last_allocated_epoch + 1,
    required_sources = $2::jsonb,
    updated_at = $3
WHERE acr.context_fabric_graph_lifecycle.status = 'serving'
RETURNING active_epoch, last_allocated_epoch, status, target_epoch, grace_epoch, COALESCE(required_sources, '[]'::jsonb), grace_deadline, updated_at`,
		orgID, string(requiredJSON), now)
	result, err := scanLifecycle(row, orgID)
	if errors.Is(err, sql.ErrNoRows) {
		return contextfabric.OrgGraphLifecycle{}, fmt.Errorf("%w: a build or grace window is already open for this organization", contextfabric.ErrLifecycleTransitionRefused)
	}
	if err != nil {
		return contextfabric.OrgGraphLifecycle{}, fmt.Errorf("pglifecycle: begin build: %w", sanitize(err))
	}
	return result, nil
}

func (s *Store) RecordSourceProgress(ctx context.Context, orgID string, epoch int64, source string, mode contextfabric.BuildCompletionMode, rowsProjected int64, now time.Time) error {
	orgID, source = strings.TrimSpace(orgID), strings.TrimSpace(source)
	if orgID == "" || source == "" || epoch < 0 {
		return errors.New("pglifecycle: organization, non-negative epoch, and source are required")
	}
	if !validCompletionMode(mode) {
		return fmt.Errorf("pglifecycle: invalid build completion mode %q", mode)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_graph_build_source_progress (org_id, epoch, source, completion_mode, rows_projected, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (org_id, epoch, source) DO UPDATE SET
    completion_mode = $4, rows_projected = $5, updated_at = $6`,
		orgID, epoch, source, string(mode), rowsProjected, now.UTC())
	if err != nil {
		return fmt.Errorf("pglifecycle: record source progress: %w", sanitize(err))
	}
	return nil
}

func (s *Store) SourceProgress(ctx context.Context, orgID string, epoch int64) ([]contextfabric.BuildSourceProgress, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" || epoch < 0 {
		return nil, errors.New("pglifecycle: organization and non-negative epoch are required")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT source, completion_mode, rows_projected, updated_at
FROM acr.context_fabric_graph_build_source_progress
WHERE org_id = $1 AND epoch = $2
ORDER BY source ASC`, orgID, epoch)
	if err != nil {
		return nil, fmt.Errorf("pglifecycle: read source progress: %w", sanitize(err))
	}
	defer rows.Close()
	var out []contextfabric.BuildSourceProgress
	for rows.Next() {
		var (
			p    contextfabric.BuildSourceProgress
			mode string
		)
		if err := rows.Scan(&p.Source, &mode, &p.RowsProjected, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("pglifecycle: scan source progress: %w", sanitize(err))
		}
		p.OrgID, p.Epoch, p.CompletionMode = orgID, epoch, contextfabric.BuildCompletionMode(mode)
		p.UpdatedAt = p.UpdatedAt.UTC()
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pglifecycle: read source progress: %w", sanitize(err))
	}
	return out, nil
}

// Flip is the building -> grace CAS transition (design brief §3.1 step 3).
// The per-source completion gate is evaluated BEFORE the CAS is attempted:
// a source named in the row's own RequiredSources (read fresh, in this same
// call) that has no BuildSourceProgress entry at all, or one still at
// contextfabric.BuildCompletionPending, refuses the flip with
// ErrLifecycleTransitionRefused -- a source that cannot report exhaustion
// therefore blocks the flip forever rather than silently passing (design
// brief §9 item 3).
func (s *Store) Flip(ctx context.Context, orgID string, expectedTargetEpoch int64, graceWindow time.Duration, now time.Time) (contextfabric.OrgGraphLifecycle, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" || expectedTargetEpoch <= 0 {
		return contextfabric.OrgGraphLifecycle{}, errors.New("pglifecycle: organization and a positive target epoch are required")
	}
	if graceWindow <= 0 {
		return contextfabric.OrgGraphLifecycle{}, errors.New("pglifecycle: a positive grace window is required")
	}
	current, found, err := s.Get(ctx, orgID)
	if err != nil {
		return contextfabric.OrgGraphLifecycle{}, err
	}
	if !found || current.Status != contextfabric.LifecycleStatusBuilding || current.TargetEpoch == nil || *current.TargetEpoch != expectedTargetEpoch {
		return contextfabric.OrgGraphLifecycle{}, fmt.Errorf("%w: no open build at epoch %d for this organization", contextfabric.ErrLifecycleConflict, expectedTargetEpoch)
	}
	progress, err := s.SourceProgress(ctx, orgID, expectedTargetEpoch)
	if err != nil {
		return contextfabric.OrgGraphLifecycle{}, err
	}
	completed := make(map[string]bool, len(progress))
	for _, p := range progress {
		if p.CompletionMode != contextfabric.BuildCompletionPending {
			completed[p.Source] = true
		}
	}
	for _, required := range current.RequiredSources {
		if !completed[required] {
			return contextfabric.OrgGraphLifecycle{}, fmt.Errorf("%w: source %q has not reported completion for epoch %d", contextfabric.ErrLifecycleTransitionRefused, required, expectedTargetEpoch)
		}
	}
	now = now.UTC()
	deadline := now.Add(graceWindow)
	row := s.db.QueryRowContext(ctx, `
UPDATE acr.context_fabric_graph_lifecycle
SET active_epoch = target_epoch,
    grace_epoch = active_epoch,
    status = 'grace',
    grace_deadline = $3,
    target_epoch = NULL,
    required_sources = NULL,
    updated_at = $4
WHERE org_id = $1 AND status = 'building' AND target_epoch = $2
RETURNING active_epoch, last_allocated_epoch, status, target_epoch, grace_epoch, COALESCE(required_sources, '[]'::jsonb), grace_deadline, updated_at`,
		orgID, expectedTargetEpoch, deadline, now)
	result, err := scanLifecycle(row, orgID)
	if errors.Is(err, sql.ErrNoRows) {
		return contextfabric.OrgGraphLifecycle{}, fmt.Errorf("%w: flip", contextfabric.ErrLifecycleConflict)
	}
	if err != nil {
		return contextfabric.OrgGraphLifecycle{}, fmt.Errorf("pglifecycle: flip: %w", sanitize(err))
	}
	return result, nil
}

// Rollback is the grace -> serving CAS transition restoring GraceEpoch
// (design brief §3.1 step 4), legal only while Status == grace. In the same
// transaction it creates a RetireReasonRollbackAbandoned EpochRetirement
// for expectedActiveEpoch (the epoch the rollback just abandoned) with
// DrainStart = now (v4.1 F3: the drain clock starts at rollback).
func (s *Store) Rollback(ctx context.Context, orgID string, expectedActiveEpoch int64, now time.Time) (contextfabric.OrgGraphLifecycle, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" || expectedActiveEpoch <= 0 {
		return contextfabric.OrgGraphLifecycle{}, errors.New("pglifecycle: organization and a positive active epoch are required")
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return contextfabric.OrgGraphLifecycle{}, fmt.Errorf("pglifecycle: begin rollback transaction: %w", sanitize(err))
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx, `
UPDATE acr.context_fabric_graph_lifecycle
SET active_epoch = grace_epoch,
    grace_epoch = NULL,
    status = 'serving',
    grace_deadline = NULL,
    updated_at = $3
WHERE org_id = $1 AND status = 'grace' AND active_epoch = $2
RETURNING active_epoch, last_allocated_epoch, status, target_epoch, grace_epoch, COALESCE(required_sources, '[]'::jsonb), grace_deadline, updated_at`,
		orgID, expectedActiveEpoch, now)
	result, err := scanLifecycle(row, orgID)
	if errors.Is(err, sql.ErrNoRows) {
		return contextfabric.OrgGraphLifecycle{}, fmt.Errorf("%w: rollback", contextfabric.ErrLifecycleConflict)
	}
	if err != nil {
		return contextfabric.OrgGraphLifecycle{}, fmt.Errorf("pglifecycle: rollback: %w", sanitize(err))
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO acr.context_fabric_graph_epoch_retirements (org_id, epoch, reason, drain_start, state, created_at, updated_at)
VALUES ($1, $2, 'rollback_abandoned', $3, 'draining', $3, $3)
ON CONFLICT (org_id, epoch) DO NOTHING`, orgID, expectedActiveEpoch, now); err != nil {
		return contextfabric.OrgGraphLifecycle{}, fmt.Errorf("pglifecycle: rollback: record abandoned epoch retirement: %w", sanitize(err))
	}
	if err := tx.Commit(); err != nil {
		return contextfabric.OrgGraphLifecycle{}, fmt.Errorf("pglifecycle: rollback: commit: %w", sanitize(err))
	}
	return result, nil
}

// BeginRetire is the grace -> serving CAS transition that forecloses
// rollback (design brief §3.1 step 5, §3.5): the point of no return. Legal
// only while Status == grace AND (now >= GraceDeadline OR force). In the
// same transaction it creates a RetireReasonGraceExpired EpochRetirement
// for GraceEpoch, DrainStart = now.
func (s *Store) BeginRetire(ctx context.Context, orgID string, expectedActiveEpoch int64, now time.Time, force bool) (contextfabric.OrgGraphLifecycle, contextfabric.EpochRetirement, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" || expectedActiveEpoch <= 0 {
		return contextfabric.OrgGraphLifecycle{}, contextfabric.EpochRetirement{}, errors.New("pglifecycle: organization and a positive active epoch are required")
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return contextfabric.OrgGraphLifecycle{}, contextfabric.EpochRetirement{}, fmt.Errorf("pglifecycle: begin retire transaction: %w", sanitize(err))
	}
	defer func() { _ = tx.Rollback() }()

	lockedRow := tx.QueryRowContext(ctx, `
SELECT active_epoch, last_allocated_epoch, status, target_epoch, grace_epoch, COALESCE(required_sources, '[]'::jsonb), grace_deadline, updated_at
FROM acr.context_fabric_graph_lifecycle
WHERE org_id = $1
FOR UPDATE`, orgID)
	current, err := scanLifecycle(lockedRow, orgID)
	if errors.Is(err, sql.ErrNoRows) {
		return contextfabric.OrgGraphLifecycle{}, contextfabric.EpochRetirement{}, fmt.Errorf("%w: begin_retire: no lifecycle row", contextfabric.ErrLifecycleConflict)
	}
	if err != nil {
		return contextfabric.OrgGraphLifecycle{}, contextfabric.EpochRetirement{}, fmt.Errorf("pglifecycle: begin retire: %w", sanitize(err))
	}
	if current.Status != contextfabric.LifecycleStatusGrace || current.ActiveEpoch != expectedActiveEpoch || current.GraceEpoch == nil {
		return contextfabric.OrgGraphLifecycle{}, contextfabric.EpochRetirement{}, fmt.Errorf("%w: begin_retire", contextfabric.ErrLifecycleConflict)
	}
	if !force && (current.GraceDeadline == nil || now.Before(*current.GraceDeadline)) {
		return contextfabric.OrgGraphLifecycle{}, contextfabric.EpochRetirement{}, fmt.Errorf("%w: grace window has not elapsed", contextfabric.ErrLifecycleTransitionRefused)
	}
	graceEpoch := *current.GraceEpoch

	updateResult, err := tx.ExecContext(ctx, `
UPDATE acr.context_fabric_graph_lifecycle
SET grace_epoch = NULL, status = 'serving', grace_deadline = NULL, updated_at = $3
WHERE org_id = $1 AND status = 'grace' AND active_epoch = $2`, orgID, expectedActiveEpoch, now)
	if err != nil {
		return contextfabric.OrgGraphLifecycle{}, contextfabric.EpochRetirement{}, fmt.Errorf("pglifecycle: begin retire: %w", sanitize(err))
	}
	if rows, err := updateResult.RowsAffected(); err != nil || rows != 1 {
		return contextfabric.OrgGraphLifecycle{}, contextfabric.EpochRetirement{}, fmt.Errorf("%w: begin_retire", contextfabric.ErrLifecycleConflict)
	}

	// ON CONFLICT is defensive only: since epoch numbers are never reused
	// (the monotonic allocator, design brief P3) and this whole sequence is
	// one transaction, a genuine conflict here should be unreachable in
	// practice -- but DO UPDATE (a true no-op self-assignment, bare column
	// name so it refers to the pre-existing row) keeps RETURNING working
	// rather than silently discarding the row via DO NOTHING.
	retireRow := tx.QueryRowContext(ctx, `
INSERT INTO acr.context_fabric_graph_epoch_retirements (org_id, epoch, reason, drain_start, state, created_at, updated_at)
VALUES ($1, $2, 'grace_expired', $3, 'draining', $3, $3)
ON CONFLICT (org_id, epoch) DO UPDATE SET drain_start = acr.context_fabric_graph_epoch_retirements.drain_start
RETURNING org_id, epoch, reason, drain_start, state, created_at, updated_at`, orgID, graceEpoch, now)
	retirement, err := scanRetirement(retireRow)
	if err != nil {
		return contextfabric.OrgGraphLifecycle{}, contextfabric.EpochRetirement{}, fmt.Errorf("pglifecycle: begin retire: record retirement: %w", sanitize(err))
	}
	if err := tx.Commit(); err != nil {
		return contextfabric.OrgGraphLifecycle{}, contextfabric.EpochRetirement{}, fmt.Errorf("pglifecycle: begin retire: commit: %w", sanitize(err))
	}
	current.Status, current.GraceEpoch, current.GraceDeadline = contextfabric.LifecycleStatusServing, nil, nil
	current.UpdatedAt = now
	return current, retirement, nil
}

func (s *Store) DrainingRetirements(ctx context.Context, cutoff time.Time) ([]contextfabric.EpochRetirement, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT org_id, epoch, reason, drain_start, state, created_at, updated_at
FROM acr.context_fabric_graph_epoch_retirements
WHERE state = 'draining' AND drain_start <= $1
ORDER BY drain_start ASC`, cutoff.UTC())
	if err != nil {
		return nil, fmt.Errorf("pglifecycle: list draining retirements: %w", sanitize(err))
	}
	defer rows.Close()
	var out []contextfabric.EpochRetirement
	for rows.Next() {
		r, err := scanRetirement(rows)
		if err != nil {
			return nil, fmt.Errorf("pglifecycle: scan draining retirement: %w", sanitize(err))
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pglifecycle: list draining retirements: %w", sanitize(err))
	}
	return out, nil
}

func (s *Store) AdvanceRetirement(ctx context.Context, orgID string, epoch int64, expected, next contextfabric.RetireRecordState, now time.Time) (contextfabric.EpochRetirement, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" || epoch < 0 {
		return contextfabric.EpochRetirement{}, errors.New("pglifecycle: organization and a non-negative epoch are required")
	}
	row := s.db.QueryRowContext(ctx, `
UPDATE acr.context_fabric_graph_epoch_retirements
SET state = $4, updated_at = $5
WHERE org_id = $1 AND epoch = $2 AND state = $3
RETURNING org_id, epoch, reason, drain_start, state, created_at, updated_at`,
		orgID, epoch, string(expected), string(next), now.UTC())
	result, err := scanRetirement(row)
	if errors.Is(err, sql.ErrNoRows) {
		return contextfabric.EpochRetirement{}, fmt.Errorf("%w: advance retirement", contextfabric.ErrLifecycleConflict)
	}
	if err != nil {
		return contextfabric.EpochRetirement{}, fmt.Errorf("pglifecycle: advance retirement: %w", sanitize(err))
	}
	return result, nil
}

// scanner is the shared subset of *sql.Row/*sql.Rows this package scans
// from -- both a single QueryRowContext result and one row of a
// QueryContext result set carry the same shape.
type scanner interface {
	Scan(dest ...interface{}) error
}

func scanLifecycle(row scanner, orgID string) (contextfabric.OrgGraphLifecycle, error) {
	var (
		result              contextfabric.OrgGraphLifecycle
		status              string
		targetEpoch         sql.NullInt64
		graceEpoch          sql.NullInt64
		requiredSourcesJSON []byte
		graceDeadline       sql.NullTime
	)
	if err := row.Scan(&result.ActiveEpoch, &result.LastAllocatedEpoch, &status, &targetEpoch, &graceEpoch, &requiredSourcesJSON, &graceDeadline, &result.UpdatedAt); err != nil {
		return contextfabric.OrgGraphLifecycle{}, err
	}
	result.OrgID = orgID
	result.Status = contextfabric.LifecycleStatus(status)
	result.UpdatedAt = result.UpdatedAt.UTC()
	// required_sources is COALESCE'd to '[]'::jsonb by every caller's SELECT/
	// RETURNING clause above, so requiredSourcesJSON is never nil here --
	// the RequiredSources field's OWN "NULL only while status != building"
	// meaning is reconstructed by leaving it nil whenever the decoded slice
	// is empty, matching the column's real NULL/populated states.
	var requiredSources []string
	if err := json.Unmarshal(requiredSourcesJSON, &requiredSources); err != nil {
		return contextfabric.OrgGraphLifecycle{}, fmt.Errorf("decode required sources: %w", err)
	}
	if len(requiredSources) > 0 {
		result.RequiredSources = requiredSources
	}
	if targetEpoch.Valid {
		v := targetEpoch.Int64
		result.TargetEpoch = &v
	}
	if graceEpoch.Valid {
		v := graceEpoch.Int64
		result.GraceEpoch = &v
	}
	if graceDeadline.Valid {
		v := graceDeadline.Time.UTC()
		result.GraceDeadline = &v
	}
	return result, nil
}

func scanRetirement(row scanner) (contextfabric.EpochRetirement, error) {
	var (
		r      contextfabric.EpochRetirement
		reason string
		state  string
	)
	if err := row.Scan(&r.OrgID, &r.Epoch, &reason, &r.DrainStart, &state, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return contextfabric.EpochRetirement{}, err
	}
	r.Reason = contextfabric.RetireReason(reason)
	r.State = contextfabric.RetireRecordState(state)
	r.DrainStart, r.CreatedAt, r.UpdatedAt = r.DrainStart.UTC(), r.CreatedAt.UTC(), r.UpdatedAt.UTC()
	return r, nil
}

func validCompletionMode(mode contextfabric.BuildCompletionMode) bool {
	switch mode {
	case contextfabric.BuildCompletionPending, contextfabric.BuildCompletionPagedFinal,
		contextfabric.BuildCompletionEmptyFirstTick, contextfabric.BuildCompletionDisabledAtFreeze,
		contextfabric.BuildCompletionCursorExhausted:
		return true
	default:
		return false
	}
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
