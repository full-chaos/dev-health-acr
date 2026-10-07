package contextfabric

import (
	"fmt"

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
	detail, reason := emptyMemberSearchDetail(frame, graph)
	bundle.Coverage.Partial = true
	bundle.Coverage.DegradedReasons = []string{reason}
	bundle.Coverage.Details = []CoverageDetail{detail}
	bundle.Coverage.Sources = []SourceObservation{{Source: "context-fabric:graph", State: SourceAvailable}}
	return bundle
}

// emptyMemberSearchDetail is the one place the empty-population row is built.
// "None found" is claimed only when the graph search itself reported no
// degradation: an authorization denial or a cut walk is a different cause and
// keeps its own row.
func emptyMemberSearchDetail(frame *QuestionFrame, graph Coverage) (CoverageDetail, string) {
	detail := CoverageDetail{DetailID: "cov-fact-01", Source: "context-fabric:graph", Degrading: true}
	var reason string
	if emptyMemberSearchHolds(frame, graph) {
		detail.Code = contractsv1.ContextFabricCoverageDetailGraphNoMemberFound
		detail.Kind = frame.SubjectExpression.Scoped.MemberKind
		reason = "no_member_found:" + string(detail.Kind)
	} else {
		detail.Code = contractsv1.ContextFabricCoverageDetailRequirementReadNotPlanned
		reason = emptyFactReadNotPlannedRaw
	}
	detail.Raw = reason
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	return detail, reason
}

// emptyMemberSearchHolds reports a clean graph search that was asked for the
// members of one kind under an anchor.
func emptyMemberSearchHolds(frame *QuestionFrame, graph Coverage) bool {
	return !graph.Partial && frame != nil && frame.SubjectExpression.Scoped != nil && frame.SubjectExpression.Scoped.MemberKind != ""
}

// recordEmptyMemberSearch files the none-found row from the cohort census
// result, whatever the plan read: a scoped member kind, a clean graph search
// and a cohort with no member and a zero counted population. A bundle that already carries the row is left
// alone.
func recordEmptyMemberSearch(bundle *CanonicalFactBundle, frame *QuestionFrame, graph Coverage, cohort *Cohort, population int) {
	if cohort != nil && len(cohort.Members) > 0 {
		return
	}
	// The graph discovered no cohort for a reason other than an empty search
	// when the frame's member kind is not one the graph can list, or when it
	// counted members it did not carry: neither is "none found".
	if population != 0 || !emptyMemberSearchHolds(frame, graph) || !CohortMemberSetResolvableForFrame(*frame) {
		return
	}
	for _, existing := range bundle.Coverage.Details {
		if existing.Code == contractsv1.ContextFabricCoverageDetailGraphNoMemberFound {
			return
		}
	}
	detail, reason := emptyMemberSearchDetail(frame, graph)
	detail.DetailID = fmt.Sprintf("cov-fact-%02d", len(bundle.Coverage.Details)+1)
	bundle.Coverage.Partial = true
	bundle.Coverage.DegradedReasons = append(bundle.Coverage.DegradedReasons, reason)
	bundle.Coverage.Details = append(bundle.Coverage.Details, detail)
}
