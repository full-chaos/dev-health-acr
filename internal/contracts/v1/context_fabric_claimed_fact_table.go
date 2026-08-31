package v1

import (
	"fmt"
	"strings"
)

// CHAOS-4637 (S6) / CHAOS-4627: a claimed fact's row table DECLARES what it
// is, on the wire, instead of leaving every consumer to infer it.
//
// WHY THIS EXISTS, stated as the defect it closes. ContextFabricClaimedFact
// carried Rows (CHAOS-4347) as a bag of rows with no statement of what the
// bag was. Anything drawing a chart from one had to decide FOR ITSELF which
// column was an axis and which columns were measurements, and this
// repository has three defeated attempts on record: skipping numeric
// columns turned a numeric team_id into a plotted series; an `id`/`*_id`
// NAME test let a column called `year` through; and reading the claim's own
// Field was correct for one measure but silently narrowed every
// multi-measure table to nothing. The rule that rested on those inferences
// (dated_fact_trend) was withdrawn outright in acr #340 rather than fixed a
// fourth time.
//
// The information was never missing -- it was simply never carried.
// devhealthfacts producers have declared contextfabric.FactTable since
// CHAOS-4633: a closed Shape, the COMPOSITE Key that identifies a row, and
// the Measures. This type is that declaration reaching the wire, so
// selection keys on the DECLARATION and no renaming, retyping or column
// addition by a producer can reopen the defect.
//
// It is a DECLARATION ONLY and deliberately carries no rows of its own: it
// describes the rows already in ContextFabricClaimedFact.Rows. Carrying a
// second copy would double a row table's bytes against the same
// ContextFabricResponseBudget the answer is measured by (CHAOS-4636 stage
// 3), which would make the declaration a cause of the refusals it exists to
// help avoid.
//
// ABSENT MEANS UNDECLARED, AND UNDECLARED MEANS NEVER CHARTED. That is
// CHAOS-4627's ruled default and today's behaviour both: a producer that
// declares nothing gets no server-asserted chart. It is additive and
// schema-OPTIONAL (CHAOS-4656 doctrine), so every pre-4637 consumer and
// every result already stored is unaffected.

// ContextFabricFactTableShape is the CLOSED vocabulary of what a declared
// row table IS. It mirrors contextfabric.FactTableShape member for member;
// the two are asserted equal by a parity test rather than left to drift.
type ContextFabricFactTableShape string

const (
	// ContextFabricFactTableShapeTimeSeries: one entity, indexed by an
	// instant. Key is EXACTLY one column and it parses as an instant on
	// every row -- which is CHAOS-4616's correction stated as a property
	// of the declaration rather than as a rule inside a selector. A table
	// whose identity needs a second column is a breakdown by definition,
	// not by judgement, so `scope_breakdown` can never be drawn as a
	// trend again.
	ContextFabricFactTableShapeTimeSeries ContextFabricFactTableShape = "time_series"
	// ContextFabricFactTableShapeBreakdown: many entities, one
	// observation each.
	ContextFabricFactTableShapeBreakdown ContextFabricFactTableShape = "breakdown"
	// ContextFabricFactTableShapeRanking: many entities, ordered by the
	// measure OrderBy names.
	ContextFabricFactTableShapeRanking ContextFabricFactTableShape = "ranking"
)

// ValidContextFabricFactTableShape reports closed-vocabulary membership.
func ValidContextFabricFactTableShape(shape ContextFabricFactTableShape) bool {
	switch shape {
	case ContextFabricFactTableShapeTimeSeries, ContextFabricFactTableShapeBreakdown, ContextFabricFactTableShapeRanking:
		return true
	}
	return false
}

// Declared-table bounds. Each matches the domain-side bound in
// internal/contextfabric (maxFactTableKeyColumns / maxFactTableMeasures) and
// the column bound matches the row-key bound validateScalarMap already
// enforces on a row's own fields: a declaration naming a column longer than
// a row key could name could never describe a real column, so the two
// bounds are one bound and are written as one.
const (
	ContextFabricFactTableKeyMaxCount      = 8
	ContextFabricFactTableMeasuresMaxCount = 32
	ContextFabricFactTableColumnMaxLength  = 128
)

