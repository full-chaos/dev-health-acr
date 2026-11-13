package devhealthsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
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

// projectTeamOwnershipReconcilePeriod is the cadence of the project -> team
// ownership reconcile pass: the stored groups' current rows, re-emitted so an
// edge the backend lost is written again without a log change.
const projectTeamOwnershipReconcilePeriod = time.Hour

// reconcilePlan is one named reconcile pass of a source. The first pass of a
// plan is the teams pass, whose batch ids, acks and log line predate named
// passes and stay as they were.
type reconcilePlan struct {
	name   string
	query  func(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID, after string, limit int) ([]candidate, bool, error)
	period time.Duration
	// table names the read in table-read errors; logMsg is the pass's Info line.
	table  string
	logMsg string
}

const (
	reconcilePassTeams        = "teams"
	reconcilePassProjectTeams = "project_team_ownership"
)

// reconcilePass is one scope's reconcile state.
type reconcilePass struct {
	lastDone time.Time // end of the last completed pass; zero = never ran in this process
	after    string    // the pass resumes AFTER this row key; empty = not walking
	teams    int       // rows emitted by the pass so far
	pending  *pendingReconcile
}

// pendingReconcile is an emitted reconcile page not yet known to be applied.
type pendingReconcile struct {
	ack   string
	after string
	rows  int
}

