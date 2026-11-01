package falkorgraph

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12327: a requested team narrowing (scope.team_ids) on the GENERAL
// graph read (DiscoverContext), the twin of the repository walk's
// (team_narrowing.go, CHAOS-12319). Repository, project, pull-request and
// work-item nodes carry the "*" team list, so the node rule
// (graphrank.AuthorizedAttributes) narrows none of them; "*" is never team
// evidence. Team = ownership only (the entity tree,
// docs/design/context-fabric-architecture-diagrams.md §3). Under a team
// narrowing a served subject is in scope when:
//
//   - team: its own team list names a requested team (AuthorizedAttributes
//     decides it, as before);
//   - repository / project: it is currently OWNED_BY_TEAM a requested team
//     (any current edge, co-owners included);
//   - pull request: its repository (BELONGS_TO_REPOSITORY) is owned so;
//   - work item: (i) it is an issue of such a repository -- an actual
//     LINKS_PULL_REQUEST row of an admitted tier to one of that repository's
//     pull requests -- or (ii) its own project (BELONGS_TO_PROJECT) is owned
//     so. Never its own repository column, a key prefix or person membership;
//   - organization: the caller's organization node stays;
//   - every other kind has no ownership path to a team and is out of scope
//     (lead ruling Q1).
//
// The committed subjects stay subjects (Anchor: B): only their neighbours are
// narrowed, and a committed subject outside the narrowing does not make its
// neighbours in scope.
//
// An exclusion is SCOPE: never a denial, never an authorization detail and
// never a floor. A subject the caller's grants or the rest of the request
// refuse is left to that check, as before (decide-first): only subjects the
// request otherwise admits are decided here. The read is floored only where
// the ownership it needed to decide an admitted subject is absent, not
// current, or cut -- an absent ownership row is missing data, not "not owned"
// (team-attribution.md R74).

// issuePullRequestStep reads an issue's linked pull requests: the
// issue -> pull request link of record, with its tier.
var issuePullRequestStep = walkStep{
	fromKind: contractsv1.ContextFabricSubjectWorkItem, toKind: contractsv1.ContextFabricSubjectPullRequest,
	relation: contractsv1.ContextFabricRelationshipLinksPullRequest, direction: walkOut,
}

// pullRequestRepositoryStep reads a pull request's repository.
var pullRequestRepositoryStep = walkStep{
	fromKind: contractsv1.ContextFabricSubjectPullRequest, toKind: contractsv1.ContextFabricSubjectRepository,
	relation: contractsv1.ContextFabricRelationshipBelongsToRepository, direction: walkOut,
}

// discoverTeamScope is one general read's requested team narrowing.
type discoverTeamScope struct {
	narrowing *teamNarrowing
	temporal  temporalFilter
	// keep: the committed subjects (they stay subjects).
	keep map[string]bool
	// inScope / decided: canonical id -> in scope, for every subject decided.
	inScope, decided map[string]bool
	// missing: canonical id -> the ownership needed to decide it was absent,
	// not current, or cut (only for a subject decided out of scope).
	missing map[string]bool
	// undecided: a cohort member candidate (of the member kind) or a status
	// match was left out on missing ownership. Path endpoints never floor
	// (lead ruling Q2 (a)), and a candidate of another kind loses no member.
	undecided bool
}

// newDiscoverTeamScope returns the read's team narrowing, or nil when the
// request narrows by no team.
func (a *Adapter) newDiscoverTeamScope(key, orgID string, scope contextfabric.RequestedScope, temporal temporalFilter, committed []contextfabric.SubjectRef) *discoverTeamScope {
	requested := requestedTeamNarrowing(scope)
	if requested == nil {
		return nil
	}
	s := &discoverTeamScope{
		narrowing: &teamNarrowing{adapter: a, key: key, orgID: orgID, requested: requested},
		temporal:  temporal, keep: map[string]bool{}, inScope: map[string]bool{}, decided: map[string]bool{}, missing: map[string]bool{},
	}
	for _, subject := range committed {
		s.keep[subject.CanonicalID] = true
	}
	return s
}

// Undecided reports whether the read must be floored: an ownership read the
// narrowing needed was absent, not current, or cut.
func (s *discoverTeamScope) Undecided() bool { return s != nil && s.undecided }

