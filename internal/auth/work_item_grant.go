package auth

import (
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-go/readers"
)

// WorkItemRepositorySelectorSet normalizes the same exact and wildcard
// forms accepted by NormalizeRepositoryScopes. Exact selectors also go
// through NormalizeRepositorySlug, which is the repository-name
// normalizer used by auth.RepositoryAllowed and graphrank.ScopeMatch.
// Invalid selectors are dropped. A non-empty input that contains no valid
// selector therefore returns a zero set, which is an intentional deny for a
// requested scope (and a fail-closed grant for malformed principal data).
func WorkItemRepositorySelectorSet(values []string, grants bool) readers.RepositorySelectorSet {
	if grants && len(values) == 0 {
		return readers.RepositorySelectorSet{All: true}
	}

	set := readers.RepositorySelectorSet{}
	exact := make(map[string]struct{}, len(values))
	owners := make(map[string]struct{}, len(values))
	for _, raw := range values {
		kind, value, ok := workItemRepositorySelector(raw)
		if !ok {
			continue
		}
		switch kind {
		case workItemSelectorAll:
			// The global wildcard is an unrestricted grant. Drop any
			// narrower entries that arrived beside it so the typed value has
			// one canonical representation of that policy.
			return readers.RepositorySelectorSet{All: true}
		case workItemSelectorOwner:
			owners[value] = struct{}{}
		case workItemSelectorExact:
			exact[value] = struct{}{}
		}
	}
	for value := range exact {
		set.ExactSlugs = append(set.ExactSlugs, value)
	}
	for value := range owners {
		set.Owners = append(set.Owners, value)
	}
	sort.Strings(set.ExactSlugs)
	sort.Strings(set.Owners)
	return set
}

type workItemSelectorKind uint8

const (
	workItemSelectorExact workItemSelectorKind = iota + 1
	workItemSelectorOwner
	workItemSelectorAll
)

func workItemRepositorySelector(raw string) (workItemSelectorKind, string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "*" {
		return workItemSelectorAll, "", true
	}

	if strings.HasSuffix(strings.ToLower(trimmed), "/*") {
		normalized, err := NormalizeRepositoryScopes([]string{trimmed})
		if err != nil {
			return 0, "", false
		}
		if len(normalized) != 1 {
			return 0, "", false
		}
		owner, ok := strings.CutSuffix(normalized[0], "/*")
		if !ok {
			return 0, "", false
		}
		if owner == "" {
			return 0, "", false
		}
		return workItemSelectorOwner, owner, true
	}

	normalized, err := NormalizeRepositorySlug(trimmed)
	if err != nil {
		return 0, "", false
	}
	return workItemSelectorExact, normalized, true
}
