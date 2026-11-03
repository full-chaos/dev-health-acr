package devhealthfacts

import (
	"context"
	"log/slog"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
)

// logSubcategoryBreakdownWithheld writes the Warn line of a subject whose
// investment fact carries no subcategory_breakdown table (CHAOS-12554): the
// closed reason, and for the cross-check the theme and its relative gap. It is
// the count an alert keys on; the same reason is served as coverage. Closed
// values and numbers only: no subject id, no key and no value from ops.
func logSubcategoryBreakdownWithheld(ctx context.Context, orgID string, withheld subcategoryWithheld) {
	fields := eventspec.NewInvestmentSubcategoryBreakdownWithheldFields(orgID, string(withheld.subject.Kind), withheld.reason, withheld.theme, withheld.gap)
	slog.WarnContext(ctx, "devhealthfacts.investment_subcategory_breakdown_withheld", append(fields.SlogArgs(), requestIDLogAttrs(ctx)...)...)
}

// logSubcategoryBreakdownNoncanonicalKey writes the Warn line of a read whose
// ops answer holds a key outside the canonical lists (lead ruling on Codex r1
// of #2002): the canonical-themes contract says it never happens, so it is
// logged even at 0, where it places nothing and the table is still served.
// Counts only: no subject id, no key and no value from ops.
func logSubcategoryBreakdownNoncanonicalKey(ctx context.Context, orgID string, subject contextfabric.SubjectRef, keys noncanonicalKeys) {
	fields := eventspec.NewInvestmentSubcategoryBreakdownNoncanonicalKeyFields(orgID, string(subject.Kind), keys.theme, keys.subcategory, keys.aboveZero)
	slog.WarnContext(ctx, "devhealthfacts.investment_subcategory_breakdown_noncanonical_key", append(fields.SlogArgs(), requestIDLogAttrs(ctx)...)...)
}
