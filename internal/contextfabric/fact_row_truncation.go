package contextfabric

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-6558: the BYTE-AXIS lever -- claimed-fact row truncation, disclosed.
//
// THE DEFECT (prod, 2026-09-24, api req_745310015e271972029c854d74886738).
// "Which teams need attention over the last 30 days?" assembled 86,666 bytes
// against a 65,536-byte ceiling while carrying 17 of 30 items. Stage 3's only
// lever was the cohort retry: it halved 2 members to 1, retention kept 26 of
// 26 facts, the second synthesis came back at 122,402 bytes, and the answer
// was refused with a 413 after two syntheses and ~80 s. Nothing in stage 3
// could act on what actually overran: claimed-fact ROW TABLES, which are not
// charged items, so the item-axis candidate reduction declined
// (`not_items_axis`) by design.
//
// THE RULING (chris, 2026-09-24, option b; max_serialized_bytes NOT raised):
//
//  1. On a byte overrun, truncate fact rows FIRST -- before any cohort cut --
//     keeping each table's highest-ranked rows, and serve the answer as
//     PARTIAL with the cut disclosed: original vs served row counts and the
//     axis, on the completeness outcome set and in a service-authored
//     limitation. Never `complete`.
//  2. Never run a retry that cannot reduce the overrunning axis. When the
//     rows are what overran and the cohort retry would keep every fact that
//     carries them, the retry is declined and the refusal is immediate, with
//     advice that can actually reduce rows (a shorter evidence window).
//
// WHICH ROWS SURVIVE (team-lead ruling, 2026-09-24): a dated time series is
// cut from its OLDEST end (trends over absolutes -- recency matters), a
// ranking from its lowest rank, and only a table with neither declaration
// falls back to the source-order prefix the 64-row contract cap uses. See
// keepFactTableRows. One cap K applies to every table, so no subject loses its
// evidence to make room for another's.
//
// ONE ROW KEPT PER TABLE, AT LEAST. A declared table with no rows fails
// validation ("declared table describes rows the fact does not carry"), and a
// claim stripped of its whole table is a different claim. So the floor is one
// row per table; when even that does not fit, the lever reports `insufficient`
// and the caller decides between a retry and a refusal.

// FactRowTruncationDeclined is the CLOSED vocabulary of why the byte-axis
// lever did not serve an answer.
type FactRowTruncationDeclined string

const (
	// FactRowTruncationNotApplicable is the zero value: the lever served
	// the answer, or was never reached.
	FactRowTruncationNotApplicable FactRowTruncationDeclined = ""
	// FactRowTruncationNotBytesAxis: the answer overran on ITEMS. Rows are
	// not charged items, so cutting them cannot help.
	FactRowTruncationNotBytesAxis FactRowTruncationDeclined = "not_bytes_axis"
	// FactRowTruncationNothingTruncatable: no table carries more than one
	// row, so there is nothing above the one-row floor to cut.
	FactRowTruncationNothingTruncatable FactRowTruncationDeclined = "nothing_truncatable"
	// FactRowTruncationInsufficient: the lever ran down to one row per table
	// and the served document still did not fit.
	FactRowTruncationInsufficient FactRowTruncationDeclined = "insufficient"
	// FactRowTruncationUnmeasurable: a truncated document could not be
	// marshaled. A server defect, never counted as an oversized answer.
	FactRowTruncationUnmeasurable FactRowTruncationDeclined = "unmeasurable"
	// FactRowTruncationInvalidResult: the truncated document fit but failed
	// contract validation (e.g. no room left for the disclosure outcome row).
	FactRowTruncationInvalidResult FactRowTruncationDeclined = "invalid_result"
)

