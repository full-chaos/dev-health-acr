package devhealthsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash/fnv"
	"log/slog"
	"strconv"
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
	consumedRows  int // rows of this arm in the page the cursor moved past
	consumedNew   int // of those, rows this run had not consumed before
	sharedRows    int // of consumedRows, rows on a cursor position an earlier row of this page holds
	lastStamp     time.Time
	lastKey       string
}

type membershipPage struct {
	n    int
	more bool
	arms map[string]*membershipArmPage
}

// replay reports a page that consumed membership rows and no new one: the
// same page built again (a failed apply).
func (p membershipPage) replay() bool {
	consumed, fresh := 0, 0
	for _, arm := range p.arms {
		consumed, fresh = consumed+arm.consumedRows, fresh+arm.consumedNew
	}
	return consumed > 0 && fresh == 0
}

// membershipReadLedger is the page-level part of presenceTelemetryLedger. It
// lives as long as the ledger: one run, from an empty cursor (or process
// start) to the next empty cursor.
type membershipReadLedger struct {
	run        int
	statements int
	pages      []membershipPage // not logged yet
	// seen holds, per arm, a hash of every row the cursor moved past in this
	// run, so a row counts once however many pages carry it: a page built
	// again, or a retry that returns an overlapping or changed page.
	seen          map[string]map[uint64]struct{}
	consumedPages map[string]int
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
// returned inside its limit, more is whether rows lay beyond the limit. Both
// arms get a record, so an arm that returned nothing is stated, not absent.
func (l *presenceTelemetryLedger) recordStatement(rows []candidate, more bool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.membership.statements++
	page := membershipPage{n: l.membership.statements, more: more}
	for _, name := range membershipArms {
		page.arm(name)
	}
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
// merged, sorted, cut page of every table.
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
	inPage := map[string]map[uint64]struct{}{}
	for _, c := range all {
		if c.table != membershipTable || c.arm == "" {
			continue
		}
		arm := page.arm(c.arm)
		arm.consumedRows++
		arm.lastStamp, arm.lastKey = c.position(), c.sortKey
		if inPage[c.arm] == nil {
			inPage[c.arm] = map[uint64]struct{}{}
		}
		if _, ok := inPage[c.arm][rowHash(c)]; ok {
			arm.sharedRows++
		}
		inPage[c.arm][rowHash(c)] = struct{}{}
		if m.seen == nil {
			m.seen, m.consumedPages = map[string]map[uint64]struct{}{}, map[string]int{}
		}
		if m.seen[c.arm] == nil {
			m.seen[c.arm] = map[uint64]struct{}{}
		}
		id := rowHash(c)
		if _, ok := m.seen[c.arm][id]; ok {
			continue
		}
		m.seen[c.arm][id] = struct{}{}
		if arm.consumedNew == 0 {
			m.consumedPages[c.arm]++
		}
		arm.consumedNew++
		m.summaryOwed = true
	}
}

func rowHash(c candidate) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strconv.FormatInt(c.position().UnixNano(), 10)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(c.sortKey))
	return h.Sum64()
}

// membershipConsumedTotal is the distinct rows of one arm the cursor moved
// past in this run.
func (l *presenceTelemetryLedger) membershipConsumedTotal(arm string) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.membership.seen[arm])
}

// logMembershipPages writes one line per arm for every statement that returned
// or consumed a membership row, and one summary line per arm when a statement
// finds the read drained: after rows were consumed, or at the first empty
// statement of a run that started from an empty cursor. statement_more is a
// fact about the whole statement: while it is true, an arm with no row may
// still hold rows beyond the limit; when it is false, that arm has no row past
// the cursor. No line carries a row id: the last key is logged as a digest.
func logMembershipPages(ctx context.Context, logger *slog.Logger, orgID string, ledger *presenceTelemetryLedger) {
	if logger == nil || ledger == nil {
		return
	}
	ledger.mu.Lock()
	m := &ledger.membership
	pages := m.pages
	m.pages = nil
	run, statements := m.run, m.statements
	// total starts at each arm's distinct total as it stood before these pages.
	total, pagesWithRows := map[string]int{}, map[string]int{}
	for _, name := range membershipArms {
		total[name], pagesWithRows[name] = len(m.seen[name]), m.consumedPages[name]
	}
	for _, page := range pages {
		for name, arm := range page.arms {
			total[name] -= arm.consumedNew
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

	for _, page := range pages {
		rows := 0
		for _, arm := range page.arms {
			rows += arm.statementRows + arm.consumedRows
		}
		if rows == 0 {
			continue
		}
		for _, name := range membershipArms {
			arm := page.arms[name]
			total[name] += arm.consumedNew
			lastStamp := ""
			if !arm.lastStamp.IsZero() {
				lastStamp = arm.lastStamp.UTC().Format(time.RFC3339Nano)
			}
			logger.InfoContext(ctx, "devhealthsource project membership page",
				"org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)), "source", TeamsProjectsSourceName, "arm", contextfabric.SanitizeLogAttr(name), "run", run, "page_n", page.n,
				"statement_rows", arm.statementRows, "statement_more", page.more,
				"consumed_rows", arm.consumedRows, "consumed_new", arm.consumedNew, "consumed_total", total[name], "replay", page.replay(),
				"last_stamp", contextfabric.SanitizeLogAttr(lastStamp), "last_key_digest", contextfabric.SanitizeLogAttr(keyDigest(arm.lastKey)))
			// A first read moves the cursor past each row once. Rows that are
			// not new on a page that is not a replay share one cursor position
			// (shared_position_rows), or the page changed between two attempts.
			if arm.consumedRows != arm.consumedNew && !page.replay() {
				logger.ErrorContext(ctx, "devhealthsource project membership page consumed rows that are not new and is not a replay",
					"org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)), "source", contextfabric.SanitizeLogAttr(TeamsProjectsSourceName), "arm", contextfabric.SanitizeLogAttr(name), "run", run, "page_n", page.n,
					"consumed_rows", arm.consumedRows, "consumed_new", arm.consumedNew, "shared_position_rows", arm.sharedRows,
					"last_stamp", contextfabric.SanitizeLogAttr(lastStamp), "last_key_digest", contextfabric.SanitizeLogAttr(keyDigest(arm.lastKey)))
			}
		}
	}
	if !drained {
		return
	}
	for _, name := range membershipArms {
		logger.InfoContext(ctx, "devhealthsource project membership read drained",
			"org_id", contextfabric.SanitizeLogAttr(redactOrg(orgID)), "source", TeamsProjectsSourceName, "arm", contextfabric.SanitizeLogAttr(name), "run", run,
			"rows_total", total[name], "pages", pagesWithRows[name], "statements", statements,
			"scanned_rows_total", ledger.presenceReadCount(name, "work_item")+ledger.presenceReadCount(name, "pull_request"))
	}
}

func keyDigest(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:6])
}
