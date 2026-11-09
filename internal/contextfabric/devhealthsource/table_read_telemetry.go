package devhealthsource

import (
	"context"
	"log/slog"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
)

// Every table on the teams and projects cursor but the membership producer
// (membership_read_telemetry.go) states its read the same way: per page, the
// rows its statement returned and the rows the shared cursor moved past, one
// line per table per page, and one summary per table when the read drains.
// A row the cursor moved past twice outside a replayed page is an error line.
// No line carries a row id: the last key is logged as a digest.

type tablePage struct {
	n             int
	more          bool
	statementRows int
	consumedRows  int
	consumedNew   int
	sharedRows    int
	lastStamp     time.Time
	lastKey       string
}

func (p *tablePage) replay() bool { return p.consumedRows > 0 && p.consumedNew == 0 }

type tableReadState struct {
	statements    int
	pages         []*tablePage // not logged yet
	seen          map[uint64]struct{}
	pagesWithRows int
}

// tableReadLedger lives as long as its presenceTelemetryLedger: one run.
type tableReadLedger struct {
	order       []string
	byTable     map[string]*tableReadState
	summaryOwed bool
}

func (l *tableReadLedger) state(table string) *tableReadState {
	if l.byTable == nil {
		l.byTable = map[string]*tableReadState{}
	}
	if l.byTable[table] == nil {
		l.byTable[table] = &tableReadState{seen: map[uint64]struct{}{}}
		l.order = append(l.order, table)
	}
	return l.byTable[table]
}

// recordTableStatement notes one statement of a non-membership table: rows are
// the rows it returned inside its limit, more is whether rows lay beyond it.
func (l *presenceTelemetryLedger) recordTableStatement(table string, rows []candidate, more bool) {
	if l == nil || table == membershipTable {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.tables.state(table)
	st.statements++
	page := &tablePage{n: st.statements, more: more, statementRows: len(rows)}
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		page.lastStamp, page.lastKey = last.position(), last.sortKey
	}
	st.pages = append(st.pages, page)
}

// recordTableConsumed notes the page the shared cursor moves past: all is the
// merged, sorted, cut page of every table.
func (l *presenceTelemetryLedger) recordTableConsumed(all []candidate) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	pages := map[string]*tablePage{}
	inPage := map[string]map[uint64]struct{}{}
	for _, c := range all {
		if c.table == "" || c.table == membershipTable {
			continue
		}
		st := l.tables.state(c.table)
		page := pages[c.table]
		if page == nil {
			if len(st.pages) == 0 {
				st.statements++
				st.pages = append(st.pages, &tablePage{n: st.statements})
			}
			page = st.pages[len(st.pages)-1]
			pages[c.table], inPage[c.table] = page, map[uint64]struct{}{}
		}
		id := rowHash(c)
		page.consumedRows++
		page.lastStamp, page.lastKey = c.position(), c.sortKey
		if _, ok := inPage[c.table][id]; ok {
			page.sharedRows++
		}
		inPage[c.table][id] = struct{}{}
		if _, ok := st.seen[id]; ok {
			continue
		}
		st.seen[id] = struct{}{}
		if page.consumedNew == 0 {
			st.pagesWithRows++
		}
		page.consumedNew++
		l.tables.summaryOwed = true
	}
}

// tableConsumedTotal is the distinct rows of one table the cursor moved past
// in this run.
func (l *presenceTelemetryLedger) tableConsumedTotal(table string) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if st := l.tables.byTable[table]; st != nil {
		return len(st.seen)
	}
	return 0
}

// logTableReadPages writes one line per table for every statement that
// returned or consumed a row, and one summary per table when the last
// statements find every table drained: after rows were consumed, or at the
// first empty read of a run that started from an empty cursor.
func logTableReadPages(ctx context.Context, logger *slog.Logger, orgID string, ledger *presenceTelemetryLedger) {
	if logger == nil || ledger == nil {
		return
	}
	type tableReport struct {
		name                                   string
		pages                                  []*tablePage
		total, final, pagesWithRows, statement int
	}
	ledger.mu.Lock()
	t := &ledger.tables
	run := ledger.membership.run
	var reports []tableReport
	drained := t.summaryOwed
	hasPages := false
	for _, name := range t.order {
		st := t.byTable[name]
		report := tableReport{name: name, pages: st.pages, total: len(st.seen), final: len(st.seen), pagesWithRows: st.pagesWithRows, statement: st.statements}
		for _, page := range st.pages {
			report.total -= page.consumedNew
		}
		if len(st.pages) > 0 {
			hasPages = true
			last := st.pages[len(st.pages)-1]
			drained = drained && !last.more && last.statementRows == 0
		}
		st.pages = nil
		reports = append(reports, report)
	}
	drained = drained && hasPages
	if drained {
		t.summaryOwed = false
	}
	ledger.mu.Unlock()

	for _, report := range reports {
		for _, page := range report.pages {
			report.total += page.consumedNew
			if page.statementRows+page.consumedRows == 0 {
				continue
			}
			lastStamp := ""
			if !page.lastStamp.IsZero() {
				lastStamp = page.lastStamp.UTC().Format(time.RFC3339Nano)
			}
			org := redactOrg(orgID)
			pageFields := eventspec.NewDevhealthsourceTableReadPageFields(
				org, TeamsProjectsSourceName, report.name, run, page.n,
				page.statementRows, page.more, page.consumedRows, page.consumedNew, report.total, page.replay(),
				lastStamp, keyDigest(page.lastKey))
			logger.InfoContext(ctx, eventspec.DevhealthsourceTableReadPageLogMessage, pageFields.SlogArgs()...)
			if page.consumedRows != page.consumedNew && !page.replay() {
				notNewFields := eventspec.NewDevhealthsourceTableReadNotNewFields(
					org, TeamsProjectsSourceName, report.name, run, page.n,
					page.consumedRows, page.consumedNew, page.sharedRows, lastStamp, keyDigest(page.lastKey))
				logger.ErrorContext(ctx, eventspec.DevhealthsourceTableReadNotNewLogMessage, notNewFields.SlogArgs()...)
			}
		}
	}
	if !drained {
		return
	}
	for _, report := range reports {
		drainedFields := eventspec.NewDevhealthsourceTableReadDrainedFields(
			redactOrg(orgID), TeamsProjectsSourceName, report.name, run,
			report.final, report.pagesWithRows, report.statement)
		logger.InfoContext(ctx, eventspec.DevhealthsourceTableReadDrainedLogMessage, drainedFields.SlogArgs()...)
	}
}
