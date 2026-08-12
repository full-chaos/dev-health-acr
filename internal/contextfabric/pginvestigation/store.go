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
type Store struct {
	db *sql.DB
}

// NewStore builds a Store around a caller-owned *sql.DB.
func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("pginvestigation: store requires a database")
	}
	return &Store{db: db}, nil
}

// Save persists an immutable InvestigationResult snapshot. It never issues
// an UPDATE: a first save for a result_id inserts the row; a replay with an
// identical payload is treated as success (idempotent retry); a replay
// under the same result_id with a DIFFERENT payload is rejected, since that
// would silently overwrite an immutable record.
func (s *Store) Save(ctx context.Context, principal storage.Principal, result contextfabric.InvestigationResult) error {
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

	insertResult, err := s.db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_investigation_results (result_id, org_id, payload, generated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (result_id) DO NOTHING`,
		resultID, orgID, payload, result.GeneratedAt)
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