// ContextFabricClaimedFactTable declares what ContextFabricClaimedFact.Rows
// IS. See this file's header for why it carries no rows of its own.
type ContextFabricClaimedFactTable struct {
	// Field names the canonical fact field the rows came from. A fact may
	// carry more than one row-shaped field (a legacy breakdown beside a
	// CHAOS-4645 time series, for instance), so a declaration that did not
	// say WHICH field it describes would be exactly the unidentified table
	// CHAOS-4355 had to fail closed on.
	Field string                      `json:"field"`
	Shape ContextFabricFactTableShape `json:"shape"`
	// Key is the COMPOSITE identity of a row, in declared order. The
	// composite is load-bearing rather than cosmetic: flow.go's scope rows
	// legitimately partition on (provider, work_scope_id) because two
	// providers can share one work_scope_id string, so a single-column
	// axis would force a dishonest declaration on real, correct data.
	//
	// Key names ROW columns only. Row identity is relative to the fact's
	// own Subject, which is carried by ContextFabricClaimedFact.Subject
	// and must never be duplicated into the key.
	Key []string `json:"key"`
	// Measures are the columns that MEASURE something. Every column of
	// every row belongs to exactly one of Key or Measures at the producer;
	// a Measure may legitimately be absent from an individual row (a
	// conditionally-computed mttr_hours, say), which is why only Key is
	// cross-checked against the rows here.
	Measures []string `json:"measures,omitempty"`
	// OrderBy names the Measure a ranking's row order is by. Required for
	// ranking, empty for every other shape.
	OrderBy string `json:"order_by,omitempty"`
}

// HasMeasure reports whether name is a declared measure of this table.
func (t ContextFabricClaimedFactTable) HasMeasure(name string) bool {
	for _, measure := range t.Measures {
		if measure == name {
			return true
		}
	}
	return false
}

// Validate enforces the declaration's own closed vocabulary and internal
// consistency. It deliberately does NOT re-derive the producer-side
// invariant that every row column is classified: that is
// contextfabric.FactTable.Validate's job, at the producer, in the
// producer's own test -- and a validator here that can reject a whole
// investigation is a liability, not defence in depth. What IS checked here
// is what a consumer would otherwise be unable to trust: the vocabulary,
// the arities, and (against the rows, in validateClaimedFactTable) that the
// declared key columns actually exist to be read.
func (t ContextFabricClaimedFactTable) Validate() error {
	if !stringLengthBetween(t.Field, 1, ContextFabricClaimedFieldMaxLength) || strings.TrimSpace(t.Field) != t.Field {
		return fmt.Errorf("declared table field violates v1 bounds")
	}
	if !ValidContextFabricFactTableShape(t.Shape) {
		return fmt.Errorf("declared table shape %q is not a member of the closed vocabulary", t.Shape)
	}
	if len(t.Key) == 0 || len(t.Key) > ContextFabricFactTableKeyMaxCount {
		return fmt.Errorf("declared table key must name between 1 and %d columns", ContextFabricFactTableKeyMaxCount)
	}
	if len(t.Measures) > ContextFabricFactTableMeasuresMaxCount {
		return fmt.Errorf("declared table names more than %d measures", ContextFabricFactTableMeasuresMaxCount)
	}
	seen := make(map[string]struct{}, len(t.Key)+len(t.Measures))
	for _, column := range append(append([]string{}, t.Key...), t.Measures...) {
		if !stringLengthBetween(column, 1, ContextFabricFactTableColumnMaxLength) || strings.TrimSpace(column) != column {
			return fmt.Errorf("declared table column name violates v1 bounds")
		}
		if _, exists := seen[column]; exists {
			// A column in both Key and Measures would be claiming to
			// identify a row AND to measure it, and the two readings
			// disagree about whether varying it means "a different row"
			// or "the same row changed".
			return fmt.Errorf("declared table column %q appears more than once across key and measures", column)
		}
		seen[column] = struct{}{}
	}
	// time_series arity is the whole of CHAOS-4616's correction: a second
	// key column means the rows identify more than one entity, and a line
	// across them says one entity changed over time.
	if t.Shape == ContextFabricFactTableShapeTimeSeries && len(t.Key) != 1 {
		return fmt.Errorf("a time_series table declares exactly one key column, not %d", len(t.Key))
	}
	if t.Shape == ContextFabricFactTableShapeRanking {
		if t.OrderBy == "" || !t.HasMeasure(t.OrderBy) {
			return fmt.Errorf("a ranking table's order_by must name one of its declared measures")
		}
	} else if t.OrderBy != "" {
		return fmt.Errorf("order_by is meaningful only for a ranking table, not %q", t.Shape)
	}
	return nil
}

// validateClaimedFactTable checks the declaration and then the one thing
// only the pair can answer: that every declared key column is actually
// present on every row, so an axis a consumer reads by declaration resolves
// to a cell on every row rather than to a hole. Measures are NOT required
// per row -- see ContextFabricClaimedFactTable.Measures.
//
// A declaration with no rows to describe is refused: it would be a
// statement about a table that is not there.
func validateClaimedFactTable(table *ContextFabricClaimedFactTable, rows []ContextFabricClaimedFactRow) error {
	if table == nil {
		return nil
	}
	if err := table.Validate(); err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("declared table describes rows the fact does not carry")
	}
	for index, row := range rows {
		for _, column := range table.Key {
			if _, present := row.Fields[column]; !present {
				return fmt.Errorf("declared key column %q is absent from row %d", column, index)
			}
		}
	}
	return nil
}
