package auth

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var repositoryPartPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,98}[a-z0-9])?$`)

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
	if len(parts) != 2 || !repositoryPartPattern.MatchString(parts[0]) || !repositoryPartPattern.MatchString(parts[1]) {
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

func AuthorizeRepository(principal storage.Principal, slug string) error {
	if !RepositoryAllowed(principal.RepositoryScopes, slug) {
		return ErrRepositoryForbidden
	}
	return nil
}

func validRepositoryScope(scope string) bool {
	if scope == "*" {
		return true
	}
	parts := strings.Split(scope, "/")
	if len(parts) != 2 || !repositoryPartPattern.MatchString(parts[0]) {
		return false
	}
	return parts[1] == "*" || repositoryPartPattern.MatchString(parts[1])
}
