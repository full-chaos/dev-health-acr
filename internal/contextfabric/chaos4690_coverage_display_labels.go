package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// applyCoverageDisplayLabels is CHAOS-4690 item 4's single result-level
// label composer, called at EVERY fresh-result exit from Investigate,
// immediately before that exit's own Validate/ValidateResult call (never
// on the tryReuse path -- a stored result is immutable, and a legacy
// stored row carrying no EvidenceRefLabels map at all is the RULED
// exception, design §7.3).
//
// It does two things, both idempotent and safe to call unconditionally
// regardless of how result.Coverage was composed:
//
//  1. Ensures every coverage source AND detail carries its display label.
//     MergeCoverage already stamps SourceObservation.Label/StateLabel for
//     the decisive (merged) path, and every producer already stamps
//     CoverageDetail.Label at mint time (fact_registry.appendFactCoverage,
//     falkorgraph/reader.go's appendGraphDetail) -- but a subjectless/
//     structure/window terminal composes its Coverage directly from ONE
//     producer group (graphContext.Coverage), never through MergeCoverage,
//     so its Sources would otherwise reach the wire unlabeled. Stamping
//     here, unconditionally, from the same contracts registry either path
//     would have used, makes "every source/detail carries a label" true
//     by construction rather than by remembering to call MergeCoverage on
//     every path.
//
//  2. Builds result.EvidenceRefLabels from the result's OWN evidence-ref
//     closure (contractsv1.ContextFabricEvidenceRefClosure) via the
//     display-label registry (contractsv1.ContextFabricEvidenceRefLabel).
//     ALWAYS a non-nil map on a fresh result (even when the closure is
//     empty) -- the nil-map exception is reserved for a legacy STORED
//     result that predates this field entirely (design §7.3); every
//     result composed through this function is never that.
//
// Returns the number of evidence refs whose acr:v1:<entity-type> segment
// fell outside the registry's known set and so received the generic
// fallback label -- the caller (which alone has ctx/principal/telemetry in
// scope) reports that count via EngineTelemetry.RecordEvidenceLabelFallback
// when it is greater than zero, the same "nothing to do is not an
// outcome" gating every sibling counter on EngineTelemetry already
// follows.
func applyCoverageDisplayLabels(result *InvestigationResult) int {
	sources := result.Coverage.Sources
	for i := range sources {
		sources[i].Label = contractsv1.ContextFabricSourceObservationLabel(sources[i].Source)
		sources[i].StateLabel = contractsv1.ContextFabricSourceStateLabel(sources[i].State)
	}
	details := result.Coverage.Details
	for i := range details {
		details[i].Label = contractsv1.ComposeCoverageDetailLabel(details[i])
	}

	closure := contractsv1.ContextFabricEvidenceRefClosure(*result)
	labels := make(map[string]string, len(closure))
	fallbacks := 0
	for ref := range closure {
		label, known := contractsv1.ContextFabricEvidenceRefLabel(ref)
		labels[ref] = label
		if !known {
			fallbacks++
		}
	}
	result.EvidenceRefLabels = labels
	return fallbacks
}
