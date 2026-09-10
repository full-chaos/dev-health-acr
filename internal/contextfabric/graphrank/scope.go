package graphrank

import (
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// ScopeMatch reports whether a node/edge's decoded authorization list (a
// plain []string of "owner/repo" slugs -- however the backend actually
// stores that list; zepgraph decodes its pipe-encoded attribute string
// before calling this, falkorgraph can pass a native list property
// directly) admits the caller-side scope value. value is one entry from a
// principal's storage.Principal.RepositoryScopes (or a requested-scope
// filter), which, per internal/auth.RepositoryAllowed, may be an exact
// "owner/repo" slug, the global wildcard "*", or an "owner/*" wildcard.
//
// This is the exact wildcard-matching core zepgraph's scopeContains used
// (Codex findings G3(a)/G3(b): a missing/empty list must deny, never
// authorize, regardless of how permissive the caller-side value is; an
// "owner/*" match must validate each decoded entry as a well-formed slug via
// internal/auth.NormalizeRepositorySlug before trusting its owner prefix, so
// a malformed entry like "acme/" or "acme/not/real" cannot satisfy the
// wildcard by prefix alone) -- extracted unchanged so it cannot drift
// between backends. Backend-specific "is this list absent/malformed at the
// wire level" handling (zepgraph's fail-closed sentinel for an unencodable
// write) stays in the backend package; entries reaching this function are
// assumed to already be the backend's best-effort decoded list, which may
// legitimately be empty (denies, per the caller loop below).
func ScopeMatch(entries []string, value string, kind ScopeValueKind) bool {
	value = strings.TrimSpace(value)
	if value == "*" {
		return true
	}
	if owner, ok := strings.CutSuffix(value, "/*"); ok && owner != "" {
		// THE OWNER WILDCARD IS KIND-AWARE TOO, and it has to be: chris's
		// rule binds the whole function, not the arm that happened to be
		// under review. An arm that still folds an IDENTIFIER's owner is the
		// same defect as the exact arm had, reached by a different route --
		// scope "ACME/*" authorized project id "acme/team", reproduced on the
		// tip before this fix.
		if kind != ScopeValueRepositoryName {
			// IDENTIFIER: the owner segment compares BYTE FOR BYTE, and the
			// entry is NOT validated as a repository slug -- an id is not a
			// slug and rejecting it for failing to parse as one would deny
			// every legitimate id under a prefix scope.
			//
			// The "/" must actually be present. Without that, prefix "ACME"
			// would match an id "ACMEX-1", which is a different id.
			for _, entry := range entries {
				entryOwner, _, found := strings.Cut(entry, "/")
				if found && entryOwner == owner {
					return true
				}
			}
			return false
		}
		owner = strings.ToLower(owner)
		for _, entry := range entries {
			normalized, err := auth.NormalizeRepositorySlug(entry)
			if err != nil {
				continue
			}
			if entryOwner, _, _ := strings.Cut(normalized, "/"); entryOwner == owner {
				return true
			}
		}
		return false
	}
	for _, entry := range entries {
		if exactScopeMatch(entry, value, kind) {
			return true
		}
	}
	return false
}

// ScopeValueKind names WHAT the caller-side value IS, so exactScopeMatch can
// apply the ruled comparison instead of inferring one from the value's shape.
//
// THE RULE, chris 2026-09-09, verbatim: "ids need to be case-sensitive
// specifically but named aliases shouldn't e.g project_id vs name I suppose."
//
// The first version of this seam inferred the kind: it normalised anything
// that PARSED as "owner/repo" and compared everything else byte for byte. That
// is wrong wherever an ID happens to be shaped like a slug -- codex r2 F1,
// reproduced in both arms before this fix:
//
//	project id "acme/team" vs authorization_projects ["ACME/TEAM"] -> authorized
//
// A project id and a team id are IDs whatever they look like, and folding two
// of them together hands a principal scoped for one the authority of the
// other. The kind is never a property of the string; it is a property of WHICH
// LIST is being consulted, and every call site already knows that
// (scopeContainsAttr holds the authorization_* key). So it is passed, not
// guessed.
//
// The ZERO VALUE IS THE IDENTIFIER, deliberately: a call site that forgets to
// state a kind gets the case-SENSITIVE comparison, which denies where the
// other would admit. A defaulting mistake here must fail closed.
type ScopeValueKind int

const (
	// ScopeValueIdentifier is an ID minted by a system -- a project id, a
	// team id. Compared byte for byte. The zero value, so this is also
	// what an unstated kind means.
	ScopeValueIdentifier ScopeValueKind = iota
	// ScopeValueRepositoryName is a repository slug: a NAMED ALIAS a human
	// types, where "example-org/widget-service" and
	// "EXAMPLE-ORG/WIDGET-SERVICE" are the same repository. Compared
	// case-insensitively through auth.NormalizeRepositorySlug.
	ScopeValueRepositoryName
)

// exactScopeMatch compares one authorization entry with one caller-side scope
// on the NON-wildcard path, under the rule stated on ScopeValueKind.
//
// On ScopeValueRepositoryName both sides go through
// auth.NormalizeRepositorySlug, which lowercases and validates the
// "owner/repo" shape. If EITHER side fails to parse it is not a well-formed
// repository name and cannot be name-matched, so the comparison falls back to
// exact -- a malformed entry like "acme/" still only ever matches itself.
//
// On ScopeValueIdentifier the comparison is exact, unconditionally, no matter
// what the value looks like.
//
// This function is where the two cases meet: scopeContainsAttr reaches it for
// authorization_repositories (names) AND for authorization_projects and
// authorization_teams (ids). The pin beside it asserts BOTH halves -- a
// case-differing slug matches, a case-differing team or project id does not,
// including the ids that LOOK like slugs -- so the distinction is enforced by
// a test rather than described by this comment.
//
// The name half also closed a real divergence: the pushed-down work-item
// predicate agreed with auth.RepositoryAllowed and disagreed with this
// function, so the two authorization gates could reach opposite answers about
// the same repository.
func exactScopeMatch(entry, value string, kind ScopeValueKind) bool {
	if kind != ScopeValueRepositoryName {
		return entry == value
	}
	normalizedEntry, entryErr := auth.NormalizeRepositorySlug(entry)
	normalizedValue, valueErr := auth.NormalizeRepositorySlug(value)
	if entryErr == nil && valueErr == nil {
		return normalizedEntry == normalizedValue
	}
	return entry == value
}

// AnyScopeMatch reports whether ScopeMatch admits any of values, all of the
// same kind.
func AnyScopeMatch(entries []string, values []string, kind ScopeValueKind) bool {
	for _, value := range values {
		if ScopeMatch(entries, value, kind) {
			return true
		}
	}
	return false
}
