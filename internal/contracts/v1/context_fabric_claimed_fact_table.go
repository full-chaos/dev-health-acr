package v1

import (
	"fmt"
	"strings"
	"time"
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
// CHAOS-4633: a closed Shape, the COMPOSITE Key that identifies a row, the
// Measures, and (CHAOS-4680) the per-row categorical Observations. This
// type is that declaration reaching the wire, so selection keys on the
// DECLARATION and no renaming, retyping or column addition by a producer
// can reopen the defect.
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
	ContextFabricFactTableKeyMaxCount          = 8
	ContextFabricFactTableMeasuresMaxCount     = 32
	ContextFabricFactTableObservationsMaxCount = 32
	ContextFabricFactTableColumnMaxLength      = 128
)

// ContextFabricMeasureUnit is the CLOSED vocabulary of the unit a declared
// measure is counted in (CHAOS-11984). The producer that owns a column
// declares its unit; the server copies that declaration to the wire, and the
// model never writes it. A measure whose declared unit is not a member of this
// vocabulary carries no unit on the wire: absent means undeclared, and a
// consumer shows a unit only where the wire states one.
type ContextFabricMeasureUnit string

const (
	// ContextFabricMeasureUnitCount is a number of things: commits, pull
	// requests, work items.
	ContextFabricMeasureUnitCount ContextFabricMeasureUnit = "count"
	// ContextFabricMeasureUnitRatio is a fraction of a whole: one part over
	// its whole.
	ContextFabricMeasureUnitRatio ContextFabricMeasureUnit = "ratio"
	// ContextFabricMeasureUnitHours is a duration in hours.
	ContextFabricMeasureUnitHours ContextFabricMeasureUnit = "hours"
	// ContextFabricMeasureUnitDays is a duration in days.
	ContextFabricMeasureUnitDays ContextFabricMeasureUnit = "days"
	// ContextFabricMeasureUnitPoints is an estimate in story points.
	ContextFabricMeasureUnitPoints ContextFabricMeasureUnit = "points"
)

var contextFabricMeasureUnits = [...]ContextFabricMeasureUnit{
	ContextFabricMeasureUnitCount,
	ContextFabricMeasureUnitRatio,
	ContextFabricMeasureUnitHours,
	ContextFabricMeasureUnitDays,
	ContextFabricMeasureUnitPoints,
}

// ContextFabricMeasureUnitVocabulary returns the closed unit vocabulary in
// published order.
func ContextFabricMeasureUnitVocabulary() [len(contextFabricMeasureUnits)]ContextFabricMeasureUnit {
	return contextFabricMeasureUnits
}

// ValidContextFabricMeasureUnit reports closed-vocabulary membership.
func ValidContextFabricMeasureUnit(unit ContextFabricMeasureUnit) bool {
	for _, member := range contextFabricMeasureUnits {
		if unit == member {
			return true
		}
	}
	return false
}

// ContextFabricFactTableCadence is the CLOSED vocabulary of the expected
// spacing of a time_series table's rows (CHAOS-11985). The producer that owns
// a table declares it; the server states it only when the served rows keep
// it, and the model never writes it. Absent means no cadence is stated, and a
// consumer then joins consecutive served points.
type ContextFabricFactTableCadence string

const (
	// ContextFabricFactTableCadenceDay is one row per UTC calendar day: every
	// key value is a date written YYYY-MM-DD, and no day repeats. A day with
	// no row between two served rows has no stored row; it is not a zero.
	ContextFabricFactTableCadenceDay ContextFabricFactTableCadence = "day"
)

var contextFabricFactTableCadences = [...]ContextFabricFactTableCadence{
	ContextFabricFactTableCadenceDay,
}

// ContextFabricFactTableCadenceVocabulary returns the closed cadence
// vocabulary in published order.
func ContextFabricFactTableCadenceVocabulary() [len(contextFabricFactTableCadences)]ContextFabricFactTableCadence {
	return contextFabricFactTableCadences
}

// ValidContextFabricFactTableCadence reports closed-vocabulary membership.
func ValidContextFabricFactTableCadence(cadence ContextFabricFactTableCadence) bool {
	for _, member := range contextFabricFactTableCadences {
		if cadence == member {
			return true
		}
	}
	return false
}

// contextFabricCadenceDayLayout is the only form a `day` key value takes.
const contextFabricCadenceDayLayout = "2006-01-02"

