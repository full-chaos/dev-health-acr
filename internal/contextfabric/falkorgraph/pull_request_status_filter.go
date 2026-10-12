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

// storedNodePropertyCypher reads one stored property of a batch of nodes of one
// kind, as the column "value".
func storedNodePropertyCypher(property string) string {
	return fmt.Sprintf("UNWIND $ids AS id MATCH (p:%s {%s:$org, %s:$kind, %s:id}) RETURN id, p.%s AS value ORDER BY id",
		labelSubject, propOrgID, propKind, propCanonicalID, propPropertyPrefix+property)
}

// pullRequestStateCypher reads the stored state of a batch of pull requests.
func pullRequestStateCypher() string { return storedNodePropertyCypher("state") }

// readStoredNodeProperty reads the stored property of every id from the stored
// node, never from the pool node, because a link read returns members without
// their properties. An id with no stored node has no entry.
func (a *Adapter) readStoredNodeProperty(ctx context.Context, key, orgID string, kind contextfabric.SubjectKind, cypher string, ids []string, what string) (map[string]string, error) {
	values := make(map[string]string, len(ids))
	for start := 0; start < len(ids); start += storedSubjectBatch {
		end := min(start+storedSubjectBatch, len(ids))
		batch := make([]interface{}, 0, end-start)
		for _, id := range ids[start:end] {
			batch = append(batch, id)
		}
		rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{
			"org": orgID, "ids": batch, "kind": string(kind),
		}, true)
		if err != nil {
			return nil, safeDependencyError("read "+string(kind)+" "+what, err)
		}
		for _, r := range rows {
			if id, _ := r["id"].(string); id != "" {
				values[id] = propStringValue(r["value"])
			}
		}
	}
	return values, nil
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
	states, err := a.readStoredNodeProperty(ctx, key, orgID, contextfabric.SubjectPullRequest, pullRequestStateCypher(), ids, "state")
	if err != nil {
		return out, err
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
	// MemberKind is the kind filtered: pull_request when empty, else project
	// or incident.
	MemberKind contextfabric.SubjectKind
	// Status is the value of contextfabric.MemberStatusVocabulary(MemberKind) applied.
	Status string
	// Candidates and Kept count the pool's members of the kind before and after.
	Candidates, Kept int
	// OutsideSet counts the candidates whose stored status is outside the
	// kind's closed set; written only for a project or incident.
	OutsideSet int
	Err        error
}

func (d PullRequestStatusFilterDecision) memberKind() contextfabric.SubjectKind {
	if d.MemberKind == "" {
		return contextfabric.SubjectPullRequest
	}
	return d.MemberKind
}

func (d PullRequestStatusFilterDecision) outcome() PullRequestStatusFilterOutcome {
	if d.Err != nil {
		return PullRequestStatusFilterReadFailed
	}
	return PullRequestStatusFilterApplied
}
