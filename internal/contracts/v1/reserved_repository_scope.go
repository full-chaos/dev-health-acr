package v1

import "strings"

// ReservedRepositoryScopePrefix (CHAOS-12298, c2 of CHAOS-12228) marks a
// repository-scope value acr reserves for itself. It is never a repository: it
// is refused where repository names enter acr (credential scopes,
// auth.NormalizeRepositoryScopes; the projection port,
// ContextFabricAuthorizationScope.Validate; the source slug producer) and is
// denied in the raw-equality matchers (auth.RepositoryAllowed,
// graphrank.ScopeMatch). No digest, reuse key or census proof hashes it as a
// repository. Design: docs/design/context-fabric-project-only-scope.md
// ("Digests and reuse").
const ReservedRepositoryScopePrefix = "acr-reserved:"

// ProjectOnlyRepositoryScope is the one value a project-only principal's
// repository list holds (set by the access-read decoder, c5 CHAOS-12301), so
// any site that still reads the raw list sees a restricted list that matches
// no repository, never the empty (organization-wide) one.
const ProjectOnlyRepositoryScope = ReservedRepositoryScopePrefix + "project-only"

// IsReservedRepositoryScope reports whether value carries the reserved
// prefix, compared as repository scopes are: trimmed and case-insensitive.
func IsReservedRepositoryScope(value string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), ReservedRepositoryScopePrefix)
}

func containsReservedRepositoryScope(values []string) bool {
	for _, value := range values {
		if IsReservedRepositoryScope(value) {
			return true
		}
	}
	return false
}
