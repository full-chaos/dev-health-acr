// Package pginvestigation is the production Postgres implementation of
// contextfabric.InvestigationResultStore. It persists immutable
// InvestigationResult snapshots for prior-turn binding, replay, Workbench
// inspection, and future consumer projections.
package pginvestigation

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ErrNotFound identifies a Get that found no row for the requested
// (org_id, result_id). It fires identically whether the result_id is
// genuinely unknown or belongs to a different organization -- the caller
// must not be able to distinguish "wrong org" from "truly missing" (see
// internal/storage/AGENTS.md's non-enumerating-404 convention, and
// contextfabric.InvestigationResultStore's Get doc comment). This is
// deliberately not contextfabric.ErrUnavailable: a missing/foreign result
// is a 404, not a transient 503.
var ErrNotFound = errors.New("pginvestigation: investigation result not found")

// Store is the production contextfabric.InvestigationResultStore. The
// caller owns database construction; this package never parses or logs
// DSNs (repository convention, internal/storage/AGENTS.md).
//
// Store also implements contextfabric.AnswerReuseGate and
// contextfabric.ReuseInvalidator (CHAOS-3782): the reuse-key columns
// migration 0011 added live on this same table, alongside the immutable
// payload, so the write and read sides of answer reuse belong with the
// store that already owns Save/Get's first-insert-wins and org-scoping
// invariants rather than in a separate package.
type Store struct {
	db *sql.DB

	// reuseEnabled and reuseMaxAge are set together by WithAnswerReuse, or
	// both left zero. reuseEnabled==false is Store's signal that answer
	// reuse was never turned on: Save then leaves every reuse column
	// NULL, and FindReusable always reports an ordinary, safe miss
	// (ok=false, no error) rather than reusing anything -- the same
	// "optional dependency, absent means degrade, never fail" shape
	// every other Context Fabric optional dependency (ModelRuntime,
	// EngineTelemetry, ...) already uses.
	//
	// Deliberately NOT a separate ProjectionCheckpointStore dependency:
	// Save/FindReusable read acr.context_fabric_projection_checkpoints
	// directly, with plain SQL, over the SAME *sql.DB this Store already
	// owns -- both tables live in the identical Postgres schema (see
	// migrations/postgres/0006 and 0011). This also means Store discovers
	// whichever sources currently have a checkpoint row for an
	// organization at query time, rather than needing a statically
	// injected source-name list that composition would otherwise have to
	// keep in sync with acr-projector's own configured source set.
	reuseEnabled bool
	reuseMaxAge  time.Duration
}

// StoreOption configures optional Store behavior. See WithAnswerReuse.
type StoreOption func(*Store)

// WithAnswerReuse enables CHAOS-3782 answer reuse on Store: Save
// additionally snapshots, at insert time, the CURRENT backend_watermark of
// EVERY source that currently has a checkpoint row for the organization --
// not only the sources a given question happened to touch (see
// migrations/postgres/0011_context_fabric_answer_reuse.sql's header
// comment for why binding to the full configured set is the conservative,
// fail-closed reading of TRD §19.7.3 condition 3). maxAge is the
// staleness window condition 4 enforces; it must be configured
// conservatively per drift item D15 (TRD §19.2/§19.7.3, and see
// config.Config.AnswerReuseMaxAge's doc comment) -- the watermark alone
// cannot prove freshness against a backfilled or corrected source row, so
// this window is the second, independent bound.
func WithAnswerReuse(maxAge time.Duration) StoreOption {
	return func(s *Store) {
		s.reuseEnabled = true
		s.reuseMaxAge = maxAge
	}
}