// floor is the read's floor value for the team-narrowing log line.
func (s *discoverTeamScope) floor() contextfabric.TeamNarrowingReadFloor {
	if s.Undecided() {
		return contextfabric.TeamNarrowingReadFloorUndecided
	}
	return contextfabric.TeamNarrowingReadFloorNone
}

// counts reports the requested team ids, the subjects decided, and those
// decided out of scope (committed subjects, teams and the organization are
// never decided, so never counted).
func (s *discoverTeamScope) counts() (requested, decided, excluded int) {
	for id := range s.decided {
		if !s.inScope[id] {
			excluded++
		}
	}
	return len(s.narrowing.requested), len(s.decided), excluded
}

// edges keeps the resolved edges whose two endpoints are in scope. An edge
// the rest of the request refuses is kept for that check to refuse, as before.
func (s *discoverTeamScope) edges(ctx context.Context, principal storage.Principal, scope contextfabric.RequestedScope, edges []graphrank.ResolvedEdge) ([]graphrank.ResolvedEdge, error) {
	if s == nil {
		return edges, nil
	}
	var subjects []contextfabric.SubjectRef
	for _, e := range edges {
		if graphrank.AuthorizedAttributes(principal, scope, e.Attributes) {
			subjects = append(subjects, e.From, e.To)
		}
	}
	if err := s.decide(ctx, subjects); err != nil {
		return nil, err
	}
	kept := make([]graphrank.ResolvedEdge, 0, len(edges))
	for _, e := range edges {
		if !graphrank.AuthorizedAttributes(principal, scope, e.Attributes) || (s.admits(e.From) && s.admits(e.To)) {
			kept = append(kept, e)
		}
	}
	return kept, nil
}

// nodes keeps the cohort candidate nodes that are in scope. A node the rest of
// the request refuses is kept for that check to refuse (and count), as before.
// A candidate of memberKind (any kind when memberKind is empty) left out on
// missing ownership floors the read: a member may be missing.
func (s *discoverTeamScope) nodes(ctx context.Context, principal storage.Principal, scope contextfabric.RequestedScope, memberKind contextfabric.SubjectKind, nodes []graphrank.CandidateNode) ([]graphrank.CandidateNode, error) {
	if s == nil {
		return nodes, nil
	}
	var subjects []contextfabric.SubjectRef
	for _, n := range nodes {
		if subject, ok := graphrank.NodeSubject(n); ok && graphrank.AuthorizedAttributes(principal, scope, n.Attributes) {
			subjects = append(subjects, subject)
		}
	}
	if err := s.decide(ctx, subjects); err != nil {
		return nil, err
	}
	kept := make([]graphrank.CandidateNode, 0, len(nodes))
	for _, n := range nodes {
		subject, ok := graphrank.NodeSubject(n)
		if !ok || !graphrank.AuthorizedAttributes(principal, scope, n.Attributes) || s.admits(subject) {
			kept = append(kept, n)
			continue
		}
		if s.missing[subject.CanonicalID] && (memberKind == "" || subject.Kind == memberKind) {
			s.undecided = true
		}
	}
	return kept, nil
}

// inScopeIDs decides ids of one kind (members a status walk already
// authorized) and returns those in scope. A nil scope returns ids unchanged.
func (s *discoverTeamScope) inScopeIDs(ctx context.Context, kind contextfabric.SubjectKind, ids map[string]bool) (map[string]bool, error) {
	out, undecided, err := s.inScopeIDsUnfloored(ctx, kind, ids)
	if undecided {
		s.undecided = true
	}
	return out, err
}

// inScopeIDsUnfloored is inScopeIDs for ids that are not cohort candidates (the
// members a status read checked, or those outside the status set): it never
// floors the read (Q2 (a)). undecided reports that the ownership needed for at
// least one id was missing, so a count of the result is a lower bound.
func (s *discoverTeamScope) inScopeIDsUnfloored(ctx context.Context, kind contextfabric.SubjectKind, ids map[string]bool) (map[string]bool, bool, error) {
	if s == nil {
		return ids, false, nil
	}
	subjects := make([]contextfabric.SubjectRef, 0, len(ids))
	for id := range ids {
		subjects = append(subjects, contextfabric.SubjectRef{Kind: kind, CanonicalID: id})
	}
	if err := s.decide(ctx, subjects); err != nil {
		return nil, false, err
	}
	out := make(map[string]bool, len(ids))
	undecided := false
	for id := range ids {
		if s.admits(contextfabric.SubjectRef{Kind: kind, CanonicalID: id}) {
			out[id] = true
		} else if s.missing[id] {
			undecided = true
		}
	}
	return out, undecided, nil
}

