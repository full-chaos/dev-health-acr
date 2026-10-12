package contextfabric

import "slices"

// graphWithoutRemovedMembers is the graph input of a narrowed pass: every
// relationship path and driver candidate that names a removed member is
// dropped, a kept driver's citation of a dropped path is removed, and the
// evidence refs only the dropped members, paths and drivers carried are
// removed. Synthesis derives the answer's paths, drivers and evidence from
// this input, so what it does not carry the answer cannot name. Refs a kept
// member, path, driver or retained fact still carries stay.
func graphWithoutRemovedMembers(graph GraphContext, removed []CohortMember, kept []CohortMember, facts []CanonicalFact) GraphContext {
	if len(removed) == 0 {
		return graph
	}
	gone := make(map[string]bool, len(removed))
	for _, member := range removed {
		gone[SubjectMapKey(member.Subject)] = true
	}
	dropRefs := map[string]bool{}
	keepRefs := map[string]bool{}
	mark := func(into map[string]bool, refs []string) {
		for _, ref := range refs {
			into[ref] = true
		}
	}
	for _, member := range removed {
		mark(dropRefs, member.EvidenceRefIDs)
	}
	for _, member := range kept {
		mark(keepRefs, member.EvidenceRefIDs)
	}
	for _, fact := range facts {
		mark(keepRefs, fact.EvidenceRefIDs)
	}

	paths := make([]RelationshipPath, 0, len(graph.Paths))
	droppedPaths := map[string]bool{}
	for _, path := range graph.Paths {
		target := keepRefs
		if pathNamesRemoved(path, gone) {
			target = dropRefs
			droppedPaths[path.PathID] = true
		} else {
			paths = append(paths, path)
		}
		mark(target, path.EvidenceRefIDs)
		for _, edge := range path.Edges {
			mark(target, edge.EvidenceRefIDs)
		}
	}

	drivers := make([]DriverJudgment, 0, len(graph.DriverCandidates))
	for _, driver := range graph.DriverCandidates {
		if slices.ContainsFunc(driver.AffectedSubjects, func(subject SubjectRef) bool { return gone[SubjectMapKey(subject)] }) {
			mark(dropRefs, driver.EvidenceRefIDs)
			continue
		}
		if slices.ContainsFunc(driver.PathIDs, func(id string) bool { return droppedPaths[id] }) {
			driver.PathIDs = slices.DeleteFunc(slices.Clone(driver.PathIDs), func(id string) bool { return droppedPaths[id] })
		}
		mark(keepRefs, driver.EvidenceRefIDs)
		drivers = append(drivers, driver)
	}

	refs := make([]string, 0, len(graph.EvidenceRefIDs))
	for _, ref := range graph.EvidenceRefIDs {
		if dropRefs[ref] && !keepRefs[ref] {
			continue
		}
		refs = append(refs, ref)
	}
	graph.Paths, graph.DriverCandidates, graph.EvidenceRefIDs = paths, drivers, refs
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
