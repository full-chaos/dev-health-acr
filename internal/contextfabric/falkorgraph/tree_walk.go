package falkorgraph

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The entity tree, for every integration: Repository <> Pull request <> Issue
// <> Project. Teams and deployments hang off it. The diagram and the two rules
// are in docs/design/context-fabric-architecture-diagrams.md §3:
//
//   - a link is an actual linked row between an issue and a pull request (the
//     LINKS_PULL_REQUEST edge, issue -> pull request, with its provenance tier),
//     never an issue-key prefix, an external issue key or an issue's own
//     repository;
//   - a team is reached through ownership only.
//
// The members of a named anchor are found by walking the unique path of the
// tree from the anchor's position to the member's position. The path is
// derived from the tree table below; no pair of positions has a hand-built
// walk.

// treePosition is one position of the entity tree.
type treePosition string

const (
	treeRepository  treePosition = "repository"
	treePullRequest treePosition = "pull_request"
	treeIssue       treePosition = "issue"
	treeProject     treePosition = "project"
	treeDeployment  treePosition = "deployment"
	treeTeam        treePosition = "team"
)

// treeNode is the graph shape of a position: its subject kind and, for a
// work item, the stored types it has or does not have. A pull request is its
// own node kind (pull_request); an issue is a work item that is not typed as
// a pull request.
type treeNode struct {
	kind            contractsv1.ContextFabricSubjectKind
	types, notTypes []interface{}
	// leaf positions hang off the tree: a path may end or start there, never
	// pass through. Without this a project would reach a repository through a
	// team that owns both, which is ownership, not the tree.
	leaf bool
}

var treeNodes = map[treePosition]treeNode{
	treeRepository:  {kind: contractsv1.ContextFabricSubjectRepository},
	treePullRequest: {kind: contractsv1.ContextFabricSubjectPullRequest},
	treeIssue:       {kind: contractsv1.ContextFabricSubjectWorkItem, notTypes: pullRequestWorkItemTypes},
	treeProject:     {kind: contractsv1.ContextFabricSubjectProject},
	treeDeployment:  {kind: contractsv1.ContextFabricSubjectDeployment, leaf: true},
	treeTeam:        {kind: contractsv1.ContextFabricSubjectTeam, leaf: true},
}

// The link edge properties the projection persists (projection.go
// linkEdgePropertyNames), under the node property prefix on the stored edge.
const (
	linkTierProperty = "link_provenance"
	linkRankProperty = "link_provenance_rank"
)

// linkTier is one provenance tier of the issue -> pull request link.
type linkTier struct {
	// name is the tier as the projection stores it.
	name string
	// grantsAuthority: a link of this tier admits a repository-less issue
	// through the pull request the caller is granted. A link grants authority
	// only when native; every tier is a link, and an issue authorized by its
	// own repository is admitted through any of them.
	grantsAuthority bool
}

// linkTiers is the ONE table of the tiers the walk admits, strongest first. A
// stored edge whose tier is not a row here, or has none, is not a link. To
// veto a tier, delete its row (and add its row to the veto test); to stop a
// tier granting authority, clear grantsAuthority.
var linkTiers = []linkTier{
	{name: "native", grantsAuthority: true},
	{name: "explicit_text"},
	{name: "heuristic"},
}

// admittedLinkTiers are the tiers the link read admits.
func admittedLinkTiers() []interface{} {
	out := make([]interface{}, 0, len(linkTiers))
	for _, t := range linkTiers {
		out = append(out, t.name)
	}
	return out
}

// authorityLinkTiers are the admitted tiers whose link admits a
// repository-less issue.
func authorityLinkTiers() []interface{} {
	out := []interface{}{}
	for _, t := range linkTiers {
		if t.grantsAuthority {
			out = append(out, t.name)
		}
	}
	return out
}

// linkTierOf reads the tier of a stored link edge: its row of the table, or
// false when the edge has no tier or one the table does not list.
func linkTierOf(rl *edge) (linkTier, bool) {
	if rl == nil {
		return linkTier{}, false
	}
	name := propStringValue(rl.Properties[propPropertyPrefix+linkTierProperty])
	for _, t := range linkTiers {
		if t.name == name {
			return t, true
		}
	}
	return linkTier{}, false
}

