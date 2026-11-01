package auth

import (
	"fmt"
	"sort"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/reposlug"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// NormalizeRepositoryScopes accepts exact owner/repo scopes, owner/* scopes,
// or the explicit global wildcard. Empty repository scope is forbidden.
func NormalizeRepositoryScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return nil, fmt.Errorf("%w: at least one repository scope is required", ErrInvalidCredential)
	}
	seen := make(map[string]struct{}, len(scopes))
	result := make([]string, 0, len(scopes))
	for _, raw := range scopes {
		scope := strings.ToLower(strings.TrimSpace(raw))
		// CHAOS-12298: the reserved prefix is acr's own (a project-only
		// principal's repository list), never a credential's scope. Refused
		// by name, before the slug grammar, so it stays refused if the
		// grammar ever widens.
		if contractsv1.IsReservedRepositoryScope(scope) {
			return nil, fmt.Errorf("%w: reserved repository scope prefix %q", ErrInvalidCredential, raw)
		}
		if !validRepositoryScope(scope) {
			return nil, fmt.Errorf("%w: invalid repository scope %q", ErrInvalidCredential, raw)
		}
		if _, duplicate := seen[scope]; duplicate {
			continue
		}
		seen[scope] = struct{}{}
		result = append(result, scope)
	}
	sort.Strings(result)
	return result, nil
}

func NormalizeRepositorySlug(slug string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(slug))
	parts := strings.Split(normalized, "/")
	if len(parts) != 2 || !reposlug.ValidOwner(parts[0]) || !reposlug.ValidName(parts[1]) {
		return "", fmt.Errorf("%w: invalid repository slug", ErrRepositoryForbidden)
	}
	return normalized, nil
}

func RepositoryAllowed(scopes []string, slug string) bool {
	normalized, err := NormalizeRepositorySlug(slug)
	if err != nil {
		return false
	}
	owner, _, _ := strings.Cut(normalized, "/")
	for _, raw := range scopes {
		scope := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case contractsv1.IsReservedRepositoryScope(scope):
			// CHAOS-12298: a reserved value is never a grant.
			continue
		case scope == "*":
			return true
		case scope == normalized:
			return true
		case scope == owner+"/*":
			return true
		}
	}
	return false
}

// AuthorizeRepository refuses a project-only principal every repository,
// whatever its repository list holds: a project grant names no repository
// (CHAOS-12299).
func AuthorizeRepository(principal storage.Principal, slug string) error {
	if ClassifyGrant(principal) == GrantProjectOnly || !RepositoryAllowed(principal.RepositoryScopes, slug) {
		return ErrRepositoryForbidden
	}
	return nil
}

func validRepositoryScope(scope string) bool {
	if scope == "*" {
		return true
	}
	parts := strings.Split(scope, "/")
	if len(parts) != 2 || !reposlug.ValidOwner(parts[0]) {
		return false
	}
	return parts[1] == "*" || reposlug.ValidName(parts[1])
}

// repositorySlugFault says which part of a rejected owner/repo slug is wrong
// and why, for logs. It is only called for a slug NormalizeRepositorySlug
// refused.
func repositorySlugFault(slug string) string {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(slug)), "/")
	if len(parts) != 2 {
		return "not of the form owner/repo"
	}
	if fault := reposlug.Fault(parts[0], true); fault != "" {
		return "owner " + fault
	}
	if fault := reposlug.Fault(parts[1], false); fault != "" {
		return "repository " + fault
	}
	return "invalid repository slug"
}

// repositoryScopeCovers reports whether a parent repository scope grants every
// repository the requested scope grants: everything covers all, an owner
// wildcard covers itself and that owner's repositories, a repository covers
// only itself.
func repositoryScopeCovers(parent, requested string) bool {
	switch {
	case parent == "*", parent == requested:
		return true
	case strings.HasSuffix(parent, "/*"):
		owner, _, found := strings.Cut(requested, "/")
		return found && owner == strings.TrimSuffix(parent, "/*")
	default:
		return false
	}
}

// rotationWithinParent reports whether a rotation's normalized successor asks
// for no scope and no repository its parent lacks. The parent's own lists are
// the bound, so an empty list grants nothing and never reads as "all".
func rotationWithinParent(parent contractsv1.ClientCredential, successor CreateCredentialRequest) bool {
	parentScopes := make(map[string]struct{}, len(parent.Scopes))
	for _, scope := range parent.Scopes {
		parentScopes[strings.TrimSpace(scope)] = struct{}{}
	}
	for _, scope := range successor.Scopes {
		if _, ok := parentScopes[scope]; !ok {
			return false
		}
	}
	for _, requested := range successor.RepositoryScopes {
		covered := false
		for _, held := range parent.RepositoryScopes {
			if repositoryScopeCovers(strings.ToLower(strings.TrimSpace(held)), requested) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}
