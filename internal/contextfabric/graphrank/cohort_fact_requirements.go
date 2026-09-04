package graphrank

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

// The fact kinds a discovered cohort asks for, PER COHORT KIND.
//
// WHY THIS IS KEYED ON THE KIND AT ALL. The graph adapter used to merge
// FactHealth and FactWorkload onto every discovered cohort with no read of
// the cohort's kind, which was correct for exactly as long as the only
// servable kinds were team and project -- both of which every one of those
// producers answers for. Admitting `repository` broke that coincidence:
// devhealthfacts' workload provider declares team and project only, so a
// repository cohort asked for a fact kind no registered producer can serve
// for it.
//
// WHAT THAT ACTUALLY COST, measured rather than assumed. The fact planner
// partitions a requirement's subjects by the capability's own
// SupportedSubjectKinds and, when none survives, PRUNES the requirement with
// reason `pruned:subject_kind_unsupported`. So the unconditional merge did
// not fail a read and did not lose an answer -- every repository cohort would
// simply have carried a prune in its coverage record that was guaranteed by
// the vocabulary rather than caused by anything about that organization's
// data. This table is a disclosure fix, not a crash fix, and it is worth
// stating plainly: a requirement that can only ever prune is not a
// requirement, it is noise in the record an operator reads to find real gaps.
//
// DENY BY DEFAULT, the same direction the seam allow-list runs. A kind with
// no row here asks for nothing, so a future kind admitted at the seam without
// a row gets a cohort with no cohort-derived fact requirements -- visibly
// empty -- rather than silently inheriting team's set and disclosing a prune
// nobody chose. The pairing is pinned in both directions by a test that reads
// the real providers' Capability() rather than this list.
var cohortFactRequirements = map[contextfabric.SubjectKind][]contextfabric.FactKind{
	contextfabric.SubjectTeam:       {contextfabric.FactHealth, contextfabric.FactWorkload},
	contextfabric.SubjectProject:    {contextfabric.FactHealth, contextfabric.FactWorkload},
	contextfabric.SubjectRepository: {contextfabric.FactHealth},
}

// CohortFactRequirements returns the fact kinds a cohort of this kind asks
// for, in declared order, or nothing for a kind with no row.
//
// Returns a copy: the table is package state shared by every request, and a
// caller that appended to the returned slice would mutate what the next
// investigation asks for.
func CohortFactRequirements(kind contextfabric.SubjectKind) []contextfabric.FactKind {
	declared, ok := cohortFactRequirements[kind]
	if !ok {
		return nil
	}
	return append([]contextfabric.FactKind(nil), declared...)
}

// CohortFactRequirementKinds returns the whole table, copied, so a test in a
// package that can see the real fact providers can pin it against their
// declared capabilities in BOTH directions -- nothing claimed that no
// producer serves, and nothing omitted that one does.
//
// Exported for that pin alone. The rule this table encodes lives with the
// providers, not here; keeping the authority and the copy in separate
// packages is what makes the disagreement a test failure instead of a
// comment nobody re-reads.
func CohortFactRequirementKinds() map[contextfabric.SubjectKind][]contextfabric.FactKind {
	out := make(map[contextfabric.SubjectKind][]contextfabric.FactKind, len(cohortFactRequirements))
	for kind, declared := range cohortFactRequirements {
		out[kind] = append([]contextfabric.FactKind(nil), declared...)
	}
	return out
}