// treeEdge is one edge of the tree, as projected: child -relation-> parent.
type treeEdge struct {
	child, parent treePosition
	relation      contractsv1.ContextFabricRelationshipType
	// link marks the issue <> pull request edge, directed issue -> pull
	// request, and the hop whose near side holds many nodes with no link at
	// all. The hop that feeds it from the anchor is read fused with it
	// (linkSegment).
	link bool
	// ownership edges carry the period they held: they are read under the
	// question's window or the adapter clock (currentOwnership), and under the
	// census bound, since one owned node can carry several ownership edges.
	ownership bool
	// linkOrder, when set, is the link edge property that orders link rows
	// before anything else, highest first, so a cut keeps the higher-ranked
	// links (the strongest provenance tier first). Empty: links are ordered
	// by their endpoints only.
	linkOrder string
}

// entityTree is the tree. Every walk is derived from it.
var entityTree = []treeEdge{
	{child: treePullRequest, parent: treeRepository, relation: contractsv1.ContextFabricRelationshipBelongsToRepository},
	{child: treeIssue, parent: treePullRequest, relation: contractsv1.ContextFabricRelationshipLinksPullRequest, link: true, linkOrder: linkRankProperty},
	{child: treeIssue, parent: treeProject, relation: contractsv1.ContextFabricRelationshipBelongsToProject},
	{child: treeDeployment, parent: treeRepository, relation: contractsv1.ContextFabricRelationshipBelongsToRepository},
	{child: treeRepository, parent: treeTeam, relation: contractsv1.ContextFabricRelationshipOwnedByTeam, ownership: true},
	{child: treeProject, parent: treeTeam, relation: contractsv1.ContextFabricRelationshipOwnedByTeam, ownership: true},
}

// treeHop is one edge of a path, walked from one position to the next.
type treeHop struct {
	from, to treePosition
	edge     treeEdge
	step     walkStep
	// toChild: the hop walks from the parent to the child.
	toChild bool
}

func hopOf(e treeEdge, from treePosition) treeHop {
	to, direction, toChild := e.parent, walkOut, false
	if from == e.parent {
		to, direction, toChild = e.child, walkIn, true
	}
	target := treeNodes[to]
	return treeHop{from: from, to: to, edge: e, toChild: toChild, step: walkStep{
		fromKind: treeNodes[from].kind, toKind: target.kind, relation: e.relation, direction: direction,
		toTypes: target.types, notToTypes: target.notTypes,
	}}
}

// treePath is the shortest path of the tree from one position to another.
// No path, or two shortest paths, is no path: the pair is off the tree.
func treePath(from, to treePosition) ([]treeHop, bool) {
	if _, ok := treeNodes[from]; !ok || from == to {
		return nil, false
	}
	if _, ok := treeNodes[to]; !ok {
		return nil, false
	}
	var best []treeHop
	ties := 0
	var walk func(at treePosition, seen map[treePosition]bool, path []treeHop)
	walk = func(at treePosition, seen map[treePosition]bool, path []treeHop) {
		if at == to {
			switch {
			case best == nil || len(path) < len(best):
				best, ties = append([]treeHop(nil), path...), 0
			case len(path) == len(best):
				ties++
			}
			return
		}
		if at != from && treeNodes[at].leaf {
			return
		}
		for _, e := range entityTree {
			if e.child != at && e.parent != at {
				continue
			}
			hop := hopOf(e, at)
			if seen[hop.to] {
				continue
			}
			seen[hop.to] = true
			walk(hop.to, seen, append(path, hop))
			delete(seen, hop.to)
		}
	}
	walk(from, map[treePosition]bool{from: true}, nil)
	if best == nil || ties > 0 {
		return nil, false
	}
	return best, true
}

// anchorPosition is the tree position of a named anchor. A work item is not an
// anchor position: its position is its stored type.
func anchorPosition(kind contextfabric.SubjectKind) (treePosition, bool) {
	switch kind {
	case contextfabric.SubjectRepository:
		return treeRepository, true
	case contextfabric.SubjectProject:
		return treeProject, true
	case contextfabric.SubjectTeam:
		return treeTeam, true
	}
	return "", false
}

