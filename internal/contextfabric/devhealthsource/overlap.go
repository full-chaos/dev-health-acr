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
// A bounded overlap closes that: once the source is caught up it walks a
// trailing window behind the frontier and emits the rows it has not already
// emitted (late arrivals) WITHOUT moving the cursor. Writes are idempotent
// MERGEs, so re-emitting is safe; a per-process memo keeps a caught-up tick
// from re-emitting the same window forever (a restart re-emits it once).
//
// THE WALK IS LOSSLESS IN DEPTH. The window is walked in PASSES, page by page
// at the ordinary page size, at most overlapWindowPagesPerCall pages per call.
// A pass that does not reach the window's end in one call stops at the last
// fully read row and resumes from exactly there on the next call (logged);
// it never skips the rest of the window and never restarts short of it. When
// a pass reaches the end, the window's lower edge moves up to (that pass's
// start - overlap - clock slack): a row stamped below that edge could only
// still be unseen if it landed more than overlap (+ slack) after its stamp,
// which is the documented bound. So a quiet organization's window closes on
// its own once the passes outrun the frontier, and costs nothing after that.
//
// The lower edge of the FIRST pass in a scope (process start, or a new epoch)
// is min(frontier, now) - overlap - slack.

// defaultReprojectOverlap is the trailing window re-read once caught up.
const defaultReprojectOverlap = 15 * time.Minute

// overlapWindowPagesPerCall bounds how many pages of incrementalBatchCap rows
// one call walks. Each page is an ordinary keyset read at the ordinary page
// size: one statement per table with LIMIT incrementalBatchCap+1. A single
// larger read is not an option -- the projector's ClickHouse client runs
// with max_result_rows = 1,000 and result_overflow_mode = throw, so any
// statement returning more rows FAILS the tick, and a failing caught-up tick
// fails again every tick until the frontier moves. The bound is on work per
// call, not on depth: the pass resumes where it stopped.
const overlapWindowPagesPerCall = 5

// cursorSpaceIngest names the cursor position space: the row INGEST time.
// Cursors saved before CHAOS-7263 carry no space (provider/updated_at time) and
// decode as a reset, i.e. one full re-read (idempotent), which is also what
// recovers rows the old cursor space skipped.
const cursorSpaceIngest = "ingest.v1"

// windowMemo remembers, per scope (organization and epoch, windowScopeFor),
// the rows this process has emitted near the frontier -- so the overlap walk
// only emits NEW rows -- and where the scope's current pass stands.
type windowMemo struct {
	mu     sync.Mutex
	scopes map[string]*windowScope
}

// windowScope is one scope's memo. low/walk/passStart/walking describe the
// pass (see the header above); seen holds the emitted rows at or above low.
type windowScope struct {
	seen map[string]time.Time // row key -> position
	windowPass
}

// windowPass is the pass state overlapBatch reads and writes as one value.
type windowPass struct {
	low       time.Time   // lower edge of the walk (ingest-stamp clock); zero = no pass yet
	walk      cursorState // the pass resumes AFTER this position
	passStart time.Time   // this process's clock when the current pass started
	walking   bool        // a pass is in progress
}

// windowScopeFor keys the memo by organization AND checkpoint epoch: the
// serving graph and a build-aside graph of one organization are drained by
// the same source, and each must receive a late row on its own.
func windowScopeFor(orgID string, epoch int64) string {
	return orgID + "\x00" + strconv.FormatInt(epoch, 10)
}

func newWindowMemo() *windowMemo { return &windowMemo{scopes: map[string]*windowScope{}} }

func rowMemoKey(c candidate) string {
	return strconv.FormatInt(c.position().UnixNano(), 10) + "|" + c.sortKey
}

func (m *windowMemo) scopeLocked(scope string) *windowScope {
	sc := m.scopes[scope]
	if sc == nil {
		sc = &windowScope{seen: map[string]time.Time{}}
		m.scopes[scope] = sc
	}
	return sc
}

// record notes every candidate as emitted and prunes entries no walk can
// reach again: below the scope's window edge once a pass exists, else more
// than 3*overlap behind the newest recorded position.
func (m *windowMemo) record(scope string, all []candidate, overlap time.Duration) {
	if m == nil || len(all) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sc := m.scopeLocked(scope)
	newest := time.Time{}
	for _, c := range all {
		sc.seen[rowMemoKey(c)] = c.position()
		if c.position().After(newest) {
			newest = c.position()
		}
	}
	floor := sc.low
	if floor.IsZero() {
		floor = newest.Add(-3 * overlap)
	}
	for k, at := range sc.seen {
		if at.Before(floor) {
			delete(sc.seen, k)
		}
	}
}