// ContextFabricMeasureUnitDeclaration states the unit of one declared
// measure.
type ContextFabricMeasureUnitDeclaration struct {
	Measure string                   `json:"measure"`
	Unit    ContextFabricMeasureUnit `json:"unit"`
}

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
	// Observations are columns that vary row to row, are not part of the
	// row's identity, and are not a quantity -- a per-day severity label,
	// say (CHAOS-4680). Every column of every row belongs to exactly one of
	// Key, Measures or Observations at the producer; like Measures, an
	// Observation may legitimately be absent from an individual row.
	Observations []string `json:"observations,omitempty"`
	// OrderBy names the Measure a ranking's row order is by. Required for
	// ranking, empty for every other shape.
	OrderBy string `json:"order_by,omitempty"`
	// MeasureUnits (CHAOS-11984) states the unit of each measure whose
	// producer declares one from the closed ContextFabricMeasureUnit
	// vocabulary, in Measures order. Each names a declared measure once. A
	// measure with no entry has no stated unit, and a consumer adds none.
	//
	// jsonschema:"-" keeps it out of the model output schema, which is
	// inferred from this type (genkitruntime inferSchemaMap): the server
	// states it from the declarations (withDeclaredMeasureUnits) and never
	// offers it to the model to write, as with ClaimedFact.InvestmentMix.
	MeasureUnits []ContextFabricMeasureUnitDeclaration `json:"measure_units,omitempty" jsonschema:"-"`
	// Cadence (CHAOS-11985) states the expected spacing of the rows of a
	// time_series table. It is set only on a time_series table whose rows keep
	// it (CadenceAligned). jsonschema:"-" keeps it out of the model output
	// schema, as for MeasureUnits: the server states it from the declarations
	// (withDeclaredCadence) and never offers it to the model to write.
	Cadence ContextFabricFactTableCadence `json:"cadence,omitempty" jsonschema:"-"`
	// ColumnVocabularies (CHAOS-12639) names, per key or observation column
	// whose values are tokens of a closed vocabulary, that vocabulary, so a
	// consumer reads each cell's served word from vocabulary_labels under
	// `<vocabulary>:<value>` and never keeps a word table of its own
	// (CHAOS-4690). jsonschema:"-": stated by the server from the producer's
	// declaration (withDeclaredColumnWords), never model-authored.
	ColumnVocabularies []ContextFabricColumnVocabularyDeclaration `json:"column_vocabularies,omitempty" jsonschema:"-"`
	// WordColumns (CHAOS-12639) names, per id column, the observation column
	// of the same row that carries its served word (a repository's handle
	// beside its canonical id), so a consumer shows the word and keeps the id
	// out of sight. jsonschema:"-", as for ColumnVocabularies.
	WordColumns []ContextFabricWordColumnDeclaration `json:"word_columns,omitempty" jsonschema:"-"`
}

// ContextFabricColumnVocabularyDeclaration states the closed vocabulary of one
// column's values (CHAOS-12639).
type ContextFabricColumnVocabularyDeclaration struct {
	Column     string                  `json:"column"`
	Vocabulary ContextFabricVocabulary `json:"vocabulary"`
}

// ContextFabricWordColumnDeclaration states the column that carries the served
// word of one id column (CHAOS-12639).
type ContextFabricWordColumnDeclaration struct {
	Column     string `json:"column"`
	WordColumn string `json:"word_column"`
}

// contextFabricColumnVocabularies is the CLOSED set of vocabularies a column's
// values may be declared in: the ones whose values are cell tokens of a claimed
// table, each served in vocabulary_labels. Any other vocabulary names a field,
// a status or a unit, never a cell.
var contextFabricColumnVocabularies = [...]ContextFabricVocabulary{
	ContextFabricVocabularyInvestmentTheme,
	ContextFabricVocabularySubcategory,
}

// ValidContextFabricColumnVocabulary reports whether vocabulary may be declared
// for a column's values.
func ValidContextFabricColumnVocabulary(vocabulary ContextFabricVocabulary) bool {
	for _, member := range contextFabricColumnVocabularies {
		if vocabulary == member {
			return true
		}
	}
	return false
}

// ContextFabricColumnVocabularyNames returns the vocabularies a column may be
// declared in, in published order.
func ContextFabricColumnVocabularyNames() [len(contextFabricColumnVocabularies)]ContextFabricVocabulary {
	return contextFabricColumnVocabularies
}

// HasKey reports whether name is a declared key column of this table.
func (t ContextFabricClaimedFactTable) HasKey(name string) bool {
	for _, key := range t.Key {
		if key == name {
			return true
		}
	}
	return false
}

// ColumnVocabulary returns the declared vocabulary of one column's values, or
// false when the table declares none.
func (t ContextFabricClaimedFactTable) ColumnVocabulary(column string) (ContextFabricVocabulary, bool) {
	for _, declaration := range t.ColumnVocabularies {
		if declaration.Column == column {
			return declaration.Vocabulary, true
		}
	}
	return "", false
}