// treeWalk is the result of one walk of the tree from a named anchor.
type treeWalk struct {
	// anchorKind is the kind of the anchor the walk started from.
	anchorKind contextfabric.SubjectKind
	nodes      []graphrank.CandidateNode
	edges      []graphrank.ResolvedEdge
	filters    edgeFilterCounts
	truncated  bool
	// hasLink: the path crosses the issue <> pull request link.
	hasLink bool
	// linkSources is how many nodes the hop that feeds the link reached (a
	// project's issues), and linkTargets how many distinct nodes on the far
	// side of the link the link read returned (their linked pull requests).
	// For a restricted caller the read carries the grant clause, so
	// linkTargets counts what the grants can admit. Zero link targets over an
	// uncut read is the unlinked terminal.
	linkSources, linkTargets int
	// denied counts the links and nodes the caller's authorization hid.
	// Members unseen for that reason are not an unlinked anchor.
	denied int
	// linkDeniedTargets counts the distinct far-side nodes the link read
	// returned that no authorized row reached: nodes, never link rows, so a
	// node admitted through one link is not denied for another. Nodes the
	// read's grant clause kept out are not read, so not counted.
	linkDeniedTargets int
	// endTiers is, for each admitted far-side node of the link read, the
	// strongest tier among its admitted link rows (linkSegment). It is
	// recorded for every link read; memberTiers is its part that matters.
	endTiers map[string]string
	// memberTiers is set only when the link ends the path (linkMembers): the
	// strongest admitted link tier of each member, by canonical id.
	memberTiers map[string]string
	// heuristicOnly counts the members whose strongest tier is heuristic.
	heuristicOnly int
}

// tierRank is a tier's place in linkTiers: lower is stronger. A tier the
// table does not list ranks last.
func tierRank(name string) int {
	for i, t := range linkTiers {
		if t.name == name {
			return i
		}
	}
	return len(linkTiers)
}

// treeWalkState is the bookkeeping one walk shares between its hops.
type treeWalkState struct {
	out          *treeWalk
	principal    storage.Principal
	scope        contextfabric.RequestedScope
	collectLimit int
}

func (s treeWalkState) authorized(n *node) bool {
	return graphrank.AuthorizedAttributes(s.principal, s.scope, toCandidateNode(n).Attributes)
}

// admitted is the work-item rule at a position inside a link read: an issue
// with a repository of its own is decided by that repository, through a link
// of any tier. A repository-less issue is admitted by its link to a pull
// request the caller is granted (the other end of the same row), and for a
// repository-restricted caller only when that link's tier grants authority
// (native). An unrestricted caller needs no authority from a link.
func (s treeWalkState) admitted(position treePosition, n *node, tier linkTier) bool {
	if position == treeIssue && repositoryLess(n) {
		return tier.grantsAuthority || !s.narrowed()
	}
	return s.authorized(n)
}

// narrowed reports whether the caller sees only some repositories: a
// repository grant without the wildcard, or a requested repository scope. Only
// then does a link need to grant authority.
func (s treeWalkState) narrowed() bool {
	return needsProjectReach(s.principal) || len(s.scope.RepositorySlugs) > 0
}

func (s treeWalkState) deny() {
	s.out.filters.add(edgeFiltered, edgeFilterReasonAuthz)
	s.out.denied++
}

// cut bounds a frontier by the collect budget; a cut one is truncation.
func (s treeWalkState) cut(ids []string) []string {
	sort.Strings(ids)
	if s.collectLimit > 0 && len(ids) > s.collectLimit {
		s.out.truncated = true
		return ids[:s.collectLimit]
	}
	return ids
}

// cutRanked bounds a frontier the link read already ordered (strongest link
// tier first): the first ids are kept, not the alphabetically first.
func (s treeWalkState) cutRanked(ids []string) []string {
	if s.collectLimit > 0 && len(ids) > s.collectLimit {
		s.out.truncated = true
		return ids[:s.collectLimit]
	}
	return ids
}

