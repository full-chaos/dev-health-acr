package graphrank

import (
	"slices"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Attribute value convention for the three authorization_* keys
// ("authorization_repositories", "authorization_projects",
// "authorization_teams") on a CandidateNode/CandidateEdge's Attributes map,
// shared by every backend so ScopeContainsAttr/AuthorizedAttributes never
// need backend-specific decoding:
//
//   - key absent, or present with any type other than the two below: no
//     scope on record -- deny unconditionally, matching zepgraph's original
//     "an empty/malformed authorization attribute must never authorize"
//     rule (Codex G3(a)). A backend's conversion step must map its own
//     "missing" or "failed to decode" case to omitting the key, never to an
//     empty list (see the []string case below) or an empty string.
//   - string "*": unrestricted -- authorizes unconditionally, regardless of
//     the caller-side value. This is the ONLY string value graphrank
//     recognizes here; a backend must never store any other bare string.
//   - []string: the specific, non-wildcard authorization list. An empty
//     []string is a legitimate value (denies everything, same as it always
//     has) -- it is NOT the same as the key being absent, both by
//     convention and by ScopeMatch's own behavior (ScopeMatch over an empty
//     list only matches a caller-side "*", per the loop below).
func scopeContainsAttr(attributes map[string]interface{}, key string, value string) bool {
	raw, ok := attributes[key]
	if !ok {
		return false
	}
	if wildcard, isString := raw.(string); isString {
		return wildcard == "*"
	}
	entries, isList := raw.([]string)
	if !isList {
		return false
	}
	return ScopeMatch(entries, value, scopeValueKindForAttr(key))
}

// scopeValueKindForAttr maps an authorization_* attribute key to what the
// values under it ARE, so ScopeMatch never has to infer a kind from a value's
// shape (codex r2 F1: a project id shaped like "owner/repo" was folded
// case-insensitively because the shape, not the key, decided).
//
// The DEFAULT IS THE IDENTIFIER, and that is the whole point of doing it here
// rather than at each call: a key added later, or misspelled, gets the
// case-SENSITIVE comparison. An unrecognised key is not a licence to fold.
func scopeValueKindForAttr(key string) ScopeValueKind {
	if key == authorizationRepositoriesAttr {
		return ScopeValueRepositoryName
	}
	return ScopeValueIdentifier
}

// The three authorization_* attribute keys, named once so the kind mapping
// above and the call sites below cannot drift on a string literal.
const (
	authorizationRepositoriesAttr = "authorization_repositories"
	authorizationProjectsAttr     = "authorization_projects"
	authorizationTeamsAttr        = "authorization_teams"
)

// AuthorizedAttributes reports whether a node/edge's attribute map is
// visible to principal under the requested scope. Ported unchanged from
// zepgraph.authorizedAttributes; the attribute value convention above
// replaces backend-specific decoding, so every backend can call this
// directly.
func AuthorizedAttributes(principal storage.Principal, requested contextfabric.RequestedScope, attributes map[string]interface{}) bool {
	if auth.ClassifyGrant(principal) == auth.GrantProjectOnly {
		return projectOnlyAdmits(principal, requested, attributes)
	}
	if len(principal.RepositoryScopes) > 0 && !scopesUnrestricted(principal.RepositoryScopes) && repositoryWildcardAttr(attributes) {
		// CHAOS-7080: a "*" repository list proves NO repository, so it
		// admits nothing to a repository-restricted caller. Before this, the
		// wildcard branch of scopeContainsAttr admitted every restricted
		// caller to every wildcard node: every project (projection writes a
		// project's empty repository list as "*"), every pre-CHAOS-4390
		// team, every node or edge whose repository slug did not resolve,
		// and every project<->team ownership edge. The one wildcard node a
		// restricted caller may see is its OWN organization, identified by
		// the reserved organization scope id in authorization_projects
		// (only the organization entity may carry it: the contract rejects
		// it on any other entity). Unrestricted and universal ("*") callers
		// are unaffected.
		if !callersOrganizationAttr(principal, attributes) {
			return false
		}
	} else if len(principal.RepositoryScopes) > 0 {
		allowed := false
		for _, repository := range principal.RepositoryScopes {
			if scopeContainsAttr(attributes, authorizationRepositoriesAttr, repository) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	if len(requested.RepositorySlugs) > 0 && !requestedRepositoriesAdmit(attributes, requested.RepositorySlugs) {
		return false
	}
	if !ProjectNarrowingAdmits(requested, attributes) {
		return false
	}
	if len(requested.TeamIDs) > 0 && !anyContainsAttr(attributes, authorizationTeamsAttr, requested.TeamIDs) {
		return false
	}
	return true
}

// ProjectNarrowingUnproven reports whether a requested project narrowing
// admits these attributes only because their project list is the "*"
// string (CHAOS-12231). The projection writes an empty project list as "*",
// and scopeContainsAttr admits "*" for every requested project id, so such a
// node or edge passes AuthorizedAttributes' ProjectIDs check with nothing
// showing that it belongs to a requested project. It decides nothing:
// AuthorizedAttributes admits exactly as before. A reader uses it to
// disclose that the project narrowing it reports was not verified. The
// strict fix needs real project membership and is CHAOS-12229.
//
// A requested "*" is no narrowing: ScopeMatch admits every node for it, so
// there is nothing to verify, and a request that holds one is not reported.
//
// CHAOS-12229: once the narrowing is resolved to canonical identities
// (RequestedScope.ProjectNarrowing), a node whose stated membership holds a
// requested project is proven and not reported; a stated non-member is not
// admitted at all when every requested id resolved
// (requestedProjectNarrowingDenies). Everything else -- membership not
// stated, a requested id that did not resolve, no resolution -- stays
// unproven and is reported, never presented as narrowed.
func ProjectNarrowingUnproven(requested contextfabric.RequestedScope, attributes map[string]interface{}) bool {
	membership, applies := requestedProjectMembership(requested, attributes)
	return applies && membership != ProjectMembershipMember
}

// ProjectNarrowingAdmits is the requested-project part of AuthorizedAttributes
// alone: no project narrowing, or a real project list that holds a requested
// id, or a "*" list that stated membership does not refuse
// (requestedProjectNarrowingDenies). A reader that decides a node's grants by
// another rule (a tree walk's repository-less issue, admitted by its link)
// still applies the requested narrowing through it (CHAOS-12229).
func ProjectNarrowingAdmits(requested contextfabric.RequestedScope, attributes map[string]interface{}) bool {
	if len(requested.ProjectIDs) > 0 && !anyContainsAttr(attributes, authorizationProjectsAttr, requested.ProjectIDs) {
		return false
	}
	return !requestedProjectNarrowingDenies(requested, attributes)
}

// requestedProjectNarrowingDenies reports whether a resolved project
// narrowing refuses a node its "*" project list would otherwise admit: the
// node's stated membership holds none of the requested projects, and every
// requested id resolved, so nothing is left unverified (CHAOS-12229).
func requestedProjectNarrowingDenies(requested contextfabric.RequestedScope, attributes map[string]interface{}) bool {
	membership, applies := requestedProjectMembership(requested, attributes)
	return applies && membership == ProjectMembershipNotMember && requested.ProjectNarrowing != nil && requested.ProjectNarrowing.Complete
}

// requestedProjectMembership is what a node's stated membership says about a
// requested project narrowing, and whether the narrowing applies to the node
// at all: a narrowing is requested (no requested "*", which narrows
// nothing), and the node's project list is the "*" string that proves no
// project. With no resolution, the membership is Unstated. A node with a real
// project list is decided by that list, as before.
func requestedProjectMembership(requested contextfabric.RequestedScope, attributes map[string]interface{}) (ProjectMembership, bool) {
	if len(requested.ProjectIDs) == 0 {
		return ProjectMembershipUnstated, false
	}
	for _, id := range requested.ProjectIDs {
		if strings.TrimSpace(id) == "*" {
			return ProjectMembershipUnstated, false
		}
	}
	if value, isString := attributes[authorizationProjectsAttr].(string); !isString || value != "*" {
		return ProjectMembershipUnstated, false
	}
	if requested.ProjectNarrowing == nil {
		return ProjectMembershipUnstated, true
	}
	return ProjectMembershipOf(attributes, requested.ProjectNarrowing.Identities), true
}

// AuthorizationProjectIdentitiesAttr is the work-item node attribute that
// holds the work item's current project membership as canonical project.v2
// identities (CHAOS-12226): a list, empty when the work item is in no project,
// absent when no producer stated it. It is never "*" and never read with
// wildcard meaning. falkorgraph writes it (propAuthzProjectIdentities).
const AuthorizationProjectIdentitiesAttr = "authorization_project_identities"

// ProjectMembership is what a node's authorization_project_identities says
// about a set of projects (CHAOS-12299, shared with CHAOS-12229).
type ProjectMembership uint8

const (
	// ProjectMembershipUnstated: the key is absent, or does not hold a list of
	// strings. No membership is on record; each caller decides what that
	// means (a request narrowing discloses it; a project-only grant denies).
	ProjectMembershipUnstated ProjectMembership = iota
	// ProjectMembershipMember: a stated list holds at least one wanted
	// identity, compared exactly and case-sensitively.
	ProjectMembershipMember
	// ProjectMembershipNotMember: a stated list, possibly empty, holds none
	// of the wanted identities.
	ProjectMembershipNotMember
)

// ProjectMembershipOf reads only AuthorizationProjectIdentitiesAttr, never the
// legacy authorization_projects, so its "*" never decides. Inside a stated
// list "*" is an ordinary string that matches no canonical identity. It
// accepts []string and the []interface{} of strings a graph read returns;
// any other value is Unstated. An empty wanted set is never a member, so a
// caller with nothing to match fails closed. wanted holds canonical
// project.v2 identities; mapping raw ids to them is the caller's job.
func ProjectMembershipOf(attributes map[string]interface{}, wanted []string) ProjectMembership {
	stated, ok := statedProjectIdentities(attributes[AuthorizationProjectIdentitiesAttr])
	if !ok {
		return ProjectMembershipUnstated
	}
	for _, identity := range stated {
		for _, want := range wanted {
			if identity == want {
				return ProjectMembershipMember
			}
		}
	}
	return ProjectMembershipNotMember
}

// statedProjectIdentities is the stated list, and whether the value is one.
func statedProjectIdentities(value interface{}) ([]string, bool) {
	switch list := value.(type) {
	case []string:
		return list, true
	case []interface{}:
		out := make([]string, 0, len(list))
		for _, item := range list {
			identity, isString := item.(string)
			if !isString {
				return nil, false
			}
			out = append(out, identity)
		}
		return out, true
	default:
		return nil, false
	}
}

// projectOnlyAdmits is the project-only node rule (CHAOS-12299; design:
// docs/design/context-fabric-project-only-scope.md). The repository grant is
// never consulted. A node is admitted only when it is
//   - the caller's own organization node,
//   - a project node whose canonical id is in the grant, or
//   - a work item whose stated membership (authorization_project_identities,
//     ProjectMembershipOf) meets the grant;
//
// everything else is denied: a work item with no stated membership, a team
// (CHAOS-12323 adds ownership reach), a pull request (CHAOS-12308), a
// repository, a deployment, an incident, a document or an episode.
//
// An attribute map with no subject kind is denied: it is a synthetic
// repository or team decision, or an edge's own attributes, which only
// AuthorizedEdge decides for this caller.
//
// A requested project narrowing is refused (CHAOS-12324 intersects it with the
// grant once CHAOS-12229 maps raw ids to canonical ones); the requested
// repository and team narrowing still narrow.
func projectOnlyAdmits(principal storage.Principal, requested contextfabric.RequestedScope, attributes map[string]interface{}) bool {
	if len(requested.ProjectIDs) > 0 {
		return false
	}
	if len(requested.RepositorySlugs) > 0 && !requestedRepositoriesAdmit(attributes, requested.RepositorySlugs) {
		return false
	}
	if len(requested.TeamIDs) > 0 && !anyContainsAttr(attributes, authorizationTeamsAttr, requested.TeamIDs) {
		return false
	}
	switch contextfabric.SubjectKind(StringAttribute(attributes, "subject_kind")) {
	case contextfabric.SubjectOrganization:
		return callersOrganizationAttr(principal, attributes)
	case contextfabric.SubjectProject:
		// Exact: a canonical id is never trimmed or folded (CHAOS-12107 V3).
		return slices.Contains(principal.ProjectScopes, StringAttribute(attributes, "canonical_id"))
	case contextfabric.SubjectWorkItem:
		return ProjectMembershipOf(attributes, principal.ProjectScopes) == ProjectMembershipMember
	default:
		return false
	}
}

// AuthorizedEdge is the edge half of an edge's admission: the edge's own
// attributes. Every edge site also authorizes both endpoints through
// AuthorizedAttributes. For every class but project_only it is exactly
// AuthorizedAttributes over the edge's attributes.
//
// For a project-only caller (CHAOS-12299):
//   - E2: an attribution edge (DOCUMENTED_BY, HAS_EPISODE) is denied.
//   - A requested project narrowing is refused, as for a node.
//   - E1: the edge's repository and team lists are not consulted; the edge
//     defers to its endpoints. Its project list, when it is a real list,
//     must hold a granted canonical identity. The projection writes raw
//     project ids there, which never equal a canonical identity, so such an
//     edge is denied until CHAOS-12229 maps them. "*" or an absent list
//     defers to the endpoints; any other value denies.
func AuthorizedEdge(principal storage.Principal, requested contextfabric.RequestedScope, relation string, attributes map[string]interface{}) bool {
	if auth.ClassifyGrant(principal) != auth.GrantProjectOnly {
		return AuthorizedAttributes(principal, requested, attributes)
	}
	if len(requested.ProjectIDs) > 0 || IsObservationAttributionRelation(NormalizeRelation(relation)) {
		return false
	}
	raw, present := attributes[authorizationProjectsAttr]
	if !present {
		return true
	}
	switch projects := raw.(type) {
	case string:
		return projects == "*"
	case []string:
		return slices.ContainsFunc(projects, func(project string) bool { return slices.Contains(principal.ProjectScopes, project) })
	default:
		return false
	}
}

// AuthorizedThroughLink is the issue side of a link of record under a
// requested scope: the requested repository scope follows the link (the
// entity tree relates an issue to a repository only through its linked pull
// requests), so it is tested on the pull request at the other end of the link,
// never on the issue's own repository. The issue still meets the caller's
// grants and every other part of the request. Every reader that admits an
// issue through a link (the tree walks, the scoped work-item census and its
// cross-check, the census commit) uses this one predicate.
func AuthorizedThroughLink(principal storage.Principal, requested contextfabric.RequestedScope, attributes map[string]interface{}) bool {
	return AuthorizedAttributes(principal, issueScopeOfLink(requested), attributes)
}

// issueScopeOfLink is the part of a requested scope an issue reached through
// a link is tested against: everything but the repository slugs, which the
// link's pull request is tested against.
func issueScopeOfLink(requested contextfabric.RequestedScope) contextfabric.RequestedScope {
	requested.RepositorySlugs = nil
	return requested
}

// OwnsRepository reports whether a subject's own authorization_repositories
// property names repoSlug -- the subject's DECLARED ownership signal, never
// an authorization/visibility check against a principal or a caller-supplied
// scope. Exported so a caller building a member set for a specific anchor
// (falkorgraph's ownership-routed cohort discovery) can filter candidates by
// what they themselves declare owning, independently of AuthorizedAttributes,
// which answers a different question (is this principal allowed to see this
// node) and must not be conflated with "does this node own repoSlug."
func OwnsRepository(attributes map[string]interface{}, repoSlug string) bool {
	return scopeContainsAttr(attributes, authorizationRepositoriesAttr, repoSlug)
}

// requestedRepositoriesAdmit is the requested repository selector: the
// repository must be PRESENT on the node, as in the shared library's requested
// expression. The "*" selector therefore does not admit a node whose list
// holds only repository-absence sentinels (named in contracts/v1). Principal
// grants are tested elsewhere and keep their own "*" meaning.
func requestedRepositoriesAdmit(attributes map[string]interface{}, selectors []string) bool {
	for _, selector := range selectors {
		trimmed := strings.TrimSpace(selector)
		if contractsv1.ContextFabricIsRepositoryAbsenceSentinel(trimmed) {
			// A sentinel records that no repository is present; naming it is
			// not naming a repository.
			continue
		}
		if trimmed == "*" && !repositoryPresent(attributes) {
			continue
		}
		if scopeContainsAttr(attributes, authorizationRepositoriesAttr, selector) {
			return true
		}
	}
	return false
}

// repositoryPresent reports whether the node's repository list names at least
// one entry that is not a repository-absence sentinel. The "*" list is the
// unrestricted marker, not a repository, and keeps its existing treatment.
func repositoryPresent(attributes map[string]interface{}) bool {
	entries, isList := attributes[authorizationRepositoriesAttr].([]string)
	if !isList {
		return true
	}
	return slices.ContainsFunc(entries, func(entry string) bool {
		return !contractsv1.ContextFabricIsRepositoryAbsenceSentinel(entry)
	})
}

func anyContainsAttr(attributes map[string]interface{}, key string, values []string) bool {
	for _, value := range values {
		if scopeContainsAttr(attributes, key, value) {
			return true
		}
	}
	return false
}

// repositoryWildcardAttr reports whether a node or edge carries the "*"
// repository list (graphrank's shared attribute convention; see the top of
// this file).
func repositoryWildcardAttr(attributes map[string]interface{}) bool {
	value, isString := attributes[authorizationRepositoriesAttr].(string)
	return isString && value == "*"
}

// callersOrganizationAttr reports whether the attributes are the caller's own
// organization entity's: its authorization_projects list names the reserved
// organization scope id of the caller's org.
func callersOrganizationAttr(principal storage.Principal, attributes map[string]interface{}) bool {
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" {
		return false
	}
	projects, isList := attributes[authorizationProjectsAttr].([]string)
	return isList && slices.Contains(projects, contractsv1.ContextFabricReservedOrganizationScopePrefix+orgID)
}
