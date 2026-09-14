package devhealthfacts

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// workItemRepositoryAuthorization is the one translation point between the
// ACR repository-scope contract and the reader's typed selector contract.
// ACR credentials use an empty grant list for the organization-wide
// convention, while the reader's zero selector set denies. ACR requests use
// both nil and empty RepositorySlugs for no requested restriction, while the
// reader distinguishes a nil requested set from an explicit zero set.
// Keeping those translations here prevents a raw request list from becoming
// an authorization channel and keeps all three work-item readers on one
// selector shape.
func workItemRepositoryAuthorization(principal storage.Principal, requested []string) readers.AuthorizationScope {
	granted := workItemRepositorySelectorSet(principal.RepositoryScopes, true)
	var requestedSet *readers.RepositorySelectorSet
	if len(requested) > 0 {
		set := workItemRepositorySelectorSet(requested, false)
		requestedSet = &set
	}
	return readers.AuthorizationScope{
		RepositorySelectors: &readers.RepositorySelectorScope{
			Granted:   granted,
			Requested: requestedSet,
		},
	}
}

// workItemRepositorySelectorSet normalizes the same exact and wildcard
// forms accepted by auth.NormalizeRepositoryScopes. Exact selectors also go
// through auth.NormalizeRepositorySlug, which is the repository-name
// normalizer used by auth.RepositoryAllowed and graphrank.ScopeMatch.
// Invalid selectors are dropped. A non-empty input that contains no valid
// selector therefore returns a zero set, which is an intentional deny for a
// requested scope (and a fail-closed grant for malformed principal data).
func workItemRepositorySelectorSet(values []string, grants bool) readers.RepositorySelectorSet {
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
		normalized, err := auth.NormalizeRepositoryScopes([]string{trimmed})
		if err != nil || len(normalized) != 1 {
			return 0, "", false
		}
		owner, ok := strings.CutSuffix(normalized[0], "/*")
		if !ok || owner == "" {
			return 0, "", false
		}
		return workItemSelectorOwner, owner, true
	}

	normalized, err := auth.NormalizeRepositorySlug(trimmed)
	if err != nil {
		return 0, "", false
	}
	return workItemSelectorExact, normalized, true
}

// workItemReaderSettings provides the same fixed, caller-independent
// statement ceilings to status, title and actual-completion reads. The
// result ceiling matches the existing R+1 probe. max_rows_to_read is a
// separate physical-scan budget: ClickHouse counts source rows read before
// the WHERE/LIMIT and FINAL steps, so it cannot be derived from the 201-row
// result probe. The 10,000-row ceiling allows the keyed work-item lookup to
// scan a bounded set of MergeTree parts while still failing loudly for a
// pathological or unselective read. 512 MiB leaves room for the same
// work_items↔repos FINAL join and selection buffers; both values remain
// finite per statement. Every configured overflow mode is rendered as throw
// by the readers package; a caller context remains the final deadline
// authority.
const (
	workItemReaderMaxRowsToRead = uint64(10000)
	// ClickHouse's MergeTree reader accounts its per-query selection buffers
	// against this setting. A simple one-row read measured at ~99 MiB on the
	// hosted 26.7 image, so 64 MiB rejected valid reads before the predicate
	// could be evaluated. 512 MiB remains a finite per-statement ceiling while
	// leaving room for the bounded physical scan above.
	workItemReaderMaxMemoryUsage = uint64(512 << 20)
)

func workItemReaderSettings(ctx context.Context) readers.Settings {
	settings := readers.Settings{
		MaxRowsToRead:  workItemReaderMaxRowsToRead,
		MaxMemoryUsage: workItemReaderMaxMemoryUsage,
		MaxResultRows:  uint64(maxFactRowsProbe),
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 {
			seconds := uint64(remaining / time.Second)
			if seconds > 0 {
				settings.MaxExecutionTimeSeconds = seconds
			}
		}
	} else {
		settings.MaxExecutionTimeSeconds = uint64(defaultTimeout / time.Second)
	}
	return settings
}
