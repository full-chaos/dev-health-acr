package devhealthschema

import "fmt"

// The view of a work_item_metrics_daily row (ops migration 141, CHAOS-8863): row_view is the last sorting-key column.
// The empty string is a legacy row, written before the column; it counts in every view. 'org' counts each item once
// in the organization; 'scope' counts each item once in each work scope.
const (
	WorkItemMetricsViewOrg   = "org"
	WorkItemMetricsViewScope = "scope"
)

// WorkItemMetricsViewPredicate renders the predicate of a read of work_item_metrics_daily that wants one view: the
// legacy rows and the rows of that view. The column is unqualified; the read names one table. It panics for a view
// that does not exist, so a read cannot silently run without one.
func WorkItemMetricsViewPredicate(view string) string {
	mustWorkItemMetricsView(view)
	return fmt.Sprintf("row_view IN ('', '%s')", view)
}

// WorkItemMetricsViewOrder renders the window-order term that puts the wanted view first among the rows of one
// partition. A reader keeps ONE newest row per partition (row_number() = 1) and has no live-row rule, so with the
// legacy row, the row of a later run's retraction (newer, all zeros), the org row and the scope row of one key in one
// partition, the newest row by computed_at could be the retraction. Preferring the wanted view first reads the
// wanted row, and a key with only legacy rows reads as it always did. In a partition that spans days put this term
// AFTER the day, so a newer legacy-only day still wins over an older view row.
func WorkItemMetricsViewOrder(view string) string {
	mustWorkItemMetricsView(view)
	return fmt.Sprintf("(row_view = '%s') DESC", view)
}

func mustWorkItemMetricsView(view string) {
	if view != WorkItemMetricsViewOrg && view != WorkItemMetricsViewScope {
		panic("devhealthschema: unknown work_item_metrics_daily view " + view)
	}
}
