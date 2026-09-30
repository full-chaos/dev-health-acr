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

// overlapWindowPageFactor bounds how many pages of rows one window read may
// return; a busier window is truncated (and logged), never silently.
const overlapWindowPageFactor = 5

// cursorSpaceIngest names the cursor position space: the row INGEST time.
// Cursors saved before CHAOS-7263 carry no space (provider/updated_at time) and
// decode as a reset, i.e. one full re-read (idempotent), which is also what
// recovers rows the old cursor space skipped.
const cursorSpaceIngest = "ingest.v1"

// windowMemo remembers, per organization, the rows this process has emitted
// near the frontier so the overlap re-read only emits NEW rows.
type windowMemo struct {
	mu   sync.Mutex
	seen map[string]map[string]time.Time // org -> row key -> position
}

func newWindowMemo() *windowMemo { return &windowMemo{seen: map[string]map[string]time.Time{}} }

func rowMemoKey(c candidate) string {
	return strconv.FormatInt(c.position().UnixNano(), 10) + "|" + c.sortKey
}

// record notes every candidate as emitted and prunes entries older than
// 2*overlap behind the newest recorded position.
func (m *windowMemo) record(orgID string, all []candidate, overlap time.Duration) {
	if m == nil || len(all) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := m.seen[orgID]
	if rows == nil {
		rows = map[string]time.Time{}
		m.seen[orgID] = rows
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

func (m *windowMemo) unseen(orgID string, all []candidate) []candidate {
	if m == nil {
		return all
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := m.seen[orgID]
	out := make([]candidate, 0, len(all))
	for _, c := range all {
		if _, ok := rows[rowMemoKey(c)]; !ok {
			out = append(out, c)
		}
	}
	return out
}

func (m *windowMemo) reset(orgID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	delete(m.seen, orgID)
	m.mu.Unlock()
}

// overlapBatch is pagedBatch's caught-up tail: nothing lies beyond the frontier,
// so re-read the trailing window and emit the rows not yet emitted.
func (p sourcePlan) overlapBatch(ctx context.Context, orgID, cursor string, state cursorState, advanced bool) (contextfabric.ProjectionBatch, bool, error) {
	if p.overlap <= 0 || p.window == nil || state.Since.IsZero() {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	low := cursorState{Since: state.Since.Add(-p.overlap)}
	cap := incrementalBatchCap * overlapWindowPageFactor
	var all []candidate
	truncated := false
	for _, table := range p.tables {
		rows, tableTruncated, err := table.query(ctx, p.client, orgID, low, cap)
		if err != nil {
			logTableReadFailure(ctx, p.logger, p.source, orgID, table.name, err)
			return contextfabric.ProjectionBatch{}, false, &tableReadError{table: table.name, cause: err}
		}
		truncated = truncated || tableTruncated
		all = append(all, rows...)
	}
	all = p.window.unseen(orgID, all)
	if len(all) == 0 {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	sortCandidates(all)
	all = truncateToCompleteRows(all, incrementalBatchCap)
	if truncated && p.logger != nil {
		p.logger.WarnContext(ctx, "devhealthsource overlap window read was truncated; later-arriving rows near the frontier may be re-read on the next tick",
			"source", contextfabric.SanitizeLogAttr(p.source), "org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)))
	}
	normalizeCandidates(all, p.observeNormalization)
	items := partitionProjectableCandidates(all, p.observeQuarantine)
	// Whatever this pass consumed is now "seen": rows that are only
	// quarantined must not be re-judged every tick.
	p.window.record(orgID, all, p.overlap)
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
