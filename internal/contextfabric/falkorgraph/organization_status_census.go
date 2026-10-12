package falkorgraph

import (
	"context"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// organizationStatusAnchor is the caller's own organization when it is the
// one scope anchor of a question for pull requests holding a status. Its
// members are every pull request of the organization, so they are read by the
// status census, never by the generic walk. Nil for any other question.
func organizationStatusAnchor(request contextfabric.GraphDiscoveryRequest, declaredKind contextfabric.SubjectKind, status memberStatusPredicate, principal storage.Principal) *contextfabric.SubjectRef {
	if declaredKind != contextfabric.SubjectPullRequest || !status.appliesTo(contractsv1.ContextFabricSubjectPullRequest) {
		return nil
	}
	anchors := contextfabric.ScopeAnchorSubjects(request.Frame, request.ScopeAnchorKind, request.Resolution)
	if len(anchors) != 1 || anchors[0].Kind != contextfabric.SubjectOrganization || !contextfabric.OrganizationSubjectIsCallers(principal, anchors[0]) {
		return nil
	}
	return &anchors[0]
}

// pullRequestStatusCensus reports whether the kind census of a pull request
// cohort is read under the status the frame states.
func pullRequestStatusCensus(declaredKind contextfabric.SubjectKind, status memberStatusPredicate) bool {
	return declaredKind == contextfabric.SubjectPullRequest && status.appliesTo(contractsv1.ContextFabricSubjectPullRequest)
}

// statusCensusCypher reads the organization's pull requests, in id order,
// keeping those the clause keeps inside the read, and for a restricted caller
// only those its grants can admit (a necessary condition of the per-node
// decision), so neither non-matching nor unreadable pull requests fill the
// pages before the ones that count.
func statusCensusCypher(clause nodeStatusClause, temporal temporalFilter, restricted, projected bool) string {
	grant := ""
	if restricted {
		grant = treeNodeVar{name: "b", position: treePullRequest}.grantClause()
	}
	column := "b"
	if projected {
		column = walkProjection("b", walkNodeProperties)
	}
	return fmt.Sprintf("MATCH (b:%s) WHERE b.%s = $org AND b.%s = $toKind%s%s%s RETURN %s ORDER BY b.%s SKIP $skip LIMIT $limit",
		labelSubject, propOrgID, propKind, clause.predicate("b"), temporal.predicate("b"), grant, column, propCanonicalID)
}

// statusCensusPage reads one page of the status census. A census hit has no
// edge: the organization reaches its pull requests by belonging, not by a
// relationship.
func (a *Adapter) statusCensusPage(ctx context.Context, key, orgID string, principal storage.Principal, clause nodeStatusClause, temporal temporalFilter, projected bool, skip, limit int) ([]walkHit, int, error) {
	restricted := needsProjectReach(principal)
	params := temporal.bind(map[string]interface{}{"org": orgID, "toKind": string(contextfabric.SubjectPullRequest), "skip": skip, "limit": limit})
	clause.bind(params)
	if restricted {
		linkSegmentGrants(params, principal)
	}
	rows, err := a.api.query(ctx, key, statusCensusCypher(clause, temporal, restricted, projected), params, true)
	if err != nil {
		return nil, 0, safeDependencyError("read the pull request status census", err)
	}
	hits := make([]walkHit, 0, len(rows))
	for _, r := range rows {
		if n := walkNode(r["b"]); n != nil {
			hits = append(hits, walkHit{to: n})
		}
	}
	return hits, len(rows), nil
}

// statusCensusMembers reads the pull requests of the caller's organization
// holding the status: the paged status read of an anchor's members over the
// census, so the same cap, page cap, authorization and pool count apply. When
// the read is cut, the pool is counted up to the census bound.
func (a *Adapter) statusCensusMembers(ctx context.Context, key, orgID string, principal storage.Principal, scope contextfabric.RequestedScope, anchorKind contextfabric.SubjectKind, collectLimit int, temporal temporalFilter, status memberStatusPredicate) (treeWalk, error) {
	out := treeWalk{anchorKind: anchorKind, status: memberStatusWalk{applied: true, pool: -1}}
	state := treeWalkState{out: &out, principal: principal, scope: scope, collectLimit: collectLimit, frontierLimit: collectLimit, status: status}
	census := func(clause nodeStatusClause, projected bool) statusPage {
		return func(skip, limit int) ([]walkHit, int, error) {
			return a.statusCensusPage(ctx, key, orgID, principal, clause, temporal, projected, skip, limit)
		}
	}
	hits, err := a.statusPagedHits(ctx, key, orgID, state, treeHop{}, []statusPage{census(status.clause(), false)}, collectLimit)
	if err != nil {
		return out, err
	}
	state.members(treeHop{}, hits, nil)
	out.status.kept = len(out.nodes)
	out.status.reached = out.status.kept > 0
	if !out.status.reached {
		probe, _, err := census(nodeStatusClause{}, true)(0, max(collectLimit, 1))
		if err != nil {
			return out, err
		}
		for _, h := range probe {
			if state.authorized(h.to) {
				out.status.reached = true
				break
			}
		}
	}
	err = state.decidePoolCut(func() (int, bool, error) {
		hits, _, err := census(status.clausePredicate().clause(), true)(0, exactNameCandidateQueryLimit+1)
		if err != nil {
			return 0, false, err
		}
		counted := map[string]bool{}
		for _, h := range hits {
			if state.authorized(h.to) {
				counted[canonicalIDOf(h.to)] = true
			}
		}
		return len(counted), len(hits) > exactNameCandidateQueryLimit, nil
	})
	return out, err
}

// organizationStatusInclusionReason is the inclusion reason of a pull request
// the status census of the named organization admitted.
const organizationStatusInclusionReason = "Pull request of the organization the question names, read in the authorized Context Fabric graph."

// organizationStatusRationale is the rationale of that member set.
const organizationStatusRationale = "Pull requests of the organization the question names were read in the authorized Context Fabric graph."

// statusCensusNodesOnly keeps the nodes of a pool that are not pull requests:
// with the census read under the status, a pull request any other arm reached
// was not decided by it.
func statusCensusNodesOnly(pool []graphrank.CandidateNode) []graphrank.CandidateNode {
	kept := make([]graphrank.CandidateNode, 0, len(pool))
	for _, n := range pool {
		if subject, ok := graphrank.NodeSubject(n); ok && subject.Kind == contextfabric.SubjectPullRequest {
			continue
		}
		kept = append(kept, n)
	}
	return kept
}