// committedOwned reports whether rule (i) holds for every committed subject
// (CHAOS-12352): a repository or project currently OWNED_BY_TEAM a requested
// team, or a team whose own team list names one ("*" is never evidence). Then
// an anchor-level total is the requested team's. No committed subject, any
// other kind, a missing node or a cut read: false.
func (s *discoverTeamScope) committedOwned(ctx context.Context, committed []contextfabric.SubjectRef) (bool, error) {
	if s == nil || len(committed) == 0 {
		return false, nil
	}
	n := s.narrowing
	a := n.adapter
	// Current ownership on every axis, as decide reads it.
	owned := currentOwnership(temporalFilter{}, a.now())
	for _, subject := range committed {
		switch subject.Kind {
		case contextfabric.SubjectRepository, contextfabric.SubjectProject:
			step := repositoryOwnersStep
			if subject.Kind == contextfabric.SubjectProject {
				step = projectOwnersStep
			}
			hits, cut, err := a.walkStepHits(ctx, n.key, n.orgID, []string{subject.CanonicalID}, step, owned, exactNameCandidateQueryLimit)
			if err != nil {
				return false, err
			}
			if cut || !n.anyRequested(hits) {
				return false, nil
			}
		case contextfabric.SubjectTeam:
			team, err := a.nodeByKindID(ctx, n.key, n.orgID, string(subject.Kind), subject.CanonicalID, s.temporal)
			if err != nil {
				return false, err
			}
			if !n.isRequested(team) {
				return false, nil
			}
		default:
			return false, nil
		}
	}
	return true, nil
}

// admits reports whether a decided subject is in scope. A committed subject,
// a team (AuthorizedAttributes decides it) and the organization always pass.
func (s *discoverTeamScope) admits(subject contextfabric.SubjectRef) bool {
	if s.keep[subject.CanonicalID] {
		return true
	}
	switch subject.Kind {
	case contextfabric.SubjectTeam, contextfabric.SubjectOrganization:
		return true
	}
	return s.inScope[subject.CanonicalID]
}

