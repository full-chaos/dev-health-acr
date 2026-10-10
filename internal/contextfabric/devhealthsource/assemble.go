package devhealthsource

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
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

	// recordConsumed is called with a cursor covering rows proven to hold
	// nothing publishable, so the owning source can offer it to the worker as
	// durable progress (contextfabric.ProjectionProgress). Optional.
	recordConsumed func(orgID, cursor string)

	// dropConsumed invalidates any recorded progress for an organization,
	// called whenever a call publishes a batch. Optional.
	dropConsumed func(orgID string)

	// observePage is handed every page the cursor moves past: the merged,
	// sorted candidates of all tables after the page cut, whether the page is
	// published or skipped. Optional.
	observePage func(all []candidate)

	// logger receives the sanitized cause of a table read failure. Optional.
	logger *slog.Logger

	// observeQuarantine is called once per item dropped by per-item
	// quarantine (item_quarantine.go), with a closed reason token. Optional.
	observeQuarantine func(quarantineObservation)

	// ignored accumulates rows skipped by a documented ignore (see
	// ignoredDependencyTypes) across the pages of one pass; nextBatch flushes
	// it as one line per type when the source is caught up. Optional.
	ignored *ignoredLedger

	// yielded, when set by nextBatch, is raised by a call that ends without
	// publishing because it hit its per-tick bound (the skip-page limit)
	// rather than because the source is caught up. The pass is not over
	// then, so the ignored line is not flushed (CHAOS-8290).
	yielded *bool

	// overlap and window (CHAOS-7263) bound the caught-up trailing re-read
	// (overlap.go). overlap <= 0 or a nil window disables it. windowScope is
	// the memo key nextBatch derives from the checkpoint: organization AND
	// epoch, because a build-aside rebuild drains the same organization into
	// a second graph through this same source, and a row emitted into one
	// epoch's graph has not been emitted into the other's.
	overlap     time.Duration
	window      *windowMemo
	windowScope string
	// windowPagesPerCall overrides overlapWindowPagesPerCall when > 0.
	windowPagesPerCall int
	// windowTables, when set, is the table set the overlap walk reads instead
	// of tables: the same producers bound to throwaway run telemetry. The
	// walk re-reads rows the paged path already read and counted; counting
	// them again on every caught-up tick would grow cumulative counters
	// (rows read, edges asserted) while nothing happened.
	windowTables []entityTable

	// space is the cursor position space this plan encodes and accepts; empty
	// means cursorSpaceIngest.
	space string

	// complete and truncatedTables are set by fullSnapshot for the paged walk
	// it falls back to: the tables the from-zero read returned whole, which go
	// with the first batch (catch_up.go), and the names of the others.
	complete        []completeTable
	truncatedTables []string

	// readByteLimit is the client's max_bytes_to_read; every read of a pass
	// (paged, snapshot, overlap window, and a peek) carries it in its context,
	// so key-named reads size their statements from it. Zero: the client
	// default.
	readByteLimit uint64

	// observeNormalization is called once per token per item repaired by
	// producer-side normalization (item_normalization.go), with a closed
	// reason token from a vocabulary DISJOINT from observeQuarantine's.
	// Optional.
	observeNormalization func(normalizationObservation)
}

