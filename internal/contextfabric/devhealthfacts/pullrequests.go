package devhealthfacts

import (
	"context"
	"strconv"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
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
	// CHAOS-4377: the SQL build + scan half (the "merged wins over closed"
	// derivation, the existence guard, the UInt32 Scan quirk) moved to
	// github.com/full-chaos/dev-health-go/readers.ReadPullRequestState;
	// its doc comment carries that reasoning now. This keeps only the
	// subject-identity mapping and CanonicalFact construction.
	rows, scanErr := readers.ReadPullRequestState(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query pull requests", scanErr)
	}
	for _, row := range rows {
		// The int64 conversion happens here, immediately once the value is
		// safely in Go (see readers.PullRequestStateRow.Number's doc
		// comment for why Number is scanned as uint32), so pullRequestKey
		// and every downstream use are unchanged.
		number := int64(row.Number)
		key := pullRequestKey(row.RepoID, number)
		subject, ok := bySubject[key]
		if !ok {
			continue
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactPullRequests, Subject: subject,
			Fields:         map[string]contextfabric.FactValue{"state": stringOrNull(row.State)},
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, row.RepoID+":"+strconv.FormatInt(number, 10))},
		})
	}
	state, retentionReason := timeBound.retentionState(len(rows))
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainExact), Truncated: len(rows) >= maxFactRowsPerQuery}, nil
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
	// CHAOS-4377: the SQL build + scan half moved to
	// github.com/full-chaos/dev-health-go/readers.ReadPullRequestReviews;
	// its doc comment carries the "immutable point event" reasoning now.
	rows, scanErr := readers.ReadPullRequestReviews(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query pull request reviews", scanErr)
	}
	for _, row := range rows {
		subject, ok := bySubject[row.RepoID+":"+row.ReviewID]
		if !ok {
			continue
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactReviews, Subject: subject,
			Fields:         map[string]contextfabric.FactValue{"state": stringOrNull(row.State)},
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityReview, row.RepoID+":"+row.ReviewID)},
		})
	}
	state, retentionReason := timeBound.retentionState(len(rows))
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainExact), Truncated: len(rows) >= maxFactRowsPerQuery}, nil
}
