package contextfabric

import (
	"context"
	"errors"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-6558 (second byte-axis lever): relationship-path drop, disclosed.
//
// THE DEFECT (prod, 2026-09-25, acr 163629d2, api
// req_99c4ea66949f4fe30dff1018cae7d6ca). "Which teams need attention over the
// last 30 days?" assembled 79,376 bytes against 65,536 with 19 of 30 items.
// Fact-table rows were 11,132 bytes of it; the other 68,244 alone exceeded the
// ceiling, so the row lever at one row per table still measured 69,712 and
// declined `insufficient`. The cohort retry (2 -> 1 team) came back at 77,690,
// the row lever declined again at 69,555, and the caller got a 413 for a
// question the engine can answer.
//
// THE RULE (team-lead GO, 2026-09-25). A 413 is only for a budget that cannot
// hold the MINIMUM answer. After the row lever, relationship paths -- graph
// relevance, the one non-row section the prod shape carries 25 of and the item
// budget does not charge -- are droppable in a defined order: paths no driver
// cites first, then cited paths whose every citing driver keeps evidence refs,
// each group from the END of the list. A dropped path's id is removed from the
// drivers that cited it. The fewest drops that let the row lever fit are
// taken, then the row cap is maximized. The judgment, drivers, claims,
// members, coverage and limitations are never dropped. The answer is served
// PARTIAL with the drop disclosed.

// PathDropDeclined is the CLOSED vocabulary of why the path-drop lever did not
// serve an answer.
type PathDropDeclined string

const (
	PathDropNotApplicable PathDropDeclined = ""
	// PathDropNotBytesAxis: the answer overran on ITEMS, or had no byte
	// ceiling.
	PathDropNotBytesAxis PathDropDeclined = "not_bytes_axis"
	// PathDropNothingDroppable: no path is uncited, and no cited path can go
	// without leaving a driver with no support.
	PathDropNothingDroppable PathDropDeclined = "nothing_droppable"
	// PathDropInsufficient: every droppable path was dropped and every table
	// cut to one row, and the document still did not fit. This is the
	// minimum answer, and the only byte refusal left.
	PathDropInsufficient PathDropDeclined = "insufficient"
	// PathDropUnmeasurable: a reduced document could not be marshaled.
	PathDropUnmeasurable PathDropDeclined = "unmeasurable"
	// PathDropInvalidResult: the reduced document fit but failed contract
	// validation (e.g. no room left for the disclosure outcome row).
	PathDropInvalidResult PathDropDeclined = "invalid_result"
)

type pathDropAttempt struct {
	Result   InvestigationResult
	Served   bool
	Measured MeasuredAttempt
	Declined PathDropDeclined
	// PathsBefore / PathsAfter count relationship paths; CitedDropped counts
	// dropped paths some driver had cited.
	PathsBefore  int
	PathsAfter   int
	CitedDropped int
	// PerTable is the row cap the row lever applied on the served document,
	// zero when no row was cut.
	PerTable int
	// MinimumBytes is the size of the smallest document the levers can make
	// (every droppable path gone, every table at one row). Set whenever the
	// lever ran; it is what a refusal must exceed.
	MinimumBytes int64
}

// pathDropOrder returns the order paths are dropped in, as indices into
// result.Paths: uncited paths from the end of the list, then droppable cited
// paths from the end of the list. A cited path is droppable only when every
// driver citing it keeps at least one evidence ref, because a non-withheld
// driver with neither a path nor an evidence ref is invalid.
func pathDropOrder(result InvestigationResult) (order []int, cited map[string]bool) {
	cited = map[string]bool{}
	citers := map[string][]int{}
	for driverIndex, driver := range result.Drivers {
		for _, id := range driver.PathIDs {
			cited[id] = true
			citers[id] = append(citers[id], driverIndex)
		}
	}
	for index := len(result.Paths) - 1; index >= 0; index-- {
		if !cited[result.Paths[index].PathID] {
			order = append(order, index)
		}
	}
	for index := len(result.Paths) - 1; index >= 0; index-- {
		id := result.Paths[index].PathID
		if !cited[id] {
			continue
		}
		droppable := true
		for _, driverIndex := range citers[id] {
			if len(result.Drivers[driverIndex].EvidenceRefIDs) == 0 {
				droppable = false
			}
		}
		if droppable {
			order = append(order, index)
		}
	}
	return order, cited
}

// dropPaths returns a COPY of result without the first `count` paths of
// order, their ids removed from every driver, and the drop disclosed. It
// returns false when nothing is dropped.
func dropPaths(result InvestigationResult, order []int, cited map[string]bool, count int) (InvestigationResult, int, bool) {
	if count <= 0 {
		return result, 0, false
	}
	dropped := map[int]bool{}
	droppedIDs := map[string]bool{}
	citedDropped := 0
	for _, index := range order[:count] {
		dropped[index] = true
		id := result.Paths[index].PathID
		droppedIDs[id] = true
		if cited[id] {
			citedDropped++
		}
	}
	kept := make([]RelationshipPath, 0, len(result.Paths)-count)
	for index, path := range result.Paths {
		if !dropped[index] {
			kept = append(kept, path)
		}
	}
	sentence, ok := contractsv1.ContextFabricPathDropLimitation(len(kept), len(result.Paths))
	if !ok {
		return result, 0, false
	}
	declared := len(result.Paths)
	if citedDropped > 0 {
		drivers := make([]DriverJudgment, len(result.Drivers))
		for index, driver := range result.Drivers {
			if len(driver.PathIDs) > 0 {
				ids := make([]string, 0, len(driver.PathIDs))
				for _, id := range driver.PathIDs {
					if !droppedIDs[id] {
						ids = append(ids, id)
					}
				}
				driver.PathIDs = ids
			}
			drivers[index] = driver
		}
		result.Drivers = drivers
	}
	result.Paths = kept
	result.Completeness.Outcomes = appendOutcomeRows(result.Completeness.Outcomes, pathDropOutcomeRow(len(kept), declared))
	composed, displaced := appendBoundedLimitations(result.Limitations, []string{sentence})
	result.Limitations = composed
	result.LimitationsDisplaced += displaced
	result.Coverage.Partial = true
	if result.Status == InvestigationComplete {
		result.Status = InvestigationPartial
	}
	return result, citedDropped, true
}

// pathDropOutcomeRow: NARROWED in DEPTH (every subject stays; the answer
// carries fewer relationship paths) because of the BYTE ceiling, observed.
func pathDropOutcomeRow(served, declared int) RequirementOutcomeRow {
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

// planPathDrop runs after the row lever declined. It finds the fewest path
// drops after which the document fits -- as it stands, or with the row lever
// applied -- and serves that document.
func (e *Engine) planPathDrop(
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
) (pathDropAttempt, error) {
	attempt := pathDropAttempt{Measured: measured, PathsBefore: len(result.Paths), PathsAfter: len(result.Paths)}
	if measured.Overrun != contractsv1.ContextFabricBudgetOverrunBytes || budget.MaxSerializedBytes <= 0 {
		attempt.Declined = PathDropNotBytesAxis
		return attempt, nil
	}
	order, cited := pathDropOrder(result)

	// reduce builds the document with `count` paths dropped and, when it
	// still overruns on bytes, the row lever applied. Search passes no
	// pending telemetry; only the served document's cover events count.
	type reduced struct {
		doc          InvestigationResult
		measured     MeasuredAttempt
		fits         bool
		citedDropped int
		perTable     int
		minimumBytes int64
	}
	reduce := func(count int, served *assemblyTelemetry) (reduced, error) {
		doc := result
		citedDropped := 0
		if count > 0 {
			var ok bool
			doc, citedDropped, ok = dropPaths(result, order, cited, count)
			if !ok {
				return reduced{}, nil
			}
		}
		// Finalized WITHOUT pending telemetry first; the served document is
		// re-finalized with it exactly once, by whichever exit serves it.
		finalized := e.finalizeResult(ctx, principal, doc, *plan, frame, facts, nil, pass, cardinality)
		docMeasured, err := e.measureAssembledAttempt(ctx, principal, "path_drop", measured.Allocation, finalized, budget)
		if err != nil {
			return reduced{}, err
		}
		out := reduced{doc: finalized, measured: docMeasured, citedDropped: citedDropped, minimumBytes: docMeasured.Measurement.Bytes}
		if docMeasured.Overrun == contractsv1.ContextFabricBudgetFits {
			out.fits = !(budget.MaxItems > 0 && !docMeasured.CertifiedFit())
			if out.fits && served != nil {
				out.doc = e.finalizeResult(ctx, principal, doc, *plan, frame, facts, served, pass, cardinality)
				if out.measured, err = e.measureAssembledAttempt(ctx, principal, "path_drop", measured.Allocation, out.doc, budget); err != nil {
					return reduced{}, err
				}
				out.fits = out.measured.Overrun == contractsv1.ContextFabricBudgetFits && !(budget.MaxItems > 0 && !out.measured.CertifiedFit())
			}
			return out, nil
		}
		if docMeasured.Overrun != contractsv1.ContextFabricBudgetOverrunBytes {
			return out, nil
		}
		rows, err := e.planFactRowTruncation(ctx, principal, plan, frame, finalized, budget, docMeasured, facts, served, pass, cardinality)
		if err != nil {
			return reduced{}, err
		}
		if rows.Measured.Measurement.Bytes > 0 && rows.Measured.Measurement.Bytes < out.minimumBytes {
			out.minimumBytes = rows.Measured.Measurement.Bytes
		}
		if rows.Served {
			out.doc, out.measured, out.fits, out.perTable = rows.Result, rows.Measured, true, rows.PerTable
		}
		return out, nil
	}

	unmeasurable := func(err error) (pathDropAttempt, error) {
		if errors.Is(err, ErrItemAccounting) {
			return attempt, err
		}
		attempt.Declined = PathDropUnmeasurable
		return attempt, nil
	}
	// The minimum answer: every droppable path gone, the row lever at its
	// floor. Measured first, so a refusal always names it.
	floor, err := reduce(len(order), nil)
	if err != nil {
		return unmeasurable(err)
	}
	attempt.MinimumBytes = floor.minimumBytes
	if len(order) == 0 {
		attempt.Declined = PathDropNothingDroppable
		return attempt, nil
	}
	if !floor.fits {
		attempt.Declined = PathDropInsufficient
		attempt.PathsAfter = len(result.Paths) - len(order)
		attempt.Measured.Measurement = floor.measured.Measurement
		attempt.Measured.Overrun = floor.measured.Overrun
		return attempt, nil
	}
	// Fewest drops that fit, in [1, len(order)]. Bytes fall as paths go; the
	// served measurement below is the gate.
	low, high, best := 1, len(order), len(order)
	for low <= high {
		mid := low + (high-low)/2
		candidate, err := reduce(mid, nil)
		if err != nil {
			return unmeasurable(err)
		}
		if candidate.fits {
			best = mid
			high = mid - 1
		} else {
			low = mid + 1
		}
	}
	served, err := reduce(best, pending)
	if err != nil {
		return unmeasurable(err)
	}
	attempt.PathsAfter = len(result.Paths) - best
	attempt.CitedDropped = served.citedDropped
	attempt.PerTable = served.perTable
	attempt.Measured = served.measured
	if !served.fits {
		attempt.Declined = PathDropInsufficient
		return attempt, nil
	}
	if !servableLeverResult(served.doc) {
		attempt.Declined = PathDropInvalidResult
		return attempt, nil
	}
	attempt.Result = served.doc
	attempt.Served = true
	return attempt, nil
}

// PathDropEvent is the operator record of ONE path-drop lever application.
// Counts and closed values only.
type PathDropEvent struct {
	Family             QuestionFamily
	Stage              contractsv1.ContextFabricPlanNarrowingStage
	Pass               int
	Overrun            contractsv1.ContextFabricBudgetOverrun
	MaxSerializedBytes int64
	BytesBefore        int64
	BytesAfter         int64
	MinimumBytes       int64
	PathsBefore        int
	PathsAfter         int
	CitedDropped       int
	PerTable           int
	Served             bool
	Declined           PathDropDeclined
}

// recordPathDrop emits one lever application. Not the byte axis: nothing.
func (e *Engine) recordPathDrop(ctx context.Context, principal storage.Principal, plan *AnswerPlan, pass int, before MeasuredAttempt, attempt pathDropAttempt, budget ResponseBudget) {
	if e.telemetry == nil || attempt.Declined == PathDropNotBytesAxis {
		return
	}
	e.telemetry.RecordPathDrop(ctx, principal, PathDropEvent{
		Family:             plan.Family,
		Stage:              contractsv1.ContextFabricPlanNarrowingAssembledResult,
		Pass:               pass,
		Overrun:            before.Overrun,
		MaxSerializedBytes: budget.MaxSerializedBytes,
		BytesBefore:        before.Measurement.Bytes,
		BytesAfter:         attempt.Measured.Measurement.Bytes,
		MinimumBytes:       attempt.MinimumBytes,
		PathsBefore:        attempt.PathsBefore,
		PathsAfter:         attempt.PathsAfter,
		CitedDropped:       attempt.CitedDropped,
		PerTable:           attempt.PerTable,
		Served:             attempt.Served,
		Declined:           attempt.Declined,
	})
}