// treeMembers walks the tree from a named anchor to the member position. Every
// node the walk discloses passes the caller's authorization, every frontier is
// bounded by the collect budget, and a spent budget is truncation. A pair of
// positions off the tree returns an empty walk.
func (a *Adapter) treeMembers(ctx context.Context, key, orgID string, principal storage.Principal, scope contextfabric.RequestedScope, anchor contextfabric.SubjectRef, member treePosition, collectLimit int, temporal temporalFilter) (treeWalk, error) {
	out := treeWalk{anchorKind: anchor.Kind}
	start, ok := anchorPosition(anchor.Kind)
	if !ok {
		return out, nil
	}
	path, ok := treePath(start, member)
	if !ok {
		return out, nil
	}
	state := treeWalkState{out: &out, principal: principal, scope: scope, collectLimit: collectLimit}
	frontier := []string{anchor.CanonicalID}
	subjects := map[string]contextfabric.SubjectRef{anchor.CanonicalID: anchor}
	for k := 0; k < len(path); k++ {
		hop := path[k]
		if k == 0 && len(path) > 1 && path[1].edge.link {
			// The anchor's hop only feeds the link: read fused with it, so
			// nodes with no link do not spend the budget.
			out.hasLink = true
			ends, far, err := a.linkSegment(ctx, key, orgID, state, anchor, hop, path[1], temporal)
			if err != nil {
				return out, err
			}
			k++
			if k == len(path)-1 {
				// The link ends the path: its far side is the member set.
				state.linkMembers(ends, far)
				return out, nil
			}
			frontier, subjects = state.cutRanked(ends), map[string]contextfabric.SubjectRef{}
			if len(frontier) == 0 {
				return out, nil
			}
			continue
		}
		out.hasLink = out.hasLink || hop.edge.link
		hopTemporal, budget := temporal, collectLimit
		if hop.edge.ownership {
			hopTemporal, budget = currentOwnership(temporal, a.now()), exactNameCandidateQueryLimit
		}
		hits, hitsCut, err := a.walkStepHits(ctx, key, orgID, frontier, hop.step, hopTemporal, budget)
		if err != nil {
			return out, err
		}
		out.truncated = out.truncated || hitsCut
		if k == len(path)-1 {
			state.members(hop, hits, subjects)
			return out, nil
		}
		frontier, subjects = state.advance(hop, hits, subjects)
		if len(frontier) == 0 {
			return out, nil
		}
	}
	return out, nil
}

// advance turns one intermediate hop's hits into the next frontier: the
// authorized distinct nodes, bounded by the budget. An ownership hop also
// discloses its edges, since ownership is why the next nodes are reached.
func (s treeWalkState) advance(hop treeHop, hits []walkHit, parents map[string]contextfabric.SubjectRef) ([]string, map[string]contextfabric.SubjectRef) {
	reached := map[string]walkHit{}
	subjects := map[string]contextfabric.SubjectRef{}
	for _, h := range hits {
		if !s.authorized(h.to) {
			s.deny()
			continue
		}
		subject, ok := graphrank.NodeSubject(toCandidateNode(h.to))
		if !ok {
			continue
		}
		reached[subject.CanonicalID] = h
		subjects[subject.CanonicalID] = subject
	}
	ids := make([]string, 0, len(reached))
	for id := range reached {
		ids = append(ids, id)
	}
	ids = s.cut(ids)
	kept := make(map[string]contextfabric.SubjectRef, len(ids))
	for _, id := range ids {
		kept[id] = subjects[id]
		if hop.edge.ownership {
			s.disclose(hop, reached[id], subjects[id], parents)
		}
	}
	return ids, kept
}

// members adds the last hop's hits to the walk's members: authorized,
// distinct, bounded by the budget, each with the edge to the node it was
// reached from.
func (s treeWalkState) members(hop treeHop, hits []walkHit, parents map[string]contextfabric.SubjectRef) {
	seen := map[string]bool{}
	for _, h := range hits {
		if !s.authorized(h.to) {
			s.deny()
			continue
		}
		id := canonicalIDOf(h.to)
		if seen[id] {
			continue
		}
		if s.collectLimit > 0 && len(s.out.nodes) >= s.collectLimit {
			s.out.truncated = true
			break
		}
		seen[id] = true
		candidate := toCandidateNode(h.to)
		s.out.nodes = append(s.out.nodes, candidate)
		if subject, ok := graphrank.NodeSubject(candidate); ok {
			s.disclose(hop, h, subject, parents)
		}
	}
	sortCandidateNodesBySubjectKey(s.out.nodes)
}

