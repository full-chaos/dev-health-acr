package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// emptyFactReadBundle is what a turn reads when it planned no fact requirement:
// no fact, and a degrading coverage row that says why, so the answer cannot
// read as a complete one. When the question asked for the members of one kind
// under an anchor, the row names that kind (the search found none); otherwise
// it says the turn planned no read.
// emptyFactReadNotPlannedRaw is the raw reason of the not-planned row an empty
// fact read files, which the served-status floor keys on.
const emptyFactReadNotPlannedRaw = "no_fact_read_planned"

func emptyFactReadBundle(frame *QuestionFrame, graph Coverage) CanonicalFactBundle {
	bundle := CanonicalFactBundle{
		Version:           nonEmptyVersion("", ""),
		Facts:             []CanonicalFact{},
		Coverage:          Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		Versions:          map[FactKind]string{},
		Watermarks:        map[FactKind]string{},
		ReadSubjects:      FactReadSubjects{},
		EvaluatedSubjects: FactReadSubjects{},
		Outcomes:          FactOutcomeLedger{},
	}
	detail := CoverageDetail{DetailID: "cov-fact-01", Source: "context-fabric:graph", Degrading: true}
	var reason string
	// "None found" is claimed only when the graph search itself reported no
	// degradation: an authorization denial or a cut walk is a different cause
	// and keeps its own row.
	if !graph.Partial && frame != nil && frame.SubjectExpression.Scoped != nil && frame.SubjectExpression.Scoped.MemberKind != "" {
		detail.Code = contractsv1.ContextFabricCoverageDetailGraphNoMemberFound
		detail.Kind = frame.SubjectExpression.Scoped.MemberKind
		reason = "no_member_found:" + string(detail.Kind)
	} else {
		detail.Code = contractsv1.ContextFabricCoverageDetailRequirementReadNotPlanned
		reason = emptyFactReadNotPlannedRaw
	}
	detail.Raw = reason
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	bundle.Coverage.Partial = true
	bundle.Coverage.DegradedReasons = []string{reason}
	bundle.Coverage.Details = []CoverageDetail{detail}
	bundle.Coverage.Sources = []SourceObservation{{Source: "context-fabric:graph", State: SourceAvailable}}
	return bundle
}