func (m *windowMemo) unseen(scope string, all []candidate) []candidate {
	if m == nil {
		return all
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var rows map[string]time.Time
	if sc := m.scopes[scope]; sc != nil {
		rows = sc.seen
	}
	out := make([]candidate, 0, len(all))
	for _, c := range all {
		if _, ok := rows[rowMemoKey(c)]; !ok {
			out = append(out, c)
		}
	}
	return out
}

func (m *windowMemo) pass(scope string) windowPass {
	if m == nil {
		return windowPass{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if sc := m.scopes[scope]; sc != nil {
		return sc.windowPass
	}
	return windowPass{}
}

func (m *windowMemo) setPass(scope string, pass windowPass) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scopeLocked(scope).windowPass = pass
}

// snapshot returns an independent copy of the memo. A side-effect-free read
// (PeekProjectionBatch) runs the same engine against the copy: the overlap
// walk still sees what this process emitted, but whatever the peek "emits",
// walks or resets lands in the copy and is discarded. Recording into the
// shared memo there would mark late rows as emitted without any batch
// carrying them, and the next real tick would skip them.
func (m *windowMemo) snapshot() *windowMemo {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := newWindowMemo()
	for scope, sc := range m.scopes {
		copied := &windowScope{seen: make(map[string]time.Time, len(sc.seen)), windowPass: sc.windowPass}
		for k, at := range sc.seen {
			copied.seen[k] = at
		}
		out.scopes[scope] = copied
	}
	return out
}

func (m *windowMemo) reset(scope string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	delete(m.scopes, scope)
	m.mu.Unlock()
}

// overlapBatch is pagedBatch's caught-up tail: nothing lies beyond the
// frontier, so continue (or start) this scope's pass over the trailing window
// and emit the first page's worth of rows not yet emitted. Pages whose rows
// were all emitted already are stepped over in-process (the same keyset step
// pagedBatch uses for an omitted page). See the header for the pass rules.
func (p sourcePlan) overlapBatch(ctx context.Context, orgID, cursor string, state cursorState, advanced bool) (contextfabric.ProjectionBatch, bool, error) {
	if p.overlap <= 0 || p.window == nil || state.Since.IsZero() {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	now := p.clock()
	// slack is the clock skew allowed between the writers that stamp ingest
	// times and this process's clock, which dates the passes.
	slack := p.overlap
	pass := p.window.pass(p.windowScope)
	if pass.low.IsZero() {
		edge := state.Since
		if now.Before(edge) {
			edge = now
		}
		pass.low = edge.Add(-p.overlap - slack)
	}
	start := func() {
		pass.walking, pass.passStart, pass.walk = true, now, cursorState{Since: pass.low}
	}
	if !pass.walking {
		if pass.low.After(state.Since) {
			// Closed: nothing at or behind the frontier can still land late.
			p.window.setPass(p.windowScope, pass)
			return contextfabric.ProjectionBatch{}, false, nil
		}
		start()
	}
	tables := p.tables
	if p.windowTables != nil {
		tables = p.windowTables
	}
	completed := false
	var all []candidate
	for page := 0; ; page++ {
		if page == overlapWindowPagesPerCall {
			p.window.setPass(p.windowScope, pass)
			if p.logger != nil {
				p.logger.WarnContext(ctx, "devhealthsource overlap window pass continues on the next tick; late rows deeper in the window are not skipped, only delayed",
					"source", contextfabric.SanitizeLogAttr(p.source), "org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)),
					"pages_this_call", overlapWindowPagesPerCall, "page_rows", incrementalBatchCap,
					"window_low", contextfabric.SanitizeLogAttr(pass.low.UTC().Format(time.RFC3339Nano)), "resume_after", contextfabric.SanitizeLogAttr(pass.walk.Since.UTC().Format(time.RFC3339Nano)),
					"frontier", contextfabric.SanitizeLogAttr(state.Since.UTC().Format(time.RFC3339Nano)), "pass_age_seconds", int64(now.Sub(pass.passStart).Seconds()))
			}
			return contextfabric.ProjectionBatch{}, false, nil
		}
		var pageRows []candidate
		for _, table := range tables {
			rows, _, err := table.query(ctx, p.client, orgID, pass.walk, incrementalBatchCap)
			if err != nil {
				logTableReadFailure(ctx, p.logger, p.source, orgID, table.name, err)
				return contextfabric.ProjectionBatch{}, false, &tableReadError{table: table.name, cause: err}
			}
			pageRows = append(pageRows, rows...)
		}
		if len(pageRows) == 0 {
			// The pass reached the window's end: every row that could land
			// late with a stamp below (pass start - overlap - slack) has.
			if edge := pass.passStart.Add(-p.overlap - slack); edge.After(pass.low) {
				pass.low = edge
			}
			pass.walking = false
			if completed || pass.low.After(state.Since) {
				p.window.setPass(p.windowScope, pass)
				return contextfabric.ProjectionBatch{}, false, nil
			}
			completed = true
			start()
			continue
		}
		sortCandidates(pageRows)
		pageRows = truncateToCompleteRows(pageRows, incrementalBatchCap)
		last := pageRows[len(pageRows)-1]
		pass.walk = cursorState{Since: last.position(), After: last.sortKey}
		if all = p.window.unseen(p.windowScope, pageRows); len(all) > 0 {
			break
		}
	}
	p.window.setPass(p.windowScope, pass)
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
