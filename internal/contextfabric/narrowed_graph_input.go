package contextfabric

import "slices"

// narrowedInputRefHolders names every evidence-ref holder of a synthesis
// input besides the graph's own ref list, by its path from the input. It is
// the one list keptInputRefs walks, and
// TestEveryEvidenceRefHolderOfTheSynthesisInputIsWalked fails when the input
// types grow a holder this list does not name.
var narrowedInputRefHolders = []string{
	"Graph.Resolution.Candidates[].EvidenceRefIDs",
	"Graph.Cohort.Members[].EvidenceRefIDs",
	"Graph.Paths[].EvidenceRefIDs",
	"Graph.Paths[].Edges[].EvidenceRefIDs",
	"Graph.DriverCandidates[].EvidenceRefIDs",
	"Facts.Facts[].EvidenceRefIDs",
}

// keptInputRefs is every evidence ref a holder of the narrowed input still
// carries, walked over exactly the holders narrowedInputRefHolders names.
func keptInputRefs(graph GraphContext, facts []CanonicalFact) map[string]bool {
	kept := map[string]bool{}
	mark := func(refs []string) {
		for _, ref := range refs {
			kept[ref] = true
		}
	}
	for _, candidate := range graph.Resolution.Candidates {
		mark(candidate.EvidenceRefIDs)
	}
	if graph.Cohort != nil {
		for _, member := range graph.Cohort.Members {
			mark(member.EvidenceRefIDs)
		}
	}
	for _, path := range graph.Paths {
		mark(path.EvidenceRefIDs)
		for _, edge := range path.Edges {
			mark(edge.EvidenceRefIDs)
		}
	}
	for _, driver := range graph.DriverCandidates {
		mark(driver.EvidenceRefIDs)
	}
	for _, fact := range facts {
		mark(fact.EvidenceRefIDs)
	}
	return kept
}

// graphWithoutRemovedMembers is the graph input of a narrowed pass, whose
// cohort is already the narrowed one: every relationship path and driver
// candidate that names a removed member is dropped, and a kept driver's
// citation of a dropped path is removed. Synthesis derives the answer's paths,
// drivers and evidence from this input, so what it does not carry the answer
// cannot name. A graph evidence ref is removed only when a dropped member,
// path or driver carried it and no holder left in the narrowed input
// (keptInputRefs) carries it.
func graphWithoutRemovedMembers(graph GraphContext, removed []CohortMember, facts []CanonicalFact) GraphContext {
	if len(removed) == 0 {
		return graph
	}
	gone := make(map[string]bool, len(removed))
	dropped := map[string]bool{}
	mark := func(refs []string) {
		for _, ref := range refs {
			dropped[ref] = true
		}
	}
	for _, member := range removed {
		gone[SubjectMapKey(member.Subject)] = true
		mark(member.EvidenceRefIDs)
	}

	paths := make([]RelationshipPath, 0, len(graph.Paths))
	droppedPaths := map[string]bool{}
	for _, path := range graph.Paths {
		if !pathNamesRemoved(path, gone) {
			paths = append(paths, path)
			continue
		}
		droppedPaths[path.PathID] = true
		mark(path.EvidenceRefIDs)
		for _, edge := range path.Edges {
			mark(edge.EvidenceRefIDs)
		}
	}

	drivers := make([]DriverJudgment, 0, len(graph.DriverCandidates))
	for _, driver := range graph.DriverCandidates {
		if slices.ContainsFunc(driver.AffectedSubjects, func(subject SubjectRef) bool { return gone[SubjectMapKey(subject)] }) {
			mark(driver.EvidenceRefIDs)
			continue
		}
		if slices.ContainsFunc(driver.PathIDs, func(id string) bool { return droppedPaths[id] }) {
			driver.PathIDs = slices.DeleteFunc(slices.Clone(driver.PathIDs), func(id string) bool { return droppedPaths[id] })
		}
		drivers = append(drivers, driver)
	}

	graph.Paths, graph.DriverCandidates = paths, drivers
	kept := keptInputRefs(graph, facts)
	refs := make([]string, 0, len(graph.EvidenceRefIDs))
	for _, ref := range graph.EvidenceRefIDs {
		if dropped[ref] && !kept[ref] {
			continue
		}
		refs = append(refs, ref)
	}
	graph.EvidenceRefIDs = refs
	return graph
}

func pathNamesRemoved(path RelationshipPath, gone map[string]bool) bool {
	for _, node := range path.Nodes {
		if gone[SubjectMapKey(node)] {
			return true
		}
	}
	for _, edge := range path.Edges {
		if gone[SubjectMapKey(edge.From)] || gone[SubjectMapKey(edge.To)] {
			return true
		}
	}
	return false
}
