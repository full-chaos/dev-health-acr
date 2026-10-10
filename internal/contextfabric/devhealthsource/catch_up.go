package devhealthsource

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A source that is not caught up walks every table with ONE keyset cursor, in
// stamp order, a page per call. Two things follow from that order and are
// handled here.
//
// COMPLETE TABLES GO FIRST. A from-zero walk reaches a row only when the
// cursor reaches its stamp. A row the sync re-stamps (a repository: its row is
// rewritten on every sync) sorts last for the whole walk, so a repository that
// no other row names has no node until the last page, hours later. The
// from-zero read already asks every table for its first rows at the snapshot
// cap, so it knows which tables came back COMPLETE. Those tables go into the
// first batch of the build, beside the first page; the cursor is the page's,
// and the walk emits the same rows again when it reaches them (the writes are
// idempotent merges). A table the read truncated is never emitted early: its
// first rows are only its oldest rows.
//
// A CATCH-UP PASS HAS AN EDGE. A stretch of paged work is a pass: it opens
// with the first page a call publishes, with the source clock as its upper
// edge, and it ends when the cursor's stamp reaches that edge or the read is
// caught up. The edge does not cut pages. It bounds the pass and gives the
// caller a distance that reaches zero (edge - cursor), which "now - cursor"
// never does for a source that is written to. The pass lives in the
// per-process memo, like the window pass: a restart opens a new one.

// completeTable is one table the from-zero read returned whole.
type completeTable struct {
	name string
	rows []candidate
}

// catchUpPass is the state of one scope's catch-up pass.
type catchUpPass struct {
	open  bool
	edge  time.Time // the source clock when the pass opened
	pages int
	rows  int
}

