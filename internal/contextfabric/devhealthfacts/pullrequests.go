package devhealthfacts

import (
	"context"
	"strconv"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const pullRequestPrefix = "pull_request:"

// pullRequestSubjectIndex is subjectIndex specialized for pull request
// subjects: their raw id is the "repoID:number" composite key
// devhealthsource/tables.go's queryPullRequests uses as its rowSortKey (a
// git_pull_requests row has no single-column primary key), which is exactly
// what remains after trimming pullRequestPrefix off
// "pull_request:<repoID>:<number>".
func pullRequestSubjectIndex(subjects []contextfabric.SubjectRef) ([]string, map[string]contextfabric.SubjectRef) {
	return subjectIndex(subjects, pullRequestPrefix)
}

// PullRequestsProvider implements contextfabric.FactProvider for
// FactPullRequests from git_pull_requests.state -- the same column
// devhealthsource/tables.go's queryPullRequests already reads.
type PullRequestsProvider struct{ facts clickhouseFacts }

func newPullRequestsProvider(client contextpacket.ClickHouseQueryClient) *PullRequestsProvider {
	return &PullRequestsProvider{facts: clickhouseFacts{client: client}}
}

func (p *PullRequestsProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactPullRequests, "devhealthfacts.pull_requests", []contextfabric.SubjectKind{contextfabric.SubjectPullRequest})
}

func (p *PullRequestsProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	timeBound, unsupportedResult, unsupported := resolveTimeBound(query)
	if unsupported {
		return unsupportedResult, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := pullRequestSubjectIndex(query.Subjects)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// CHAOS-3781 Tier B: a pull request's state at a past instant is a
	// pure function of the immutable event timestamps the row already
	// carries, so this is a reconstruction of a RECORDED fact, not of an
	// unrecorded one (§19.8.3). Order matters -- merged wins over closed,
	// because a merged pull request is also closed.
	//
	// The existence guard is the outer WHERE: a pull request created
	// after the requested time is not returned at all, so the subject
	// reports no fact rather than a current-state one (AC-3781-3).
	stateExpression := "ifNull(p.state, '')"
	if timeBound.active {
		asOf := timeBound.asOfExpression()
		stateExpression = "multiIf(" +
			"p.merged_at IS NOT NULL AND p.merged_at <= " + asOf + ", 'merged', " +
			"p.closed_at IS NOT NULL AND p.closed_at <= " + asOf + ", 'closed', " +
			"'open')"
	}
	statement := withRowLimit(`SELECT toString(p.repo_id), p.number, ` + stateExpression + `
FROM git_pull_requests AS p FINAL
WHERE p.org_id = {org_id:String} AND concat(toString(p.repo_id), ':', toString(p.number)) IN {ids:Array(String)}` + timeBound.existencePredicate("p.created_at"))
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var repoID string
		// git_pull_requests.number is UInt32 in production, and
		// clickhouse-go's native driver REJECTS scanning a UInt32 column
		// into an *int64 destination outright ("converting UInt32 to
		// *int64 is unsupported"). Scanning it as int64 here meant every
		// live pull-request row failed Scan, so this provider silently
		// returned no pull-request facts at all.
		//
		// CHAOS-3789 fixed exactly this in devhealthsource
		// (tables.go's queryPullRequests) but the same defect survived
		// here, in a different package reading the same column -- and
		// this package's fixtures modeled the column as int64 too, so
		// the tests agreed with the bug. See the devhealthfacts schema
		// parity guard, which now covers these readers for that reason.
		//
		// The int64 conversion happens immediately after Scan, once the
		// value is safely in Go, so pullRequestKey and every downstream
		// use are unchanged.
		var rawNumber uint32
		var state string
		if err := row.Scan(&repoID, &rawNumber, &state); err != nil {
			return err
		}
		number := int64(rawNumber)
		key := pullRequestKey(repoID, number)
		subject, ok := bySubject[key]
		if !ok {
			return nil
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactPullRequests, Subject: subject,
			Fields:         map[string]contextfabric.FactValue{"state": stringOrNull(state)},
			EvidenceRefIDs: []string{evidenceRefID("pull-request", repoID+":"+strconv.FormatInt(number, 10))},
		})
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query pull requests", scanErr)
	}
	state, retentionReason := timeBound.retentionState(rowCount)
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainExact), Truncated: rowCount >= maxFactRowsPerQuery}, nil
}

// ReviewsProvider implements contextfabric.FactProvider for FactReviews from
// git_pull_request_reviews.state -- the same column
// devhealthsource/tables.go's queryPullRequestReviews already reads.
type ReviewsProvider struct{ facts clickhouseFacts }

func newReviewsProvider(client contextpacket.ClickHouseQueryClient) *ReviewsProvider {
	return &ReviewsProvider{facts: clickhouseFacts{client: client}}
}

func (p *ReviewsProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactReviews, "devhealthfacts.reviews", []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectPullRequestReview})
}

func (p *ReviewsProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	timeBound, unsupportedResult, unsupported := resolveTimeBound(query)
	if unsupported {
		return unsupportedResult, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := v2Index(query.Subjects, identity.KindPullRequestReview)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// CHAOS-3781 Tier B: a review is an immutable point event -- its state
	// is decided when it is submitted and is never revised -- so the only
	// temporal question is whether it had been submitted yet.
	statement := withRowLimit(`SELECT r.review_id, ifNull(r.state, ''), toString(r.repo_id)
FROM git_pull_request_reviews AS r FINAL
WHERE r.org_id = {org_id:String} AND concat(toString(r.repo_id), ':', r.review_id) IN {ids:Array(String)}` + timeBound.existencePredicate("r.submitted_at"))
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var reviewID, state, repoID string
		if err := row.Scan(&reviewID, &state, &repoID); err != nil {
			return err
		}
		subject, ok := bySubject[repoID+":"+reviewID]
		if !ok {
			return nil
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactReviews, Subject: subject,
			Fields:         map[string]contextfabric.FactValue{"state": stringOrNull(state)},
			EvidenceRefIDs: []string{evidenceRefID("review", repoID+":"+reviewID)},
		})
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query pull request reviews", scanErr)
	}
	state, retentionReason := timeBound.retentionState(rowCount)
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainExact), Truncated: rowCount >= maxFactRowsPerQuery}, nil
}
