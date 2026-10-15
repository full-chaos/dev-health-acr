package contextfabric

// GraphWalkVersion is the deployment-current identity of the rules that pick
// which graph edges an investigation reads within its edge budget. It is a
// reuse fence (ReuseKey.GraphWalkVersion): a stored answer read under other
// rules is not replayed as if a fresh read had produced it.
//
// Bump it whenever those rules change which edges fit the budget.
//
// v1: the full-text arm and the final edge admission share their budgets
// round-robin across the subjects the edges were read for: the committed
// subjects first (resolution order), then the full-text matches (match order).
// Within a committed subject (and in its hop walk), the edges the answer gate
// can use for it first, then its other own edges, then its second-hop edges;
// within each group, and within a full-text match, rank order. A subject whose edges run
// out leaves its turns to the others. An edge belongs to the subject that
// reaches it in the fewest hops (a subject's own edges are its own), ties to
// the earlier subject.
const GraphWalkVersion = "graph-walk.v1"