// NewStore builds a Store around a caller-owned *sql.DB. Answer reuse
// (CHAOS-3782) is disabled unless WithAnswerReuse is passed.
func NewStore(db *sql.DB, opts ...StoreOption) (*Store, error) {
	if db == nil {
		return nil, errors.New("pginvestigation: store requires a database")
	}
	s := &Store{db: db}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Save persists an immutable InvestigationResult snapshot. It never issues
// an UPDATE: a first save for a result_id inserts the row; a replay with an
// identical payload is treated as success (idempotent retry); a replay
// under the same result_id with a DIFFERENT payload is rejected, since that
// would silently overwrite an immutable record.
func (s *Store) Save(ctx context.Context, principal storage.Principal, result contextfabric.InvestigationResult, reuseSnapshot contextfabric.SourceWatermarkSnapshot, reuseEpoch contextfabric.RebuildEpoch, timeAxisKey string) error {
	if s == nil || s.db == nil {
		return errors.New("pginvestigation: store is not configured")
	}
	orgID := strings.TrimSpace(principal.OrgID)
	resultID := strings.TrimSpace(result.ResultID)
	if orgID == "" || resultID == "" {
		return errors.New("pginvestigation: organization and result id are required")
	}
	// M2 (Codex adversarial review, CHAOS-3755): reject a semantically
	// invalid result before it is ever persisted -- an immutable row that
	// fails the same contract the public API enforces on every returned
	// result can never be corrected later.
	if err := result.Validate(); err != nil {
		return fmt.Errorf("pginvestigation: invalid investigation result: %w", err)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("pginvestigation: marshal investigation result: %w", err)
	}
	questionHash, contractVersion, projectionVersion, modelIdentity, sourceWatermarks, invalidationEpoch := s.reuseColumnsFor(result, reuseSnapshot, reuseEpoch)
	// CHAOS-3781: the axis key is supplied by Engine from the CLAMPED
	// EFFECTIVE request context, matching exactly what FindReusable will
	// key with. It is NOT re-derived from result.Interpretation here -- an
	// interpreter that reads a current-axis request as historical would
	// then save under a key no identical request could ever look up, and
	// that whole class of question would silently never reuse.
	//
	// Interpretation identity is not lost by this: condition 6 re-resolves
	// every subject against the candidate's own stored Interpretation
	// before serving it, so a reused answer is still proved to match the
	// question that was actually asked.
	if strings.TrimSpace(timeAxisKey) == "" {
		// Engine could not canonicalize the requested time (a malformed
		// historical context). Store the row -- it is still a real result
		// -- but under a key no lookup can ever produce, so it can never
		// be reused. TimeAxisKeyFor never returns this value.
		timeAxisKey = "unkeyed"
	}

	insertResult, err := s.db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_investigation_results
    (result_id, org_id, payload, generated_at, question_hash, contract_version, projection_version, model_identity, source_watermarks, invalidation_epoch, time_axis_key)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (result_id) DO NOTHING`,
		resultID, orgID, payload, result.GeneratedAt,
		questionHash, contractVersion, projectionVersion, modelIdentity, sourceWatermarks, invalidationEpoch, timeAxisKey)
	if err != nil {
		return fmt.Errorf("save investigation result: %w", sanitizeError(err))
	}
	rows, err := insertResult.RowsAffected()
	if err != nil {
		return fmt.Errorf("save investigation result rows affected: %w", sanitizeError(err))
	}
	if rows == 1 {
		return nil
	}

	// A row already existed for this result_id. Immutability requires that
	// this only succeeds if it is a byte-for-byte replay of the same
	// content UNDER THE SAME ORGANIZATION (e.g. a retried request); any
	// divergence in either dimension must error rather than silently keep
	// or overwrite the original row.
	//
	// M1 (Codex adversarial review, CHAOS-3755): the organization check
	// runs FIRST, independent of content equality. InvestigationResult
	// carries no organization discriminator of its own, so a
	// byte-identical replay from a DIFFERENT org would otherwise pass the
	// content-equality check below and be treated as a successful
	// idempotent replay, while the row still belongs to whichever org
	// wrote it first.
	row := s.db.QueryRowContext(ctx, `
SELECT org_id, payload FROM acr.context_fabric_investigation_results WHERE result_id = $1`, resultID)
	var existingOrgID string
	var existingPayload []byte
	if err := row.Scan(&existingOrgID, &existingPayload); err != nil {
		return fmt.Errorf("read existing investigation result: %w", sanitizeError(err))
	}
	// P2 (Codex delta review, CHAOS-3755): the EXISTING stored row may
	// have reached storage some other way (see the matching comment in
	// Get) and could carry an explicit null where the schema only ever
	// allows an omitted or real-array value. Reject that before trusting
	// it as a valid idempotent-replay target.
	if err := rejectExplicitNullDegradedReasons(existingPayload); err != nil {
		return fmt.Errorf("pginvestigation: stored investigation result %q is invalid: %w", resultID, err)
	}
	if existingOrgID != orgID {
		return fmt.Errorf("pginvestigation: investigation result %q already exists under a different organization", resultID)
	}
	same, err := equivalentPayloads(existingPayload, payload)
	if err != nil {
		return fmt.Errorf("compare existing investigation result: %w", err)
	}
	if !same {
		return fmt.Errorf("pginvestigation: investigation result %q already exists with different content", resultID)
	}
	return nil
}

// Get returns the InvestigationResult for resultID, scoped to
// principal.OrgID. This organization predicate is the non-negotiable part
// of the query: result_id is already a primary key, but Get must never
// return a row belonging to a different organization (see
// contextfabric.InvestigationResultStore's doc comment).
func (s *Store) Get(ctx context.Context, principal storage.Principal, resultID string) (contextfabric.InvestigationResult, error) {
	if s == nil || s.db == nil {
		return contextfabric.InvestigationResult{}, errors.New("pginvestigation: store is not configured")
	}
	orgID := strings.TrimSpace(principal.OrgID)
	resultID = strings.TrimSpace(resultID)
	if orgID == "" || resultID == "" {
		return contextfabric.InvestigationResult{}, ErrNotFound
	}

	row := s.db.QueryRowContext(ctx, `
SELECT payload FROM acr.context_fabric_investigation_results WHERE result_id = $1 AND org_id = $2`, resultID, orgID)
	var payload []byte
	switch err := row.Scan(&payload); {
	case errors.Is(err, sql.ErrNoRows):
		return contextfabric.InvestigationResult{}, ErrNotFound
	case err != nil:
		return contextfabric.InvestigationResult{}, fmt.Errorf("get investigation result: %w", sanitizeError(err))
	}

	// P2 (Codex delta review, CHAOS-3755): an explicit `"degraded_reasons":
	// null` collapses to the identical Go nil slice an OMITTED field would
	// decode to, so Validate()'s relaxed nil-check (correct for the
	// omitted case) cannot tell them apart after decoding into the
	// struct. The JSON Schema only allows degraded_reasons to be an array
	// WHEN PRESENT -- never null -- so this must be caught on the raw
	// bytes, before or independent of the struct decode.
	if err := rejectExplicitNullDegradedReasons(payload); err != nil {
		return contextfabric.InvestigationResult{}, fmt.Errorf("pginvestigation: stored investigation result is invalid: %w", err)
	}
	var result contextfabric.InvestigationResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return contextfabric.InvestigationResult{}, fmt.Errorf("pginvestigation: decode investigation result: %w", err)
	}
	// M2 (Codex adversarial review, CHAOS-3755): validate on read too, not
	// just on write. Save already rejects an invalid result before it is
	// stored, but Get defends independently against any row that reached
	// storage some other way (e.g. written directly, or by a future/older
	// binary with different validation, or a row an operator hand-edited)
	// -- a caller must never receive a result this package cannot vouch
	// for.
	if err := result.Validate(); err != nil {
		return contextfabric.InvestigationResult{}, fmt.Errorf("pginvestigation: stored investigation result is invalid: %w", err)
	}
	return result, nil
}

