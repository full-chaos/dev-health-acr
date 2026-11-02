package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12505. A work-item handle under a requested repository scope names one
// item, or a few: the census already lists them. So the scope is read keyed on
// those ids, never by walking the scoped repositories' whole issue population:
// from the ids' work items, their LINKS_PULL_REQUEST rows to pull requests of a
// scoped repository. The cost is bounded by the ids and their links, never by a
// repository's size, so a repository with more linked issues than the census
// bound is not a cut walk.
//
// Each row is decided by the repository walk's own rule, not a copy of it:
//   - the repository by graphrank.AuthorizedAttributes, as linkScopedIssues
//     decides each repository of the scope;
//   - the row by treeWalkState.linkRowAuthorized: the pull request by the node
//     rule, the issue by the work-item rule (admitted: its own repository
//     decides, through graphrank.AuthorizedThroughLink; a repository-less one
//     needs a native link for a restricted caller);
//   - a requested team narrowing by the walk's teamNarrowing, per repository;
//   - the tier by the strongest admitted row.
//
// The repository walk (linkScopedIssues) is the test oracle of this read: for
// a scope under the census bound, the tiers this read returns for any ids are
// the walk's members intersected with those ids.

// keyedLinkRowLimit bounds the link rows one keyed read returns: the rows the
// walk may read of one repository. A read past it is cut, and a cut read is no
// answer: the census then reports itself incomplete.
const keyedLinkRowLimit = linkSegmentPageCap * (contextfabric.WorkItemMembershipCensusLimit + 1)

// keyedLinkScopeCypher is the keyed read. Its text depends only on the caller
// class and on whether the scope names "*": the ids, the scope and the grants are
// parameters, so no subject can change it.
//
// The scope clause is a necessary condition of the scope rule
// (graphrank.AuthorizedAttributes decides each row): a "*" selector admits every
// present repository, which the clause cannot express, so a scope that names it
// reads without the clause.
func keyedLinkScopeCypher(restricted, scopeWildcard bool) string {
	repository := treeNodeVar{name: "a", position: treeRepository}
	mid := treeNodeVar{name: "m", param: "mtypes", position: treePullRequest}
	end := treeNodeVar{name: "b", param: "btypes", position: treeIssue}
	scoped := " AND " + repository.repositoryMatchClause("scope")
	if scopeWildcard {
		scoped = ""
	}
	grants := ""
	if restricted {
		// The repository too: a row whose repository the grants cannot admit is
		// refused in process anyway, so it must not spend the row bound or count
		// in the read's line (codex r1 on #1929). A project-only caller binds no
		// repository grant, so it reads no row, as the walk admits no repository.
		grants = " AND " + repository.repositoryMatchClause("grant") + end.grantClause() + mid.grantClause()
	}
	return fmt.Sprintf("MATCH (b:%[2]s {%[3]s:$org, %[4]s:$endKind})-[rl:%[1]s]->(m:%[2]s {%[3]s:$org, %[4]s:$midKind})-[ra:%[1]s]->(a:%[2]s {%[3]s:$org, %[4]s:$anchorKind}) ",
		labelRelation, labelSubject, propOrgID, propKind) +
		fmt.Sprintf("WHERE b.%[3]s IN $ids AND rl.%[1]s = $linkRel AND rl.%[2]s IN $tiers AND ra.%[1]s = $feedRel", propRelationType, propPropertyPrefix+linkTierProperty, propCanonicalID) +
		mid.typeClause() + end.typeClause() + scoped + grants +
		fmt.Sprintf(" RETURN %s, %s, %s, %s LIMIT $limit",
			walkProjection("a", walkNodeProperties), walkProjection("m", walkNodeProperties), walkProjection("b", walkNodeProperties), walkProjection("rl", walkLinkProperties))
}

// scopeNamesWildcard reports whether a requested repository scope names "*".
func scopeNamesWildcard(slugs []string) bool {
	for _, slug := range slugs {
		if strings.TrimSpace(slug) == "*" {
			return true
		}
	}
	return false
}

