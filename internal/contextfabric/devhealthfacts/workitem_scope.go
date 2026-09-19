package devhealthfacts

import (
	"context"
	"errors"
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

// workItemAuthorizedIDsSQL is the set of work_item_id values the scope
// authorizes, for a statement that names a work item by its id alone (a
// dependency edge carries no repository). An id is in the set only when
// EVERY stored row carrying it is authorized, so an id shared by an
// authorized and an unauthorized row is left out; an id with no work_items
// row is left out too. The rule is the library's, over its own aliases,
// inside an uncorrelated subquery, so it never meets the caller's aliases.
func workItemAuthorizedIDsSQL(scope readers.AuthorizationScope) (string, []readers.Binding) {
	rendered := readers.WorkItemScopeSQL(scope)
	return `(SELECT w.work_item_id FROM work_items AS w FINAL ` + rendered.JoinSQL + ` WHERE w.org_id = {org_id:String} GROUP BY w.work_item_id HAVING min(toUInt8(` + rendered.AuthorizationExpr + `)) = 1)`, rendered.Bindings
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

	normalized, err := auth.NormalizeRepositorySlug(trimmed)
	if err != nil {
		return 0, "", false
	}
	return workItemSelectorExact, normalized, true
}

// Membership, status, title and actual-completion statements share these
// fixed private resource bounds. The physical scan limit is independent of
// each reader's result probe: ClickHouse counts source rows before WHERE,
// LIMIT and FINAL. These finite bounds do not promise that every input fits.
//
// workItemReaderMaxRowsToRead is sized for the library authorization
// relation, not for the id-keyed page alone. The rule joins two
// organization-wide aggregates (project -> owning team -> owned repository,
// and issue -> linked pull request repository), and ClickHouse counts every
// relation a statement reads, joined or subqueried, against
// max_rows_to_read (executed on 24.8 and 26.7: an IN subquery over a
// 100-row table breaks a 150-row cap on a 100-row outer read). So the read
// grows with the organization, not with the page. DERIVED the same way as
// workItemMembershipMaxRowsToRead, from system.query_log on the trial
// store's 1675-item project: status for a 200-id page 23,895 rows (10
// ReadFromMergeTree passes, EXPLAIN indexes=1), the project roll-up 23,948
// (11 passes). The status figure is the granule floor exactly: work_items
// 5,333 + team_project_ownership 6,242 x2 (the ownership join's two arms) +
// team_repo_ownership 2,715 + work_graph_issue_pr 2,894 + projects 53 x2 +
// repos 121 x3, every table read whole because each is below one 8192-row
// granule. Largest (23,948) x60 for a large organization, x2 margin =
// 2,873,760, rounded up.
const (
	workItemReaderMaxRowsToRead  = uint64(3_000_000)
	workItemReaderMaxMemoryUsage = uint64(64 << 20)
	workItemReaderMaxThreads     = uint64(1)
)

var errWorkItemReaderDeadlineTooShort = errors.New("work item reader deadline is too short for a bounded query")

// workItemReaderSettings applies the shared resource class to content reads
// with the existing R+1 result probe. Every overflow mode renders as throw;
// the caller context remains the final deadline authority. S1 uses its own
// deadline/error handling and K+1 result probe with the same resource bounds.
func workItemReaderSettings(ctx context.Context) (readers.Settings, error) {
	if err := ctx.Err(); err != nil {
		return readers.Settings{}, err
	}
	settings := readers.Settings{
		MaxRowsToRead:  workItemReaderMaxRowsToRead,
		MaxMemoryUsage: workItemReaderMaxMemoryUsage,
		MaxThreads:     workItemReaderMaxThreads,
		MaxResultRows:  uint64(maxFactRowsProbe),
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < time.Second {
			return readers.Settings{}, errWorkItemReaderDeadlineTooShort
		}
		seconds := uint64(remaining / time.Second)
		if seconds > 0 {
			settings.MaxExecutionTimeSeconds = seconds
		}
	} else {
		settings.MaxExecutionTimeSeconds = uint64(defaultTimeout / time.Second)
	}
	return settings, nil
}