// reuseColumnsFor computes the CHAOS-3782 reuse-key column values Save
// should write alongside result's immutable payload, or four zero
// sql.NullString values, a nil watermark blob, and an invalid
// sql.NullInt64 when answer reuse is not enabled on this Store
// (s.reuseEnabled == false) -- Save's INSERT then writes SQL NULL into
// every reuse column, exactly as if this were the pre-CHAOS-3782 schema.
//
// Codex round-1 F1 (and team-lead's veto of threading this through
// context.Context): reuseSnapshot is Save's own explicit parameter,
// captured by Engine itself, before the graph read that produced result
// -- never queried live here. Querying "current" watermarks at Save time
// (now, after synthesis has finished) would describe data possibly
// fresher than what actually went into this result, letting a later
// identical question reuse a stale answer under a watermark that only
// looks unchanged. reuseSnapshot == nil (reuse disabled for this Engine,
// or the Engine-side snapshot read failed) means this row simply never
// participates in reuse -- exactly like Save's other own-query failures,
// this fails OPEN on the write (the investigation result itself is never
// lost) and CLOSED on reuse participation (nothing here falls back to a
// live query, which would silently reopen the same race).
//
// reuseEpoch (Codex round-2 finding #7) is the same explicit-parameter,
// same-moment-as-the-graph-read discipline applied to a SECOND piece of
// snapshot-time state -- see RebuildEpoch's doc comment for the race it
// closes that reuseSnapshot/created_at alone could not. It is checked and
// nulled out independently of reuseSnapshot: a row can have watermarks
// but no epoch (or vice versa) if one of the two Engine-side reads failed
// while the other succeeded, and FindReusable's invalidation_epoch IS NOT
// NULL guard is what turns a nil reuseEpoch into "never reusable" here.
func (s *Store) reuseColumnsFor(result contextfabric.InvestigationResult, reuseSnapshot contextfabric.SourceWatermarkSnapshot, reuseEpoch contextfabric.RebuildEpoch) (questionHash, contractVersion, projectionVersion, modelIdentity sql.NullString, sourceWatermarks []byte, invalidationEpoch sql.NullInt64) {
	if !s.reuseEnabled || reuseSnapshot == nil {
		return sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullString{}, nil, sql.NullInt64{}
	}
	// Codex round-2 finding #4: a punctuation-only (or otherwise
	// entirely-stripped) question canonicalizes to the empty string, so
	// every such question would share ONE hash. Never let this row
	// become a reuse candidate at all -- see tryReuse's matching guard on
	// the lookup side.
	if contextfabric.CanonicalizeQuestion(result.Question) == "" {
		return sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullString{}, nil, sql.NullInt64{}
	}
	encoded, err := json.Marshal(reuseSnapshot)
	if err != nil {
		return sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullString{}, nil, sql.NullInt64{}
	}
	// Codex round-2 finding #7: reuseEpoch nil means the epoch snapshot
	// was never captured (reuse-epoch snapshotting not wired, or the read
	// itself failed) -- FindReusable's own invalidation_epoch IS NOT NULL
	// guard is what actually keeps such a row out of reuse; leaving it
	// NULL here, independent of whatever reuseSnapshot/watermarks
	// resolved to, is what makes that guard meaningful.
	if reuseEpoch != nil {
		invalidationEpoch = sql.NullInt64{Int64: *reuseEpoch, Valid: true}
	}
	questionHash = sql.NullString{String: contextfabric.QuestionHash(result.Question), Valid: true}
	contractVersion = sql.NullString{String: result.Versions.ContractVersion, Valid: true}
	projectionVersion = sql.NullString{String: result.Versions.ProjectionVersion, Valid: true}
	// Codex round-3 finding 4: ModelIdentity is optional at the contract
	// layer (validate_context_fabric_result.go accepts "" -- a
	// legacy-shaped, contract-valid result), but migration 0011's CHECK
	// on the model_identity column rejects an empty string outright (only
	// NULL or 1-513 chars is allowed). Writing '' as Valid:true here would
	// fail Save with a CHECK violation instead of persisting the row as
	// an ordinary, never-reusable one -- so an empty identity maps to SQL
	// NULL, the same "this row never becomes reusable" sentinel already
	// used above for a missing snapshot/epoch/punctuation-only question.
	if result.Versions.ModelIdentity != "" {
		modelIdentity = sql.NullString{String: result.Versions.ModelIdentity, Valid: true}
	}
	return questionHash, contractVersion, projectionVersion, modelIdentity, encoded, invalidationEpoch
}

