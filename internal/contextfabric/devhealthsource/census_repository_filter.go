package devhealthsource

import (
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/reposlug"
)

// maxCensusRepositoryFilter bounds the bindings one filtered census carries.
const maxCensusRepositoryFilter = 200

// requestedOwnerClause tests an owner wildcard of a requested filter. The
// graph-side post-filter still decides on the result, so a prefix is enough.
const requestedOwnerClause = "startsWith(lower(trimBoth(repo)), {%s:String})"

// grantOwnerClause tests an owner wildcard of a grant, which has no post-filter.
// It admits what the graph gate admits for that owner, a valid two-part slug,
// and not only the prefix: acme/* does not cover acme/group/secret, because
// NormalizeRepositorySlug refuses that slug (codex r2, CHAOS-12455).
const grantOwnerClause = "(startsWith(lower(trimBoth(repo)), {%s:String}) AND match(lower(trimBoth(repo)), '^[^/]+/" + reposlug.NamePattern + "$'))"

// withRepositoryFilter ANDs a repository restriction onto predicate so the
// census count and satisfier set are computed inside the caller's narrowing.
// Repositories are keyed by repos.repo, trimmed and lower-cased like the graph-side comparison (the owner/name the graph's
// authorization_repositories also carries) and joined to the base table's
// repo_id. It returns the predicate unchanged and false when the filter cannot
// be applied exactly -- a kind with no repository column, a wildcard, a slug
// that is not a well-formed owner/name or owner/* entry, or too many slugs --
// so the caller falls back to the post-filter alone.
func withRepositoryFilter(kind graphrank.CensusKind, predicate CensusPredicate, slugs []string) (CensusPredicate, bool) {
	return andRepositories(kind, predicate, slugs, "census_repo", requestedOwnerClause)
}

// withGrantFilter ANDs the caller's repository grant onto predicate, through
// the same restriction under its own binding names, so a requested narrowing
// can sit beside it (CHAOS-12452). A false return is the end of the read: a
// grant has no post-filter to fall back to, so NewCensusFunc refuses the
// census with no statement. The same conditions make it false, and a reserved
// value is one of the slugs that is not well-formed.
func withGrantFilter(kind graphrank.CensusKind, predicate CensusPredicate, grant []string) (CensusPredicate, bool) {
	return andRepositories(kind, predicate, grant, "census_grant", grantOwnerClause)
}

func andRepositories(kind graphrank.CensusKind, predicate CensusPredicate, slugs []string, bindingPrefix, ownerClause string) (CensusPredicate, bool) {
	entry, ok := censusKindRegistryEntries[kind]
	if !ok {
		return predicate, false
	}
	column := entry.repositoryColumn
	if column == "" {
		return predicate, false
	}
	clauses, bindings, ok := repositoryClauses(slugs, bindingPrefix, ownerClause)
	if !ok {
		return predicate, false
	}
	predicate.SQL = fmt.Sprintf("(%s) AND toString(%s) IN (SELECT toString(id) FROM repos FINAL WHERE org_id = {census_org_id:String} AND (%s))",
		predicate.SQL, column, strings.Join(clauses, " OR "))
	predicate.Bindings = append(append([]contextpacket.ClickHouseBinding(nil), predicate.Bindings...), bindings...)
	return predicate, true
}

// repositoryClauses renders one clause and binding per slug. False means the
// list cannot be applied exactly: empty, too long, or holding an entry that is
// not a well-formed owner/name or owner/*, a bare "*" and a reserved value
// among them.
func repositoryClauses(slugs []string, bindingPrefix, ownerClause string) ([]string, []contextpacket.ClickHouseBinding, bool) {
	if len(slugs) == 0 || len(slugs) > maxCensusRepositoryFilter {
		return nil, nil, false
	}
	var clauses []string
	var bindings []contextpacket.ClickHouseBinding
	for i, slug := range slugs {
		value := strings.ToLower(strings.TrimSpace(slug))
		name := fmt.Sprintf("%s_%d", bindingPrefix, i)
		if owner, wildcard := strings.CutSuffix(value, "/*"); wildcard {
			normalized, err := auth.NormalizeRepositorySlug(owner + "/x")
			if err != nil || owner == "" {
				return nil, nil, false
			}
			owner, _, _ = strings.Cut(normalized, "/")
			clauses = append(clauses, fmt.Sprintf(ownerClause, name))
			bindings = append(bindings, contextpacket.ClickHouseBinding{Name: name, Value: owner + "/"})
			continue
		}
		normalized, err := auth.NormalizeRepositorySlug(value)
		if err != nil {
			return nil, nil, false
		}
		clauses = append(clauses, fmt.Sprintf("lower(trimBoth(repo)) = {%s:String}", name))
		bindings = append(bindings, contextpacket.ClickHouseBinding{Name: name, Value: normalized})
	}
	return clauses, bindings, true
}

// grantApplies is the check every grant passes before any read. The work-item
// census needs it on its own: the library reads an empty list and a "*" as the
// organization-wide grant.
func grantApplies(grant []string) bool {
	_, _, ok := repositoryClauses(grant, "census_grant", grantOwnerClause)
	return ok
}