// linkMembers makes the far-side nodes of the authorized links the members,
// bounded by the budget. The link edges are not disclosed: the near side of a
// link is not a member.
func (s treeWalkState) linkMembers(ends []string, far map[string]*node) {
	s.out.memberTiers = map[string]string{}
	for _, id := range s.cutRanked(ends) {
		s.out.nodes = append(s.out.nodes, toCandidateNode(far[id]))
		tier := s.out.endTiers[id]
		s.out.memberTiers[id] = tier
		if tier == contextfabric.TreeLinkTierHeuristic {
			s.out.heuristicOnly++
		}
	}
	sortCandidateNodesBySubjectKey(s.out.nodes)
}

// disclose adds the edge of one hit, oriented as projected: child -> parent.
func (s treeWalkState) disclose(hop treeHop, h walkHit, reached contextfabric.SubjectRef, parents map[string]contextfabric.SubjectRef) {
	from, ok := parents[h.from]
	if !ok {
		return
	}
	child, parent := reached, from
	if !hop.toChild {
		child, parent = from, reached
	}
	edgeCandidate := toCandidateEdge(h.rel, string(child.Kind), child.CanonicalID, string(parent.Kind), parent.CanonicalID)
	s.out.edges = append(s.out.edges, graphrank.ResolvedEdge{
		UUID: edgeCandidate.UUID, Name: edgeCandidate.Name, Fact: edgeCandidate.Fact, From: child, To: parent,
		Attributes: edgeCandidate.Attributes, CreatedAt: edgeCandidate.CreatedAt, ValidAt: edgeCandidate.ValidAt, InvalidAt: edgeCandidate.InvalidAt,
	})
}

// linkSegmentPageCap bounds how many pages of links one walk reads. A walk
// that reaches it is cut.
const linkSegmentPageCap = 8

// treeNodeVar is a node variable of the link read with its position.
type treeNodeVar struct {
	name, param string
	position    treePosition
}

func (v treeNodeVar) typeClause() string {
	n := treeNodes[v.position]
	switch {
	case len(n.types) > 0:
		return fmt.Sprintf(" AND %s.%s IN $%s", v.name, propWorkItemType, v.param)
	case len(n.notTypes) > 0:
		return fmt.Sprintf(" AND (%[1]s.%[2]s IS NULL OR NOT %[1]s.%[2]s IN $%[3]s)", v.name, propWorkItemType, v.param)
	}
	return ""
}

// repositoryGrants is a restricted caller's repository grants, precomputed
// for the link read so the read's pushdown is a necessary condition of
// graphrank.ScopeMatch (the per-row decision):
//   - raw: the trimmed grants, for a raw equality (the fallback when a side
//     does not normalize as owner/name);
//   - norm: lower(trim(grant)), for the normalized slug equality;
//   - owners: "owner/" for each "owner/*" grant, lower-cased, for the owner
//     wildcard.
//
// A "*" grant is not here: it makes the caller unrestricted (needsProjectReach)
// and the read carries no grant clause.
type repositoryGrants struct {
	raw, norm, owners []interface{}
}

func newRepositoryGrants(scopes []string) repositoryGrants {
	g := repositoryGrants{raw: []interface{}{}, norm: []interface{}{}, owners: []interface{}{}}
	for _, scope := range scopes {
		// A blank grant stays in the lists: ScopeMatch compares it as it
		// does any other, and the pushdown must not be stricter than it.
		scope = strings.TrimSpace(scope)
		g.raw = append(g.raw, scope)
		g.norm = append(g.norm, strings.ToLower(scope))
		if owner, ok := strings.CutSuffix(scope, "/*"); ok && owner != "" {
			g.owners = append(g.owners, strings.ToLower(owner)+"/")
		}
	}
	return g
}

// grantClause keeps only the nodes a restricted caller's grants can admit: a
// necessary condition of the rule the walk applies to each row (admitted), so
// it drops no row the caller could see, and rows the caller cannot see do not
// fill the pages before the ones it can. An issue with no repository is kept
// only on a link of a tier that grants authority, as the per-row rule has it.
func (v treeNodeVar) grantClause() string {
	clause := fmt.Sprintf("ANY(s IN %s.%s WHERE s IN $grantRaw OR toLower(trim(s)) IN $grantNorm OR ANY(o IN $grantOwners WHERE toLower(trim(s)) STARTS WITH o))", v.name, propAuthzRepos)
	if v.position == treeIssue {
		clause = fmt.Sprintf("(%s OR ($noRepository IN %s.%s AND rl.%s IN $authorityTiers))", clause, v.name, propAuthzRepos, propPropertyPrefix+linkTierProperty)
	}
	return " AND " + clause
}

