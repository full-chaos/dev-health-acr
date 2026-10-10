package devhealthsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// CHAOS-7263: the projection cursor keys on each row's INGEST time. Ingest
// time is assigned when a row is normalized, BEFORE the insert lands (normalize
// -> queue/batch -> ClickHouse) and writers on different hosts skew, so a row
// stamped just before the cursor can still land after the cursor passed it.
// A bounded overlap closes that: once the source is caught up it walks a
// trailing window behind the frontier and emits the rows it has not already
// emitted (late arrivals) WITHOUT moving the cursor position. Writes are
// idempotent MERGEs, so re-emitting is safe; a per-process memo keeps a
// caught-up tick from re-emitting the same window forever (a restart
// re-emits it once). A window batch's rows enter that memo only when the
// durable checkpoint shows the batch applied: its NextCursor keeps the
// position and carries an ack of the batch, which the worker persists only
// after the backend applied it (windowMemo.settle). A failed apply leaves
// the checkpoint without the ack, and the retry walks the same rows again.
//
// THE WALK IS LOSSLESS IN DEPTH. The window is walked in PASSES, page by page
// at the ordinary page size, at most overlapWindowPagesPerCall pages per call.
// A pass that does not reach the window's end in one call stops at the last
// fully read row and resumes from exactly there on the next call (logged);
// it never skips the rest of the window and never restarts short of it.
//
// EVERY PASS ENDS. The window's upper edge is the frontier as it stood when
// the pass started (windowPass.high), a fixed keyset position. Rows after it
// are not the pass's: they lie beyond the frontier of that moment, so the
// paged read emits them. Without the edge the walk follows the frontier, and
// a writer that lands more rows per tick than one tick walks keeps the pass
// open for as long as it writes: rows that land behind the walk then wait for
// a next pass that never starts. A page that holds a row past the edge, or on
// which no table had rows past its LIMIT, is the window's end, so a window
// smaller than one page costs one statement per table. A pass this call
// started ends the call when it completes; a pass resumed from an earlier
// call is followed at once by a new pass (rows may have landed behind it
// while it was paused). When a pass reaches the end, the window's lower edge
// moves up to (that pass's start - overlap - clock slack): a row stamped below
// that edge could only still be unseen if it landed more than overlap
// (+ slack) after its stamp, which is the documented bound. So a quiet organization's window closes on
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

// cursorSpaceIngestColumns is the position space of a teams/projects source
// whose team_project_ownership and project_membership_presence producers page
// on their server-side ingest columns (ops migrations 099 and 100). A cursor
// saved in cursorSpaceIngest (those two on provider/event time) is decoded as
// a reset by sourcePlan.nextBatch, once, and so is the reverse.
const cursorSpaceIngestColumns = "ingest.v2"

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
	// pending is the window batch this scope emitted last, not yet known to
	// be applied (settle decides on the next call).
	pending *pendingWindow
	// catchUp is the scope's catch-up pass (catch_up.go).
	catchUp catchUpPass
}

// pendingWindow is an emitted window batch whose rows are not yet "seen":
// the pass as it stood before the batch was built, and the batch's rows.
type pendingWindow struct {
	ack     string
	before  windowPass
	rows    []candidate
	overlap time.Duration
}

// windowPass is the pass state overlapBatch reads and writes as one value.
type windowPass struct {
	low       time.Time   // lower edge of the walk (ingest-stamp clock); zero = no pass yet
	high      cursorState // upper edge of the current pass: the frontier when it started
	walk      cursorState // the pass resumes AFTER this position
	passStart time.Time   // this process's clock when the current pass started
	walking   bool        // a pass is in progress
	pages     int         // pages the current pass has read
	rows      int         // source rows the current pass has read
}

// pastEdge reports whether c sorts after the keyset position edge, in the
// order the readers page in: position first, row key second.
func pastEdge(c candidate, edge cursorState) bool {
	if !c.position().Equal(edge.Since) {
		return c.position().After(edge.Since)
	}
	return c.sortKey > edge.After
}

// withinEdge returns the leading rows of a sorted page that lie at or before
// edge, and whether the page held a row past it.
func withinEdge(page []candidate, edge cursorState) ([]candidate, bool) {
	for i, c := range page {
		if pastEdge(c, edge) {
			return page[:i], true
		}
	}
	return page, false
}

// windowScopeFor keys the memo by organization AND checkpoint epoch: the
// serving graph and a build-aside graph of one organization are drained by
// the same source, and each must receive a late row on its own.
func windowScopeFor(orgID string, epoch int64) string {
	return orgID + "\x00" + strconv.FormatInt(epoch, 10)
}

func newWindowMemo() *windowMemo { return &windowMemo{scopes: map[string]*windowScope{}} }

