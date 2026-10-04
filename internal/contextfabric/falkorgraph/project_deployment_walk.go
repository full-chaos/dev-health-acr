package falkorgraph

import (
	"context"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

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
			return nil, false, safeDependencyError("walk the entity tree", err)
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

// noRepositoryScope is the authorization scope a work item with no repository
// carries.
const noRepositoryScope = "acr-context-fabric:no-repository"

func repositoryLess(n *node) bool {
	repositories, _ := n.Properties[propAuthzRepos].([]string)
	return len(repositories) == 1 && repositories[0] == noRepositoryScope
}

func canonicalIDOf(n *node) string { return propStringValue(n.Properties[propCanonicalID]) }

// currentOwnership is the window ownership edges are read under: the
// question's window, or the adapter clock for a question about now. An
// ownership edge carries the period it held; an ended one is history, and a
// team that owned a repository once does not own it now.
func currentOwnership(temporal temporalFilter, now time.Time) temporalFilter {
	if temporal.active {
		return temporal
	}
	return newTemporalFilter(contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &now})
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
	// ProjectDeploymentWalkUnlinked: no member, the path crosses the issue <>
	// pull request link, and nothing crosses it (unrestricted caller, uncut
	// frontier): no issue of the project links a pull request.
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

// AnchorBasisVocabulary returns every declared anchor basis, in
// declaration order.
func AnchorBasisVocabulary() []AnchorBasis {
	return []AnchorBasis{AnchorBasisNone, AnchorBasisBound, AnchorBasisSoleCommit}
}

// ProjectDeploymentWalkDecision is one decision line of the walk: counts and
// closed values only, never a name or an id.
type ProjectDeploymentWalkDecision struct {
	Outcome     ProjectDeploymentWalkOutcome
	AnchorKind  contextfabric.SubjectKind
	AnchorBasis AnchorBasis
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
func projectDeploymentWalkOutcome(walk treeWalk, restricted bool, err error) ProjectDeploymentWalkOutcome {
	switch {
	case err != nil:
		return ProjectDeploymentWalkReadFailed
	case len(walk.nodes) > 0:
		return ProjectDeploymentWalkMembers
	case restricted:
		return ProjectDeploymentWalkDenied
	case walk.truncated:
		return ProjectDeploymentWalkTruncated
	case walk.hasLink && walk.linkTargets == 0:
		return ProjectDeploymentWalkUnlinked
	default:
		return ProjectDeploymentWalkNoDeployments
	}
}
