package devhealthfacts

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

// longInvestmentWindow is the width above which an investment read states how
// much of its window the stored rows cover. Narrower windows are unchanged.
const longInvestmentWindow = 60 * 24 * time.Hour

// investmentSpanStatement reads the earliest persisted work unit start of the
// organization: the start of the history a window can be served from.
const investmentSpanStatement = `SELECT toString(min(from_ts)), count() FROM work_unit_investments WHERE org_id = {org_id:String}`

func isLongInvestmentWindow(b factTimeBound) bool {
	return b.active && b.hasStart && b.end.Sub(b.start) > longInvestmentWindow
}

// readInvestmentSpanStart returns the earliest persisted from_ts, and false
// when the organization has no work unit rows.
func (p *InvestmentProvider) readInvestmentSpanStart(ctx context.Context, orgID string) (time.Time, bool, error) {
	var earliest time.Time
	found := false
	err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadInvestmentSpanStart", withRowLimit(investmentSpanStatement), orgID, []string{}, func(row contextpacket.ClickHouseRowScanner) error {
		var minFrom string
		var rows uint64
		if err := row.Scan(&minFrom, &rows); err != nil {
			return err
		}
		if rows == 0 {
			return nil
		}
		parsed, err := time.ParseInLocation("2006-01-02 15:04:05.999999", strings.TrimSpace(minFrom), time.UTC)
		if err != nil {
			return fmt.Errorf("parse earliest work unit start %q: %w", minFrom, err)
		}
		earliest, found = parsed.UTC(), true
		return nil
	})
	return earliest, found, err
}

// investmentWindowSpanReason is the disclosure for a window that starts before
// the earliest persisted work unit: the facts are served over the available
// span and the days before it are not stored, which is not the same as zero.
func investmentWindowSpanReason(earliest time.Time, b factTimeBound) string {
	return fmt.Sprintf("investment_window_beyond_stored_history: the window starts %s but the earliest persisted work unit starts %s; the mix covers %s to %s only and the days before are not stored (not zero)",
		b.start.UTC().Format(time.RFC3339), earliest.UTC().Format(time.RFC3339), earliest.UTC().Format(time.RFC3339), b.end.UTC().Format(time.RFC3339))
}