// SnapshotSourceWatermarks implements contextfabric.SourceWatermarkSnapshotter.
// Engine calls this directly (not through Save) immediately before
// reading the graph for a fresh investigation -- see that interface's doc
// comment and reuseColumnsFor's F1 note for why the timing matters.
func (s *Store) SnapshotSourceWatermarks(ctx context.Context, orgID string) (contextfabric.SourceWatermarkSnapshot, error) {
	snapshot, err := s.currentSourceWatermarks(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return contextfabric.SourceWatermarkSnapshot(snapshot), nil
}

// SnapshotRebuildEpoch implements contextfabric.RebuildEpochSnapshotter
// (Codex round-2 finding #7). Engine calls this directly (not through
// Save), at the same point it calls SnapshotSourceWatermarks, immediately
// before reading the graph for a fresh investigation -- see RebuildEpoch's
// doc comment for why the timing matters. An organization with no row in
// acr.context_fabric_reuse_invalidations (never invalidated) reads as
// epoch 0, the same baseline InvalidateOrganizationReuse's first-ever
// UPSERT for an organization bumps FROM.
func (s *Store) SnapshotRebuildEpoch(ctx context.Context, orgID string) (int64, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return 0, errors.New("pginvestigation: organization is required")
	}
	var epoch int64
	err := s.db.QueryRowContext(ctx, `
SELECT epoch FROM acr.context_fabric_reuse_invalidations WHERE org_id = $1`, orgID).Scan(&epoch)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("read rebuild epoch: %w", sanitizeError(err))
	}
	return epoch, nil
}

