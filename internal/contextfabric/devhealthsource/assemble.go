package devhealthsource

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// sourcePlan is the batch-assembly engine every ClickHouse-backed
// ProjectionSource in this package shares: full-snapshot attempt, oversized
// fallback to paging, keyset-paginated incremental catch-up, whole-row
// truncation, and deterministic batch identity.
//
// It exists (CHAOS-3802) because TeamsProjectsSource needs exactly the same
// paging behavior ClickHouseProjectionSource already had, and that behavior
// is not simple: it carries CHAOS-3753's C6 (refusing an oversized
// organization leaves it permanently stuck), K4 (an aggregate candidate
// count can exceed the contract bound even when no single table truncated),
// and K2 (a page boundary must never split one source row's candidates)
// fixes. Re-deriving those in a second source would re-introduce them wrong;
// a plan both sources instantiate cannot drift.
//
// The only per-source variation is data, not control flow: which tables to
// read, what Source/SourceVersion to stamp, an optional once-per-from-scratch
// seed (ClickHouseProjectionSource's synthesized Organization entity), and an
// optional per-batch observer (its orphaned-work-item log line).
type sourcePlan struct {
	client  contextpacket.ClickHouseQueryClient
	source  string
	version string
	tables  []entityTable
	now     func() time.Time

	// seed contributes candidates that belong to a from-scratch projection
	// as a whole rather than to any one source row, emitted exactly once on
	// the first page and never again. Optional.
	seed func(orgID string) []candidate

	// observe is handed every built batch alongside the candidates it came
	// from, before the batch is returned. Optional.
	observe func(ctx context.Context, batch contextfabric.ProjectionBatch, all []candidate)
}

func (p sourcePlan) nextBatch(ctx context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	if p.client == nil {
		return contextfabric.ProjectionBatch{}, false, fmt.Errorf("devhealthsource: source is not configured")
	}
	orgID := strings.TrimSpace(checkpoint.OrgID)
	if orgID == "" {
		return contextfabric.ProjectionBatch{}, false, fmt.Errorf("devhealthsource: organization is required")
	}
	if checkpoint.Cursor == "" {
		return p.fullSnapshot(ctx, orgID)
	}
	state, err := decodeCursor(checkpoint.Cursor)
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	return p.pagedBatch(ctx, orgID, checkpoint.Cursor, state, false)
}

