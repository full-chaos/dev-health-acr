package contextfabric

import (
	"context"
	"encoding/json"
	"errors"

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
// "HIGHEST-RANKED" IS THE PRODUCER'S ORDER. A row carries no rank of its own.
// The one existing row cut in this service (canonicalFieldRows, the 64-row
// contract cap) keeps a PREFIX in the order the fact producer listed the rows,
// so this lever does the same rather than inventing a second ordering. One cap
// K applies to every table, so each subject's tables keep their first K rows:
// no subject loses its evidence to make room for another's.
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
	// TablesTruncated counts the row tables the cap shortened.
	TablesTruncated int
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
// to its first perTable rows. The input slice and its row slices are never
// written: the caller's result stays the document it measured.
func truncateClaimedFactRows(claims []ClaimedFact, perTable int) (truncated []ClaimedFact, served, tables int) {
	truncated = copySlicePreservingEmpty(claims)
	for index := range truncated {
		if len(truncated[index].Rows) > perTable {
			truncated[index].Rows = copySlicePreservingEmpty(truncated[index].Rows[:perTable])
			tables++
		}
		if len(truncated[index].TimeSeriesRows) > perTable {
			truncated[index].TimeSeriesRows = copySlicePreservingEmpty(truncated[index].TimeSeriesRows[:perTable])
			tables++
		}
		served += len(truncated[index].Rows) + len(truncated[index].TimeSeriesRows)
	}
	return truncated, served, tables
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
func applyFactRowTruncation(result InvestigationResult, perTable, declared int) (InvestigationResult, int, int, bool) {
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

	candidate := func(perTable int) (InvestigationResult, int, int, contractsv1.ContextFabricResponseMeasurement, bool, error) {
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
		attempt.RowsServed, attempt.PerTable, attempt.TablesTruncated = served, 1, tables
		attempt.Measured.Measurement = smallest
		attempt.Measured.Overrun = smallest.Overrun(budget)
		return attempt, nil
	}

	truncated, served, tables, _ := applyFactRowTruncation(result, best, declared)
	truncated = e.finalizeResult(ctx, principal, truncated, *plan, frame, facts, pending, pass, cardinality)
	attempt.RowsServed, attempt.PerTable, attempt.TablesTruncated = served, best, tables
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
	Served             bool
	Declined           FactRowTruncationDeclined
}

// recordFactRowTruncation emits one lever application. A lever that never ran
// (not the byte axis) is not an application and emits nothing.
func (e *Engine) recordFactRowTruncation(ctx context.Context, principal storage.Principal, plan *AnswerPlan, pass int, before MeasuredAttempt, attempt factRowTruncationAttempt, budget ResponseBudget) {
	if e.telemetry == nil || attempt.Declined == FactRowTruncationNotBytesAxis {
		return
	}
	e.telemetry.RecordFactRowTruncation(ctx, principal, FactRowTruncationEvent{
		Family:             plan.Family,
		Stage:              contractsv1.ContextFabricPlanNarrowingAssembledResult,
		Pass:               pass,
		Overrun:            before.Overrun,
		MaxSerializedBytes: budget.MaxSerializedBytes,
		BytesBefore:        before.Measurement.Bytes,
		BytesAfter:         attempt.Measured.Measurement.Bytes,
		RowBytes:           attempt.RowBytes,
		RowsDominate:       attempt.RowsDominate,
		RowsBefore:         attempt.RowsDeclared,
		RowsAfter:          attempt.RowsServed,
		PerTable:           attempt.PerTable,
		TablesTruncated:    attempt.TablesTruncated,
		Served:             attempt.Served,
		Declined:           attempt.Declined,
	})
}