// factRowTruncationAttempt is one run of the lever.
type factRowTruncationAttempt struct {
	// Result is the served document, valid only when Served.
	Result InvestigationResult
	Served bool
	// Measured is the served document's measurement when Served, and the
	// one-row-per-table document's (the smallest the lever can make) when
	// the lever ran and was insufficient; the input's otherwise.
	Measured MeasuredAttempt
	Declined FactRowTruncationDeclined
	// RowsDeclared / RowsServed count table rows (Rows + TimeSeriesRows)
	// across every claimed fact, before and after the cut.
	RowsDeclared int
	RowsServed   int
	// PerTable is the cap applied; zero when nothing was cut.
	PerTable int
	// Tables counts the row tables the cap shortened, by the rule that cut
	// each one.
	Tables factRowCutRules
	// RowBytes is the marshaled size of every row table in the input, and
	// RowsDominate reports RowBytes >= the overrun excess: removing rows
	// alone would have been enough to fit. It is what decides whether a
	// cohort retry, which keeps every retained fact's rows, can reduce the
	// overrunning axis at all.
	RowBytes     int64
	RowsDominate bool
}

// claimedFactTableRowCounts returns the total table rows across every claimed
// fact, and the longest single table.
func claimedFactTableRowCounts(claims []ClaimedFact) (total, longest int) {
	for _, claim := range claims {
		for _, rows := range [][]contractsv1.ContextFabricClaimedFactRow{claim.Rows, claim.TimeSeriesRows} {
			total += len(rows)
			if len(rows) > longest {
				longest = len(rows)
			}
		}
	}
	return total, longest
}

// claimedFactRowBytes is the marshaled size of every row table, with the
// encoder the route serves with.
func claimedFactRowBytes(claims []ClaimedFact) (int64, error) {
	var total int64
	for _, claim := range claims {
		for _, rows := range [][]contractsv1.ContextFabricClaimedFactRow{claim.Rows, claim.TimeSeriesRows} {
			if len(rows) == 0 {
				continue
			}
			encoded, err := json.Marshal(rows)
			if err != nil {
				return 0, err
			}
			total += int64(len(encoded))
		}
	}
	return total, nil
}

// truncateClaimedFactRows returns a COPY of claims with every row table cut
// to at most perTable rows. The input slice and its row slices are never
// written: the caller's result stays the document it measured.
//
// WHICH ROWS SURVIVE (team-lead ruling, 2026-09-24: trends over absolutes,
// recency matters) is decided per table by keepFactTableRows.
func truncateClaimedFactRows(claims []ClaimedFact, perTable int) (truncated []ClaimedFact, served int, rules factRowCutRules) {
	truncated = copySlicePreservingEmpty(claims)
	for index := range truncated {
		if len(truncated[index].Rows) > perTable {
			var rule factRowCutRule
			truncated[index].Rows, rule = keepFactTableRows(truncated[index].Rows, truncated[index].Table, perTable)
			rules.add(rule)
		}
		if len(truncated[index].TimeSeriesRows) > perTable {
			var rule factRowCutRule
			truncated[index].TimeSeriesRows, rule = keepFactTableRows(truncated[index].TimeSeriesRows, truncated[index].TimeSeriesTable, perTable)
			rules.add(rule)
		}
		served += len(truncated[index].Rows) + len(truncated[index].TimeSeriesRows)
	}
	return truncated, served, rules
}

// factRowCutRule is WHICH rule cut one table (codex r2, P1: the served rows
// depend on it, so a regression from newest-days back to the source-order
// prefix must be visible at Info, not only in the served dates).
type factRowCutRule int

const (
	factRowCutNewestDays factRowCutRule = iota
	factRowCutHighestRank
	// factRowCutSourcePrefix: the table declared neither a series nor a
	// ranking (undeclared or a breakdown), so the prefix is its rule.
	factRowCutSourcePrefix
	// factRowCutSeriesUndated / factRowCutRankingUnscored: the table
	// DECLARED a series or a ranking, and its rows could not be scored, so
	// it fell back to the prefix. Counted apart from factRowCutSourcePrefix
	// because a fallback is the regression signal and a breakdown is not.
	factRowCutSeriesUndated
	factRowCutRankingUnscored
)

// factRowCutRules counts the truncated tables by the rule that cut them.
// Total is every truncated table; the five rule counts sum to it.
type factRowCutRules struct {
	Total           int
	NewestDays      int
	HighestRank     int
	SourcePrefix    int
	SeriesUndated   int
	RankingUnscored int
}