// fullSnapshot attempts one complete-enumeration batch (FullSnapshot: true,
// CompleteEnumeration: true -- ContextFabricProjectionBatch.Validate()
// requires both together). When the organization is too large for that
// single bounded batch, it falls back to pagedBatch from the same zero
// cursor instead of erroring -- CHAOS-3753 codex finding C6: refusing left
// any organization above the per-table cap permanently stuck (every
// subsequent tick re-attempted the same oversized single-batch snapshot
// and failed the same way; initial projection never completed). The
// fallback produces an ordinary bounded incremental-shaped batch per tick
// until caught up, exactly like any other incremental catch-up.
//
// "Too large" is detected two ways (codex round-2 finding K4): a single
// table individually truncated at snapshotPerQueryCap, OR the aggregate
// candidate count across every table exceeding the v1 contract's own
// per-batch bounds even when no single table was truncated -- N tables
// each just under their own per-table cap can still sum past the
// contract's aggregate entity/relationship bound (e.g. seven tables at
// 149 rows apiece is 1043 entities, over the 1000 cap). Checking only the
// per-table signal let that case reach buildBatch and fail contract
// validation instead of paging -- the same "stuck forever" shape C6 fixed
// for the per-table case, just triggered by an aggregate rather than a
// single oversized table.
func (p sourcePlan) fullSnapshot(ctx context.Context, orgID string) (contextfabric.ProjectionBatch, bool, error) {
	var all []candidate
	oversized := false
	for _, table := range p.tables {
		rows, truncated, err := table.query(ctx, p.client, orgID, cursorState{}, snapshotPerQueryCap)
		if err != nil {
			return contextfabric.ProjectionBatch{}, false, &tableReadError{table: table.name, cause: err}
		}
		if truncated {
			oversized = true
		}
		all = append(all, rows...)
	}
	seeded := p.seedCandidates(orgID)
	if !oversized {
		entities, relationships, tombstones := candidateCounts(all)
		// The seed's own candidates are counted here, not after the fact:
		// every non-oversized path below appends them before calling
		// buildBatch, so this checks against exactly what buildBatch is
		// about to validate, not what is in all right now.
		seedEntities, seedRelationships, seedTombstones := candidateCounts(seeded)
		if entities+seedEntities > contractsv1.ContextFabricProjectionBatchMaxEntities ||
			relationships+seedRelationships > contractsv1.ContextFabricProjectionBatchMaxRelationships ||
			tombstones+seedTombstones > contractsv1.ContextFabricProjectionBatchMaxTombstones {
			oversized = true
		}
	}
	if oversized {
		return p.pagedBatch(ctx, orgID, "", cursorState{}, true)
	}
	all = append(all, seeded...)
	if len(all) == 0 {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	sortCandidates(all)
	batch, err := buildBatch(orgID, p.source, p.version, "", all, true, true, p.clock())
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	p.observeBatch(ctx, batch, all)
	return batch, true, nil
}

// pagedBatch is the shared bounded-per-tick paging path for both ordinary
// incremental catch-up and the fullSnapshot oversized-organization
// fallback (C6). includeSeed is true only for the very first page of a
// from-scratch catch-up (cursor == ""), so a seeded entity is projected
// exactly once, not on every page.
func (p sourcePlan) pagedBatch(ctx context.Context, orgID, cursor string, state cursorState, includeSeed bool) (contextfabric.ProjectionBatch, bool, error) {
	var all []candidate
	for _, table := range p.tables {
		rows, _, err := table.query(ctx, p.client, orgID, state, incrementalBatchCap)
		if err != nil {
			return contextfabric.ProjectionBatch{}, false, &tableReadError{table: table.name, cause: err}
		}
		all = append(all, rows...)
	}
	if includeSeed {
		all = append(all, p.seedCandidates(orgID)...)
	}
	if len(all) == 0 {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	sortCandidates(all)
	all = truncateToCompleteRows(all, incrementalBatchCap)
	if len(all) == 0 {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	batch, err := buildBatch(orgID, p.source, p.version, cursor, all, false, false, p.clock())
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	p.observeBatch(ctx, batch, all)
	return batch, true, nil
}

// ProducerRejection is a producer's own bounded refusal of a source row -- a
// data condition this package detected and named, as opposed to a failure
// arriving from the driver. Its Reason is always a fixed string authored
// here, which is what makes it safe for tableReadError to include in an
// operator-visible message while still discarding every driver cause.
type ProducerRejection struct{ Reason string }

func (e *ProducerRejection) Error() string { return e.Reason }

// tableReadError classifies a per-table read failure for the projection
// coordinator, which logs this error verbatim.
//
// The previous form was fmt.Errorf("%w: read %s: %v", ErrUnavailable, table,
// err), and %v flattened the cause into the message. internal/runtime/
// clickhouse's operationError is already bounded ("ClickHouse query failed"),
// but it is not the only error reaching this path: rows.Scan and rows.Err
// surface driver text verbatim, so a failing statement's full SELECT list,
// column types and bound literals landed in coordinator logs -- against the
// rule that error strings and logs carry bounded classifications only.
// Flattening also DISCARDED the cause for programmatic use: errors.Is against
// the underlying error returned false, because %v copies text rather than
// preserving the chain.
//
// This mirrors operationError's shape (bounded Error(), real Unwrap()) rather
// than inventing a second convention: the message names only the
// classification and which table failed, while Unwrap keeps both
// ErrUnavailable -- the coordinator's retry signal -- and the original cause
// inspectable.
type tableReadError struct {
	table string
	cause error
}

func (e *tableReadError) Error() string {
	message := contextfabric.ErrUnavailable.Error() + ": read " + e.table
	// A producer-authored refusal is safe to surface and is the one thing an
	// operator can actually act on -- ProducerRejection's text is a fixed
	// string this package wrote, never driver output, query text, or row
	// data. Bounding the message must not mean making a data-quality
	// condition indistinguishable from a connection failure.
	var rejection *ProducerRejection
	if errors.As(e.cause, &rejection) {
		return message + ": " + rejection.Reason
	}
	return message
}

// Unwrap returns both so errors.Is answers for the classification AND the
// cause; a single-error Unwrap could only preserve one of them.
func (e *tableReadError) Unwrap() []error { return []error{contextfabric.ErrUnavailable, e.cause} }

func (p sourcePlan) seedCandidates(orgID string) []candidate {
	if p.seed == nil {
		return nil
	}
	return p.seed(orgID)
}

func (p sourcePlan) observeBatch(ctx context.Context, batch contextfabric.ProjectionBatch, all []candidate) {
	if p.observe != nil {
		p.observe(ctx, batch, all)
	}
}

func (p sourcePlan) clock() time.Time {
	if p.now == nil {
		return time.Now().UTC()
	}
	return p.now().UTC()
}