// rowMemoKey is a row's identity in the memo, and in a window batch's ack:
// its table, position and row key. Position and row key alone collide across
// tables (the keyset shares one order across all of them).
func rowMemoKey(c candidate) string {
	return c.table + "\x00" + strconv.FormatInt(c.position().UnixNano(), 10) + "|" + c.sortKey
}

// readTable runs one producer and labels its candidates with the table they
// came from (rowMemoKey).
func readTable(ctx context.Context, table entityTable, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	rows, truncated, err := table.query(ctx, client, orgID, cursor, limit)
	for i := range rows {
		rows[i].table = table.name
	}
	return rows, truncated, err
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
	m.scopeLocked(scope).recordLocked(all, overlap)
}

func (sc *windowScope) recordLocked(all []candidate, overlap time.Duration) {
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

// hold notes a window batch that was just emitted. Its rows stay unseen
// until settle learns the batch was applied.
func (m *windowMemo) hold(scope string, pending pendingWindow) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scopeLocked(scope).pending = &pending
}

// settle resolves the scope's pending window batch against the cursor the
// caller brings from its durable checkpoint. That cursor carries the batch's
// ack exactly when the worker persisted the batch's NextCursor, which it does
// only after the backend applied the batch: then the rows are emitted and
// count as seen. Any other cursor means the batch never landed (the apply
// failed, or nothing applied it): the pass goes back to where it stood before
// that batch, so the retry walks the same rows again. A memo mutation is not
// a commit; the durable checkpoint is.
func (m *windowMemo) settle(scope, ack string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sc := m.scopes[scope]
	if sc == nil || sc.pending == nil {
		return
	}
	if ack != "" && ack == sc.pending.ack {
		sc.recordLocked(sc.pending.rows, sc.pending.overlap)
	} else {
		sc.windowPass = sc.pending.before
	}
	sc.pending = nil
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
		if sc.pending != nil {
			pending := *sc.pending
			copied.pending = &pending
		}
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
func (p sourcePlan) overlapBatch(ctx context.Context, orgID, cursor string, state cursorState) (contextfabric.ProjectionBatch, bool, error) {
	if p.overlap <= 0 || p.window == nil || state.Since.IsZero() {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	now := p.clock()
	// slack is the clock skew allowed between the writers that stamp ingest
	// times and this process's clock, which dates the passes.
	slack := p.overlap
	pass := p.window.pass(p.windowScope)
	before := pass
	if pass.low.IsZero() {
		edge := state.Since
		if now.Before(edge) {
			edge = now
		}
		pass.low = edge.Add(-p.overlap - slack)
	}
	start := func() {
		pass.walking, pass.passStart, pass.walk = true, now, cursorState{Since: pass.low}
		pass.high = cursorState{Since: state.Since, After: state.After}
		pass.pages, pass.rows = 0, 0
	}
	// A pass resumed from an earlier call may have been passed by rows that
	// landed behind its position meanwhile, so when it completes a new pass
	// starts at once. A pass started by THIS call has not: when it completes,
	// the call ends and the next tick starts the next pass.
	resumed := pass.walking
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
	pagesPerCall := overlapWindowPagesPerCall
	if p.windowPagesPerCall > 0 {
		pagesPerCall = p.windowPagesPerCall
	}
	completed := false
	var all []candidate
	page := 0
	for ; ; page++ {
		if page == pagesPerCall {
			p.window.setPass(p.windowScope, pass)
			p.logOpenPass(ctx, orgID, pass, state, now, page)
			p.noteYield()
			return contextfabric.ProjectionBatch{}, false, nil
		}
		var pageRows []candidate
		// more: some row beyond this page may still lie in the window. Each
		// table reports whether it had rows past its LIMIT (fetch); the
		// merged page is also cut back to complete rows. When neither
		// happened, this page reached the window's end and no further
		// (empty) read is needed to learn that.
		more := false
		var bound pageBound
		for _, table := range tables {
			rows, truncated, err := readTable(ctx, table, p.client, orgID, pass.walk, incrementalBatchCap)
			if err == nil {
				err = p.boundRead(ctx, orgID, table.name, pass.walk, &bound, rows, truncated)
			}
			if err != nil {
				logTableReadFailure(ctx, p.logger, p.source, orgID, table.name, err)
				return contextfabric.ProjectionBatch{}, false, &tableReadError{table: table.name, cause: err}
			}
			more = more || truncated
			pageRows = append(pageRows, rows...)
		}
		if len(pageRows) > 0 {
			sortCandidates(pageRows)
			complete, bounded := truncateToCompleteRows(pageRows, incrementalBatchCap, bound)
			if bounded {
				p.logBoundedPage(ctx, orgID, bound)
			}
			more = more || len(complete) < len(pageRows)
			// The pass ends at its upper edge. A row past it lies beyond the
			// frontier this pass started at: the paged read's row, and the
			// proof that nothing of the window is left behind it.
			inside, edged := withinEdge(complete, pass.high)
			if edged {
				more = false
			}
			pageRows = inside
		}
		pass.pages++
		if len(pageRows) > 0 {
			pass.rows += sourceRows(pageRows)
			last := pageRows[len(pageRows)-1]
			pass.walk = cursorState{Since: last.position(), After: last.sortKey}
			if all = p.window.unseen(p.windowScope, pageRows); len(all) > 0 {
				break
			}
		}
		if more {
			continue
		}
		// The pass reached the window's end: every row that could land
		// late with a stamp below (pass start - overlap - slack) has.
		if edge := pass.passStart.Add(-p.overlap - slack); edge.After(pass.low) {
			pass.low = edge
		}
		pass.walking = false
		if resumed && !completed {
			p.logEndedPass(ctx, orgID, pass, now)
		}
		if completed || !resumed || pass.low.After(state.Since) {
			p.window.setPass(p.windowScope, pass)
			return contextfabric.ProjectionBatch{}, false, nil
		}
		completed = true
		start()
	}
	p.window.setPass(p.windowScope, pass)
	normalizeCandidates(all, p.observeNormalization)
	items := partitionProjectableCandidates(all, p.quarantineObserver(orgID))
	if !carriesPayload(items) {
		// Nothing to apply: rows that are only quarantined count as seen
		// now, so they are not re-judged every tick.
		p.window.record(p.windowScope, all, p.overlap)
		// The walk read a page and stopped; it is unfinished until a call
		// reaches the window's end, so the pass is not over.
		p.logOpenPass(ctx, orgID, pass, state, now, page+1)
		p.noteYield()
		return contextfabric.ProjectionBatch{}, false, nil
	}
	batch, err := buildBatchIn(p.cursorSpace(), orgID, p.source, p.version, cursor, all, items, false, false, p.clock())
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	// The window batch must NOT move the cursor position backwards (its rows
	// sit behind the frontier): NextCursor keeps the position the caller
	// reached and carries this batch's ack, which is deterministic in the
	// checkpoint cursor and the rows, so a retry of an unapplied batch is the
	// same batch again.
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", cursor, len(all))))
	for _, c := range all {
		digest = sha256.Sum256(append(digest[:], []byte(rowMemoKey(c))...))
	}
	frontier := state
	frontier.Ack = hex.EncodeToString(digest[:8])
	next, err := encodeCursorIn(p.cursorSpace(), frontier)
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	batch.NextCursor = next
	batch.BatchID = deterministicBatchID(orgID, p.source, cursor, next+"|overlap")
	if err := batch.Validate(); err != nil {
		return contextfabric.ProjectionBatch{}, false, fmt.Errorf("%w: devhealthsource: built an invalid overlap batch: %w", contextfabric.ErrInvalidResult, err)
	}
	p.window.hold(p.windowScope, pendingWindow{ack: frontier.Ack, before: before, rows: all, overlap: p.overlap})
	p.forgetConsumed(orgID)
	p.observeBatch(ctx, batch, all)
	return batch, true, nil
}

// sourceRows counts the source rows in a sorted page: one row can carry
// several candidates, which share its table, position and row key. Rows of
// two tables can share a position and a row key; they are two rows.
func sourceRows(page []candidate) int {
	rows := 0
	for i, c := range page {
		if i == 0 || c.table != page[i-1].table || !c.position().Equal(page[i-1].position()) || c.sortKey != page[i-1].sortKey {
			rows++
		}
	}
	return rows
}

// overduePass reports whether an OPEN pass is older than the lateness the
// window absorbs (one overlap). Past that, the walk itself delays a late row
// for longer than the row was late. Callers ask only about an open pass.
func overduePass(pass windowPass, overlap time.Duration, now time.Time) bool {
	return now.Sub(pass.passStart) > overlap
}

const (
	openPassMessage    = "devhealthsource overlap window pass continues on the next tick; late rows deeper in the window are not skipped, only delayed"
	overduePassMessage = "devhealthsource overlap window pass is older than the lateness the window absorbs; late rows wait longer than they were late"
	endedPassMessage   = "devhealthsource overlap window pass ended"
)

// logOpenPass writes the one line a call writes when it ends with the pass
// still open: where the pass stands and how much of the window is left. It is
// a warning only once the pass is overdue (overduePass).
func (p sourcePlan) logOpenPass(ctx context.Context, orgID string, pass windowPass, frontier cursorState, now time.Time, pagesThisCall int) {
	if p.logger == nil {
		return
	}
	// The walk never stands past the edge: a page is cut there.
	remainingSpan := pass.high.Since.Sub(pass.walk.Since)
	// A linear estimate from the rows read so far over the span read so far.
	// -1 means no estimate: the pass has not moved yet, or the walk stands on
	// the edge's own stamp, where the rows left differ only in their row key
	// and a span of zero says nothing about how many there are.
	remainingRows := int64(-1)
	if walked := pass.walk.Since.Sub(pass.low); walked > 0 && pass.rows > 0 && remainingSpan > 0 {
		remainingRows = int64(math.Round(float64(pass.rows) * float64(remainingSpan) / float64(walked)))
	}
	level, message := slog.LevelInfo, openPassMessage
	if overduePass(pass, p.overlap, now) {
		level, message = slog.LevelWarn, overduePassMessage
	}
	p.logger.Log(ctx, level, message,
		"source", contextfabric.SanitizeLogAttr(p.source), "org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)),
		"pages_this_call", pagesThisCall, "page_rows", incrementalBatchCap,
		"window_low", contextfabric.SanitizeLogAttr(pass.low.UTC().Format(time.RFC3339Nano)),
		"window_high", contextfabric.SanitizeLogAttr(pass.high.Since.UTC().Format(time.RFC3339Nano)),
		"resume_after", contextfabric.SanitizeLogAttr(pass.walk.Since.UTC().Format(time.RFC3339Nano)),
		// The walk and the edge are keyset positions: stamp AND row key. On
		// tied stamps only the key moves, so the line carries both digests.
		"resume_after_key_digest", contextfabric.SanitizeLogAttr(keyDigest(pass.walk.After)),
		"window_high_key_digest", contextfabric.SanitizeLogAttr(keyDigest(pass.high.After)),
		"frontier", contextfabric.SanitizeLogAttr(frontier.Since.UTC().Format(time.RFC3339Nano)),
		"pass_age_seconds", int64(now.Sub(pass.passStart).Seconds()),
		"pass_bound_seconds", int64(p.overlap.Seconds()), "pass_pages", pass.pages, "pass_rows", pass.rows,
		"remaining_span_seconds", int64(remainingSpan.Seconds()), "remaining_rows_estimate", remainingRows)
}

// logEndedPass closes the lines of a pass that spanned more than one call.
func (p sourcePlan) logEndedPass(ctx context.Context, orgID string, pass windowPass, now time.Time) {
	if p.logger == nil {
		return
	}
	p.logger.InfoContext(ctx, endedPassMessage,
		"source", contextfabric.SanitizeLogAttr(p.source), "org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)),
		"pass_seconds", int64(now.Sub(pass.passStart).Seconds()), "pass_pages", pass.pages, "pass_rows", pass.rows,
		"window_low", contextfabric.SanitizeLogAttr(pass.low.UTC().Format(time.RFC3339Nano)),
		"window_high", contextfabric.SanitizeLogAttr(pass.high.Since.UTC().Format(time.RFC3339Nano)))
}

// reportWindowPass is ProjectionWindowPass for both sources: the state of the
// checkpoint scope's pass, read from the memo and never changed.
func reportWindowPass(memo *windowMemo, overlap time.Duration, now time.Time, checkpoint contextfabric.ProjectionCheckpoint) contextfabric.ProjectionWindowPass {
	pass := memo.pass(windowScopeFor(strings.TrimSpace(checkpoint.OrgID), checkpoint.Epoch))
	if !pass.walking {
		return contextfabric.ProjectionWindowPass{}
	}
	return contextfabric.ProjectionWindowPass{
		Open: true, StartedAt: pass.passStart, Age: now.Sub(pass.passStart), Bound: overlap,
		Overdue: overduePass(pass, overlap, now),
	}
}

var (
	_ contextfabric.ProjectionWindowReporter = (*ClickHouseProjectionSource)(nil)
	_ contextfabric.ProjectionWindowReporter = (*TeamsProjectsSource)(nil)
)

// ProjectionWindowPass implements contextfabric.ProjectionWindowReporter.
func (s *ClickHouseProjectionSource) ProjectionWindowPass(checkpoint contextfabric.ProjectionCheckpoint) contextfabric.ProjectionWindowPass {
	if s == nil {
		return contextfabric.ProjectionWindowPass{}
	}
	return reportWindowPass(s.window, s.overlap, sourcePlan{now: s.now}.clock(), checkpoint)
}

// ProjectionWindowPass implements contextfabric.ProjectionWindowReporter.
func (s *TeamsProjectsSource) ProjectionWindowPass(checkpoint contextfabric.ProjectionCheckpoint) contextfabric.ProjectionWindowPass {
	if s == nil {
		return contextfabric.ProjectionWindowPass{}
	}
	return reportWindowPass(s.window, s.overlap, sourcePlan{now: s.now}.clock(), checkpoint)
}
