package auth

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func normalizedPrincipalRepositories(principal storage.Principal) ([]string, error) {
	repositories, err := NormalizeRepositoryScopes(principal.RepositoryScopes)
	if err != nil || !slices.Equal(repositories, principal.RepositoryScopes) {
		return nil, ErrInvalidDeviceFlow
	}
	return repositories, nil
}

func intersectRepositoryHints(hints, repositories []string) []string {
	result := make([]string, 0, len(hints))
	for _, hint := range hints {
		if RepositoryAllowed(repositories, hint) {
			result = append(result, hint)
		}
	}
	return result
}

func normalizeDeviceAuthorizationHints(hints DeviceAuthorizationHints) (DeviceAuthorizationHints, error) {
	if strings.TrimSpace(hints.OrganizationIDHint) != hints.OrganizationIDHint || utf8.RuneCountInString(hints.OrganizationIDHint) > 128 {
		return DeviceAuthorizationHints{}, ErrInvalidDeviceFlow
	}
	if hints.RepositoryHints == nil {
		return hints, nil
	}
	repositories, err := NormalizeRepositoryScopes(hints.RepositoryHints)
	if err != nil || hasRepositoryWildcard(repositories) {
		return DeviceAuthorizationHints{}, ErrInvalidDeviceFlow
	}
	return DeviceAuthorizationHints{OrganizationIDHint: hints.OrganizationIDHint, RepositoryHints: repositories}, nil
}
