package contextfabric

// OrganizationCanonicalIDPrefix is the prefix an organization subject's
// CanonicalID carries (CHAOS-12230). The projector mints the one organization
// node with it, and a caller that names its own organization by canonical id
// (Ask Dev page context) builds the same string.
const OrganizationCanonicalIDPrefix = "organization:"

// RepositoryCanonicalIDPrefix is the prefix a repository subject's CanonicalID
// carries (CHAOS-12230): the repository node and every edge endpoint that must
// land on it.
const RepositoryCanonicalIDPrefix = "repository:"

// OrganizationCanonicalID mints the canonical identity of an organization from
// its org id. It is the one builder the projector's organization node uses, so
// the published canonical-id vectors
// (contracts/examples/v1/context_fabric_canonical_id_vectors.v1.json) are the
// output of this function, never a copy of its format.
//
// A plain prefix concatenation, like the inline expression it replaces: an
// empty org id gives the bare prefix, exactly as before. Unlike
// TeamCanonicalID it does not special-case empty input, because changing what
// an existing producer mints is outside this helper's job.
func OrganizationCanonicalID(orgID string) string {
	return OrganizationCanonicalIDPrefix + orgID
}

// RepositoryCanonicalID mints the canonical identity of a repository from its
// repos.id (CHAOS-12230). The repository node and every producer edge that
// points at a repository read it, so an endpoint can never drift from the node
// it must land on.
//
// A plain prefix concatenation, like the inline expressions it replaces: an
// empty repository id gives the bare prefix, exactly as before.
func RepositoryCanonicalID(repoID string) string {
	return RepositoryCanonicalIDPrefix + repoID
}