// currentSourceWatermarks reads the CURRENT backend_watermark of every
// source that has a checkpoint row for orgID, keyed by source name. Reads
// straight from acr.context_fabric_projection_checkpoints over the same
// *sql.DB this Store already owns -- see the Store.reuseEnabled field
// comment for why this is deliberately not routed through
// contextfabric.ProjectionCheckpointStore. Used both by
// SnapshotSourceWatermarks (Engine's pre-graph-read call) and by
// watermarksStillMatch (FindReusable's condition-3 check, which
// legitimately wants the watermark AT LOOKUP TIME, not any earlier
// snapshot).
func (s *Store) currentSourceWatermarks(ctx context.Context, orgID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT source, backend_watermark FROM acr.context_fabric_projection_checkpoints WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, fmt.Errorf("query projection checkpoints: %w", sanitizeError(err))
	}
	defer rows.Close()
	snapshot := make(map[string]string)
	for rows.Next() {
		var source, watermark string
		if err := rows.Scan(&source, &watermark); err != nil {
			return nil, fmt.Errorf("scan projection checkpoint: %w", err)
		}
		snapshot[source] = watermark
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projection checkpoints: %w", err)
	}
	return snapshot, nil
}

// FindReusable implements contextfabric.AnswerReuseGate. It proves TRD
// §19.7.3 conditions 1, 2, 5, and 7 with the SQL predicate below (an exact
// match on org/question-hash/contract/projection, and -- CHAOS-3786 --
// model-identity CHAIN MEMBERSHIP via `model_identity = ANY($5)` rather
// than equality, since key.ModelIdentities is the org's current effective
// chain (primary + fallback) and a stored row's single model_identity
// need only be ONE element of it; see ReuseKey.ModelIdentities' doc
// comment for why. Results are ordered to prefer the most recently
// generated candidate. Condition 4 is proved by the same query's
// staleness-window and rebuild-invalidation predicates, and condition 3
// (per-source watermark equality) afterward in Go via
// watermarksStillMatch. It never checks condition 6 (current
// authorization) -- see the interface doc comment on why that stays
// Engine's job.
//
// Only the single most recent matching row is ever considered. That is
// sufficient, not merely convenient: condition 4's staleness window only
// gets harder to satisfy for an older row, and condition 3 checks CURRENT
// watermarks independent of which candidate row is being asked about --
// so if the newest matching row fails, no older one can pass either.
func (s *Store) FindReusable(ctx context.Context, principal storage.Principal, key contextfabric.ReuseKey) (contextfabric.InvestigationResult, bool, error) {
	if s == nil || s.db == nil {
		return contextfabric.InvestigationResult{}, false, errors.New("pginvestigation: store is not configured")
	}
	if !s.reuseEnabled {
		// Answer reuse was never enabled on this Store (WithAnswerReuse not
		// passed to NewStore). An ordinary, safe miss -- not an error.
		return contextfabric.InvestigationResult{}, false, nil
	}
	orgID := strings.TrimSpace(principal.OrgID)
	questionHash := strings.TrimSpace(key.QuestionHash)
	// CHAOS-3781: an empty TimeAxisKey means the caller could not
	// canonicalize the requested time (a malformed historical context).
	// Treat it exactly like an empty question hash or an empty identity
	// chain -- an ordinary miss, never a lookup that ignores the axis.
	if orgID == "" || questionHash == "" || len(key.ModelIdentities) == 0 || strings.TrimSpace(key.TimeAxisKey) == "" {
		return contextfabric.InvestigationResult{}, false, nil
	}

	// Codex round-1 F6: the staleness window uses created_at (DB
	// clock_timestamp(), migration 0009) against DB now() -- NEVER
	// generated_at, which is app-clock metadata a caller's own process
	// supplies and this store must not trust as an authority for a
	// security/correctness-bearing comparison. generated_at stays purely
	// a display field on the returned result (AC-3782-2's "generation
	// time of the reused result"); it plays no role in deciding reuse
	// eligibility here.
	//
	// F2: source_watermarks IS NOT NULL excludes any row where the
	// Engine-side watermark snapshot was never captured (reuse disabled
	// at save time, or the snapshot read failed) -- such a row must never
	// be treated as reusable no matter what watermarksStillMatch would
	// otherwise conclude from a NULL/empty value.
	//
	// Codex round-2 finding #7: the rebuild-invalidation check is
	// invalidation_epoch = <the organization's CURRENT epoch>, not a
	// created_at-vs-invalidated_at timestamp comparison (which this
	// replaces -- see RebuildEpoch's doc comment for the race a
	// timestamp comparison cannot close). invalidation_epoch IS NOT NULL
	// excludes any row where the Engine-side epoch snapshot was never
	// captured, exactly mirroring the source_watermarks guard just above
	// it. The COALESCE(..., 0) matches SnapshotRebuildEpoch's own
	// baseline for an organization with no invalidations row at all.
	//
	// CHAOS-3786: model_identity = ANY($5) -- $5 is the org's current
	// effective CHAIN (key.ModelIdentities), passed as a native []string
	// parameter (pgx v5 encodes it as text[]). A stored row's single
	// model_identity value matches if it is ANY element of the chain, not
	// only if it equals a single primary identity -- see
	// ReuseKey.ModelIdentities' doc comment.
	row := s.db.QueryRowContext(ctx, `
SELECT payload, source_watermarks
FROM acr.context_fabric_investigation_results
WHERE org_id = $1
  AND question_hash = $2
  AND contract_version = $3
  AND projection_version = $4
  AND model_identity = ANY($5)
  AND time_axis_key = $7
  AND source_watermarks IS NOT NULL
  AND invalidation_epoch IS NOT NULL
  AND created_at > now() - ($6 * INTERVAL '1 second')
  AND invalidation_epoch = COALESCE(
        (SELECT epoch FROM acr.context_fabric_reuse_invalidations WHERE org_id = $1),
        0)
-- Codex round-2 finding #5: created_at alone is clock_timestamp(), which
-- is NOT guaranteed unique -- two Saves landing in the same instant tie,
-- and "ORDER BY created_at DESC" alone leaves Postgres free to return
-- either one nondeterministically. result_id DESC is a stable, always-
-- unique secondary key (primary key column) purely to make candidate
-- SELECTION deterministic; it carries no freshness meaning of its own.
ORDER BY created_at DESC, result_id DESC
LIMIT 1`,
		orgID, questionHash, key.ContractVersion, key.ProjectionVersion, key.ModelIdentities, s.reuseMaxAge.Seconds(), key.TimeAxisKey)
	var payload, sourceWatermarks []byte
	switch err := row.Scan(&payload, &sourceWatermarks); {
	case errors.Is(err, sql.ErrNoRows):
		return contextfabric.InvestigationResult{}, false, nil
	case err != nil:
		return contextfabric.InvestigationResult{}, false, fmt.Errorf("find reusable investigation result: %w", sanitizeError(err))
	}

	// Condition 3. Fail closed on any error or mismatch: a candidate this
	// check cannot fully confirm fresh is never served.
	fresh, err := s.watermarksStillMatch(ctx, orgID, sourceWatermarks)
	if err != nil || !fresh {
		return contextfabric.InvestigationResult{}, false, nil
	}

	// Same defense in depth Get applies (CHAOS-3755 P2/M2 findings): never
	// trust a stored row blind, even one this package itself wrote.
	if err := rejectExplicitNullDegradedReasons(payload); err != nil {
		return contextfabric.InvestigationResult{}, false, fmt.Errorf("pginvestigation: stored investigation result is invalid: %w", err)
	}
	var result contextfabric.InvestigationResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return contextfabric.InvestigationResult{}, false, fmt.Errorf("pginvestigation: decode investigation result: %w", err)
	}
	if err := result.Validate(); err != nil {
		return contextfabric.InvestigationResult{}, false, fmt.Errorf("pginvestigation: stored investigation result is invalid: %w", err)
	}
	return result, true, nil
}

