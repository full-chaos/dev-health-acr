package devhealthsource

import (
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// maxCensusRepositoryFilter bounds the bindings one filtered census carries.
const maxCensusRepositoryFilter = 200

// withRepositoryFilter ANDs a repository restriction onto predicate so the
// census count and satisfier set are computed inside the caller's narrowing.
// Repositories are keyed by repos.repo, trimmed and lower-cased like the graph-side comparison (the owner/name the graph's
// authorization_repositories also carries) and joined to the base table's
// repo_id. It returns the predicate unchanged and false when the filter cannot
// be applied exactly -- a kind with no repository column, a wildcard, a slug
// that is not a well-formed owner/name or owner/* entry, or too many slugs --
// so the caller falls back to the post-filter alone.
func withRepositoryFilter(kind graphrank.CensusKind, predicate CensusPredicate, slugs []string) (CensusPredicate, bool) {
	entry, ok := censusKindRegistryEntries[kind]
	if !ok {
		return predicate, false
	}
	column, ok := entry.anchorColumns[contextfabric.SubjectRepository]
	if !ok || len(slugs) == 0 || len(slugs) > maxCensusRepositoryFilter {
		return predicate, false
	}
	var clauses []string
	var bindings []contextpacket.ClickHouseBinding
	for i, slug := range slugs {
		value := strings.ToLower(strings.TrimSpace(slug))
		name := fmt.Sprintf("census_repo_%d", i)
		if owner, wildcard := strings.CutSuffix(value, "/*"); wildcard {
			normalized, err := auth.NormalizeRepositorySlug(owner + "/x")
			if err != nil || owner == "" {
				return predicate, false
			}
			owner, _, _ = strings.Cut(normalized, "/")
			clauses = append(clauses, fmt.Sprintf("startsWith(lower(trimBoth(repo)), {%s:String})", name))
			bindings = append(bindings, contextpacket.ClickHouseBinding{Name: name, Value: owner + "/"})
			continue
		}
		normalized, err := auth.NormalizeRepositorySlug(value)
		if err != nil {
			return predicate, false
		}
		clauses = append(clauses, fmt.Sprintf("lower(trimBoth(repo)) = {%s:String}", name))
		bindings = append(bindings, contextpacket.ClickHouseBinding{Name: name, Value: normalized})
	}
	predicate.SQL = fmt.Sprintf("(%s) AND toString(%s) IN (SELECT toString(id) FROM repos FINAL WHERE org_id = {census_org_id:String} AND (%s))",
		predicate.SQL, column, strings.Join(clauses, " OR "))
	predicate.Bindings = append(append([]contextpacket.ClickHouseBinding(nil), predicate.Bindings...), bindings...)
	return predicate, true
}
