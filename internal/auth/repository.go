package auth

import (
	"fmt"
	"sort"
	"strings"

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
