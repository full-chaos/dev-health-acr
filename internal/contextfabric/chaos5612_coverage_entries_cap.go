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
// DEGRADING NEVER YIELDS. Every degrading detail is paired 1:1 with a
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
func capCoverageEntriesToWriteBound(result *InvestigationResult) int {
	bound := contractsv1.ContextFabricCoverageEntriesMaxCount
	details := result.Coverage.Details
	if len(details) <= bound {
		return 0
	}
	degrading := make([]CoverageDetail, 0, len(details))
	nonDegrading := make([]CoverageDetail, 0, len(details))
	for _, d := range details {
		if d.Degrading {
			degrading = append(degrading, d)
		} else {
			nonDegrading = append(nonDegrading, d)
		}
	}
	remaining := bound - len(degrading)
	if remaining < 0 {
		remaining = 0
	}
	if len(nonDegrading) <= remaining {
		return 0
	}
	omitted := len(nonDegrading) - remaining
	kept := append(degrading, nonDegrading[:remaining]...)
	for i := range kept {
		kept[i].DetailID = fmt.Sprintf("cov-%02d", i+1)
	}
	result.Coverage.Details = kept
	return omitted
}
