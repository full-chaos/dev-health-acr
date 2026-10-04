package contextfabric

// OwnershipRoutingVersion is the deployment-current identity of the
// ownership-routing rules -- falkorgraph's own gate deciding whether a
// repository-anchored team count's member set comes from the repository's
// declared ownership signal (authorization_repositories) or from
// hopWalk's graph-proximity traversal, and which committed subject's
// walk-or-census contribution the routed pool admits.
//
// This is a reuse fence (ReuseKey.OwnershipRoutingVersion): a stored
// answer computed under different routing rules must not be silently
// replayed as if a fresh read had produced it -- see that field's own doc
// comment for the full reasoning.
//
// Bump this whenever the routing gate's own arm-selection rules change --
// which pairings route through ownership, which committed subject's
// contribution the routed pool admits or excludes, or how a bound anchor
// is recognized.
//
// v1 -> v2: a repository named by its label (an exact-label commit, which is
// not an identity proof) routes through ownership when it is the one committed
// subject and the reading points at it, and a team the question text matched
// is no longer a member of an ownership-routed cohort. A stored answer saved
// under v1 for such a question holds teams that do not own the repository.
const OwnershipRoutingVersion = "ownership-routing.v2"
