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

// clausePredicate is the predicate the read's clause states with no review
// decision: the status itself, or open for a status the review rows decide.
func (p memberStatusPredicate) clausePredicate() memberStatusPredicate {
	if p.reviewDerived() {
		return memberStatusPredicate{kind: p.kind, status: contextfabric.PullRequestStatusOpen}
	}
	return p
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
	// reachCut: the reach probe stopped at its bound, so a false reached is unknown.
	reachCut bool
	// poolCut: the member read stopped with rows unread that could hold the
	// status, so the status was decided within the first read members only.
	poolCut bool
	// pageCapped: the member read stopped at its page cap; capFilled: it
	// stopped because more authorized members held the status than the walk
	// serves. Both are decided into poolCut when the walk finishes.
	pageCapped, capFilled bool
	// pool is the count of authorized members of the kind the anchor's walk
	// reaches under the state clause, counted only when poolCut; -1 otherwise
	// or when that bounded count was itself cut.
	pool int
	// poolUnknown: a cut walk's pool could not be counted.
	poolUnknown bool
	// outsideSet counts the authorized members whose stored status is outside
	// the kind's closed set; outsideAtLeast: that count's read was cut, so it
	// is a lower bound.
	outsideSet     int
	outsideAtLeast bool
}

func (w *memberStatusWalk) add(other memberStatusWalk) {
	w.applied = w.applied || other.applied
	w.read += other.read
	w.kept += other.kept
	w.reached = w.reached || other.reached
	w.reachCut = w.reachCut || other.reachCut
	w.poolCut = w.poolCut || other.poolCut
	if other.pool >= 0 {
		w.pool = max(w.pool, 0) + other.pool
	}
	w.poolUnknown = w.poolUnknown || other.poolUnknown || (other.poolCut && other.pool < 0)
	w.outsideSet += other.outsideSet
	w.outsideAtLeast = w.outsideAtLeast || other.outsideAtLeast
}

// memberStatusPageCap bounds how many pages one status member read takes. A
// read that reaches it with members of the pool unread is cut.
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

// statusMemberHits is the member read of a status: pages of the members whose
// stored status the read's clause keeps, a review status decided per page by
// the review rows, until more authorized members hold the status than the walk
// serves or the pages end. Only authorized members count toward the cap, so
// members the caller cannot see do not crowd out those it can. A hit that
// holds the status and that the caller cannot see is returned for the walk to
// deny; a hit that does not hold it is not returned.
func (a *Adapter) statusMemberHits(ctx context.Context, key, orgID string, state treeWalkState, hop treeHop, frontier []string, step walkStep, temporal temporalFilter, budget int) ([]walkHit, error) {
	pageSize := 1 << 30
	if budget > 0 {
		pageSize = budget + 1
	}
	reviewed := state.status.reviewDerived()
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
				state.out.status.capFilled = true
				return hits, nil
			}
			if pages >= memberStatusPageCap {
				state.out.status.pageCapped = true
				return hits, nil
			}
			pages++
			page, rows, err := a.walkStepPage(ctx, key, orgID, batch, step, temporal, skip, pageSize)
			if err != nil {
				return nil, err
			}
			var holding map[string]bool
			if reviewed {
				ids := make([]string, 0, len(page))
				for _, h := range page {
					ids = append(ids, canonicalIDOf(h.to))
				}
				if holding, err = a.pullRequestsHoldingReviewStatus(ctx, key, orgID, state.status.status, distinctIDs(ids)); err != nil {
					return nil, err
				}
			}
			for _, h := range page {
				id := canonicalIDOf(h.to)
				authorized := state.authorizedHit(hop, h)
				if authorized {
					read[id] = true
				}
				if reviewed && !holding[id] {
					continue
				}
				hits = append(hits, h)
				if authorized {
					kept[id] = true
				}
			}
			if rows < pageSize {
				break
			}
		}
	}
	return hits, nil
}

// decidePoolCut turns how a status member read stopped into a pool cut. A read
// stopped at its page cap is cut unless the bounded authorized pool count
// shows it read the whole pool; a review read stopped by a filled cap is cut,
// since the open pull requests past it were not decided. count reads the
// authorized pool: its size, and whether that bounded read was itself cut.
func (state treeWalkState) decidePoolCut(count func() (int, bool, error)) error {
	out := state.out
	if !out.status.pageCapped && !(out.status.capFilled && state.status.reviewDerived()) {
		return nil
	}
	pool, poolCut, err := count()
	if err != nil {
		return err
	}
	if poolCut {
		pool = -1
	}
	if out.status.pageCapped && !out.status.capFilled && pool >= 0 && pool == out.status.read {
		return nil
	}
	out.status.poolCut = true
	out.status.pool = pool
	out.truncated = true
	return nil
}

