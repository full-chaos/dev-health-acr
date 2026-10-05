package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var _ contextfabric.TreeWorkItemGraph = (*Adapter)(nil)

// TreeWorkItemMembers walks the entity tree from a named repository to its
// issues: repository <- pull request (BELONGS_TO_REPOSITORY), then pull
// request <- issue (LINKS_PULL_REQUEST), read fused; the far side of the link
// is the member set (treeMembers). An issue is a member only through an
// actual link row; its own repository, and any RELATES_TO edge, make none.
//
// A member's canonical id is the projected work item node id,
// work_item.v2:<the issue's own repo_id>:<work item id>: the walk returns the
// node's stored canonical id as is (graphrank.NodeSubject), and the projection
// mints it from the work item's own row (devhealthsource/tables.go queryWorkItems,
// identity.Derive(KindWorkItem, {repo_id, work_item_id}) at tables.go:347).
// A repository-less issue carries the repo_id of its own row, never the
// anchor's.
//
// Membership is the current view: no window is applied here; windows apply
// later, on the members' facts (TreeWorkItemFilter).
func (a *Adapter) TreeWorkItemMembers(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, scope contextfabric.RequestedScope, anchor contextfabric.SubjectRef, limit int) (contextfabric.TreeWorkItemWalk, error) {
	if strings.TrimSpace(principal.OrgID) == "" {
		return contextfabric.TreeWorkItemWalk{}, errors.New("authenticated organization is required")
	}
	if err := ctx.Err(); err != nil {
		return contextfabric.TreeWorkItemWalk{}, err
	}
	if anchor.Kind != contextfabric.SubjectRepository {
		return contextfabric.TreeWorkItemWalk{}, fmt.Errorf("tree work item members are served for a repository anchor, not %q", anchor.Kind)
	}
	key, err := a.effectiveKey(ctx, principal.OrgID, binding)
	if err != nil {
		return contextfabric.TreeWorkItemWalk{}, err
	}
	// A repository-restricted caller's reads see each project's live
	// ownership reach in place of its "*" (project_reach.go).
	ctx, err = a.withProjectReach(ctx, key, principal)
	if err != nil {
		return contextfabric.TreeWorkItemWalk{}, err
	}
	walk, err := a.treeMembers(ctx, key, principal.OrgID, principal, scope, anchor, treeIssue, limit, temporalFilter{})
	if err != nil {
		return contextfabric.TreeWorkItemWalk{}, graphNotProjectedError(err)
	}
	out := contextfabric.TreeWorkItemWalk{
		PullRequests: walk.linkSources, LinkedIssues: max(walk.linkIssueTargets, 0),
		Denied: walk.linkDeniedTargets, Truncated: walk.truncated,
	}
	for _, n := range walk.nodes {
		subject, ok := graphrank.NodeSubject(n)
		if !ok {
			continue
		}
		out.Members = append(out.Members, contextfabric.TreeWorkItemMember{Subject: subject, Tier: walk.memberTiers[subject.CanonicalID]})
	}
	sort.Slice(out.Members, func(i, j int) bool { return out.Members[i].Subject.CanonicalID < out.Members[j].Subject.CanonicalID })
	return out, nil
}

// maxLinkScopedRepositories bounds the repositories one requested scope walks.
// A scope that names more is read as cut.
const maxLinkScopedRepositories = 25

// linkScopedIssues returns the issues in a requested repository scope: the
// issues linked to a pull request of a repository the scope names, found by
// the same walk that serves a repository's work items (treeMembers, repository
// to issue, the link rule of admitted). It is the union of that walk over
// every repository of the scope the caller may see. complete is false when the
// repositories or a walk were cut, or the union is larger than limit: the set
// is then not the whole population.
func (a *Adapter) linkScopedIssues(ctx context.Context, key string, principal storage.Principal, scope contextfabric.RequestedScope, limit int) ([]string, bool, error) {
	if len(scope.RepositorySlugs) == 0 {
		return nil, false, errors.New("a link-scoped issue read needs a requested repository scope")
	}
	repositories, cut, err := a.cohortKindCensusCandidates(ctx, key, principal.OrgID, []string{string(contextfabric.SubjectRepository)}, temporalFilter{})
	if err != nil {
		return nil, false, err
	}
	complete := !cut
	walked := 0
	members := map[string]bool{}
	for _, repository := range repositories {
		if !graphrank.AuthorizedAttributes(principal, scope, repository.Attributes) {
			continue
		}
		anchor, ok := graphrank.NodeSubject(repository)
		if !ok || anchor.Kind != contextfabric.SubjectRepository {
			continue
		}
		if walked == maxLinkScopedRepositories {
			complete = false
			break
		}
		walked++
		walk, err := a.treeMembers(ctx, key, principal.OrgID, principal, scope, anchor, treeIssue, limit+1, temporalFilter{})
		if err != nil {
			return nil, false, err
		}
		complete = complete && !walk.truncated
		for _, n := range walk.nodes {
			if subject, ok := graphrank.NodeSubject(n); ok {
				members[subject.CanonicalID] = true
			}
		}
		if len(members) > limit {
			complete = false
			break
		}
	}
	ids := make([]string, 0, len(members))
	for id := range members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, complete, nil
}