// watermarksStillMatch reports whether the CURRENT set of (source,
// backend_watermark) pairs for orgID is byte-for-byte identical to
// snapshotJSON, the set Save recorded when the candidate was generated.
// Identical means the same set of source names AND the same watermark for
// each -- a source added or removed since generation, exactly like a
// changed watermark on an existing source, counts as a mismatch, not a
// pass. TRD §19.7.3 fails closed: an ambiguous "did anything change"
// answer is treated as "yes".
func (s *Store) watermarksStillMatch(ctx context.Context, orgID string, snapshotJSON []byte) (bool, error) {
	// Codex round-1 F2: an empty/missing stored snapshot is NEVER a
	// match, regardless of what the organization's current checkpoint
	// set looks like. The FindReusable SQL already excludes
	// source_watermarks IS NULL, so a nil snapshotJSON should not reach
	// here in production, but this stays a direct, explicit guard rather
	// than relying solely on the caller's WHERE clause: without it, a
	// genuinely empty snapshot (e.g. json.Marshal(nil) -> "null") decodes
	// to a nil/empty map that VACUOUSLY equals an organization with zero
	// current checkpoints -- silently treating "we never recorded
	// anything" as "nothing changed."
	if len(snapshotJSON) == 0 || string(snapshotJSON) == "null" {
		return false, nil
	}
	var snapshot map[string]string
	if err := json.Unmarshal(snapshotJSON, &snapshot); err != nil {
		return false, fmt.Errorf("decode source watermark snapshot: %w", err)
	}
	if len(snapshot) == 0 {
		return false, nil
	}
	current, err := s.currentSourceWatermarks(ctx, orgID)
	if err != nil {
		return false, fmt.Errorf("read current source watermarks: %w", err)
	}
	if len(snapshot) != len(current) {
		return false, nil
	}
	// Codex round-1 F3: explicit key PRESENCE, not just value equality --
	// current[source] returns the empty string for a source missing from
	// current, which would incorrectly equal a stored empty-string
	// watermark (a real, valid value: migration 0006's
	// backend_watermark column defaults to '') and let a REPLACED source
	// (one name removed, a different one added, net length unchanged)
	// slip past the length check above.
	for source, watermark := range snapshot {
		currentWatermark, ok := current[source]
		if !ok || currentWatermark != watermark {
			return false, nil
		}
	}
	return true, nil
}

