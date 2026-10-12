package falkorgraph

import (
	"context"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// nodeStatusClause keeps the reached nodes by a stored status, compared
// exactly: acr holds no synonyms (contextfabric.PullRequestStatusMatches and
// the project and incident filter compare the same way). The zero value keeps
// every node.
type nodeStatusClause struct {
	// property is the stored node property, with its prefix.
	property string
	values   []interface{}
	// outside inverts the clause: the nodes whose status is absent or not one
	// of values.
	outside bool
}

func (c nodeStatusClause) set() bool { return c.property != "" }

func (c nodeStatusClause) predicate(variable string) string {
	if !c.set() {
		return ""
	}
	if c.outside {
		return fmt.Sprintf(" AND (%[1]s.%[2]s IS NULL OR NOT %[1]s.%[2]s IN $bstatus)", variable, c.property)
	}
	return fmt.Sprintf(" AND %s.%s IN $bstatus", variable, c.property)
}

func (c nodeStatusClause) bind(params map[string]interface{}) {
	if c.set() {
		params["bstatus"] = c.values
	}
}

// memberStatusPredicate is the status a frame filters its members by: a pull
// request status (contextfabric.PullRequestStatusFilterOf) or a project or
// incident status (contextfabric.MemberStatusFilterOf). The zero value filters
// nothing.
type memberStatusPredicate struct {
	kind   contextfabric.SubjectKind
	status string
}

func memberStatusPredicateOf(frame *contextfabric.QuestionFrame) memberStatusPredicate {
	if status := contextfabric.PullRequestStatusFilterOf(frame); status != "" {
		return memberStatusPredicate{kind: contextfabric.SubjectPullRequest, status: status}
	}
	if kind, status := contextfabric.MemberStatusFilterOf(frame); status != "" {
		return memberStatusPredicate{kind: kind, status: status}
	}
	return memberStatusPredicate{}
}

func (p memberStatusPredicate) set() bool { return p.status != "" }

// appliesTo reports whether the predicate filters nodes of kind.
func (p memberStatusPredicate) appliesTo(kind contractsv1.ContextFabricSubjectKind) bool {
	return p.set() && string(p.kind) == string(kind)
}

// reviewDerived reports whether the status is decided from review rows, so
// the read can push only the state (open) and the review rows decide the rest.
func (p memberStatusPredicate) reviewDerived() bool {
	return p.kind == contextfabric.SubjectPullRequest && contextfabric.PullRequestStatusReviewDerived(p.status)
}

func (p memberStatusPredicate) storedProperty() string {
	if p.kind == contextfabric.SubjectPullRequest {
		return propPropertyPrefix + "state"
	}
	return propPropertyPrefix + contextfabric.MemberStatusStoredProperty(p.kind)
}

// clause is the read's status clause: the status itself, or open for a status
// the review rows decide.
func (p memberStatusPredicate) clause() nodeStatusClause {
	if !p.set() {
		return nodeStatusClause{}
	}
	value := p.status
	if p.reviewDerived() {
		value = contextfabric.PullRequestStatusOpen
	}
	return nodeStatusClause{property: p.storedProperty(), values: []interface{}{value}}
}

// outsideClause keeps the nodes whose stored status is outside the kind's
// closed set; empty for pull requests, whose outside count is not served.
func (p memberStatusPredicate) outsideClause() nodeStatusClause {
	if !p.set() || p.kind == contextfabric.SubjectPullRequest {
		return nodeStatusClause{}
	}
	vocabulary := contextfabric.MemberStatusVocabulary(p.kind)
	values := make([]interface{}, 0, len(vocabulary))
	for _, v := range vocabulary {
		values = append(values, v)
	}
	return nodeStatusClause{property: p.storedProperty(), values: values, outside: true}
}

// memberStatusWalk is what the status predicate did inside one walk.
type memberStatusWalk struct {
	// applied: the walk's member read carried the predicate.
	applied bool
	// read counts the distinct authorized members the status-narrowed read
	// returned (for a review status, the open pull requests read), and kept
	// the members the walk served.
	read, kept int
	// reached: the anchor reaches at least one authorized member of the kind,
	// whatever its status.
	reached bool
	// poolCut: the review pages stopped at their cap with rows left, so the
	// status was decided within the first read members only.
	poolCut bool
	// pool is the bounded count of members of the kind the anchor's walk
	// reaches under the state clause, counted only when poolCut; -1 otherwise.
	pool int
	// outsideSet counts the authorized members whose stored status is outside
	// the kind's closed set.
	outsideSet int
}

func (w *memberStatusWalk) add(other memberStatusWalk) {
	w.applied = w.applied || other.applied
	w.read += other.read
	w.kept += other.kept
	w.reached = w.reached || other.reached
	w.poolCut = w.poolCut || other.poolCut
	if other.pool >= 0 {
		w.pool = max(w.pool, 0) + other.pool
	}
	w.outsideSet += other.outsideSet
}

// memberStatusPageCap bounds how many review pages one member read takes. A
// read that reaches it is cut.
const memberStatusPageCap = 8

// pullRequestsHoldingReviewStatus keeps the ids of open pull requests that hold
// a review status now. Every review row of each pull request is read under no
// window: a cut read would leave a pull request's latest review unknown.
func (a *Adapter) pullRequestsHoldingReviewStatus(ctx context.Context, key, orgID, status string, ids []string) (map[string]bool, error) {
	kept := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return kept, nil
	}
	reviews, err := a.pullRequestReviewRows(ctx, key, orgID, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if contextfabric.PullRequestStatusMatches(status, contextfabric.PullRequestStatusOpen, reviews[id]) {
			kept[id] = true
		}
	}
	return kept, nil
}