// linkScopedWorkItemsOf returns, of ids, the work items in a requested
// repository scope, each with the strongest tier of its admitted links.
// complete is false when the read was cut, or a requested team narrowing could
// not decide an item: the answer is then not the whole truth about the ids.
func (a *Adapter) linkScopedWorkItemsOf(ctx context.Context, key string, principal storage.Principal, scope contextfabric.RequestedScope, ids []string) (map[string]string, bool, error) {
	if len(scope.RepositorySlugs) == 0 {
		return nil, false, errors.New("a keyed link read needs a requested repository scope")
	}
	tiers := map[string]string{}
	if len(ids) == 0 {
		return tiers, true, nil
	}
	path, ok := treePath(treeRepository, treeIssue)
	if !ok || len(path) != 2 || !path[1].edge.link {
		return nil, false, errors.New("the entity tree has no repository to issue path through the link")
	}
	feed, link := path[0], path[1]
	keyed := make([]interface{}, 0, len(ids))
	for _, id := range ids {
		keyed = append(keyed, id)
	}
	restricted := needsProjectReach(principal)
	params := map[string]interface{}{
		"org": principal.OrgID, "ids": keyed,
		"endKind": string(treeNodes[treeIssue].kind), "midKind": string(treeNodes[treePullRequest].kind), "anchorKind": string(treeNodes[treeRepository].kind),
		"linkRel": string(link.edge.relation), "feedRel": string(feed.edge.relation), "tiers": admittedLinkTiers(),
		"btypes": treeNodes[treeIssue].notTypes, "limit": keyedLinkRowLimit + 1,
	}
	bindRepositoryMatch(params, "scope", scope.RepositorySlugs)
	if restricted {
		params = linkSegmentGrants(params, principal)
	}
	rows, err := a.api.query(ctx, key, keyedLinkScopeCypher(restricted, scopeNamesWildcard(scope.RepositorySlugs)), params, true)
	if err != nil {
		return nil, false, safeDependencyError("read the links of work items in a requested repository scope", err)
	}
	if len(rows) > keyedLinkRowLimit {
		slog.DebugContext(ctx, "context fabric: keyed link read of a requested repository scope was cut", "ids", len(ids), "rows", len(rows))
		return nil, false, nil
	}
	state := treeWalkState{out: &treeWalk{}, principal: principal, scope: scope}
	anchors := map[string]contextfabric.SubjectRef{}
	admitted := map[string]map[string]string{}
	for _, r := range rows {
		repository, near, far := walkNode(r["a"]), walkNode(r["m"]), walkNode(r["b"])
		tier, isLink := linkTierOf(walkEdge(r["rl"]))
		if repository == nil || near == nil || far == nil || !isLink {
			continue
		}
		candidate := toCandidateNode(repository)
		if !graphrank.AuthorizedAttributes(principal, scope, candidate.Attributes) {
			continue
		}
		anchor, ok := graphrank.NodeSubject(candidate)
		if !ok || anchor.Kind != contextfabric.SubjectRepository {
			continue
		}
		if !state.linkRowAuthorized(r, feed, link, near, far, tier) {
			continue
		}
		id := canonicalIDOf(far)
		anchors[anchor.CanonicalID] = anchor
		if admitted[anchor.CanonicalID] == nil {
			admitted[anchor.CanonicalID] = map[string]string{}
		}
		if best, seen := admitted[anchor.CanonicalID][id]; !seen || tierRank(tier.name) < tierRank(best) {
			admitted[anchor.CanonicalID][id] = tier.name
		}
	}
	narrowed := requestedTeamNarrowing(scope) != nil
	if narrowed && len(anchors) > maxLinkScopedRepositories {
		// Each repository is one ownership read; past the walk's own bound the
		// read is cut, as the walk is.
		return nil, false, nil
	}
	order := make([]string, 0, len(anchors))
	for id := range anchors {
		order = append(order, id)
	}
	sort.Strings(order)
	for _, anchorID := range order {
		members := admitted[anchorID]
		var excluded map[string]bool
		if narrowed {
			narrowCtx, narrowing, err := a.withTeamNarrowing(ctx, key, principal.OrgID, scope, anchors[anchorID])
			if err != nil {
				return nil, false, err
			}
			memberIDs := make([]string, 0, len(members))
			for id := range members {
				memberIDs = append(memberIDs, id)
			}
			sort.Strings(memberIDs)
			if excluded, err = teamNarrowingFrom(narrowCtx).excluded(narrowCtx, memberIDs); err != nil {
				return nil, false, err
			}
			if narrowing.Undecided() {
				return nil, false, nil
			}
		}
		for id, tier := range members {
			if excluded[id] {
				continue
			}
			if best, seen := tiers[id]; !seen || tierRank(tier) < tierRank(best) {
				tiers[id] = tier
			}
		}
	}
	slog.DebugContext(ctx, "context fabric: keyed link read of a requested repository scope",
		"ids", len(ids), "rows", len(rows), "repositories", len(anchors), "linked", len(tiers))
	return tiers, true, nil
}