func (r *factRowCutRules) add(rule factRowCutRule) {
	r.Total++
	switch rule {
	case factRowCutNewestDays:
		r.NewestDays++
	case factRowCutHighestRank:
		r.HighestRank++
	case factRowCutSeriesUndated:
		r.SeriesUndated++
	case factRowCutRankingUnscored:
		r.RankingUnscored++
	default:
		r.SourcePrefix++
	}
}

// keepFactTableRows returns a new slice of the perTable rows a table keeps,
// in the table's own row order:
//
//   - a dated time series (declared time_series, every row's single key a
//     parseable instant) keeps its MOST RECENT rows -- cutting from the
//     oldest end, whichever way the producer listed them;
//   - a ranking keeps its HIGHEST order_by values (the declared ranking
//     direction is descending, highest first);
//   - anything else -- undeclared, a breakdown, or a declaration its own rows
//     do not satisfy -- keeps its first rows in the order the source listed
//     them, the prefix rule the 64-row contract cap already applies.
//
// The disclosure sentence states this whole policy, so it is true of every
// table whichever rule applied.
func keepFactTableRows(rows []contractsv1.ContextFabricClaimedFactRow, table *contractsv1.ContextFabricClaimedFactTable, perTable int) ([]contractsv1.ContextFabricClaimedFactRow, factRowCutRule) {
	rule := factRowCutRuleFor(table)
	if perTable >= len(rows) {
		return copySlicePreservingEmpty(rows), rule
	}
	scores, ok := factTableRowScores(rows, table)
	if !ok {
		switch rule {
		case factRowCutNewestDays:
			rule = factRowCutSeriesUndated
		case factRowCutHighestRank:
			rule = factRowCutRankingUnscored
		}
		return copySlicePreservingEmpty(rows[:perTable]), rule
	}
	order := make([]int, len(rows))
	for index := range order {
		order[index] = index
	}
	// Highest score first; ties keep the source order, so the cut is
	// deterministic. EXACT comparison (factRowScore.compare), never a
	// float64 projection: two distinct int64 ranks above 2^53 are equal as
	// float64, and the cut then kept the lower one (codex r1, P1, executed).
	sort.SliceStable(order, func(a, b int) bool { return scores[order[a]].compare(scores[order[b]]) > 0 })
	chosen := append([]int(nil), order[:perTable]...)
	sort.Ints(chosen)
	kept := make([]contractsv1.ContextFabricClaimedFactRow, 0, perTable)
	for _, index := range chosen {
		kept = append(kept, rows[index])
	}
	return kept, rule
}

// factRowCutRuleFor is the rule a table's DECLARATION asks for, before its
// rows are looked at; keepFactTableRows downgrades it to a fallback when the
// rows cannot be scored.
func factRowCutRuleFor(table *contractsv1.ContextFabricClaimedFactTable) factRowCutRule {
	switch {
	case table == nil:
		return factRowCutSourcePrefix
	case table.Shape == contractsv1.ContextFabricFactTableShapeTimeSeries:
		return factRowCutNewestDays
	case table.Shape == contractsv1.ContextFabricFactTableShapeRanking:
		return factRowCutHighestRank
	}
	return factRowCutSourcePrefix
}

// factRowScore is one row's "keep me first" value, held EXACTLY as the row
// carries it: an instant, an int64, or a finite float64. It is never projected
// onto float64, which cannot represent every int64 (or every instant in
// nanoseconds) and would make distinct values tie.
type factRowScore struct {
	kind    factRowScoreKind
	instant time.Time
	integer int64
	number  float64
}

type factRowScoreKind int

const (
	factRowScoreInstant factRowScoreKind = iota
	factRowScoreInteger
	factRowScoreNumber
)

// compare returns -1, 0 or +1. Instants compare only with instants (a table
// is scored under one rule). Integers and numbers compare exactly through
// big.Float, which represents every int64 and every finite float64.
func (s factRowScore) compare(other factRowScore) int {
	if s.kind == factRowScoreInstant || other.kind == factRowScoreInstant {
		return s.instant.Compare(other.instant)
	}
	if s.kind == factRowScoreInteger && other.kind == factRowScoreInteger {
		switch {
		case s.integer < other.integer:
			return -1
		case s.integer > other.integer:
			return 1
		}
		return 0
	}
	return s.big().Cmp(other.big())
}

