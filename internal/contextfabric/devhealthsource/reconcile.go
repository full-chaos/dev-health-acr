package devhealthsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// A max-watermark cannot see a hard delete: when the newest row a derived
// value was built from disappears, the watermark falls behind the saved
// cursor and the value is never re-read. The reconcile pass is the deletion
// signal the cursor lacks. Once caught up, and at most once per
// reconcilePeriod per scope, the source re-derives its current rows in key
// order and emits them WITHOUT moving the cursor position (the same shape as
// the overlap window: the batch carries an ack, the worker persists it only
// after the backend applied the batch). The pass state lives in the window
// memo, so a restart makes the next caught-up call start one pass.

// teamOwnershipReconcilePeriod is the cadence of the team reconcile pass.
const teamOwnershipReconcilePeriod = time.Hour

// reconcilePass is one scope's reconcile state.
type reconcilePass struct {
	lastDone time.Time // end of the last completed pass; zero = never ran in this process
	after    string    // the pass resumes AFTER this row key; empty = not walking
	walking  bool
	teams    int // rows emitted by the pass so far
	pending  *pendingReconcile
}

// pendingReconcile is an emitted reconcile page not yet known to be applied.
type pendingReconcile struct {
	ack   string
	after string
	final bool
	rows  int
}

func (m *windowMemo) reconcileState(scope string) reconcilePass {
	if m == nil {
		return reconcilePass{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if sc := m.scopes[scope]; sc != nil {
		return sc.recon
	}
	return reconcilePass{}
}

func (m *windowMemo) setReconcileState(scope string, state reconcilePass) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scopeLocked(scope).recon = state
}

// settleReconcile resolves the pending reconcile page against the cursor the
// caller brings from its durable checkpoint: the page counts as applied only
// when the cursor carries its ack. Otherwise the pass stays where it stood and
// the retry reads the same page again.
func (m *windowMemo) settleReconcile(scope, ack string, now time.Time) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sc := m.scopes[scope]
	if sc == nil || sc.recon.pending == nil {
		return
	}
	pending := sc.recon.pending
	sc.recon.pending = nil
	if ack == "" || ack != pending.ack {
		return
	}
	if pending.final {
		sc.recon = reconcilePass{lastDone: now}
		return
	}
	sc.recon.after, sc.recon.walking = pending.after, true
	sc.recon.teams += pending.rows
}

func (p sourcePlan) reconcileDue(rs reconcilePass, now time.Time) bool {
	if rs.walking {
		return true
	}
	return rs.lastDone.IsZero() || now.Sub(rs.lastDone) >= p.reconcilePeriod
}

// reconcileBatch is pagedBatch's caught-up step before the overlap window: when
// a pass is due or open, emit the next page of the source's current rows.
func (p sourcePlan) reconcileBatch(ctx context.Context, orgID, cursor string, state cursorState) (contextfabric.ProjectionBatch, bool, error) {
	if p.reconcile == nil || p.window == nil || p.reconcilePeriod <= 0 || state.Since.IsZero() {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	now := p.clock()
	rs := p.window.reconcileState(p.windowScope)
	if !p.reconcileDue(rs, now) {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	rows, truncated, err := p.reconcile(ctx, p.client, orgID, rs.after, incrementalBatchCap)
	if err != nil {
		logTableReadFailure(ctx, p.logger, p.source, orgID, reconcileTable, err)
		return contextfabric.ProjectionBatch{}, false, &tableReadError{table: reconcileTable, cause: err}
	}
	final := !truncated
	if len(rows) == 0 {
		p.window.setReconcileState(p.windowScope, reconcilePass{lastDone: now})
		p.logReconcile(ctx, orgID, rs.teams, true)
		return contextfabric.ProjectionBatch{}, false, nil
	}
	last := rows[len(rows)-1].sortKey
	normalizeCandidates(rows, p.observeNormalization)
	items := partitionProjectableCandidates(rows, p.quarantineObserver(orgID))
	if !carriesPayload(items) {
		// Nothing to apply on this page: the pass moves on in memory.
		next := rs
		next.after, next.walking = last, true
		if final {
			next = reconcilePass{lastDone: now}
		}
		p.window.setReconcileState(p.windowScope, next)
		p.noteYield()
		return contextfabric.ProjectionBatch{}, false, nil
	}
	frontier := state
	if !cursorKeyFits(p.cursorSpace(), frontier.Since, frontier.After) {
		if frontier.After >= cursorSentinelKey {
			return contextfabric.ProjectionBatch{}, false, nil
		}
		frontier.After = cursorSentinelKey
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("reconcile\x00%s\x00%d\x00%s", cursor, len(rows), rs.after)))
	for _, c := range rows {
		digest = sha256.Sum256(append(digest[:], []byte(c.sortKey)...))
	}
	frontier.Ack = hex.EncodeToString(digest[:8])
	next, err := encodeCursorIn(p.cursorSpace(), frontier)
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	batch, err := buildBatchTo(orgID, p.source, p.version, cursor, next, items, false, false, now)
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, err
	}
	batch.BatchID = deterministicBatchID(orgID, p.source, cursor, next+"|reconcile")
	if err := batch.Validate(); err != nil {
		return contextfabric.ProjectionBatch{}, false, fmt.Errorf("%w: devhealthsource: built an invalid reconcile batch: %w", contextfabric.ErrInvalidResult, err)
	}
	rs.pending = &pendingReconcile{ack: frontier.Ack, after: last, final: final, rows: len(rows)}
	p.window.setReconcileState(p.windowScope, rs)
	p.forgetConsumed(orgID)
	p.observeBatch(ctx, batch, rows)
	p.logReconcile(ctx, orgID, rs.teams+len(rows), final)
	return batch, true, nil
}

// reconcileTable names the reconcile read in table-read errors.
const reconcileTable = "teams_reconcile"

func (p sourcePlan) logReconcile(ctx context.Context, orgID string, teams int, final bool) {
	if p.logger == nil {
		return
	}
	p.logger.InfoContext(ctx, "team ownership reconcile",
		"source", contextfabric.SanitizeLogAttr(p.source), "org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)),
		"teams_re_derived", teams, "pass_complete", final, "period", p.reconcilePeriod.String())
}
