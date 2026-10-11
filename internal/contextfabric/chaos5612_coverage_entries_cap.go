package contextfabric

import (
	"fmt"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// capCoverageEntriesToWriteBound is CHAOS-5612's own single result-level cap
// composer, called at EVERY fresh-result exit from Investigate, immediately
// before applyCoverageDisplayLabels -- same "single stamp point, every
// terminal exit" discipline that function's own doc comment records, and so
// before that exit's own Validate/ValidateResult call.
//
// WHY THIS EXISTS: validateCoverageDetails refuses the WHOLE write the
// moment len(Details) exceeds contractsv1.ContextFabricCoverageEntriesMaxCount
// (100) -- a legal turn whose vocabulary happens to fill the bound would not
// serve a NARROWER answer, it would refuse to serve at all. The
// vocabulary-maximal grouped turn already reaches most of the bound
// (TestTheOriginRowsFitTheCoverageBoundAtTheVocabularyMaximum). The invariant:
// a new fact kind or disclosure code can never silently drop an entry --
// once the bound is reached, the cap behaves deterministically and is
// emitted, never a silent drop and never a write refusal.
//
// DEGRADING NEVER YIELDS (and neither does the bounded-input disclosure, see coverageDetailNeverYields). Every degrading detail is paired 1:1 with a
// DegradedReasons entry (validateCoverageDetails' own dual-write derivation
// invariant); trimming one would desync that pairing -- mergeCoverageDetails
// would then have to drop the WHOLE array on the next merge, per its own
// fail-open contract -- and would silently erase the disclosure of a real
// read failure, the one outcome this cap must never produce. Only
// NON-degrading rows -- disclosure-only codes such as
// fact_read_origin_state and population_truncated, which add context beside
// a state already reported elsewhere on the document -- ever yield to the
// cap.
//
// DETERMINISTIC. Both groups keep the relative order they already carry
// (mergeCoverageDetails' own sort), so the kept non-degrading rows are
// always that order's PREFIX, never an arrival-order or random subset --
// the same "sort, then take a prefix" rule boundGroupFactsToRemainingCapacity
// applies to its own cap.
//
// Returns how many non-degrading details the cap omitted -- the caller
// (which alone has ctx/principal/telemetry in scope) reports that count via
// EngineTelemetry.RecordCoverageEntriesCapped when it is greater than zero,
// the same "nothing to do is not an outcome" convention every sibling
// gated counter on EngineTelemetry already follows.
//
// NO SEPARATE FAST-PATH ON THE TOTAL. Whenever len(details) <= bound,
// remaining (bound minus however many are degrading) can never be
// negative, so it is never clamped, and it always equals AT LEAST
// len(nonDegrading) -- the "nothing to cap" early return below is already
// exact for that case. A guard restating it against the total would be
// implied by this one, never a correction of it.
func capCoverageEntriesToWriteBound(result *InvestigationResult) int {
	bound := contractsv1.ContextFabricCoverageEntriesMaxCount
	details := result.Coverage.Details
	degrading := make([]CoverageDetail, 0, len(details))
	nonDegrading := make([]CoverageDetail, 0, len(details))
	for _, d := range details {
		if d.Degrading {
			degrading = append(degrading, d)
		} else {
			nonDegrading = append(nonDegrading, d)
		}
	}
	// Degrading rows never yield. A protected row comes next, and the rest fill
	// what is left, so the total never passes the bound unless the degrading
	// rows alone do.
	capacity := bound - len(degrading)
	if capacity < 0 {
		capacity = 0
	}
	if len(nonDegrading) <= capacity {
		return 0
	}
	protected := 0
	for _, d := range nonDegrading {
		if coverageDetailNeverYields(d) {
			protected++
		}
	}
	protectedRoom := min(protected, capacity)
	otherRoom := capacity - protectedRoom
	omitted := len(nonDegrading) - capacity
	kept := degrading
	for _, d := range nonDegrading {
		if coverageDetailNeverYields(d) {
			if protectedRoom > 0 {
				kept = append(kept, d)
				protectedRoom--
			}
		} else if otherRoom > 0 {
			kept = append(kept, d)
			otherRoom--
		}
	}
	for i := range kept {
		kept[i].DetailID = fmt.Sprintf("cov-%02d", i+1)
	}
	result.Coverage.Details = kept
	return omitted
}

// coverageDetailNeverYields names the non-degrading rows the cap keeps ahead of
// the rest: the per-kind disclosure of a bounded synthesis input and the failure of the
// model call, which the answer's own limitation or warning promises. Their
// count is the number of fact kinds read plus one, a small closed set.
func coverageDetailNeverYields(d CoverageDetail) bool {
	return d.Code == contractsv1.ContextFabricCoverageDetailSynthesisInputBounded || d.Code == contractsv1.ContextFabricCoverageDetailSynthesisModelFailed
}
