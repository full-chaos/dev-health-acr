package graphrank

import (
	"slices"
	"strings"

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
	if len(requested.RepositorySlugs) > 0 && !anyContainsAttr(attributes, authorizationRepositoriesAttr, requested.RepositorySlugs) {
		return false
	}
	if len(requested.ProjectIDs) > 0 && !anyContainsAttr(attributes, authorizationProjectsAttr, requested.ProjectIDs) {
		return false
	}
	if len(requested.TeamIDs) > 0 && !anyContainsAttr(attributes, authorizationTeamsAttr, requested.TeamIDs) {
		return false
	}
	return true
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
