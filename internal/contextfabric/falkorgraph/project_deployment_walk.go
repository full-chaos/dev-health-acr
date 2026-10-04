package falkorgraph

import (
	"context"
	"fmt"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A project has no edge to a repository. It reaches its deployments through
// its issues' linked pull requests: project <-BELONGS_TO_PROJECT- issue
// -RELATES_TO- pull request -BELONGS_TO_REPOSITORY-> repository
// <-BELONGS_TO_REPOSITORY- deployment. The link is the projected
// work_item_dependencies row between an issue and a pull-request work item,
// never an issue-key prefix, an external issue key, or an issue's own
// repository.
//
// The walk is four batched reads, one per step, not a wider hop radius: each
// step is bounded by the caller's collect budget and a spent budget is
// reported as truncation.

// propWorkItemType is the work item's stored `type` property.
const propWorkItemType = propPropertyPrefix + "type"

// pullRequestWorkItemTypes are the stored types of a pull-request work item.
var pullRequestWorkItemTypes = []interface{}{"pr", "merge_request"}

type walkDirection int

const (
	walkOut walkDirection = iota
	walkIn
	walkEither
)

type walkStep struct {
	fromKind, toKind contractsv1.ContextFabricSubjectKind
	relation         contractsv1.ContextFabricRelationshipType
	direction        walkDirection
	toTypes          []interface{}
	notToTypes       []interface{}
}

type walkHit struct {
	from string
	to   *node
	rel  *edge
}

// The four steps of the project deployment walk, in order.
var (
	projectIssuesStep = walkStep{
		fromKind: contractsv1.ContextFabricSubjectProject, toKind: contractsv1.ContextFabricSubjectWorkItem,
		relation: contractsv1.ContextFabricRelationshipBelongsToProject, direction: walkIn, notToTypes: pullRequestWorkItemTypes,
	}
	issuePullRequestsStep = walkStep{
		fromKind: contractsv1.ContextFabricSubjectWorkItem, toKind: contractsv1.ContextFabricSubjectWorkItem,
		relation: contractsv1.ContextFabricRelationshipRelatesTo, direction: walkEither, toTypes: pullRequestWorkItemTypes,
	}
	pullRequestRepositoriesStep = walkStep{
		fromKind: contractsv1.ContextFabricSubjectWorkItem, toKind: contractsv1.ContextFabricSubjectRepository,
		relation: contractsv1.ContextFabricRelationshipBelongsToRepository, direction: walkOut,
	}
	repositoryDeploymentsStep = walkStep{
		fromKind: contractsv1.ContextFabricSubjectRepository, toKind: contractsv1.ContextFabricSubjectDeployment,
		relation: contractsv1.ContextFabricRelationshipBelongsToRepository, direction: walkIn,
	}
)

// walkStepCypher is the read of one step: the neighbours of the batched ids
// along one relationship, as ONE path pattern from the origin node, in a
// deterministic order, with the window applied to the edge and the neighbour.
func walkStepCypher(step walkStep, temporal temporalFilter) string {
	var arrow string
	switch step.direction {
	case walkOut:
		arrow = "-[r:%s]->"
	case walkIn:
		arrow = "<-[r:%s]-"
	default:
		arrow = "-[r:%s]-"
	}
	typeClause := ""
	switch {
	case len(step.toTypes) > 0:
		typeClause = fmt.Sprintf(" AND b.%s IN $btypes", propWorkItemType)
	case len(step.notToTypes) > 0:
		typeClause = fmt.Sprintf(" AND (b.%s IS NULL OR NOT b.%s IN $btypes)", propWorkItemType, propWorkItemType)
	}
	return fmt.Sprintf("UNWIND $ids AS id MATCH (a:%s {%s:$org, %s:$fromKind, %s:id})"+arrow+"(b:%s {%s:$org, %s:$toKind}) WHERE r.%s = $rel%s%s%s RETURN id, b, r ORDER BY id, b.%s, r.%s LIMIT $limit",
		labelSubject, propOrgID, propKind, propCanonicalID, labelRelation, labelSubject, propOrgID, propKind,
		propRelationType, typeClause, temporal.predicate("r"), temporal.predicate("b"), propCanonicalID, propRelationshipID)
}

// walkStepParams binds one batch of a step read.
func walkStepParams(orgID string, ids []interface{}, step walkStep, limit int, temporal temporalFilter) map[string]interface{} {
	params := temporal.bind(map[string]interface{}{
		"org": orgID, "ids": ids, "fromKind": string(step.fromKind), "toKind": string(step.toKind), "rel": string(step.relation),
		"limit": limit,
	})
	if len(step.toTypes) > 0 {
		params["btypes"] = step.toTypes
	} else if len(step.notToTypes) > 0 {
		params["btypes"] = step.notToTypes
	}
	return params
}

// walkStepHits reads the neighbours of ids along one relationship, in a
// deterministic order, with the window applied to the edge and the neighbour.
func (a *Adapter) walkStepHits(ctx context.Context, key, orgID string, ids []string, step walkStep, temporal temporalFilter, budget int) ([]walkHit, bool, error) {
	cypher := walkStepCypher(step, temporal)
	var hits []walkHit
	cut := false
	for start := 0; start < len(ids); start += storedSubjectBatch {
		remaining := budget - len(hits)
		if budget > 0 && remaining <= 0 {
			cut = true
			break
		}
		end := min(start+storedSubjectBatch, len(ids))
		batch := make([]interface{}, 0, end-start)
		for _, id := range ids[start:end] {
			batch = append(batch, id)
		}
		limit := 1 << 30
		if budget > 0 {
			limit = remaining + 1
		}
		rows, err := a.api.query(ctx, key, cypher, walkStepParams(orgID, batch, step, limit, temporal), true)
		if err != nil {
			return nil, false, safeDependencyError("walk project deployments", err)
		}
		if budget > 0 && len(rows) > remaining {
			rows = rows[:remaining]
			cut = true
		}
		for _, r := range rows {
			id, _ := r["id"].(string)
			n, _ := r["b"].(*node)
			e, _ := r["r"].(*edge)
			if id == "" || n == nil || e == nil {
				continue
			}
			hits = append(hits, walkHit{from: id, to: n, rel: e})
		}
	}
	return hits, cut, nil
}

// projectDeploymentWalk is the result of the project -> deployments read.
type projectDeploymentWalk struct {
	nodes     []graphrank.CandidateNode
	edges     []graphrank.ResolvedEdge
	filters   edgeFilterCounts
	truncated bool
	// issues is how many of the project's issues the read examined and
	// linkedPullRequests how many pull-request work items they link, before
	// authorization. Zero linked pull requests is the unlinked terminal.
	issues, linkedPullRequests int
	// denied counts the links, repositories and deployments the caller's
	// authorization hid. Members unseen for that reason are not an unlinked
	// project.
	denied int
}

// noRepositoryScope is the authorization scope a work item with no repository
// carries.
const noRepositoryScope = "acr-context-fabric:no-repository"

func repositoryLess(n *node) bool {
	repositories, _ := n.Properties[propAuthzRepos].([]string)
	return len(repositories) == 1 && repositories[0] == noRepositoryScope
}

func canonicalIDOf(n *node) string { return propStringValue(n.Properties[propCanonicalID]) }

// projectDeploymentMembers returns the deployments of the repositories the
// project's issues' linked pull requests belong to. Only the pull request,
// the repository and the deployment must pass the caller's authorization, and
// so must the issue under the work-item rule: an issue with a repository of its
// own is decided by that repository; a repository-less issue (Linear, Jira) is
// admitted by its native link to a pull request the caller is granted.
func (a *Adapter) projectDeploymentMembers(ctx context.Context, key, orgID string, principal storage.Principal, scope contextfabric.RequestedScope, project contextfabric.SubjectRef, collectLimit int, temporal temporalFilter) (projectDeploymentWalk, error) {
	var out projectDeploymentWalk
	authorized := func(n *node) bool {
		return graphrank.AuthorizedAttributes(principal, scope, toCandidateNode(n).Attributes)
	}
	// cut bounds a frontier by the collect budget; a cut one is truncation.
	cut := func(ids []string) []string {
		sort.Strings(ids)
		if collectLimit > 0 && len(ids) > collectLimit {
			out.truncated = true
			return ids[:collectLimit]
		}
		return ids
	}
	unique := func(hits []walkHit, pick func(walkHit) string) []string {
		seen := map[string]bool{}
		var ids []string
		for _, h := range hits {
			if id := pick(h); id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		return ids
	}
	toID := func(h walkHit) string { return canonicalIDOf(h.to) }
	deny := func() {
		out.filters.add(edgeFiltered, edgeFilterReasonAuthz)
		out.denied++
	}

	issueHits, issuesCut, err := a.walkStepHits(ctx, key, orgID, []string{project.CanonicalID}, projectIssuesStep, temporal, collectLimit)
	if err != nil {
		return out, err
	}
	out.truncated = out.truncated || issuesCut
	issues := cut(unique(issueHits, toID))
	out.issues = len(issues)
	issueNodes := map[string]*node{}
	for _, h := range issueHits {
		issueNodes[canonicalIDOf(h.to)] = h.to
	}

	var pullRequests []string
	if len(issues) > 0 {
		linkHits, linksCut, err := a.walkStepHits(ctx, key, orgID, issues, issuePullRequestsStep, temporal, collectLimit)
		if err != nil {
			return out, err
		}
		out.truncated = out.truncated || linksCut
		pullRequests = unique(linkHits, toID)
		out.linkedPullRequests = len(pullRequests)
		authorizedPRs := map[string]bool{}
		for _, h := range linkHits {
			issue := issueNodes[h.from]
			switch {
			case issue == nil || !(authorized(issue) || repositoryLess(issue)):
				deny()
			case !authorized(h.to):
				deny()
			default:
				authorizedPRs[canonicalIDOf(h.to)] = true
			}
		}
		pullRequests = pullRequests[:0]
		for id := range authorizedPRs {
			pullRequests = append(pullRequests, id)
		}
		pullRequests = cut(pullRequests)
	}
	if len(pullRequests) == 0 {
		return out, nil
	}

	repoHits, reposCut, err := a.walkStepHits(ctx, key, orgID, pullRequests, pullRequestRepositoriesStep, temporal, collectLimit)
	if err != nil {
		return out, err
	}
	out.truncated = out.truncated || reposCut
	repositories := map[string]*node{}
	for _, h := range repoHits {
		if !authorized(h.to) {
			deny()
			continue
		}
		repositories[canonicalIDOf(h.to)] = h.to
	}
	repositoryIDs := make([]string, 0, len(repositories))
	for id := range repositories {
		repositoryIDs = append(repositoryIDs, id)
	}
	repositoryIDs = cut(repositoryIDs)
	if len(repositoryIDs) == 0 {
		return out, nil
	}

	deploymentHits, deploymentsCut, err := a.walkStepHits(ctx, key, orgID, repositoryIDs, repositoryDeploymentsStep, temporal, collectLimit)
	if err != nil {
		return out, err
	}
	out.truncated = out.truncated || deploymentsCut
	seen := map[string]bool{}
	for _, h := range deploymentHits {
		if !authorized(h.to) {
			deny()
			continue
		}
		id := canonicalIDOf(h.to)
		if seen[id] {
			continue
		}
		if collectLimit > 0 && len(out.nodes) >= collectLimit {
			out.truncated = true
			break
		}
		seen[id] = true
		candidate := toCandidateNode(h.to)
		out.nodes = append(out.nodes, candidate)
		deployment, okDeployment := graphrank.NodeSubject(candidate)
		repository, okRepository := graphrank.NodeSubject(toCandidateNode(repositories[h.from]))
		if !okDeployment || !okRepository {
			continue
		}
		edgeCandidate := toCandidateEdge(h.rel, string(deployment.Kind), deployment.CanonicalID, string(repository.Kind), repository.CanonicalID)
		out.edges = append(out.edges, graphrank.ResolvedEdge{
			UUID: edgeCandidate.UUID, Name: edgeCandidate.Name, Fact: edgeCandidate.Fact, From: deployment, To: repository,
			Attributes: edgeCandidate.Attributes, CreatedAt: edgeCandidate.CreatedAt, ValidAt: edgeCandidate.ValidAt, InvalidAt: edgeCandidate.InvalidAt,
		})
	}
	sortCandidateNodesBySubjectKey(out.nodes)
	return out, nil
}

// ProjectDeploymentWalkOutcome is the closed outcome of one deployment-members
// discovery with respect to the project walk.
type ProjectDeploymentWalkOutcome string

const (
	// ProjectDeploymentWalkNotRouted: the frame asks for deployment members
	// and the walk did not run, because no committed project is the anchor.
	ProjectDeploymentWalkNotRouted ProjectDeploymentWalkOutcome = "not_routed"
	// ProjectDeploymentWalkMembers: the walk reached at least one deployment.
	ProjectDeploymentWalkMembers ProjectDeploymentWalkOutcome = "members"
	// ProjectDeploymentWalkUnlinked: no member, and no issue of the project
	// links a pull request (unrestricted caller, uncut frontier).
	ProjectDeploymentWalkUnlinked ProjectDeploymentWalkOutcome = "unlinked"
	// ProjectDeploymentWalkDenied: no member for a repository-restricted
	// caller. The served reason is neutral; the counts stay on this line.
	ProjectDeploymentWalkDenied ProjectDeploymentWalkOutcome = "denied"
	// ProjectDeploymentWalkTruncated: no member, and a frontier was cut.
	ProjectDeploymentWalkTruncated ProjectDeploymentWalkOutcome = "truncated"
	// ProjectDeploymentWalkNoDeployments: no member, links exist, nothing was
	// cut: the reached repositories hold no deployment in the window.
	ProjectDeploymentWalkNoDeployments ProjectDeploymentWalkOutcome = "no_deployments"
	// ProjectDeploymentWalkReadFailed: a step read failed and the call ends.
	ProjectDeploymentWalkReadFailed ProjectDeploymentWalkOutcome = "read_failed"
)

// ProjectDeploymentWalkOutcomeVocabulary returns every declared outcome, in
// declaration order.
func ProjectDeploymentWalkOutcomeVocabulary() []ProjectDeploymentWalkOutcome {
	return []ProjectDeploymentWalkOutcome{
		ProjectDeploymentWalkNotRouted, ProjectDeploymentWalkMembers, ProjectDeploymentWalkUnlinked, ProjectDeploymentWalkDenied,
		ProjectDeploymentWalkTruncated, ProjectDeploymentWalkNoDeployments, ProjectDeploymentWalkReadFailed,
	}
}

// DeploymentAnchorBasisVocabulary returns every declared anchor basis, in
// declaration order.
func DeploymentAnchorBasisVocabulary() []DeploymentAnchorBasis {
	return []DeploymentAnchorBasis{DeploymentAnchorNone, DeploymentAnchorBound, DeploymentAnchorSoleCommit}
}

// ProjectDeploymentWalkDecision is one decision line of the walk: counts and
// closed values only, never a name or an id.
type ProjectDeploymentWalkDecision struct {
	Outcome     ProjectDeploymentWalkOutcome
	AnchorKind  contextfabric.SubjectKind
	AnchorBasis DeploymentAnchorBasis
	// Committed is how many subjects the resolution committed.
	Committed int
	// Issues, LinkedPullRequests, Members, Denied and Truncated describe a
	// walk that ran.
	Issues, LinkedPullRequests, Members, Denied int
	Truncated                                   bool
	// Err is the failed read of a walk that did not finish.
	Err error
}

// projectDeploymentWalkOutcome classifies a finished walk. A restricted caller
// with no member is denied whether links are hidden or absent; a cut frontier
// is never reported as unlinked.
func projectDeploymentWalkOutcome(walk projectDeploymentWalk, restricted bool, err error) ProjectDeploymentWalkOutcome {
	switch {
	case err != nil:
		return ProjectDeploymentWalkReadFailed
	case len(walk.nodes) > 0:
		return ProjectDeploymentWalkMembers
	case restricted:
		return ProjectDeploymentWalkDenied
	case walk.truncated:
		return ProjectDeploymentWalkTruncated
	case walk.linkedPullRequests == 0:
		return ProjectDeploymentWalkUnlinked
	default:
		return ProjectDeploymentWalkNoDeployments
	}
}