// logTableReadFailure names WHY a dependency read failed, in a closed form.
//
// tableReadError classifies correctly (a closed "dependency unavailable")
// and preserves the cause through Unwrap rather than flattening driver text
// into a message. Both are right. Together they left a gap: nothing ever
// LOOKED at the preserved cause, so every ClickHouse failure -- a memory
// limit, a timeout, a syntax error -- reached an operator as the same
// sentence. A wrapper that classifies correctly and reports nothing
// actionable is not safe, it is silent.
//
// The budget is deliberately tiny: ClickHouse's numeric code and its
// exception CLASS name, plus the table label this package authored. Never
// the driver's Message -- that is where ClickHouse puts query text, bound
// literals and row values, and bounding it is why the wrapper exists. A
// non-ClickHouse cause is reported by Go TYPE only, which is a compile-time
// identifier rather than data.
func logTableReadFailure(ctx context.Context, logger *slog.Logger, source, orgID, table string, cause error) {
	if logger == nil || cause == nil {
		return
	}
	attrs := []any{"source", contextfabric.SanitizeLogAttr(source), "org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)), "table", contextfabric.SanitizeLogAttr(table)}
	var exception *clickhousedriver.Exception
	if errors.As(cause, &exception) {
		attrs = append(attrs, "clickhouse_exception_code", exception.Code, "clickhouse_exception_name", contextfabric.SanitizeLogAttr(exception.Name))
	} else {
		attrs = append(attrs, "cause_type", contextfabric.SanitizeLogAttr(fmt.Sprintf("%T", cause)))
	}
	logger.ErrorContext(ctx, "devhealthsource table read failed", attrs...)
}

func (p sourcePlan) nextBatch(ctx context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	ctx = withReadByteLimit(ctx, p.readByteLimit)
	yielded := false
	p.yielded = &yielded
	batch, available, err := p.nextBatchPage(ctx, checkpoint)
	// One line per type per pass: flushed when the pass ends (caught up) or
	// fails, never at a mid-pass yield or a published batch.
	if err != nil {
		p.ignored.flush(ctx, p.logger, p.source, strings.TrimSpace(checkpoint.OrgID), "error")
	} else if !available && !yielded {
		p.ignored.flush(ctx, p.logger, p.source, strings.TrimSpace(checkpoint.OrgID), "caught_up")
	}
	return batch, available, err
}

// quarantineObserver is the per-item observer for one partition call. Ignore
// observations go to the run ledger instead of the log. The overlap re-read
// counts them too (CHAOS-8287): it only judges rows the window memo has not
// seen, so a row that landed behind the frontier is counted exactly when it is
// first judged. After a restart the memo is empty and the window re-judges its
// rows once, so ignored_count is rows read, not unique rows.
func (p sourcePlan) quarantineObserver(orgID string) func(quarantineObservation) {
	if p.ignored == nil {
		return p.observeQuarantine
	}
	return func(o quarantineObservation) {
		if o.IgnoredCount > 0 {
			p.ignored.add(orgID, o.Detail, o.IgnoredCount)
			return
		}
		if p.observeQuarantine != nil {
			p.observeQuarantine(o)
		}
	}
}

func (p sourcePlan) nextBatchPage(ctx context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	if p.client == nil {
		return contextfabric.ProjectionBatch{}, false, fmt.Errorf("devhealthsource: source is not configured")
	}
	orgID := strings.TrimSpace(checkpoint.OrgID)
	if orgID == "" {
		return contextfabric.ProjectionBatch{}, false, fmt.Errorf("devhealthsource: organization is required")
	}
	p.windowScope = windowScopeFor(orgID, checkpoint.Epoch)
	if checkpoint.Cursor == "" {
		// From scratch (first run, or a rebuild's reset checkpoint): what
		// this process emitted before describes a graph that is gone.
		p.window.reset(p.windowScope)
		p.ignored.flush(ctx, p.logger, p.source, orgID, "abandoned")
		return p.fullSnapshot(ctx, orgID)
	}
	state, err := decodeCursor(checkpoint.Cursor)
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	if state.Space != p.cursorSpace() {
		// A cursor saved before the ingest-time position space (CHAOS-7263):
		// its Since is provider/updated_at time and means nothing here. Re-read
		// from the start under the ORIGINAL cursor string (the worker requires
		// batch.Cursor == checkpoint.Cursor); the batch's NextCursor is in the
		// new space. Idempotent, and it recovers rows the old space skipped.
		state = cursorState{}
		p.window.reset(p.windowScope)
		p.ignored.flush(ctx, p.logger, p.source, orgID, "abandoned")
	}
	p.window.settle(p.windowScope, state.Ack)
	state.Ack = ""
	if state.Dim != nil {
		return p.dimensionBatch(ctx, orgID, checkpoint.Cursor, dimensionPositionOf(state))
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
	all, complete, truncatedTables, err := p.snapshotRead(ctx, orgID)
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	oversized := len(truncatedTables) > 0
	if dimensions := p.dimensionTablesIn(truncatedTables); len(dimensions) > 0 {
		// The organization holds more rows of a dimension table than the
		// read asked for: those tables are read whole before the fact walk.
		return p.dimensionBatch(ctx, orgID, "", dimensionPosition{Tables: dimensions})
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
		p.complete, p.truncatedTables = complete, truncatedTables
		return p.pagedBatch(ctx, orgID, "", cursorState{}, true)
	}
	all = append(all, seeded...)
	// An organization whose only rows are omitted has nothing to enumerate;
	// an empty batch is not a valid full snapshot (Validate rejects it).
	if len(all) == 0 {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	sortCandidates(all)
	p.notePage(all)
	// Normalize BEFORE quarantine: an item repaired to a contract bound is
	// never offered to quarantine at all, which is what makes the quarantine
	// counters for these bounds read zero instead of merely smaller.
	normalizeCandidates(all, p.observeNormalization)
	items := partitionProjectableCandidates(all, p.quarantineObserver(orgID))
	if !carriesPayload(items) {
		// Everything this snapshot read was quarantined, so there is nothing
		// to publish -- but the rows WERE consumed, and without recording
		// that the checkpoint never moves. A from-scratch projection whose
		// first snapshot is entirely unprojectable would otherwise re-read
		// and re-drop the same rows on every tick, forever, with no batch
		// and no error: the wedge with its diagnosis removed.
		//
		// pagedBatch's skip path already does exactly this; the full-snapshot
		// path did not, which mattered the moment per-item quarantine made an
		// all-unprojectable candidate set reachable. It bites hardest on a
		// source with no seed candidate (TeamsProjectsSource), where nothing
		// guarantees at least one valid item.
		p.window.record(p.windowScope, all, p.overlap)
		noteConsumedFrom(p, orgID, all)
		return contextfabric.ProjectionBatch{}, false, nil
	}
	batch, err := buildBatchIn(p.cursorSpace(), orgID, p.source, p.version, "", all, items, true, true, p.clock())
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	p.forgetConsumed(orgID)
	p.window.record(p.windowScope, all, p.overlap)
	p.observeBatch(ctx, batch, all)
	return batch, true, nil
}

// snapshotRead asks every table for its first rows at the snapshot cap. It
// returns the rows, the tables that came back whole (with rows) and the names
// of the tables that did not.
func (p sourcePlan) snapshotRead(ctx context.Context, orgID string) (all []candidate, complete []completeTable, truncatedTables []string, err error) {
	for _, table := range p.tables {
		rows, truncated, err := readTable(ctx, table, p.client, orgID, cursorState{}, snapshotPerQueryCap)
		if err != nil {
			logTableReadFailure(ctx, p.logger, p.source, orgID, table.name, err)
			return nil, nil, nil, &tableReadError{table: table.name, cause: err}
		}
		if truncated {
			truncatedTables = append(truncatedTables, table.name)
		} else if len(rows) > 0 {
			complete = append(complete, completeTable{name: table.name, rows: rows})
		}
		all = append(all, rows...)
	}
	return all, complete, truncatedTables, nil
}

// pagedBatch is the shared bounded-per-tick paging path for both ordinary
// incremental catch-up and the fullSnapshot oversized-organization
// fallback (C6). includeSeed is true only for the very first page of a
// from-scratch catch-up (cursor == ""), so a seeded entity is projected
// exactly once, not on every page.
func (p sourcePlan) pagedBatch(ctx context.Context, orgID, cursor string, state cursorState, includeSeed bool) (contextfabric.ProjectionBatch, bool, error) {
	// consumed records the furthest position proven to hold nothing
	// publishable, so a caller can persist it even when no batch is returned.
	// cursor stays the caller's ORIGINAL position for every batch built here.
	// Only state advances as fully-omitted pages are skipped, so the
	// coordinator moves from where it was straight to the first page with
	// real content, and deterministicBatchID stays stable for replay.
	for skips := 0; ; skips++ {
		var all []candidate
		var bound pageBound
		// ahead: rows lie beyond what this iteration hands out, because a
		// table had more than the read asked for or the merged page was cut.
		ahead := false
		for _, table := range p.tables {
			rows, truncated, err := readTable(ctx, table, p.client, orgID, state, incrementalBatchCap)
			ahead = ahead || truncated
			if err == nil {
				err = p.boundRead(ctx, orgID, table.name, state, &bound, rows, truncated)
			}
			if err != nil {
				logTableReadFailure(ctx, p.logger, p.source, orgID, table.name, err)
				return contextfabric.ProjectionBatch{}, false, &tableReadError{table: table.name, cause: err}
			}
			all = append(all, rows...)
		}
		if includeSeed {
			all = append(all, p.seedCandidates(orgID)...)
			includeSeed = false
		}
		if len(all) == 0 {
			// Caught up: the catch-up pass, if one is open, ends here; then
			// re-read the trailing overlap window for rows that landed
			// behind the frontier (CHAOS-7263).
			p.noteCaughtUp(ctx, orgID)
			return p.overlapBatch(ctx, orgID, cursor, state)
		}
		sortCandidates(all)
		read := len(all)
		all, bounded := truncateToCompleteRows(all, incrementalBatchCap, bound)
		ahead = ahead || len(all) < read
		p.window.setAhead(p.windowScope, ahead)
		if len(all) == 0 {
			p.noteYield()
			return contextfabric.ProjectionBatch{}, false, nil
		}
		if bounded {
			p.logBoundedPage(ctx, orgID, bound)
		}
		p.notePage(all)
		// Per-item quarantine BEFORE the payload check: an item the
		// contract validator rejects must not reach buildBatch, and a page
		// whose every item is quarantined is indistinguishable, from here
		// on, from a page whose every row was omitted -- both take the skip
		// path below, which advances past them and keeps looking.
		normalizeCandidates(all, p.observeNormalization)
		items := partitionProjectableCandidates(all, p.quarantineObserver(orgID))
		// The first page of a from-zero walk takes the complete tables with
		// it (catch_up.go). They are judged as a page of their own; all stays
		// the page, and it alone moves the cursor.
		early := p.completeTableItems(orgID, all)
		p.complete, p.truncatedTables = nil, nil
		if carriesPayload(items) || carriesPayload(early.items) {
			batch, err := p.buildPageBatch(ctx, orgID, cursor, all, items, early)
			if err != nil {
				return contextfabric.ProjectionBatch{}, false, err
			}
			p.notePagedPage(ctx, orgID, all)
			// This call published something, so any progress memo recorded by
			// an earlier iteration no longer describes it -- see
			// forgetConsumed for the invariant.
			p.forgetConsumed(orgID)
			p.window.record(p.windowScope, all, p.overlap)
			p.observeBatch(ctx, batch, all)
			return batch, true, nil
		}
		// Every row on this page was consumed but emitted nothing --
		// omitted for an ambiguous project_key, or every item quarantined as
		// unprojectable. A batch built
		// from them would be empty, and ContextFabricProjectionBatch.Validate
		// rejects an empty batch outright -- so the page cannot be published
		// to carry its own cursor. Skip past it in-process instead and keep
		// looking for real content, bounded so one tick cannot spin.
		last := all[len(all)-1]
		state = cursorState{Since: last.position(), After: last.sortKey}
		// Consumed (and judged): the overlap re-read must not re-judge them.
		p.window.record(p.windowScope, all, p.overlap)
		noteConsumedFrom(p, orgID, all)
		p.notePagedPage(ctx, orgID, all)
		if skips >= maxOmittedPageSkips {
			p.noteYield()
			return contextfabric.ProjectionBatch{}, false, nil
		}
	}
}

// noteConsumedFrom records the furthest position a call proved holds nothing
// publishable, derived from the LAST candidate the call consumed. Shared by
// the full-snapshot and paging paths so the two cannot drift: a cursor
// recorded one way and not the other is how a stall hides.
func noteConsumedFrom(p sourcePlan, orgID string, consumed []candidate) {
	if len(consumed) == 0 {
		return
	}
	last := consumed[len(consumed)-1]
	if encoded, err := encodeCursorIn(p.cursorSpace(), cursorState{Since: last.position(), After: last.sortKey}); err == nil {
		p.noteConsumed(orgID, encoded)
	}
}

// maxOmittedPageSkips bounds how many consecutive fully-omitted pages one
// tick will skip before yielding. Without a bound, a pathological
// organization could hold a tick indefinitely; with it, the walk resumes on
// the next tick from the same place, having lost only the re-scan.
const maxOmittedPageSkips = 50

// carriesPayload reports whether any candidate would actually appear in a
// built batch. Progress markers (progressCandidate) carry none.
func carriesPayload(all []candidate) bool {
	for _, c := range all {
		if c.entity != nil || c.relationship != nil || c.episode != nil || c.tombstone != nil {
			return true
		}
	}
	return false
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
// classification and which table failed, while Unwrap keeps both the
// classification sentinel -- the coordinator's retry signal -- and the
// original cause inspectable.
//
// CHAOS-3848: a ClickHouse TOO_MANY_BYTES/TOO_MANY_ROWS exception (the query
// exceeded its own configured read budget) is classified as
// ErrQueryBudgetExceeded instead of ErrUnavailable. It is a PERMANENT
// condition for the current query/data shape, not a transient dependency
// outage -- the coordinator's identical-retry-with-backoff behavior is
// unchanged (checkpoint still held for replay), but the class it logs now
// names the real cause instead of masquerading as one it isn't.
// runtimeclickhouse.QueryBudgetExceededCode returns the driver's numeric
// exception code only, never its Message: the message is unbounded
// query/row-shaped driver text, and this error's own Error() reaches the
// coordinator's logs.
type tableReadError struct {
	table string
	cause error
}

func (e *tableReadError) Error() string {
	if code, exceeded := runtimeclickhouse.QueryBudgetExceededCode(e.cause); exceeded {
		return fmt.Sprintf("%s: read %s (clickhouse exception code %d)", contextfabric.ErrQueryBudgetExceeded.Error(), e.table, code)
	}
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
func (e *tableReadError) Unwrap() []error {
	if runtimeclickhouse.IsQueryBudgetExceeded(e.cause) {
		return []error{contextfabric.ErrQueryBudgetExceeded, e.cause}
	}
	return []error{contextfabric.ErrUnavailable, e.cause}
}

// boundRead takes one table's read into the page bound. A read the bound
// rejects holds more rows on one cursor position than one page, directly after
// the cursor the read started from. The cursor stays, so every retry reads the
// same rows and writes this line again, until the rows change.
func (p sourcePlan) boundRead(ctx context.Context, orgID, table string, from cursorState, bound *pageBound, rows []candidate, truncated bool) error {
	err := bound.note(table, rows, truncated)
	if err != nil && p.logger != nil {
		after := ""
		if !from.Since.IsZero() {
			after = from.Since.UTC().Format(time.RFC3339Nano)
		}
		p.logger.ErrorContext(ctx, "devhealthsource read stopped: more rows of one table share one cursor position than one page holds",
			"source", contextfabric.SanitizeLogAttr(p.source), "org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)), "table", contextfabric.SanitizeLogAttr(table),
			"page_rows", incrementalBatchCap, "rows_on_position_min", incrementalBatchCap+1,
			"after_stamp", contextfabric.SanitizeLogAttr(after), "after_key_digest", contextfabric.SanitizeLogAttr(keyDigest(from.After)))
	}
	return err
}

// logBoundedPage reports a page that ended at a truncated table's last row
// before it was full: rows of that table share one cursor position. The page
// lost nothing; the line names the table that holds such rows.
func (p sourcePlan) logBoundedPage(ctx context.Context, orgID string, bound pageBound) {
	if p.logger == nil {
		return
	}
	p.logger.WarnContext(ctx, "devhealthsource page ended at the last row a truncated table returned; rows of that table share one cursor position",
		"source", contextfabric.SanitizeLogAttr(p.source), "org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)), "table", contextfabric.SanitizeLogAttr(bound.table),
		"last_stamp", contextfabric.SanitizeLogAttr(bound.at.UTC().Format(time.RFC3339Nano)), "last_key_digest", contextfabric.SanitizeLogAttr(keyDigest(bound.key)))
}

func (p sourcePlan) notePage(all []candidate) {
	if p.observePage != nil {
		p.observePage(all)
	}
}

func (p sourcePlan) noteYield() {
	if p.yielded != nil {
		*p.yielded = true
	}
}

func (p sourcePlan) noteConsumed(orgID, cursor string) {
	if p.recordConsumed != nil {
		p.recordConsumed(orgID, cursor)
	}
}

func (p sourcePlan) forgetConsumed(orgID string) {
	if p.dropConsumed != nil {
		p.dropConsumed(orgID)
	}
}

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

// cursorSpace is the position space this plan's cursors live in.
func (p sourcePlan) cursorSpace() string {
	if p.space == "" {
		return cursorSpaceIngest
	}
	return p.space
}