// finishMemberStatus records what the predicate did on a walk whose last hop
// read the members with it: what was kept, whether the anchor reaches any
// member of the kind at all (read only when none was kept), whether the read
// is a pool cut, and the members outside the kind's closed set. unfiltered is
// the member hop without the status clause.
func (a *Adapter) finishMemberStatus(ctx context.Context, key, orgID string, state treeWalkState, hop treeHop, frontier []string, unfiltered, filtered walkStep, temporal temporalFilter, budget int) error {
	out := state.out
	out.status.applied = true
	out.status.kept = len(out.nodes)
	out.status.reached = out.status.kept > 0
	if !out.status.reached {
		probe := unfiltered
		probe.projected = true
		hits, reachCut, err := a.walkStepHits(ctx, key, orgID, frontier, probe, temporal, budget)
		if err != nil {
			return err
		}
		out.status.reachCut = reachCut
		for _, h := range hits {
			if state.authorizedHit(hop, h) {
				out.status.reached = true
				break
			}
		}
	}
	err := state.decidePoolCut(func() (int, bool, error) {
		pool := filtered
		pool.projected = true
		return a.authorizedHitCount(ctx, key, orgID, state, hop, frontier, pool, temporal)
	})
	if err != nil {
		return err
	}
	if outside := state.status.outsideClause(); outside.set() {
		probe := unfiltered
		probe.status = outside
		probe.projected = true
		// A cut count is a lower bound: at least that many members hold a
		// status outside the set.
		count, cut, err := a.authorizedHitCount(ctx, key, orgID, state, hop, frontier, probe, temporal)
		if err != nil {
			return err
		}
		out.status.outsideSet, out.status.outsideAtLeast = count, cut
	}
	return nil
}

// authorizedHitCount counts the distinct nodes a step reaches from frontier
// that the caller may see, bounded by the census bound, and reports whether
// that read was cut.
func (a *Adapter) authorizedHitCount(ctx context.Context, key, orgID string, state treeWalkState, hop treeHop, frontier []string, step walkStep, temporal temporalFilter) (int, bool, error) {
	hits, cut, err := a.walkStepHits(ctx, key, orgID, frontier, step, temporal, exactNameCandidateQueryLimit)
	if err != nil {
		return 0, false, err
	}
	counted := map[string]bool{}
	for _, h := range hits {
		if state.authorizedHit(hop, h) {
			counted[canonicalIDOf(h.to)] = true
		}
	}
	return len(counted), cut, nil
}

// finishLinkMemberStatus is finishMemberStatus for a walk whose link read
// ends the path: the members are the far side of the link (pull requests).
func (a *Adapter) finishLinkMemberStatus(ctx context.Context, key, orgID string, state treeWalkState, anchor contextfabric.SubjectRef, feed, link treeHop, temporal temporalFilter) error {
	out := state.out
	out.status.applied = true
	out.status.kept = len(out.linkNodes)
	out.status.reached = out.status.kept > 0
	if !out.status.reached {
		probe := state
		probe.out = &treeWalk{}
		probe.status = memberStatusPredicate{}
		ends, _, err := a.linkSegment(ctx, key, orgID, probe, anchor, feed, link, temporal)
		if err != nil {
			return err
		}
		out.status.reached = len(ends) > 0
	}
	return state.decidePoolCut(func() (int, bool, error) {
		// The authorized members the anchor's links reach under the read's
		// clause, with no review decision, bounded by the census bound.
		probeOut := treeWalk{}
		probe := state
		probe.out = &probeOut
		probe.status = state.status.clausePredicate()
		probe.collectLimit = exactNameCandidateQueryLimit
		ends, _, err := a.linkSegment(ctx, key, orgID, probe, anchor, feed, link, temporal)
		if err != nil {
			return 0, false, err
		}
		return len(ends), probeOut.truncated || probeOut.status.pageCapped || len(ends) > exactNameCandidateQueryLimit, nil
	})
}
