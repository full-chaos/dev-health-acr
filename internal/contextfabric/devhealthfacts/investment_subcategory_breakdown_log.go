package devhealthfacts

import (
	"context"
	"log/slog"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
)

// logSubcategoryBreakdownWithheld writes the Warn line of a subject whose
// investment fact carries no subcategory_breakdown table (CHAOS-12554): the
// closed reason, and for the cross-check the theme and its relative gap. It is
// the count an alert keys on; the same reason is served as coverage. Closed
// values and numbers only: no subject id, no key and no value from ops.
func logSubcategoryBreakdownWithheld(ctx context.Context, orgID string, withheld subcategoryWithheld) {
	fields := eventspec.NewInvestmentSubcategoryBreakdownWithheldFields(orgID, string(withheld.subject.Kind), withheld.reason, withheld.theme, withheld.gap)
	slog.WarnContext(ctx, eventspec.InvestmentSubcategoryBreakdownWithheld.Msg, append(fields.SlogArgs(), requestIDLogAttrs(ctx)...)...)
}