func hopArrow(direction walkDirection, rel string) string {
	switch direction {
	case walkOut:
		return "-[" + rel + ":%[1]s]->"
	case walkIn:
		return "<-[" + rel + ":%[1]s]-"
	}
	return "-[" + rel + ":%[1]s]-"
}

// linkSegmentMatch is the path of the link read: the anchor, its hop to the
// near side of the link (m), and the link to the far side (b).
func linkSegmentMatch(feed treeHop) string {
	return fmt.Sprintf("MATCH (a:%[2]s {%[3]s:$org, %[4]s:$anchorKind, %[5]s:$anchor})"+hopArrow(feed.step.direction, "ra")+"(m:%[2]s {%[3]s:$org, %[4]s:$midKind})",
		labelRelation, labelSubject, propOrgID, propKind, propCanonicalID)
}

// linkSegmentCypher is the fused read of the anchor's hop and the link: one
// row per link, in a deterministic order, paged. A near-side node with no link
// is not a row, so the read budget is spent on links only, and an anchor none
// of whose near-side nodes links returns no row at all.
func linkSegmentCypher(feed, link treeHop, temporal temporalFilter, restricted bool) string {
	mid := treeNodeVar{name: "m", param: "mtypes", position: feed.to}
	end := treeNodeVar{name: "b", param: "btypes", position: link.to}
	grants := ""
	if restricted {
		grants = end.grantClause() + mid.grantClause()
	}
	order := ""
	if link.edge.linkOrder != "" {
		order = fmt.Sprintf("rl.%s DESC, ", propPropertyPrefix+link.edge.linkOrder)
	}
	return linkSegmentMatch(feed) + fmt.Sprintf(hopArrow(link.step.direction, "rl")+"(b:%[2]s {%[3]s:$org, %[4]s:$endKind}) ", labelRelation, labelSubject, propOrgID, propKind) +
		fmt.Sprintf("WHERE ra.%[1]s = $feedRel AND rl.%[1]s = $linkRel AND rl.%[2]s IN $tiers", propRelationType, propPropertyPrefix+linkTierProperty) + mid.typeClause() + end.typeClause() + grants +
		temporal.predicate("ra") + temporal.predicate("m") + temporal.predicate("rl") + temporal.predicate("b") +
		fmt.Sprintf(" RETURN m, b, rl ORDER BY %[2]sm.%[1]s, b.%[1]s, rl.%[3]s SKIP $skip LIMIT $limit", propCanonicalID, order, propRelationshipID)
}

// linkSourceCountCypher counts the near-side nodes of the anchor's hop, before
// authorization.
func linkSourceCountCypher(feed treeHop, temporal temporalFilter) string {
	mid := treeNodeVar{name: "m", param: "mtypes", position: feed.to}
	return linkSegmentMatch(feed) + fmt.Sprintf(" WHERE ra.%s = $feedRel", propRelationType) + mid.typeClause() +
		temporal.predicate("ra") + temporal.predicate("m") + " RETURN count(DISTINCT m) AS sources"
}

// linkSegmentParams binds the link read and the source count.
func linkSegmentParams(orgID string, anchor contextfabric.SubjectRef, feed, link treeHop, skip, limit int, temporal temporalFilter) map[string]interface{} {
	params := temporal.bind(map[string]interface{}{
		"org": orgID, "anchor": anchor.CanonicalID, "anchorKind": string(treeNodes[feed.from].kind),
		"midKind": string(treeNodes[feed.to].kind), "endKind": string(treeNodes[link.to].kind),
		"feedRel": string(feed.edge.relation), "linkRel": string(link.edge.relation),
		"tiers": admittedLinkTiers(),
		"skip":  skip, "limit": limit,
	})
	for param, position := range map[string]treePosition{"mtypes": feed.to, "btypes": link.to} {
		n := treeNodes[position]
		switch {
		case len(n.types) > 0:
			params[param] = n.types
		case len(n.notTypes) > 0:
			params[param] = n.notTypes
		}
	}
	return params
}

// linkSegmentGrants binds a restricted caller's grants to the link read.
func linkSegmentGrants(params map[string]interface{}, principal storage.Principal) map[string]interface{} {
	grants := newRepositoryGrants(principal.RepositoryScopes)
	params["grantRaw"], params["grantNorm"], params["grantOwners"] = grants.raw, grants.norm, grants.owners
	params["noRepository"] = noRepositoryScope
	params["authorityTiers"] = authorityLinkTiers()
	return params
}

