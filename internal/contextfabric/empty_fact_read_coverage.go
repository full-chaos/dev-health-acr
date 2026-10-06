package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// emptyFactReadBundle is what a turn reads when it planned no fact requirement:
// no fact, and a degrading coverage row that says why, so the answer cannot
// read as a complete one. When the question asked for the members of one kind
// under an anchor, the row names that kind (the search found none); otherwise
// it says the turn planned no read.
func emptyFactReadBundle(frame *QuestionFrame) CanonicalFactBundle {
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
	if frame != nil && frame.SubjectExpression.Scoped != nil && frame.SubjectExpression.Scoped.MemberKind != "" {
		detail.Code = contractsv1.ContextFabricCoverageDetailGraphNoMemberFound
		detail.Kind = frame.SubjectExpression.Scoped.MemberKind
		reason = "no_member_found:" + string(detail.Kind)
	} else {
		detail.Code = contractsv1.ContextFabricCoverageDetailRequirementReadNotPlanned
		reason = "no_fact_read_planned"
	}
	detail.Raw = reason
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	bundle.Coverage.Partial = true
	bundle.Coverage.DegradedReasons = []string{reason}
	bundle.Coverage.Details = []CoverageDetail{detail}
	bundle.Coverage.Sources = []SourceObservation{{Source: "context-fabric:graph", State: SourceAvailable}}
	return bundle
}
