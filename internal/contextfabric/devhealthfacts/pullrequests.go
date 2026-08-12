package devhealthfacts

import (
	"context"
	"strconv"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
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
	if result, unsupported := checkCurrentTimeOnly(query); unsupported {
		return result, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := pullRequestSubjectIndex(query.Subjects)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	statement := withRowLimit(`SELECT toString(p.repo_id), p.number, ifNull(p.state, '')
FROM git_pull_requests AS p FINAL
WHERE p.org_id = {org_id:String} AND concat(toString(p.repo_id), ':', toString(p.number)) IN {ids:Array(String)}`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var repoID string
		var number int64
		var state string
		if err := row.Scan(&repoID, &number, &state); err != nil {
			return err
		}
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
	})
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query pull requests", scanErr)
	}
	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable, Version: queryVersion, Truncated: rowCount >= maxFactRowsPerQuery}, nil
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
	if result, unsupported := checkCurrentTimeOnly(query); unsupported {
		return result, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := subjectIndex(query.Subjects, "pull_request_review:")
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	statement := withRowLimit(`SELECT r.review_id, ifNull(r.state, '')
FROM git_pull_request_reviews AS r FINAL
WHERE r.org_id = {org_id:String} AND r.review_id IN {ids:Array(String)}`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var reviewID, state string
		if err := row.Scan(&reviewID, &state); err != nil {
			return err
		}
		subject, ok := bySubject[reviewID]
		if !ok {
			return nil
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactReviews, Subject: subject,
			Fields:         map[string]contextfabric.FactValue{"state": stringOrNull(state)},
			EvidenceRefIDs: []string{evidenceRefID("review", reviewID)},
		})
		return nil
	})
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query pull request reviews", scanErr)
	}
	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable, Version: queryVersion, Truncated: rowCount >= maxFactRowsPerQuery}, nil
}