func (s factRowScore) big() *big.Float {
	if s.kind == factRowScoreInteger {
		return new(big.Float).SetInt64(s.integer)
	}
	return big.NewFloat(s.number)
}

// factTableRowScores scores every row -- the instant for a dated time series,
// the order_by value for a ranking -- or returns false when the table is
// neither, or any row does not carry a usable value (a non-finite number
// included), so the caller falls back to the source-order prefix.
func factTableRowScores(rows []contractsv1.ContextFabricClaimedFactRow, table *contractsv1.ContextFabricClaimedFactTable) ([]factRowScore, bool) {
	if table == nil {
		return nil, false
	}
	scores := make([]factRowScore, len(rows))
	switch {
	case table.Shape == contractsv1.ContextFabricFactTableShapeTimeSeries && len(table.Key) == 1:
		for index, row := range rows {
			cell, present := row.Fields[table.Key[0]]
			if !present || cell.String == nil {
				return nil, false
			}
			instant, parsed := parseFactTableInstant(*cell.String)
			if !parsed {
				return nil, false
			}
			scores[index] = factRowScore{kind: factRowScoreInstant, instant: instant}
		}
		return scores, true
	case table.Shape == contractsv1.ContextFabricFactTableShapeRanking && table.OrderBy != "":
		for index, row := range rows {
			cell, present := row.Fields[table.OrderBy]
			switch {
			case present && cell.Integer != nil:
				scores[index] = factRowScore{kind: factRowScoreInteger, integer: *cell.Integer}
			case present && cell.Number != nil && !math.IsNaN(*cell.Number) && !math.IsInf(*cell.Number, 0):
				scores[index] = factRowScore{kind: factRowScoreNumber, number: *cell.Number}
			default:
				return nil, false
			}
		}
		return scores, true
	}
	return nil, false
}

// parseFactTableInstant parses under the SAME layouts a time_series key is
// validated against (factTableInstantLayouts), so "dated" here means exactly
// what the producer contract means by it.
func parseFactTableInstant(value string) (time.Time, bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, false
	}
	for _, layout := range factTableInstantLayouts {
		if instant, err := time.Parse(layout, value); err == nil {
			return instant, true
		}
	}
	return time.Time{}, false
}

// factRowTruncationOutcomeRow is the completeness record of the cut: the
// answer was NARROWED in DEPTH (every subject stays; each carries fewer rows),
// because of the BYTE ceiling, observed here. Unattributed on purpose: the
// cap applies across every requirement's tables at once, and naming one would
// be a wrong attribution a reader acts on.
func factRowTruncationOutcomeRow(served, declared int) RequirementOutcomeRow {
	return contractsv1.ContextFabricWithReductionRefinement(RequirementOutcomeRow{
		Stage:         contractsv1.ContextFabricOutcomeStageAssembledResult,
		Outcome:       contractsv1.ContextFabricRequirementNarrowed,
		Impact:        contractsv1.ContextFabricAnswerImpactDepth,
		CauseOverrun:  contractsv1.ContextFabricBudgetOverrunBytes,
		CauseObserved: true,
		Served:        served,
		Declared:      declared,
	})
}

// applyFactRowTruncation builds the truncated, disclosed document for one cap.
// It returns false when the cap cuts nothing.
func applyFactRowTruncation(result InvestigationResult, perTable, declared int) (InvestigationResult, int, factRowCutRules, bool) {
	claims, served, tables := truncateClaimedFactRows(result.ClaimedFacts, perTable)
	if served >= declared {
		return result, served, tables, false
	}
	sentence, ok := contractsv1.ContextFabricFactRowTruncationLimitation(served, declared, perTable)
	if !ok {
		return result, served, tables, false
	}
	result.ClaimedFacts = claims
	result.Completeness.Outcomes = appendOutcomeRows(result.Completeness.Outcomes, factRowTruncationOutcomeRow(served, declared))
	composed, displaced := appendBoundedLimitations(result.Limitations, []string{sentence})
	result.Limitations = composed
	result.LimitationsDisplaced += displaced
	result.Coverage.Partial = true
	// PARTIAL, never complete: the answer carries less than its facts did.
	// Only a COMPLETE status is lowered; a status already weaker than partial
	// says more and is kept.
	if result.Status == InvestigationComplete {
		result.Status = InvestigationPartial
	}
	return result, served, tables, true
}

