package answerprojection

import v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

// DisplayLogArgs measures the executed projection using only public input
// and output values. Callers own logging and transport correlation; Project
// itself has no side effects. Counts include explicit measured zeroes.
func DisplayLogArgs(r v1.ContextFabricInvestigationResult, p v1.ContextFabricAnswerProjection, b Budget, rendered, truncated bool) []any {
	b = b.withDefaults()
	canonicalMembers, projectedMembers := 0, 0
	if r.Cohort != nil {
		canonicalMembers = len(r.Cohort.Members)
	}
	if p.Cohort != nil {
		projectedMembers = len(p.Cohort.Members)
	}
	floor, recorded := 0, 0
	for _, f := range p.KeyFacts {
		switch WorkItemCountQualification(p, f) {
		case "floor":
			floor++
		case "recorded":
			recorded++
		}
	}
	basis := "absent"
	if floor > 0 {
		basis = "floor"
	}
	if recorded > 0 {
		basis = "recorded"
	}
	if floor > 0 && recorded > 0 {
		basis = "mixed"
	}
	return []any{
		"canonical_members", canonicalMembers, "projected_members", projectedMembers,
		"canonical_eligible_facts", countProjectedFactsOmitted(r, nil), "projected_facts", len(p.KeyFacts),
		"facts_omitted", p.ProjectionBudget.FactsOmitted, "members_omitted", p.ProjectionBudget.CohortMembersOmitted, "evidence_omitted", p.ProjectionBudget.EvidenceRefsOmitted,
		"max_facts", b.MaxFacts, "max_members", b.MaxCohortMembers, "max_evidence", b.MaxEvidenceRefs,
		"count_basis", basis, "floor_counts", floor, "recorded_counts", recorded,
		"projection_truncated", p.ProjectionBudget.Truncated, "markdown_rendered", rendered, "markdown_truncated", truncated,
	}
}