// WordColumn returns the column that carries the served word of an id column,
// or false when the table declares none.
func (t ContextFabricClaimedFactTable) WordColumn(column string) (string, bool) {
	for _, declaration := range t.WordColumns {
		if declaration.Column == column {
			return declaration.WordColumn, true
		}
	}
	return "", false
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

// MeasureUnit returns the stated unit of one declared measure, or false when
// the table states none.
func (t ContextFabricClaimedFactTable) MeasureUnit(measure string) (ContextFabricMeasureUnit, bool) {
	for _, declaration := range t.MeasureUnits {
		if declaration.Measure == measure {
			return declaration.Unit, true
		}
	}
	return "", false
}

// HasObservation reports whether name is a declared observation of this
// table.
func (t ContextFabricClaimedFactTable) HasObservation(name string) bool {
	for _, observation := range t.Observations {
		if observation == name {
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
	if len(t.Observations) > ContextFabricFactTableObservationsMaxCount {
		return fmt.Errorf("declared table names more than %d observations", ContextFabricFactTableObservationsMaxCount)
	}
	seen := make(map[string]struct{}, len(t.Key)+len(t.Measures)+len(t.Observations))
	for _, column := range append(append(append([]string{}, t.Key...), t.Measures...), t.Observations...) {
		if !stringLengthBetween(column, 1, ContextFabricFactTableColumnMaxLength) || strings.TrimSpace(column) != column {
			return fmt.Errorf("declared table column name violates v1 bounds")
		}
		if _, exists := seen[column]; exists {
			// A column in more than one of Key, Measures and Observations
			// would be claiming more than one role at once, and the
			// readings disagree about what varying it means.
			return fmt.Errorf("declared table column %q appears more than once across key, measures, and observations", column)
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
	if t.Cadence != "" {
		if !ValidContextFabricFactTableCadence(t.Cadence) {
			return fmt.Errorf("declared table cadence %q is not a member of the closed vocabulary", t.Cadence)
		}
		if t.Shape != ContextFabricFactTableShapeTimeSeries {
			return fmt.Errorf("a cadence is meaningful only for a time_series table, not %q", t.Shape)
		}
	}
	if err := t.validateMeasureUnits(); err != nil {
		return err
	}
	return t.validateColumnWords()
}

// validateColumnWords holds the word declarations to the table's own columns
// (CHAOS-12639). A vocabulary is declared only for a key or observation column,
// once, from the closed column set: a measure holds numbers, never tokens. A
// word column is an observation of the same table, beside a key or observation
// column that is not itself a word column, once each. Each refusal would point
// a consumer at a word the producer never declared.
//
// No count bound of its own is needed: each entry names a different declared
// column, so there are never more entries than columns.
func (t ContextFabricClaimedFactTable) validateColumnWords() error {
	tokenColumn := func(name string) bool { return t.HasKey(name) || t.HasObservation(name) }
	stated := make(map[string]struct{}, len(t.ColumnVocabularies))
	for _, declaration := range t.ColumnVocabularies {
		if !tokenColumn(declaration.Column) {
			return fmt.Errorf("declared table states a vocabulary for %q, which is not one of its key or observation columns", declaration.Column)
		}
		if _, twice := stated[declaration.Column]; twice {
			return fmt.Errorf("declared table states the vocabulary of column %q more than once", declaration.Column)
		}
		stated[declaration.Column] = struct{}{}
		if !ValidContextFabricColumnVocabulary(declaration.Vocabulary) {
			return fmt.Errorf("declared table vocabulary %q of column %q is not one a column may be declared in", declaration.Vocabulary, declaration.Column)
		}
	}
	words := make(map[string]struct{}, len(t.WordColumns))
	for _, declaration := range t.WordColumns {
		if !tokenColumn(declaration.Column) {
			return fmt.Errorf("declared table states a word column for %q, which is not one of its key or observation columns", declaration.Column)
		}
		if !t.HasObservation(declaration.WordColumn) || declaration.WordColumn == declaration.Column {
			return fmt.Errorf("declared table word column %q of column %q is not another of its observations", declaration.WordColumn, declaration.Column)
		}
		if _, twice := words[declaration.Column]; twice {
			return fmt.Errorf("declared table states the word column of %q more than once", declaration.Column)
		}
		words[declaration.Column] = struct{}{}
	}
	for _, declaration := range t.WordColumns {
		if _, isID := words[declaration.WordColumn]; isID {
			return fmt.Errorf("declared table word column %q is itself an id column with a word column", declaration.WordColumn)
		}
	}
	return nil
}

// CadenceAligned reports whether rows keep the table's cadence. For `day`,
// every row's key value is a calendar date written YYYY-MM-DD, and no day
// repeats. A table with no cadence holds any rows. The server states a cadence
// only when this passes, and the validator holds every served table to it.
func (t ContextFabricClaimedFactTable) CadenceAligned(rows []ContextFabricClaimedFactRow) error {
	if t.Cadence == "" {
		return nil
	}
	if t.Cadence != ContextFabricFactTableCadenceDay || len(t.Key) == 0 {
		return fmt.Errorf("declared table cadence %q has no alignment rule for this table", t.Cadence)
	}
	seen := make(map[string]struct{}, len(rows))
	for index, row := range rows {
		cell := row.Fields[t.Key[0]]
		if cell.String == nil {
			return fmt.Errorf("row %d key %q is not a calendar date, so the table does not keep the %q cadence", index, t.Key[0], t.Cadence)
		}
		day := *cell.String
		// The layout is strict: a value with a time, a zone, a missing zero or
		// a day that does not exist fails to parse.
		if _, err := time.Parse(contextFabricCadenceDayLayout, day); err != nil {
			return fmt.Errorf("row %d key %q value %q is not a calendar date written YYYY-MM-DD, so the table does not keep the %q cadence", index, t.Key[0], day, t.Cadence)
		}
		if _, twice := seen[day]; twice {
			return fmt.Errorf("row %d repeats day %s, so the table does not keep the %q cadence", index, day, t.Cadence)
		}
		seen[day] = struct{}{}
	}
	return nil
}

// validateMeasureUnits holds each stated unit to a declared measure and to the
// closed vocabulary. A unit on a key or observation column, a unit stated
// twice for one measure, or a token outside the vocabulary is refused: each
// would tell a consumer a unit the producer never declared.
//
// No count bound of its own is needed: each entry names a different declared
// measure, so there are never more entries than measures.
func (t ContextFabricClaimedFactTable) validateMeasureUnits() error {
	stated := make(map[string]struct{}, len(t.MeasureUnits))
	for _, declaration := range t.MeasureUnits {
		if !t.HasMeasure(declaration.Measure) {
			return fmt.Errorf("declared table states a unit for %q, which is not one of its measures", declaration.Measure)
		}
		if _, twice := stated[declaration.Measure]; twice {
			return fmt.Errorf("declared table states the unit of measure %q more than once", declaration.Measure)
		}
		stated[declaration.Measure] = struct{}{}
		if !ValidContextFabricMeasureUnit(declaration.Unit) {
			return fmt.Errorf("declared table unit %q of measure %q is not a member of the closed vocabulary", declaration.Unit, declaration.Measure)
		}
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
	return table.CadenceAligned(rows)
}

// renderableRows (CHAOS-4682, §5.1 P2) picks the SAME row array a
// ContextFabricRenderSourceClaimedFactRow point could have come from --
// mirroring internal/contextfabric's datedFactTrendShape, the only current
// producer of that source kind: it prefers the additive TimeSeriesRows
// whenever present (the only array datedFactTrendShape ever addresses on a
// dual-table fact), falling back to Rows otherwise. Resolving a RowIndex
// with this same preference is not a guess -- it is the one deterministic
// rule every ClaimedFactRow-sourced point in the system already follows;
// see this file's own note on ContextFabricClaimedFact.Table for why P2
// never changes what Rows itself means.
func (c ContextFabricClaimedFact) renderableRows() []ContextFabricClaimedFactRow {
	if len(c.TimeSeriesRows) > 0 {
		return c.TimeSeriesRows
	}
	return c.Rows
}

// renderableRows mirrors ContextFabricClaimedFact.renderableRows for the
// answer-projection surface -- see that method's doc comment.
func (f ContextFabricProjectedFact) renderableRows() []ContextFabricClaimedFactRow {
	if len(f.TimeSeriesRows) > 0 {
		return f.TimeSeriesRows
	}
	return f.Rows
}

// validateTimeSeriesTable is CHAOS-4682's (§5.1 P2) validator for the
// ADDITIVE TimeSeriesTable/TimeSeriesRows pair: everything
// validateClaimedFactTable already checks, PLUS the one invariant that pair
// exists to guarantee and Table/Rows does not -- when present, its Shape is
// ALWAYS time_series. A declaration of any other shape in this field would
// contradict the field's own name and defeat the trend rule's "read
// TimeSeriesTable first" preference (render_shapes.go's datedFactTrendShape)
// silently, by describing a table that TimeSeriesTable was never meant to
// hold.
func validateTimeSeriesTable(table *ContextFabricClaimedFactTable, rows []ContextFabricClaimedFactRow) error {
	if table == nil {
		return nil
	}
	if table.Shape != ContextFabricFactTableShapeTimeSeries {
		return fmt.Errorf("time_series_table declares shape %q, want %q", table.Shape, ContextFabricFactTableShapeTimeSeries)
	}
	return validateClaimedFactTable(table, rows)
}