// planFactRowTruncation is the lever. It searches for the LARGEST per-table
// cap whose finalized, served-shape document fits the byte ceiling, then
// re-finalizes and re-measures that document through the same accounting
// gate every stage-3 exit uses.
//
// The search finalizes each candidate WITHOUT pending telemetry: finalizeResult
// appends observation-cover events per call, and only the served candidate's
// may be recorded.
func (e *Engine) planFactRowTruncation(
	ctx context.Context,
	principal storage.Principal,
	plan *AnswerPlan,
	frame *QuestionFrame,
	result InvestigationResult,
	budget ResponseBudget,
	measured MeasuredAttempt,
	facts CanonicalFactBundle,
	pending *assemblyTelemetry,
	pass int,
	cardinality MembershipCardinality,
) (factRowTruncationAttempt, error) {
	declared, longest := claimedFactTableRowCounts(result.ClaimedFacts)
	attempt := factRowTruncationAttempt{Measured: measured, RowsDeclared: declared, RowsServed: declared}
	if measured.Overrun != contractsv1.ContextFabricBudgetOverrunBytes || budget.MaxSerializedBytes <= 0 {
		attempt.Declined = FactRowTruncationNotBytesAxis
		return attempt, nil
	}
	rowBytes, err := claimedFactRowBytes(result.ClaimedFacts)
	if err != nil {
		attempt.Declined = FactRowTruncationUnmeasurable
		return attempt, nil
	}
	attempt.RowBytes = rowBytes
	attempt.RowsDominate = rowBytes >= measured.Measurement.Bytes-budget.MaxSerializedBytes
	if longest <= 1 {
		attempt.Declined = FactRowTruncationNothingTruncatable
		return attempt, nil
	}

	candidate := func(perTable int) (InvestigationResult, int, factRowCutRules, contractsv1.ContextFabricResponseMeasurement, bool, error) {
		truncated, served, tables, cut := applyFactRowTruncation(result, perTable, declared)
		if !cut {
			return InvestigationResult{}, served, tables, contractsv1.ContextFabricResponseMeasurement{}, false, nil
		}
		truncated = e.finalizeResult(ctx, principal, truncated, *plan, frame, facts, nil, pass, cardinality)
		measurement, err := contractsv1.MeasureContextFabricResponse(servedMeasurementShape(truncated))
		return truncated, served, tables, measurement, true, err
	}

	// Largest fitting cap in [1, longest-1]. Bytes are monotone in the cap up
	// to the few digits the sentence interpolates; the final measurement
	// below is the gate, so the search only has to be good, not exact.
	low, high, best := 1, longest-1, 0
	var smallest contractsv1.ContextFabricResponseMeasurement
	for low <= high {
		mid := low + (high-low)/2
		_, _, _, measurement, cut, err := candidate(mid)
		if err != nil {
			attempt.Declined = FactRowTruncationUnmeasurable
			return attempt, nil
		}
		if mid == 1 {
			smallest = measurement
		}
		if cut && measurement.Overrun(budget) == contractsv1.ContextFabricBudgetFits {
			best = mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	if best == 0 {
		attempt.Declined = FactRowTruncationInsufficient
		if smallest.Bytes == 0 {
			if _, _, _, measurement, _, err := candidate(1); err == nil {
				smallest = measurement
			}
		}
		_, served, tables := truncateClaimedFactRows(result.ClaimedFacts, 1)
		attempt.RowsServed, attempt.PerTable, attempt.Tables = served, 1, tables
		attempt.Measured.Measurement = smallest
		attempt.Measured.Overrun = smallest.Overrun(budget)
		return attempt, nil
	}

	truncated, served, tables, _ := applyFactRowTruncation(result, best, declared)
	truncated = e.finalizeResult(ctx, principal, truncated, *plan, frame, facts, pending, pass, cardinality)
	attempt.RowsServed, attempt.PerTable, attempt.Tables = served, best, tables
	servedMeasured, err := e.measureAssembledAttempt(ctx, principal, "fact_row_truncation", measured.Allocation, truncated, budget)
	if err != nil {
		if errors.Is(err, ErrItemAccounting) {
			return attempt, err
		}
		attempt.Declined = FactRowTruncationUnmeasurable
		return attempt, nil
	}
	attempt.Measured = servedMeasured
	// BOTH AXES, and the item certificate when there is an item ceiling:
	// the same gate the candidate reduction applies, for the same reason --
	// a document announced as cut to fit must actually fit.
	if servedMeasured.Overrun != contractsv1.ContextFabricBudgetFits || (budget.MaxItems > 0 && !servedMeasured.CertifiedFit()) {
		attempt.Declined = FactRowTruncationInsufficient
		return attempt, nil
	}
	if !servableLeverResult(truncated) {
		attempt.Declined = FactRowTruncationInvalidResult
		return attempt, nil
	}
	attempt.Result = truncated
	attempt.Served = true
	return attempt, nil
}

// FactRowTruncationEvent is the operator record of ONE lever application:
// axis, before/after bytes, rows before/after, and whether it served. Counts
// and closed values only.
type FactRowTruncationEvent struct {
	Family             QuestionFamily
	Stage              contractsv1.ContextFabricPlanNarrowingStage
	Pass               int
	Overrun            contractsv1.ContextFabricBudgetOverrun
	MaxSerializedBytes int64
	BytesBefore        int64
	BytesAfter         int64
	RowBytes           int64
	RowsDominate       bool
	RowsBefore         int
	RowsAfter          int
	PerTable           int
	TablesTruncated    int
	// Per-rule counts of the truncated tables; they sum to TablesTruncated.
	TablesNewestDays      int
	TablesHighestRank     int
	TablesSourcePrefix    int
	TablesSeriesUndated   int
	TablesRankingUnscored int
	Served                bool
	Declined              FactRowTruncationDeclined
}

// recordFactRowTruncation emits one lever application. A lever that never ran
// (not the byte axis) is not an application and emits nothing.
func (e *Engine) recordFactRowTruncation(ctx context.Context, principal storage.Principal, plan *AnswerPlan, pass int, before MeasuredAttempt, attempt factRowTruncationAttempt, budget ResponseBudget) {
	if e.telemetry == nil || attempt.Declined == FactRowTruncationNotBytesAxis {
		return
	}
	e.telemetry.RecordFactRowTruncation(ctx, principal, FactRowTruncationEvent{
		Family:                plan.Family,
		Stage:                 contractsv1.ContextFabricPlanNarrowingAssembledResult,
		Pass:                  pass,
		Overrun:               before.Overrun,
		MaxSerializedBytes:    budget.MaxSerializedBytes,
		BytesBefore:           before.Measurement.Bytes,
		BytesAfter:            attempt.Measured.Measurement.Bytes,
		RowBytes:              attempt.RowBytes,
		RowsDominate:          attempt.RowsDominate,
		RowsBefore:            attempt.RowsDeclared,
		RowsAfter:             attempt.RowsServed,
		PerTable:              attempt.PerTable,
		TablesTruncated:       attempt.Tables.Total,
		TablesNewestDays:      attempt.Tables.NewestDays,
		TablesHighestRank:     attempt.Tables.HighestRank,
		TablesSourcePrefix:    attempt.Tables.SourcePrefix,
		TablesSeriesUndated:   attempt.Tables.SeriesUndated,
		TablesRankingUnscored: attempt.Tables.RankingUnscored,
		Served:                attempt.Served,
		Declined:              attempt.Declined,
	})
}
