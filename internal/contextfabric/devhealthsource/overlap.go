package devhealthsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CHAOS-7263: the projection cursor keys on each row's INGEST time. Ingest
// time is assigned when a row is normalized, BEFORE the insert lands (normalize
// -> queue/batch -> ClickHouse) and writers on different hosts skew, so a row
// stamped just before the cursor can still land after the cursor passed it.
// A bounded overlap closes that: once the source is caught up it re-reads the
// trailing [frontier - overlap, frontier] window and emits the rows it has not
// already emitted (late arrivals) WITHOUT moving the cursor. Writes are
// idempotent MERGEs, so re-emitting is safe; a per-process `seen` set keeps a
// caught-up tick from re-emitting the same window forever (a restart merely
// re-emits the window once).

// defaultReprojectOverlap is the trailing window re-read once caught up.
const defaultReprojectOverlap = 15 * time.Minute

// overlapWindowMaxPages bounds how deep one caught-up tick walks the trailing
// window, in pages of incrementalBatchCap rows. Each page is an ordinary
// keyset read at the ordinary page size: one statement per table with
// LIMIT incrementalBatchCap+1. A single larger read is not an option -- the
// projector's ClickHouse client runs with max_result_rows = 1,000 and
// result_overflow_mode = throw, so any statement returning more rows FAILS
// the tick, and a failing caught-up tick fails again every tick until the
// frontier moves (a stall for a quiet organization). A late row deeper into
// the window than this bound is not found (logged, never silently); a rebuild
// recovers it.
const overlapWindowMaxPages = 5

// cursorSpaceIngest names the cursor position space: the row INGEST time.
// Cursors saved before CHAOS-7263 carry no space (provider/updated_at time) and
// decode as a reset, i.e. one full re-read (idempotent), which is also what
// recovers rows the old cursor space skipped.
const cursorSpaceIngest = "ingest.v1"

// windowMemo remembers, per scope (organization and epoch, windowScopeFor),
// the rows this process has emitted near the frontier so the overlap re-read
// only emits NEW rows.
type windowMemo struct {
	mu   sync.Mutex
	seen map[string]map[string]time.Time // scope -> row key -> position
}

// windowScopeFor keys the memo by organization AND checkpoint epoch: the
// serving graph and a build-aside graph of one organization are drained by
// the same source, and each must receive a late row on its own.
func windowScopeFor(orgID string, epoch int64) string {
	return orgID + "\x00" + strconv.FormatInt(epoch, 10)
}

func newWindowMemo() *windowMemo { return &windowMemo{seen: map[string]map[string]time.Time{}} }

func rowMemoKey(c candidate) string {
	return strconv.FormatInt(c.position().UnixNano(), 10) + "|" + c.sortKey
}

// record notes every candidate as emitted and prunes entries older than
// 2*overlap behind the newest recorded position.
func (m *windowMemo) record(scope string, all []candidate, overlap time.Duration) {
	if m == nil || len(all) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := m.seen[scope]
	if rows == nil {
		rows = map[string]time.Time{}
		m.seen[scope] = rows
	}
	newest := time.Time{}
	for _, c := range all {
		rows[rowMemoKey(c)] = c.position()
		if c.position().After(newest) {
			newest = c.position()
		}
	}
	floor := newest.Add(-2 * overlap)
	for k, at := range rows {
		if at.Before(floor) {
			delete(rows, k)
		}
	}
}

