package v1

import "testing"

// CHAOS-12298 (c2): the reserved repository-scope prefix never enters acr as a
// repository through the projection port. A project-only principal's
// repository list holds the reserved value (c5), so a node carrying it would
// be matched by the raw-equality fallback of a repository matcher.
func TestAProjectionAuthorizationScopeRefusesTheReservedRepositoryPrefix(t *testing.T) {
	for _, slug := range []string{"acr-reserved:project-only", "ACR-RESERVED:project-only", " acr-reserved:anything", "acr-reserved:"} {
		scope := ContextFabricAuthorizationScope{RepositorySlugs: []string{"acme/api", slug}}
		if err := scope.Validate(); err == nil {
			t.Errorf("Validate admitted the reserved repository slug %q", slug)
		}
		if err := scope.validateEntity(); err == nil {
			t.Errorf("validateEntity admitted the reserved repository slug %q", slug)
		}
	}
	// A project or team id is not a repository slug: the prefix is reserved
	// on the repository list only.
	for _, scope := range []ContextFabricAuthorizationScope{
		{RepositorySlugs: []string{"acme/api"}},
		{RepositorySlugs: []string{ContextFabricNoRepositorySentinel}},
		{ProjectIDs: []string{"acr-reserved:project-only"}},
	} {
		if err := scope.Validate(); err != nil {
			t.Errorf("Validate refused %+v: %v", scope, err)
		}
	}
}

// IsReservedRepositoryScope compares as repository scopes are compared:
// trimmed and case-insensitive.
func TestTheReservedRepositoryScopeCarriesTheReservedPrefix(t *testing.T) {
	for _, value := range []string{ProjectOnlyRepositoryScope, " acr-reserved:project-only", "acr-reserved:x\t", "ACR-Reserved:project-only", "acr-reserved:"} {
		if !IsReservedRepositoryScope(value) {
			t.Errorf("%q does not read as reserved", value)
		}
	}
	for _, value := range []string{"acme/api", "*", "acme/*", "", ContextFabricNoRepositorySentinel, "xacr-reserved:project-only"} {
		if IsReservedRepositoryScope(value) {
			t.Errorf("%q reads as reserved", value)
		}
	}
}
