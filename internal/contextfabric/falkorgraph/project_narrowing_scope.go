package falkorgraph

// CHAOS-12329: a requested project narrowing is SCOPE, not authorization. The
// walk's admission (treeWalkState.admitted, through graphrank's node rule)
// refuses a node outside the requested projects together with a node the
// caller's grants hide, and the link read used to count both as denied: the
// answer then said that work items were "outside this principal's authorized
// scope", and an all-excluded answer was served degraded, for a caller allowed
// to see everything. The link read now asks the same question without the
// narrowing: a row that passes then is out of the requested scope, never a
// denial (not in linkDeniedTargets, so no authorization-gap disclosure and no
// floor from it). What the grants hide is still denied.

// withoutProjectNarrowing is the walk state with the request's project
// narrowing removed: what the caller's grants and the rest of the request
// decide. It records nothing: the unverified-narrowing flag is set only under
// a requested narrowing (graphrank.ProjectNarrowingUnproven).
func (s treeWalkState) withoutProjectNarrowing() treeWalkState {
	s.scope.ProjectIDs, s.scope.ProjectNarrowing = nil, nil
	return s
}

// narrowsByProject reports whether the request narrows by project at all.
func (s treeWalkState) narrowsByProject() bool {
	return len(s.scope.ProjectIDs) > 0
}