// linkSources reads how many near-side nodes the anchor's hop reaches, before
// authorization.
func (a *Adapter) linkSources(ctx context.Context, key, orgID string, anchor contextfabric.SubjectRef, feed, link treeHop, temporal temporalFilter) (int, error) {
	rows, err := a.api.query(ctx, key, linkSourceCountCypher(feed, temporal), linkSegmentParams(orgID, anchor, feed, link, 0, 1, temporal), true)
	if err != nil {
		return 0, safeDependencyError("count link sources", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	switch n := rows[0]["sources"].(type) {
	case int64:
		return int(n), nil
	case int:
		return n, nil
	case float64:
		return int(n), nil
	}
	return 0, nil
}

// linkSegment reads the anchor's hop and the link fused, paged, each row
// authorized before it counts against the budget, so links the caller cannot
// see do not crowd out links it can. It returns the far-side nodes of the
// authorized links, by canonical id, in the order of their strongest link.
func (a *Adapter) linkSegment(ctx context.Context, key, orgID string, state treeWalkState, anchor contextfabric.SubjectRef, feed, link treeHop, temporal temporalFilter) ([]string, map[string]*node, error) {
	out := state.out
	sources, err := a.linkSources(ctx, key, orgID, anchor, feed, link, temporal)
	if err != nil {
		return nil, nil, err
	}
	out.linkSources = sources

	pageSize := 1 << 30
	if state.collectLimit > 0 {
		pageSize = state.collectLimit + 1
	}
	restricted := needsProjectReach(state.principal)
	cypher := linkSegmentCypher(feed, link, temporal, restricted)
	seen := map[string]bool{}
	admitted := map[string]*node{}
	out.endTiers = map[string]string{}
	// ends are the admitted far-side nodes in the read's order: strongest
	// link tier first, so a cut keeps the higher tiers.
	var ends []string
	for page := 0; ; page++ {
		if page >= linkSegmentPageCap {
			out.truncated = true
			break
		}
		params := linkSegmentParams(orgID, anchor, feed, link, page*pageSize, pageSize, temporal)
		if restricted {
			params = linkSegmentGrants(params, state.principal)
		}
		rows, err := a.api.query(ctx, key, cypher, params, true)
		if err != nil {
			return nil, nil, safeDependencyError("walk the entity tree", err)
		}
		for _, r := range rows {
			near, _ := r["m"].(*node)
			far, _ := r["b"].(*node)
			tier, isLink := linkTierOf(asEdge(r["rl"]))
			if near == nil || far == nil || !isLink {
				// No row, or an edge with no tier the table lists: not a link.
				continue
			}
			id := canonicalIDOf(far)
			seen[id] = true
			switch {
			case !state.admitted(feed.to, near, tier):
				state.deny()
			case !state.admitted(link.to, far, tier):
				state.deny()
			default:
				if admitted[id] == nil {
					ends = append(ends, id)
				}
				admitted[id] = far
				// The strongest admitted tier wins; a later, weaker row
				// never overwrites it.
				if best, ok := out.endTiers[id]; !ok || tierRank(tier.name) < tierRank(best) {
					out.endTiers[id] = tier.name
				}
			}
		}
		if len(rows) < pageSize || (state.collectLimit > 0 && len(admitted) > state.collectLimit) {
			break
		}
	}
	out.linkTargets = len(seen)
	out.linkDeniedTargets = len(seen) - len(admitted)
	return ends, admitted, nil
}

// anchorDeploymentMembers returns the deployments a named anchor reaches on
// the tree: a project through its issues' linked pull requests' repositories,
// a team through the repositories it owns, a repository directly.
func (a *Adapter) anchorDeploymentMembers(ctx context.Context, key, orgID string, principal storage.Principal, scope contextfabric.RequestedScope, anchor contextfabric.SubjectRef, collectLimit int, temporal temporalFilter) (treeWalk, error) {
	return a.treeMembers(ctx, key, orgID, principal, scope, anchor, treeDeployment, collectLimit, temporal)
}

func asEdge(v interface{}) *edge {
	e, _ := v.(*edge)
	return e
}
