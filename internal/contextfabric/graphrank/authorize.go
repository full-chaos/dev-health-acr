package graphrank

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
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
	if len(principal.RepositoryScopes) > 0 {
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

func anyContainsAttr(attributes map[string]interface{}, key string, values []string) bool {
	for _, value := range values {
		if scopeContainsAttr(attributes, key, value) {
			return true
		}
	}
	return false
}