// InvalidateOrganizationReuse implements contextfabric.ReuseInvalidator.
// It records the invalidation as a row in the separate, mutable
// acr.context_fabric_reuse_invalidations table -- never by rewriting
// anything in the immutable investigation-results table -- and is safe to
// call whether or not answer reuse is enabled on this Store.
//
// epoch is a COUNTER of invalidation events, not a derivative of the
// invalidated_at clock (Codex round-3 finding 3): every call here
// represents a real rebuild and must bump the epoch UNCONDITIONALLY, on
// every ON CONFLICT, with no timestamp predicate gating it. An earlier
// version guarded the bump on `invalidated_at < EXCLUDED.invalidated_at`
// to stop a rare out-of-order call from moving invalidated_at backward --
// but that same guard just as easily suppressed the bump for two calls
// landing at (or the second at/before) an equal clock_timestamp(), which
// silently left a stale result reusable through a rebuild the epoch never
// recorded. invalidated_at stays informational only now (still set to
// clock_timestamp() on every call, for operator visibility); it no longer
// gates whether epoch advances. The first-ever invalidation for an
// organization sets epoch to 1, one past SnapshotRebuildEpoch's 0
// baseline for a never-invalidated organization, so a snapshot captured
// before this call and one captured after it can never compare equal.
func (s *Store) InvalidateOrganizationReuse(ctx context.Context, orgID string) error {
	if s == nil || s.db == nil {
		return errors.New("pginvestigation: store is not configured")
	}
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return errors.New("pginvestigation: organization is required")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_reuse_invalidations (org_id, invalidated_at, epoch)
VALUES ($1, clock_timestamp(), 1)
ON CONFLICT (org_id) DO UPDATE
    SET invalidated_at = EXCLUDED.invalidated_at,
        epoch = acr.context_fabric_reuse_invalidations.epoch + 1`, orgID)
	if err != nil {
		return fmt.Errorf("invalidate organization reuse: %w", sanitizeError(err))
	}
	return nil
}

// equivalentPayloads reports whether two JSONB payloads decode to the same
// InvestigationResult. It cannot compare the raw bytes directly: PostgreSQL
// JSONB storage does not preserve object key order or formatting, so a
// byte-identical Save can read back different bytes than it wrote. Both
// sides are decoded then re-encoded through the same encoding/json code
// path (which sorts map keys and formats time.Time identically) so the
// comparison is over a canonical form rather than either payload's
// as-stored bytes.
func equivalentPayloads(existing, incoming []byte) (bool, error) {
	existingCanonical, err := canonicalize(existing)
	if err != nil {
		return false, fmt.Errorf("pginvestigation: decode stored investigation result: %w", err)
	}
	incomingCanonical, err := canonicalize(incoming)
	if err != nil {
		return false, fmt.Errorf("pginvestigation: decode candidate investigation result: %w", err)
	}
	return bytes.Equal(existingCanonical, incomingCanonical), nil
}

func canonicalize(payload []byte) ([]byte, error) {
	var result contextfabric.InvestigationResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

// rejectExplicitNullDegradedReasons reports whether payload contains a
// literal `"coverage":{"degraded_reasons":null,...}` (P2, Codex delta
// review, CHAOS-3755). coverage.degraded_reasons is `omitempty` in Go and
// not in the Coverage schema's required set, so "absent" is the only
// schema-conformant way to skip it -- explicit null is not a valid array
// and violates the schema even though, once decoded into
// ContextFabricCoverage.DegradedReasons ([]string), it is indistinguishable
// from the omitted case (both become a nil slice). This check runs on the
// raw bytes specifically because that distinction stops existing the
// moment json.Unmarshal returns.
func rejectExplicitNullDegradedReasons(payload []byte) error {
	var probe struct {
		Coverage struct {
			DegradedReasons json.RawMessage `json:"degraded_reasons"`
		} `json:"coverage"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return fmt.Errorf("decode for explicit-null check: %w", err)
	}
	if bytes.Equal(bytes.TrimSpace(probe.Coverage.DegradedReasons), []byte("null")) {
		return errors.New("coverage.degraded_reasons must be omitted or an array, not explicit null")
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
