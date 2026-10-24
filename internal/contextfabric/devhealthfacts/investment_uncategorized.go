package devhealthfacts

import (
	"context"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// Gated units carry a fixed uniform prior, not a measured split (ops analytics.CategorizedOnlySQL).
const evidenceGatedStatuses = "('insufficient_evidence', 'no_text_sources')"

// latestUnitGatedSQL is judged on the unit's latest row, inside a GROUP BY work_unit_id fold.
const latestUnitGatedSQL = "argMax(categorization_status, computed_at) IN " + evidenceGatedStatuses

const investmentUnitsUncategorized = "investment_units_uncategorized"

func investmentUncategorizedFields(fields map[string]contextfabric.FactValue, categorized, gated int64) {
	fields["uncategorized_unit_count"] = contextfabric.IntegerFactValue(gated)
	if all := categorized + gated; all > 0 {
		fields["uncategorized_unit_share"] = contextfabric.NumberFactValue(float64(gated) / float64(all))
	}
}

func investmentUncategorizedReason(categorized, gated int64) string {
	if gated <= 0 {
		return ""
	}
	reason := fmt.Sprintf("%s: %d of %d work units in the window had too little text to categorize (insufficient_evidence or no_text_sources) and are left out of the theme mix and of work_unit_count",
		investmentUnitsUncategorized, gated, categorized+gated)
	if categorized == 0 {
		return reason + "; uncategorized_unit_share is 1 and no theme mix is served (an empty mix is not zero investment)"
	}
	return reason
}

type uncategorizedCoverage struct{ categorized, gated int64 }

// replace is set by the mix a subject is served from: a project's native mix replaces its roll-up.
func recordInvestmentUncategorized(ctx context.Context, subject contextfabric.SubjectRef, categorized, gated int64, replace bool) {
	span := investmentSpanFrom(ctx)
	if span == nil {
		return
	}
	span.mu.Lock()
	defer span.mu.Unlock()
	key := contextfabric.FactSubjectKey(subject)
	if _, ok := span.uncategorized[key]; ok && !replace {
		return
	}
	span.uncategorized[key] = uncategorizedCoverage{categorized: categorized, gated: gated}
}

func (s *investmentSpan) uncategorizedReasonFor(subject contextfabric.SubjectRef) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.uncategorized[contextfabric.FactSubjectKey(subject)]
	return investmentUncategorizedReason(c.categorized, c.gated)
}