// advanceCatchUp notes one page the paged read moved past. It opens a pass if
// none is open and returns the pass when this page ended it.
func (m *windowMemo) advanceCatchUp(scope string, now, pageEnd time.Time, rows int) (ended catchUpPass, done bool) {
	if m == nil {
		return catchUpPass{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sc := m.scopeLocked(scope)
	if !sc.catchUp.open {
		sc.catchUp = catchUpPass{open: true, edge: now}
	}
	sc.catchUp.pages++
	sc.catchUp.rows += rows
	if pageEnd.Before(sc.catchUp.edge) {
		return catchUpPass{}, false
	}
	ended = sc.catchUp
	sc.catchUp = catchUpPass{}
	return ended, true
}

// endCatchUp closes the scope's pass when the read found nothing beyond the
// cursor, and returns it when one was open.
func (m *windowMemo) endCatchUp(scope string) (ended catchUpPass, done bool) {
	if m == nil {
		return catchUpPass{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sc := m.scopes[scope]
	if sc == nil || !sc.catchUp.open {
		return catchUpPass{}, false
	}
	ended = sc.catchUp
	sc.catchUp = catchUpPass{}
	return ended, true
}

func (m *windowMemo) catchUp(scope string) catchUpPass {
	if m == nil {
		return catchUpPass{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if sc := m.scopes[scope]; sc != nil {
		return sc.catchUp
	}
	return catchUpPass{}
}

func (m *windowMemo) workAhead(scope string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sc := m.scopes[scope]
	return sc != nil && sc.ahead
}

// setAhead records whether the scope's paged read stopped with rows beyond
// it. The caller of the source reads it as "work is left", whatever the
// reason the read stopped.
func (m *windowMemo) setAhead(scope string, ahead bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scopeLocked(scope).ahead = ahead
}

const (
	catchUpEndedMessage    = "devhealthsource catch-up pass ended"
	completeTablesMessage  = "devhealthsource from-zero read emits its complete tables with the first batch"
	catchUpEndEdgeReached  = "edge_reached"
	catchUpEndNothingAhead = "caught_up"
)

// notePagedPage records a page the paged read moved past in the scope's
// catch-up pass and writes the line of a pass that this page ended.
func (p sourcePlan) notePagedPage(ctx context.Context, orgID string, page []candidate) {
	if len(page) == 0 {
		return
	}
	now := p.clock()
	if ended, done := p.window.advanceCatchUp(p.windowScope, now, page[len(page)-1].position(), sourceRows(page)); done {
		p.logCatchUpEnded(ctx, orgID, ended, now, catchUpEndEdgeReached)
	}
}

// noteCaughtUp ends the scope's catch-up pass: nothing lies beyond the cursor.
func (p sourcePlan) noteCaughtUp(ctx context.Context, orgID string) {
	p.window.setAhead(p.windowScope, false)
	if ended, done := p.window.endCatchUp(p.windowScope); done {
		p.logCatchUpEnded(ctx, orgID, ended, p.clock(), catchUpEndNothingAhead)
	}
}

func (p sourcePlan) logCatchUpEnded(ctx context.Context, orgID string, pass catchUpPass, now time.Time, reason string) {
	if p.logger == nil {
		return
	}
	p.logger.InfoContext(ctx, catchUpEndedMessage,
		"source", contextfabric.SanitizeLogAttr(p.source), "org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)),
		"end_reason", contextfabric.SanitizeLogAttr(reason),
		"pass_seconds", int64(now.Sub(pass.edge).Seconds()), "pass_pages", pass.pages, "pass_rows", pass.rows,
		"pass_edge", contextfabric.SanitizeLogAttr(pass.edge.UTC().Format(time.RFC3339Nano)))
}

// earlyTables is what the complete tables add to the first batch of a
// from-zero walk.
type earlyTables struct {
	items         []candidate // judged (normalized, quarantine applied)
	emitted       []string    // tables whose rows are in items
	leftForBounds []string    // complete tables that did not fit the batch
	truncated     []string    // tables the from-zero read did not return whole
	rows          int
	offered       bool // the plan carried complete tables (a from-zero walk)
}

// selectCompleteTables picks the complete tables that go with the page: whole
// tables only, in table order, while page and tables together stay inside the
// contract's batch bounds, and never a row the page already carries.
func selectCompleteTables(page []candidate, complete []completeTable) (rows []candidate, emitted, leftForBounds []string) {
	inPage := make(map[string]struct{}, len(page))
	for _, c := range page {
		inPage[rowMemoKey(c)] = struct{}{}
	}
	entities, relationships, tombstones := candidateCounts(page)
	for _, table := range complete {
		extra := make([]candidate, 0, len(table.rows))
		for _, c := range table.rows {
			if _, carried := inPage[rowMemoKey(c)]; !carried {
				extra = append(extra, c)
			}
		}
		e, r, t := candidateCounts(extra)
		if entities+e > contractsv1.ContextFabricProjectionBatchMaxEntities ||
			relationships+r > contractsv1.ContextFabricProjectionBatchMaxRelationships ||
			tombstones+t > contractsv1.ContextFabricProjectionBatchMaxTombstones {
			leftForBounds = append(leftForBounds, table.name)
			continue
		}
		entities, relationships, tombstones = entities+e, relationships+r, tombstones+t
		rows = append(rows, extra...)
		emitted = append(emitted, table.name)
	}
	return rows, emitted, leftForBounds
}

// completeTableItems judges the complete tables that go with the page. The
// zero value when the plan carries none (every call but the first of a
// from-zero walk).
func (p sourcePlan) completeTableItems(orgID string, page []candidate) earlyTables {
	if len(p.complete) == 0 && len(p.truncatedTables) == 0 {
		return earlyTables{}
	}
	// The rows keep the order the tables returned them in, table after table.
	rows, emitted, leftForBounds := selectCompleteTables(page, p.complete)
	normalizeCandidates(rows, p.observeNormalization)
	return earlyTables{
		items:   partitionProjectableCandidates(rows, p.quarantineObserver(orgID)),
		emitted: emitted, leftForBounds: leftForBounds, truncated: p.truncatedTables, rows: sourceRows(rows), offered: true,
	}
}

// buildPageBatch builds the batch of a page. With complete tables it builds
// page and tables as one batch; if that batch is not a valid one (a subject
// or a relationship the page and a table both carry, or a table's tombstone
// against a page's edge), the page goes alone and the tables are left to the
// walk, which reaches every row of theirs in stamp order. The walk must never
// stop on what was added to speed it up.
func (p sourcePlan) buildPageBatch(ctx context.Context, orgID, cursor string, page, items []candidate, early earlyTables) (contextfabric.ProjectionBatch, error) {
	if !early.offered {
		return buildBatchIn(p.cursorSpace(), orgID, p.source, p.version, cursor, page, items, false, false, p.clock())
	}
	if len(early.items) > 0 {
		combined := append(items[:len(items):len(items)], early.items...)
		batch, err := buildBatchIn(p.cursorSpace(), orgID, p.source, p.version, cursor, page, combined, false, false, p.clock())
		if err == nil {
			p.logCompleteTables(ctx, orgID, early.emitted, early.leftForBounds, early.truncated, early.rows, "")
			return batch, nil
		}
		if !carriesPayload(items) {
			// The page alone is no batch either: the error stands.
			return contextfabric.ProjectionBatch{}, err
		}
		p.logCompleteTables(ctx, orgID, nil, append(early.emitted[:len(early.emitted):len(early.emitted)], early.leftForBounds...), early.truncated, 0, "batch_invalid_with_tables")
		return buildBatchIn(p.cursorSpace(), orgID, p.source, p.version, cursor, page, items, false, false, p.clock())
	}
	p.logCompleteTables(ctx, orgID, nil, early.leftForBounds, early.truncated, 0, "")
	return buildBatchIn(p.cursorSpace(), orgID, p.source, p.version, cursor, page, items, false, false, p.clock())
}

func (p sourcePlan) logCompleteTables(ctx context.Context, orgID string, emitted, left, truncated []string, rows int, fallback string) {
	if p.logger == nil {
		return
	}
	level := slog.LevelInfo
	if fallback != "" {
		level = slog.LevelWarn
	}
	p.logger.Log(ctx, level, completeTablesMessage,
		"source", contextfabric.SanitizeLogAttr(p.source), "org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)),
		"tables_emitted", contextfabric.SanitizeLogStrings(emitted), "rows_emitted", rows,
		"tables_left_to_the_walk", contextfabric.SanitizeLogStrings(left),
		"tables_left_truncated", contextfabric.SanitizeLogStrings(truncated),
		"fallback", contextfabric.SanitizeLogAttr(fallback))
}

// reportCatchUp is ProjectionCatchUp for both sources: where the checkpoint's
// cursor stands, the scope's open catch-up pass, and whether its last read
// stopped with rows beyond it. It reads; it changes
// nothing.
func reportCatchUp(memo *windowMemo, checkpoint contextfabric.ProjectionCheckpoint) contextfabric.ProjectionCatchUp {
	var report contextfabric.ProjectionCatchUp
	// An empty cursor and one that does not decode are both the zero state.
	if state, _ := decodeCursor(checkpoint.Cursor); !state.Since.IsZero() &&
		(state.Space == cursorSpaceIngest || state.Space == cursorSpaceIngestColumns) {
		report.CursorKnown, report.CursorAt = true, state.Since.UTC()
	}
	scope := windowScopeFor(strings.TrimSpace(checkpoint.OrgID), checkpoint.Epoch)
	if pass := memo.catchUp(scope); pass.open {
		report.PassOpen, report.PassEdge = true, pass.edge.UTC()
	}
	report.WorkAhead = memo.workAhead(scope)
	return report
}

var (
	_ contextfabric.ProjectionCatchUpReporter = (*ClickHouseProjectionSource)(nil)
	_ contextfabric.ProjectionCatchUpReporter = (*TeamsProjectsSource)(nil)
)

// ProjectionCatchUp implements contextfabric.ProjectionCatchUpReporter.
func (s *ClickHouseProjectionSource) ProjectionCatchUp(checkpoint contextfabric.ProjectionCheckpoint) contextfabric.ProjectionCatchUp {
	if s == nil {
		return contextfabric.ProjectionCatchUp{}
	}
	return reportCatchUp(s.window, checkpoint)
}

// ProjectionCatchUp implements contextfabric.ProjectionCatchUpReporter.
func (s *TeamsProjectsSource) ProjectionCatchUp(checkpoint contextfabric.ProjectionCheckpoint) contextfabric.ProjectionCatchUp {
	if s == nil {
		return contextfabric.ProjectionCatchUp{}
	}
	return reportCatchUp(s.window, checkpoint)
}