// reviewedMemberHits is the member read of a review status: pages of open pull
// requests, each page's authorized members decided by their review rows,
// until more members hold the status than the walk serves or the pages end. A
// hit the caller cannot see is returned for the walk to deny; a hit that does
// not hold the status is not returned.
func (a *Adapter) reviewedMemberHits(ctx context.Context, key, orgID string, state treeWalkState, hop treeHop, frontier []string, step walkStep, temporal temporalFilter) ([]walkHit, error) {
	pageSize := 1 << 30
	if state.collectLimit > 0 {
		pageSize = state.collectLimit + 1
	}
	var hits []walkHit
	read := map[string]bool{}
	kept := map[string]bool{}
	pages := 0
	defer func() { state.out.status.read = len(read) }()
	for start := 0; start < len(frontier); start += storedSubjectBatch {
		end := min(start+storedSubjectBatch, len(frontier))
		batch := make([]interface{}, 0, end-start)
		for _, id := range frontier[start:end] {
			batch = append(batch, id)
		}
		for skip := 0; ; skip += pageSize {
			if state.collectLimit > 0 && len(kept) > state.collectLimit {
				return hits, nil
			}
			if pages >= memberStatusPageCap {
				state.out.status.poolCut = true
				state.out.truncated = true
				return hits, nil
			}
			pages++
			page, rows, err := a.walkStepPage(ctx, key, orgID, batch, step, temporal, skip, pageSize)
			if err != nil {
				return nil, err
			}
			var open []string
			for _, h := range page {
				if !state.authorizedHit(hop, h) {
					hits = append(hits, h)
					continue
				}
				id := canonicalIDOf(h.to)
				if !read[id] {
					read[id] = true
					open = append(open, id)
				}
			}
			holding, err := a.pullRequestsHoldingReviewStatus(ctx, key, orgID, state.status.status, open)
			if err != nil {
				return nil, err
			}
			for _, h := range page {
				id := canonicalIDOf(h.to)
				if holding[id] && state.authorizedHit(hop, h) {
					kept[id] = true
					hits = append(hits, h)
				}
			}
			if rows < pageSize {
				break
			}
		}
	}
	return hits, nil
}

// finishMemberStatus records what the predicate did on a walk whose last hop
// read the members with it: what was kept, whether the anchor reaches any
// member of the kind at all (read only when none was kept), the bounded pool
// count of a cut read, and the members outside the kind's closed set.
// unfiltered is the member hop without the status clause.
func (a *Adapter) finishMemberStatus(ctx context.Context, key, orgID string, state treeWalkState, hop treeHop, frontier []string, unfiltered, filtered walkStep, temporal temporalFilter, budget int) error {
	out := state.out
	out.status.applied = true
	out.status.kept = len(out.nodes)
	if !state.status.reviewDerived() {
		out.status.read = out.status.kept
	}
	out.status.reached = out.status.kept > 0
	if !out.status.reached {
		probe := unfiltered
		probe.projected = true
		hits, _, err := a.walkStepHits(ctx, key, orgID, frontier, probe, temporal, budget)
		if err != nil {
			return err
		}
		for _, h := range hits {
			if state.authorizedHit(hop, h) {
				out.status.reached = true
				break
			}
		}
	}
	if out.status.poolCut {
		pool, err := a.walkStepCount(ctx, key, orgID, frontier, filtered, temporal)
		if err != nil {
			return err
		}
		out.status.pool = pool
	}
	if outside := state.status.outsideClause(); outside.set() {
		probe := unfiltered
		probe.status = outside
		probe.projected = true
		hits, _, err := a.walkStepHits(ctx, key, orgID, frontier, probe, temporal, exactNameCandidateQueryLimit)
		if err != nil {
			return err
		}
		counted := map[string]bool{}
		for _, h := range hits {
			if state.authorizedHit(hop, h) {
				counted[canonicalIDOf(h.to)] = true
			}
		}
		out.status.outsideSet = len(counted)
	}
	return nil
}

// finishLinkMemberStatus is finishMemberStatus for a walk whose link read
// ends the path: the members are the far side of the link (pull requests).
func (a *Adapter) finishLinkMemberStatus(ctx context.Context, key, orgID string, state treeWalkState, anchor contextfabric.SubjectRef, feed, link treeHop, temporal temporalFilter) error {
	out := state.out
	out.status.applied = true
	out.status.kept = len(out.linkNodes)
	if !state.status.reviewDerived() {
		out.status.read = out.status.kept
	}
	out.status.reached = out.status.kept > 0
	if !out.status.reached {
		probeOut := treeWalk{}
		probe := state
		probe.out = &probeOut
		probe.status = memberStatusPredicate{}
		ends, _, err := a.linkSegment(ctx, key, orgID, probe, anchor, feed, link, temporal)
		if err != nil {
			return err
		}
		out.status.reached = len(ends) > 0
	}
	if out.status.poolCut {
		params := linkSegmentParams(orgID, anchor, feed, link, 0, 1, temporal)
		status := state.status.clause()
		status.bind(params)
		rows, err := a.api.query(ctx, key, linkTargetCountCypherWith(feed, link, temporal, status), params, true)
		if err != nil {
			return safeDependencyError("count link targets", err)
		}
		if len(rows) > 0 {
			out.status.pool = countValue(rows[0]["targets"])
		}
	}
	return nil
}
