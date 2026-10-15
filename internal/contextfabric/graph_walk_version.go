package contextfabric

// GraphWalkVersion is the deployment-current identity of the rules that pick
// which graph edges an investigation reads within its edge budget. It is a
// reuse fence (ReuseKey.GraphWalkVersion): a stored answer read under other
// rules is not replayed as if a fresh read had produced it.
//
// Bump it whenever those rules change which edges fit the budget.
//
// v1: the full-text arm shares its edge budget round-robin across the matched
// subjects, in their match order, with each subject's edges in rank order. A
// subject whose edges run out leaves its turns to the others.
const GraphWalkVersion = "graph-walk.v1"