func (m *windowMemo) reconcileState(scope, pass string) reconcilePass {
	if m == nil {
		return reconcilePass{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if sc := m.scopes[scope]; sc != nil {
		return sc.recon[pass]
	}
	return reconcilePass{}
}

func (m *windowMemo) setReconcileState(scope, pass string, state reconcilePass) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sc := m.scopeLocked(scope)
	if sc.recon == nil {
		sc.recon = map[string]reconcilePass{}
	}
	sc.recon[pass] = state
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
	if sc == nil {
		return
	}
	for name, rs := range sc.recon {
		if rs.pending == nil {
			continue
		}
		pending := rs.pending
		rs.pending = nil
		if ack != "" && ack == pending.ack {
			rs.after = pending.after
			rs.teams += pending.rows
		}
		sc.recon[name] = rs
	}
}

func reconcileDue(pass reconcilePlan, rs reconcilePass, now time.Time) bool {
	// A zero lastDone (never ran in this process) is due: Sub saturates.
	return now.Sub(rs.lastDone) >= pass.period
}

// startReconcilePasses dates every pass's last completion now: a from-scratch
// projection derives every row just now.
func (p sourcePlan) startReconcilePasses() {
	for _, pass := range p.reconciles {
		p.window.setReconcileState(p.windowScope, pass.name, reconcilePass{lastDone: p.clock()})
	}
}

// reconcileBatch is pagedBatch's caught-up step before the overlap window: the
// first pass, in plan order, that is due or open and has a page with something
// to apply emits it. A pass that ends lets the next pass run in the same call.
func (p sourcePlan) reconcileBatch(ctx context.Context, orgID, cursor string) (contextfabric.ProjectionBatch, bool, error) {
	if p.window == nil {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	for i, pass := range p.reconciles {
		if pass.query == nil || pass.period <= 0 {
			continue
		}
		batch, published, ended, err := p.reconcilePage(ctx, orgID, cursor, pass, i == 0)
		if err != nil || published || !ended {
			return batch, published, err
		}
	}
	return contextfabric.ProjectionBatch{}, false, nil
}

// reconcilePage reads one page of one pass. ended reports that the pass is not
// due or has just ended, so the next pass may run.
func (p sourcePlan) reconcilePage(ctx context.Context, orgID, cursor string, pass reconcilePlan, legacy bool) (contextfabric.ProjectionBatch, bool, bool, error) {
	now := p.clock()
	rs := p.window.reconcileState(p.windowScope, pass.name)
	if !reconcileDue(pass, rs, now) {
		return contextfabric.ProjectionBatch{}, false, true, nil
	}
	rows, _, err := pass.query(ctx, p.client, orgID, rs.after, incrementalBatchCap)
	if err != nil {
		logTableReadFailure(ctx, p.logger, p.source, orgID, pass.table, err)
		return contextfabric.ProjectionBatch{}, false, false, &tableReadError{table: pass.table, cause: err}
	}
	// The pass ends on the read that returns no rows: a page that came back
	// short is followed by one more cheap read rather than a second code path.
	if len(rows) == 0 {
		p.window.setReconcileState(p.windowScope, pass.name, reconcilePass{lastDone: now})
		p.logReconcile(ctx, orgID, pass, rs.teams, true)
		return contextfabric.ProjectionBatch{}, false, true, nil
	}
	last := rows[len(rows)-1].sortKey
	normalizeCandidates(rows, p.observeNormalization)
	items := partitionProjectableCandidates(rows, p.quarantineObserver(orgID))
	if !carriesPayload(items) {
		// Nothing to apply on this page: the pass moves on in memory.
		rs.after = last
		p.window.setReconcileState(p.windowScope, pass.name, rs)
		p.noteYield()
		return contextfabric.ProjectionBatch{}, false, false, nil
	}
	// A reconcile batch never moves the checkpoint: the position it carries is
	// the checkpoint's own, not the one this call's skipped pages reached.
	frontier, err := decodeCursor(cursor)
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, false, err
	}
	frontier.Ack, frontier.Dim = "", nil
	if !cursorKeyFits(p.cursorSpace(), frontier.Since, frontier.After) {
		if frontier.After >= cursorSentinelKey {
			return contextfabric.ProjectionBatch{}, false, false, nil
		}
		frontier.After = cursorSentinelKey
	}
	label, batchSuffix := "reconcile", "|reconcile"
	if !legacy {
		label, batchSuffix = "reconcile:"+pass.name, "|reconcile:"+pass.name
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s", label, cursor, len(rows), rs.after)))
	for _, c := range rows {
		digest = sha256.Sum256(append(digest[:], []byte(c.sortKey)...))
	}
	frontier.Ack = hex.EncodeToString(digest[:8])
	next, err := encodeCursorIn(p.cursorSpace(), frontier)
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, false, err
	}
	batch, err := buildBatchTo(orgID, p.source, p.version, cursor, next, items, false, false, now)
	if err != nil {
		return contextfabric.ProjectionBatch{}, false, false, err
	}
	batch.BatchID = deterministicBatchID(orgID, p.source, cursor, next+batchSuffix)
	if err := batch.Validate(); err != nil {
		return contextfabric.ProjectionBatch{}, false, false, fmt.Errorf("%w: devhealthsource: built an invalid reconcile batch: %w", contextfabric.ErrInvalidResult, err)
	}
	rs.pending = &pendingReconcile{ack: frontier.Ack, after: last, rows: len(rows)}
	p.window.setReconcileState(p.windowScope, pass.name, rs)
	p.forgetConsumed(orgID)
	p.observeBatch(ctx, batch, rows)
	p.logReconcile(ctx, orgID, pass, rs.teams+len(rows), false)
	return batch, true, false, nil
}

// reconcileTable names the teams reconcile read in table-read errors.
const reconcileTable = "teams_reconcile"

// projectTeamReconcileTable names the ownership reconcile read.
const projectTeamReconcileTable = "project_team_ownership_reconcile"

// teamsReconcilePlan is the teams pass.
func teamsReconcilePlan(ledger *teamAuthorizationLedger) reconcilePlan {
	return reconcilePlan{name: reconcilePassTeams, query: teamsReconcileQuery(ledger), period: teamOwnershipReconcilePeriod, table: reconcileTable, logMsg: "team ownership reconcile"}
}

// projectTeamsReconcilePlan is the project -> team ownership pass.
func projectTeamsReconcilePlan(omissions *ambiguityLedger) reconcilePlan {
	return reconcilePlan{name: reconcilePassProjectTeams, query: ownershipGroupsReconcileQuery(omissions), period: projectTeamOwnershipReconcilePeriod, table: projectTeamReconcileTable, logMsg: "project team ownership reconcile"}
}

func (p sourcePlan) logReconcile(ctx context.Context, orgID string, pass reconcilePlan, rows int, final bool) {
	if p.logger == nil {
		return
	}
	if pass.name == reconcilePassTeams {
		p.logger.InfoContext(ctx, pass.logMsg,
			"source", contextfabric.SanitizeLogAttr(p.source), "org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)),
			"teams_re_derived", rows, "pass_complete", final, "period", contextfabric.SanitizeLogAttr(pass.period.String()))
		return
	}
	p.logger.InfoContext(ctx, pass.logMsg,
		"source", contextfabric.SanitizeLogAttr(p.source), "org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)),
		"groups_re_derived", rows, "pass_complete", final, "period", contextfabric.SanitizeLogAttr(pass.period.String()))
}