func (m *windowMemo) unseen(scope string, all []candidate) []candidate {
	if m == nil {
		return all
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := m.seen[scope]
	out := make([]candidate, 0, len(all))
	for _, c := range all {
		if _, ok := rows[rowMemoKey(c)]; !ok {
			out = append(out, c)
		}
	}
	return out
}

// snapshot returns an independent copy of the memo. A side-effect-free read
// (PeekProjectionBatch) runs the same engine against the copy: the overlap
// walk still sees what this process emitted, but whatever the peek "emits"
// and resets lands in the copy and is discarded. Recording into the shared
// memo there would mark late rows as emitted without any batch carrying
// them, and the next real tick would skip them.
func (m *windowMemo) snapshot() *windowMemo {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := newWindowMemo()
	for org, rows := range m.seen {
		copied := make(map[string]time.Time, len(rows))
		for k, at := range rows {
			copied[k] = at
		}
		out.seen[org] = copied
	}
	return out
}

func (m *windowMemo) reset(scope string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	delete(m.seen, scope)
	m.mu.Unlock()
}

// overlapBatch is pagedBatch's caught-up tail: nothing lies beyond the frontier,
// so walk the trailing window [frontier - overlap, frontier] page by page and
// emit the first page's worth of rows not yet emitted. Pages whose rows were
// all emitted already are stepped over in-process (the same keyset step
// pagedBatch uses for an omitted page), bounded by overlapWindowMaxPages.
func (p sourcePlan) overlapBatch(ctx context.Context, orgID, cursor string, state cursorState, advanced bool) (contextfabric.ProjectionBatch, bool, error) {
	if p.overlap <= 0 || p.window == nil || state.Since.IsZero() {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	walk := cursorState{Since: state.Since.Add(-p.overlap)}
	var all []candidate
	for page := 0; ; page++ {
		if page == overlapWindowMaxPages {
			if p.logger != nil {
				p.logger.WarnContext(ctx, "devhealthsource overlap window walk stopped at its depth bound; a late row deeper into the window is not re-read this tick",
					"source", contextfabric.SanitizeLogAttr(p.source), "org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)),
					"max_pages", overlapWindowMaxPages, "page_rows", incrementalBatchCap)
			}
			return contextfabric.ProjectionBatch{}, false, nil
		}
		var pageRows []candidate
		tables := p.tables
		if p.windowTables != nil {
			tables = p.windowTables
		}
		for _, table := range tables {
			rows, _, err := table.query(ctx, p.client, orgID, walk, incrementalBatchCap)
			if err != nil {
				logTableReadFailure(ctx, p.logger, p.source, orgID, table.name, err)
				return contextfabric.ProjectionBatch{}, false, &tableReadError{table: table.name, cause: err}
			}
			pageRows = append(pageRows, rows...)
		}
		if len(pageRows) == 0 {
			return contextfabric.ProjectionBatch{}, false, nil
		}
		sortCandidates(pageRows)
		pageRows = truncateToCompleteRows(pageRows, incrementalBatchCap)
		if all = p.window.unseen(p.windowScope, pageRows); len(all) > 0 {
			break
		}
		last := pageRows[len(pageRows)-1]
		walk = cursorState{Since: last.position(), After: last.sortKey}
	}
	normalizeCandidates(all, p.observeNormalization)
	items := partitionProjectableCandidates(all, p.observeQuarantine)
	// Whatever this pass consumed is now "seen": rows that are only
	// quarantined must not be re-judged every tick.
	p.window.record(p.windowScope, all, p.overlap)
	if !carriesPayload(items) {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	batch, err := buildBatch(orgID, p.source, p.version, cursor, all, items, false, false, p.clock())
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	// The window batch must NOT move the cursor backwards (its rows sit behind
	// the frontier): keep the frontier the caller reached.
	next := cursor
	if advanced {
		if encoded, err := encodeCursor(state); err == nil {
			next = encoded
		}
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", cursor, next, len(all))))
	for _, c := range all {
		digest = sha256.Sum256(append(digest[:], []byte(rowMemoKey(c))...))
	}
	batch.NextCursor = next
	batch.BatchID = deterministicBatchID(orgID, p.source, cursor, next+"|overlap|"+hex.EncodeToString(digest[:8]))
	if err := batch.Validate(); err != nil {
		return contextfabric.ProjectionBatch{}, false, fmt.Errorf("%w: devhealthsource: built an invalid overlap batch: %w", contextfabric.ErrInvalidResult, err)
	}
	p.forgetConsumed(orgID)
	p.observeBatch(ctx, batch, all)
	return batch, true, nil
}