// decide reads, in batches, the ownership that decides every not yet decided
// subject.
func (s *discoverTeamScope) decide(ctx context.Context, subjects []contextfabric.SubjectRef) error {
	byKind := map[contextfabric.SubjectKind][]string{}
	for _, subject := range subjects {
		id := subject.CanonicalID
		if id == "" || s.keep[id] || s.decided[id] {
			continue
		}
		switch subject.Kind {
		case contextfabric.SubjectTeam, contextfabric.SubjectOrganization:
			continue
		case contextfabric.SubjectRepository, contextfabric.SubjectProject, contextfabric.SubjectPullRequest, contextfabric.SubjectWorkItem:
			byKind[subject.Kind] = append(byKind[subject.Kind], id)
		default:
			// No ownership path to a team: out of scope, decided, no floor.
		}
		s.decided[id] = true
	}
	n := s.narrowing
	a, key, orgID := n.adapter, n.key, n.orgID
	current := currentOwnership(s.temporal, a.now())
	// The rule is CURRENT ownership on every axis (CHAOS-12319, as the
	// repository walk's narrowing reads it): an ownership edge is read at the
	// adapter clock, never at a historical question's instant.
	owned := currentOwnership(temporalFilter{}, a.now())
	budget := exactNameCandidateQueryLimit

	// Repositories reached by the pull requests and issues to decide, plus the
	// repositories to decide themselves.
	prs := byKind[contextfabric.SubjectPullRequest]
	issues := byKind[contextfabric.SubjectWorkItem]
	links, linksCut, err := a.walkStepHits(ctx, key, orgID, issues, issuePullRequestStep, s.temporal, budget)
	if err != nil {
		return err
	}
	linkedPRsOf := map[string][]string{}
	prSet := map[string]bool{}
	for _, pr := range prs {
		prSet[pr] = true
	}
	var linkedPRs []string
	for _, h := range links {
		if _, ok := linkTierOf(h.rel); !ok {
			continue
		}
		pr := canonicalIDOf(h.to)
		linkedPRsOf[h.from] = append(linkedPRsOf[h.from], pr)
		if !prSet[pr] {
			prSet[pr] = true
			linkedPRs = append(linkedPRs, pr)
		}
	}
	prRepos, prReposCut, err := a.walkStepHits(ctx, key, orgID, append(append([]string{}, prs...), linkedPRs...), pullRequestRepositoryStep, s.temporal, budget)
	if err != nil {
		return err
	}
	reposOf := map[string][]string{}
	repoSet := map[string]bool{}
	repos := append([]string{}, byKind[contextfabric.SubjectRepository]...)
	for _, r := range repos {
		repoSet[r] = true
	}
	for _, h := range prRepos {
		repo := canonicalIDOf(h.to)
		reposOf[h.from] = append(reposOf[h.from], repo)
		if !repoSet[repo] {
			repoSet[repo] = true
			repos = append(repos, repo)
		}
	}
	repoOwners, repoOwnersCut, err := a.walkStepHits(ctx, key, orgID, repos, repositoryOwnersStep, owned, budget)
	if err != nil {
		return err
	}
	repoOwned, repoHasOwner := map[string]bool{}, map[string]bool{}
	for _, h := range repoOwners {
		repoHasOwner[h.from] = true
		if n.isRequested(h.to) {
			repoOwned[h.from] = true
		}
	}

	// Projects: the projects to decide, plus the issues' own projects.
	projectHits, projectHitsCut, err := a.walkStepHits(ctx, key, orgID, issues, issueProjectStep, current, budget)
	if err != nil {
		return err
	}
	projectsOf := map[string][]string{}
	projectSet := map[string]bool{}
	projects := append([]string{}, byKind[contextfabric.SubjectProject]...)
	for _, p := range projects {
		projectSet[p] = true
	}
	for _, h := range projectHits {
		project := canonicalIDOf(h.to)
		projectsOf[h.from] = append(projectsOf[h.from], project)
		if !projectSet[project] {
			projectSet[project] = true
			projects = append(projects, project)
		}
	}
	projectOwners, projectOwnersCut, err := a.walkStepHits(ctx, key, orgID, projects, projectOwnersStep, owned, budget)
	if err != nil {
		return err
	}
	projectOwned, projectHasOwner := map[string]bool{}, map[string]bool{}
	for _, h := range projectOwners {
		projectHasOwner[h.from] = true
		if n.isRequested(h.to) {
			projectOwned[h.from] = true
		}
	}

	cutProjects := projectOwnersCut
	// repoReach: owned, or (not owned and) its ownership is missing.
	repoReach := func(repo string) (owned, missing bool) {
		return repoOwned[repo], !repoOwned[repo] && (!repoHasOwner[repo] || repoOwnersCut)
	}
	prReach := func(pr string) (owned, missing bool) {
		rs := reposOf[pr]
		if len(rs) == 0 {
			// No repository edge: the pull request's ownership is missing.
			return false, true
		}
		for _, r := range rs {
			if o, m := repoReach(r); o {
				return true, false
			} else if m {
				missing = true
			}
		}
		return false, missing || prReposCut
	}
	floor := func(id string, missing bool) {
		if missing {
			s.missing[id] = true
		}
	}
	for _, repo := range byKind[contextfabric.SubjectRepository] {
		owned, missing := repoReach(repo)
		s.inScope[repo] = owned
		floor(repo, !owned && missing)
	}
	for _, project := range byKind[contextfabric.SubjectProject] {
		owned := projectOwned[project]
		s.inScope[project] = owned
		floor(project, !owned && (!projectHasOwner[project] || cutProjects))
	}
	for _, pr := range prs {
		owned, missing := prReach(pr)
		s.inScope[pr] = owned
		floor(pr, !owned && missing)
	}
	for _, issue := range issues {
		reached, missing := false, linksCut || projectHitsCut
		for _, pr := range linkedPRsOf[issue] {
			if o, m := prReach(pr); o {
				reached = true
			} else if m {
				missing = true
			}
		}
		for _, project := range projectsOf[issue] {
			if projectOwned[project] {
				reached = true
			} else if !projectHasOwner[project] || cutProjects {
				missing = true
			}
		}
		s.inScope[issue] = reached
		floor(issue, !reached && missing)
	}
	return nil
}
