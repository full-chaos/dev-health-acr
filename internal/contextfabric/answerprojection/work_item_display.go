package answerprojection

import v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

type subjectIdentity struct {
	kind v1.ContextFabricSubjectKind
	id   string
}

// The exception is limited to the unranked work-item tuple. Other uncited
// status claims keep their existing projection rule.
func workItemDirectClaims(result v1.ContextFabricInvestigationResult) map[subjectIdentity][]v1.ContextFabricClaimedFact {
	if result.AnswerPlan == nil || result.AnswerPlan.Family != v1.ContextFabricQuestionFamilyScopedCohortStatus || result.AnswerPlan.MemberKind != v1.ContextFabricSubjectWorkItem || result.Cohort == nil || result.Cohort.Kind != v1.ContextFabricSubjectWorkItem {
		return nil
	}
	members := make(map[subjectIdentity][]v1.ContextFabricClaimedFact, len(result.Cohort.Members))
	for _, m := range result.Cohort.Members {
		if m.RankingComputed {
			return nil
		}
		members[subjectIdentity{m.Subject.Kind, m.Subject.CanonicalID}] = nil
	}
	for _, f := range result.ClaimedFacts {
		key := subjectIdentity{f.Subject.Kind, f.Subject.CanonicalID}
		if _, ok := members[key]; !ok || f.Subject.Kind != v1.ContextFabricSubjectWorkItem || (f.Kind != v1.ContextFabricFactStatus && f.Kind != v1.ContextFabricFactWork) || len(f.Rows) > 0 || f.Table != nil || len(f.TimeSeriesRows) > 0 || f.TimeSeriesTable != nil {
			continue
		}
		members[key] = append(members[key], f)
	}
	return members
}

func projectedScalarFact(f v1.ContextFabricClaimedFact) v1.ContextFabricProjectedFact {
	return v1.ContextFabricProjectedFact{ClaimID: f.ClaimID, Kind: f.Kind, Subject: f.Subject, Field: f.Field, Value: f.Value}
}

// Count the union of eligible driver, server and direct member claims once,
// including direct claims whose member was omitted by any projection bound.
func countProjectedFactsOmitted(result v1.ContextFabricInvestigationResult, facts []v1.ContextFabricProjectedFact) int {
	retained := make(map[string]struct{}, len(facts))
	for _, f := range facts {
		retained[f.ClaimID] = struct{}{}
	}
	eligible := make(map[string]struct{})
	for _, d := range result.Drivers {
		if d.Standing != v1.ContextFabricDriverWithheld {
			for _, id := range d.ClaimedFactIDs {
				eligible[id] = struct{}{}
			}
		}
	}
	for _, f := range result.ClaimedFacts {
		if projectionCarriesUncited(f.Kind) {
			eligible[f.ClaimID] = struct{}{}
		}
	}
	for _, claims := range workItemDirectClaims(result) {
		for _, f := range claims {
			eligible[f.ClaimID] = struct{}{}
		}
	}
	omitted := 0
	for id := range eligible {
		if _, ok := retained[id]; !ok {
			omitted++
		}
	}
	return omitted
}

// WorkItemCountQualification uses only a matching structured D47. Without
// it, the projection does not establish exactness, including legacy copies.
// This pure helper is shared by the renderer and caller observation.
func WorkItemCountQualification(p v1.ContextFabricAnswerProjection, f v1.ContextFabricProjectedFact) string {
	if f.Kind != v1.ContextFabricFactCardinality || f.Field != "work_item_count" || f.Value.Integer == nil {
		return "absent"
	}
	for _, d := range p.CoverageDetails {
		if d.Code == v1.ContextFabricCoverageDetailKindCensusTruncated && d.Kind == v1.ContextFabricSubjectWorkItem && d.Declared != nil && int64(*d.Declared) == *f.Value.Integer {
			return "floor"
		}
	}
	return "recorded"
}
