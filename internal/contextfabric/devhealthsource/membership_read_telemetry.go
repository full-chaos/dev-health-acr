package devhealthsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// The project_membership_presence statement reads two arms (transition,
// work_item_column) in one keyset over a cursor shared with five other tables.
// presence_rows_* counts every row a statement returned: the lookahead row and
// every re-read of a row that still lies past the shared cursor. It grows by a
// full page per call while the cursor walks another table, so it says nothing
// about how many distinct rows the read reached. The page records below do:
// per statement and arm, the rows the statement returned, whether it hit its
// limit, and the rows the page cut actually moved the cursor past.

// membershipTable is the entityTable name of the membership producer.
const membershipTable = "project_membership_presence"

var membershipArms = []string{"transition", "work_item_column"}

type membershipArmPage struct {
	statementRows int
	consumedRows  int
	lastStamp     time.Time
	lastKey       string
}

type membershipPage struct {
	n      int
	more   bool
	replay bool
	arms   map[string]*membershipArmPage
}

// membershipReadLedger is the page-level part of presenceTelemetryLedger.
type membershipReadLedger struct {
	statements    int
	pages         []membershipPage // not logged yet
	consumedTotal map[string]int
	consumedPages map[string]int
	lastConsumed  string // fingerprint of the last consumed page, to spot a replay
	summaryOwed   bool
}

func (p *membershipPage) arm(name string) *membershipArmPage {
	if p.arms == nil {
		p.arms = map[string]*membershipArmPage{}
	}
	if p.arms[name] == nil {
		p.arms[name] = &membershipArmPage{}
	}
	return p.arms[name]
}

// recordStatement notes one membership statement: rows are the rows it
// returned inside its limit, more is whether rows lay beyond the limit.
func (l *presenceTelemetryLedger) recordStatement(rows []candidate, more bool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.membership.statements++
	page := membershipPage{n: l.membership.statements, more: more}
	for _, c := range rows {
		if c.arm == "" {
			continue
		}
		arm := page.arm(c.arm)
		arm.statementRows++
		arm.lastStamp, arm.lastKey = c.position(), c.sortKey
	}
	l.membership.pages = append(l.membership.pages, page)
}

// recordConsumed notes the page the shared cursor moves past: all is the
// merged, sorted, cut page of every table. A page built again from the same
// cursor (a failed apply) is marked a replay and not added to the totals.
func (l *presenceTelemetryLedger) recordConsumed(all []candidate) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	m := &l.membership
	if len(m.pages) == 0 {
		return
	}
	page := &m.pages[len(m.pages)-1]
	consumed := map[string]int{}
	first, last := "", ""
	for _, c := range all {
		if c.table != membershipTable || c.arm == "" {
			continue
		}
		consumed[c.arm]++
		arm := page.arm(c.arm)
		arm.lastStamp, arm.lastKey = c.position(), c.sortKey
		last = c.position().UTC().Format(time.RFC3339Nano) + "|" + c.sortKey
		if first == "" {
			first = last
		}
	}
	if first == "" {
		return
	}
	fingerprint := first + "\x00" + last
	page.replay = fingerprint == m.lastConsumed
	m.lastConsumed = fingerprint
	for name, n := range consumed {
		page.arm(name).consumedRows = n
		if page.replay {
			continue
		}
		if m.consumedTotal == nil {
			m.consumedTotal, m.consumedPages = map[string]int{}, map[string]int{}
		}
		m.consumedTotal[name] += n
		m.consumedPages[name]++
		m.summaryOwed = true
	}
}

// membershipConsumedTotal is the distinct rows of one arm the cursor moved
// past in this run.
func (l *presenceTelemetryLedger) membershipConsumedTotal(arm string) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.membership.consumedTotal[arm]
}

// logMembershipPages writes one line per statement and arm that returned or
// consumed rows, and one summary line per arm when a statement finds the read
// drained after rows were consumed. No line carries a row id: the last key is
// logged as a digest.
func logMembershipPages(ctx context.Context, logger *slog.Logger, orgID string, ledger *presenceTelemetryLedger) {
	if logger == nil || ledger == nil {
		return
	}
	ledger.mu.Lock()
	m := &ledger.membership
	pages := m.pages
	m.pages = nil
	statements := m.statements
	// before is each arm's consumed total as it stood before these pages.
	before := map[string]int{}
	pagesWithRows := map[string]int{}
	for _, name := range membershipArms {
		before[name], pagesWithRows[name] = m.consumedTotal[name], m.consumedPages[name]
	}
	for _, page := range pages {
		for name, arm := range page.arms {
			if !page.replay {
				before[name] -= arm.consumedRows
			}
		}
	}
	// Drained: the last statement returned no membership row at all.
	drained := len(pages) > 0 && m.summaryOwed
	if drained {
		last := pages[len(pages)-1]
		drained = !last.more
		for _, arm := range last.arms {
			drained = drained && arm.statementRows == 0
		}
	}
	if drained {
		m.summaryOwed = false
	}
	ledger.mu.Unlock()

	org := contextfabric.SanitizeLogAttr(redactOrg(orgID))
	total := before
	for _, page := range pages {
		for _, name := range membershipArms {
			arm := page.arms[name]
			if arm == nil || (arm.statementRows == 0 && arm.consumedRows == 0) {
				continue
			}
			if !page.replay {
				total[name] += arm.consumedRows
			}
			logger.InfoContext(ctx, "devhealthsource project membership page",
				"org_id", org, "source", TeamsProjectsSourceName, "arm", name, "page_n", page.n,
				"statement_rows", arm.statementRows, "statement_more", page.more,
				"consumed_rows", arm.consumedRows, "consumed_total", total[name], "replay", page.replay,
				"last_stamp", contextfabric.SanitizeLogAttr(arm.lastStamp.UTC().Format(time.RFC3339Nano)), "last_key_digest", keyDigest(arm.lastKey))
		}
	}
	if !drained {
		return
	}
	for _, name := range membershipArms {
		logger.InfoContext(ctx, "devhealthsource project membership read drained",
			"org_id", org, "source", TeamsProjectsSourceName, "arm", name,
			"rows_total", total[name], "pages", pagesWithRows[name], "statements", statements,
			"scanned_rows_total", ledger.presenceReadCount(name, "work_item")+ledger.presenceReadCount(name, "pull_request"))
	}
}

func keyDigest(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:6])
}
