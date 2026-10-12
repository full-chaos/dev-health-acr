package falkorgraph

import (
	"context"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// pullRequestReviewStep reads the review rows of pull requests: each review
// belongs to its pull request (review -BELONGS_TO_PULL_REQUEST-> pull request).
var pullRequestReviewStep = walkStep{
	fromKind:  contractsv1.ContextFabricSubjectPullRequest,
	toKind:    contractsv1.ContextFabricSubjectPullRequestReview,
	relation:  contractsv1.ContextFabricRelationshipBelongsToPullRequest,
	direction: walkIn,
}

// pullRequestStatusFilter is the outcome of filtering one member pool by a
// pull request status.
type pullRequestStatusFilter struct {
	nodes []graphrank.CandidateNode
	// candidates and kept count the pool's pull requests before and after.
	candidates, kept int
}

// pullRequestStatusNoMatchRationale is the rationale of a pull request member
// set in which no pull request reached holds the status asked for.
const pullRequestStatusNoMatchRationale = "No pull request reached from the subject the question names holds the status asked for now; that is a count of matches, not a statement about health."

// pullRequestStateCypher reads the stored state of a batch of pull requests.
func pullRequestStateCypher() string {
	return fmt.Sprintf("UNWIND $ids AS id MATCH (p:%s {%s:$org, %s:$kind, %s:id}) RETURN id, p.%s AS state ORDER BY id",
		labelSubject, propOrgID, propKind, propCanonicalID, propPropertyPrefix+"state")
}

// filterPullRequestsByStatus keeps the pool's nodes that are not pull requests
// and the pull requests that hold status now (contextfabric.PullRequestStatusMatches).
// The state is read from each pull request's stored node, never from the pool
// node, because a link read returns members without their properties. The
// status is as of now whatever window the question states, so the review rows
// are read under no window, and every review row of every open pull request is
// read: a cut read would leave a pull request's latest review unknown.
func (a *Adapter) filterPullRequestsByStatus(ctx context.Context, key, orgID, status string, pool []graphrank.CandidateNode) (pullRequestStatusFilter, error) {
	out := pullRequestStatusFilter{}
	ids := []string{}
	for _, n := range pool {
		if subject, ok := graphrank.NodeSubject(n); ok && subject.Kind == contextfabric.SubjectPullRequest {
			ids = append(ids, subject.CanonicalID)
		}
	}
	out.candidates = len(ids)
	if len(ids) == 0 {
		out.nodes = pool
		return out, nil
	}
	states := make(map[string]string, len(ids))
	for start := 0; start < len(ids); start += storedSubjectBatch {
		end := min(start+storedSubjectBatch, len(ids))
		batch := make([]interface{}, 0, end-start)
		for _, id := range ids[start:end] {
			batch = append(batch, id)
		}
		rows, err := a.api.query(ctx, key, pullRequestStateCypher(), map[string]interface{}{
			"org": orgID, "ids": batch, "kind": string(contextfabric.SubjectPullRequest),
		}, true)
		if err != nil {
			return out, safeDependencyError("read pull request states", err)
		}
		for _, r := range rows {
			if id, _ := r["id"].(string); id != "" {
				states[id] = propStringValue(r["state"])
			}
		}
	}
	reviews := map[string][]contextfabric.PullRequestReviewRow{}
	if contextfabric.PullRequestStatusReviewDerived(status) {
		open := make([]string, 0, len(ids))
		for _, id := range ids {
			if contextfabric.PullRequestStatusMatches(contextfabric.PullRequestStatusOpen, states[id], nil) {
				open = append(open, id)
			}
		}
		hits, _, err := a.walkStepHits(ctx, key, orgID, open, pullRequestReviewStep, temporalFilter{}, 0)
		if err != nil {
			return out, err
		}
		for _, h := range hits {
			submitted, _ := h.to.Properties[propValidFromNs].(int64)
			reviews[h.from] = append(reviews[h.from], contextfabric.PullRequestReviewRow{
				State: propStringValue(h.to.Properties[propPropertyPrefix+"state"]), SubmittedNs: submitted,
			})
		}
	}
	out.nodes = make([]graphrank.CandidateNode, 0, len(pool))
	for _, n := range pool {
		subject, ok := graphrank.NodeSubject(n)
		if !ok || subject.Kind != contextfabric.SubjectPullRequest {
			out.nodes = append(out.nodes, n)
			continue
		}
		if contextfabric.PullRequestStatusMatches(status, states[subject.CanonicalID], reviews[subject.CanonicalID]) {
			out.nodes = append(out.nodes, n)
			out.kept++
		}
	}
	return out, nil
}

// PullRequestStatusFilterOutcome is the closed outcome of one pull request
// status filter.
type PullRequestStatusFilterOutcome string

const (
	// PullRequestStatusFilterApplied: the filter read finished.
	PullRequestStatusFilterApplied PullRequestStatusFilterOutcome = "applied"
	// PullRequestStatusFilterReadFailed: a read failed and the call ends.
	PullRequestStatusFilterReadFailed PullRequestStatusFilterOutcome = "read_failed"
)

// PullRequestStatusFilterOutcomeVocabulary returns every declared outcome, in
// declaration order.
func PullRequestStatusFilterOutcomeVocabulary() []PullRequestStatusFilterOutcome {
	return []PullRequestStatusFilterOutcome{PullRequestStatusFilterApplied, PullRequestStatusFilterReadFailed}
}

// PullRequestStatusFilterDecision is one decision line of the pull request
// status filter: counts and closed values only, never a name or an id.
type PullRequestStatusFilterDecision struct {
	// Status is the value of contextfabric.PullRequestStatusVocabulary applied.
	Status string
	// Candidates and Kept count the pool's pull requests before and after.
	Candidates, Kept int
	Err              error
}

func (d PullRequestStatusFilterDecision) outcome() PullRequestStatusFilterOutcome {
	if d.Err != nil {
		return PullRequestStatusFilterReadFailed
	}
	return PullRequestStatusFilterApplied
}
