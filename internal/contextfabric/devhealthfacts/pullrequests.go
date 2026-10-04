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
func pullRequestSubjectIndex(subjects []contextfabric.SubjectRef) ([]string, map[string]contextfabric.SubjectRef, int) {
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
	capability := newCapability(contextfabric.FactPullRequests, "devhealthfacts.pull_requests", []contextfabric.SubjectKind{contextfabric.SubjectPullRequest, contextfabric.SubjectTeam})
	capability.Tables = map[contextfabric.SubjectKind][]contextfabric.FactTableShape{
		contextfabric.SubjectTeam: {contextfabric.FactTableBreakdown},
	}
	return capability
}

func (p *PullRequestsProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
	timeBound, unsupportedResult, unsupported := resolveTimeBound(query)
	if unsupported {
		return unsupportedResult, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject, rejected := pullRequestSubjectIndex(subjectsOfKind(query.Subjects, contextfabric.SubjectPullRequest))
	// CHAOS-5026: deferred so every return path passes through the
	// disclosure -- see ci.go's identical note.
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.pull_requests", contextfabric.FactPullRequests, rejected)
		}
	}()
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	var teamOutcome teamRollupOutcome
	if teamSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectTeam); len(teamSubjects) > 0 {
		var teamErr error
		teamOutcome, teamErr = p.readTeamRollup(ctx, orgID, teamSubjects, &facts, timeBound, query.Time.EvidenceWindow)
		if teamErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team pull requests", teamErr)
		}
		rejected += teamOutcome.rejected
	}
	teamFactCount := len(facts)
	// CHAOS-4377: the SQL build + scan half (the "merged wins over closed"
	// derivation, the existence guard, the UInt32 Scan quirk) moved to
	// github.com/full-chaos/dev-health-go/readers.ReadPullRequestState;
	// its doc comment carries that reasoning now. This keeps only the
	// subject-identity mapping and CanonicalFact construction.
	var rows []readers.PullRequestStateRow
	var scanErr error
	if len(ids) > 0 {
		rows, scanErr = readers.ReadPullRequestState(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	}
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
	state, retentionReason := timeBound.retentionState(len(rows) + teamFactCount)
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainExact), Truncated: len(rows) >= maxFactRowsPerQuery || teamOutcome.truncated}
	applyNoOwnedRepositories(&result, teamOutcome.teamsWithoutRepos)
	return result, nil
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

func (p *ReviewsProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
	timeBound, unsupportedResult, unsupported := resolveTimeBound(query)
	if unsupported {
		return unsupportedResult, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject, rejected := v2Index(query.Subjects, identity.KindPullRequestReview)
	// CHAOS-5026: deferred so every return path passes through the
	// disclosure -- see ci.go's identical note.
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.reviews", contextfabric.FactReviews, rejected)
		}
	}()
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
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainExact), Truncated: len(rows) >= maxFactRowsPerQuery}
	return result, nil
}

// pullRequestRollupRepo is one owned repository's pull requests counted by
// their own event time inside the rollup window.
type pullRequestRollupRepo struct{ opened, merged, closedUnmerged int64 }

// readTeamRollup serves a team subject: pull requests opened, merged and
// closed without merging inside the request window, summed across the team's
// owned repositories, with the per-repository breakdown and the
// owned_repositories pointer. A repository holding no pull request row at all
// is counted as without data, never as zero; a repository that holds rows but
// none in the window is a measured zero.
func (p *PullRequestsProvider) readTeamRollup(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound, evidence *contractsv1.ContextFabricRequestedEvidenceWindow) (outcome teamRollupOutcome, err error) {
	teamIDs, bySubject, rejected := subjectIndex(subjects, teamPrefix)
	outcome.rejected = rejected
	if len(teamIDs) == 0 {
		return outcome, nil
	}
	owned, err := teamOwnedRepositories(ctx, p.facts.client, orgID, teamIDs, timeBound)
	if err != nil {
		return outcome, err
	}
	window := resolveRollupWindow(timeBound, evidence, clock())
	byRepo := map[string]pullRequestRollupRepo{}
	if repoKeys := repoKeysOf(owned); len(repoKeys) > 0 {
		statement := `SELECT toString(repo_id),
	toInt64(countIf(` + window.timestampExpr("created_at") + `)),
	toInt64(countIf(merged_at IS NOT NULL AND ` + window.timestampExpr("merged_at") + `)),
	toInt64(countIf(merged_at IS NULL AND closed_at IS NOT NULL AND ` + window.timestampExpr("closed_at") + `))
FROM git_pull_requests FINAL
WHERE org_id = {org_id:String} AND toString(repo_id) IN {ids:Array(String)}
GROUP BY repo_id
ORDER BY repo_id`
		if scanErr := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadTeamPullRequestRollup", statement, orgID, repoKeys, func(row contextpacket.ClickHouseRowScanner) error {
			var repoID string
			var r pullRequestRollupRepo
			if err := row.Scan(&repoID, &r.opened, &r.merged, &r.closedUnmerged); err != nil {
				return err
			}
			byRepo[repoID] = r
			return nil
		}, window.bindings()...); scanErr != nil {
			return outcome, scanErr
		}
	}
	for _, teamID := range teamIDs {
		subject := bySubject[teamID]
		repos := owned[teamID]
		if len(repos) == 0 {
			outcome.teamsWithoutRepos++
			continue
		}
		var opened, merged, closedUnmerged, withData int64
		breakdown := make([]contextfabric.FactValueRow, 0, len(repos))
		for _, repo := range repos {
			r, ok := byRepo[repo.key]
			if !ok {
				continue
			}
			outcome.contributed++
			withData++
			opened += r.opened
			merged += r.merged
			closedUnmerged += r.closedUnmerged
			cells := map[string]contextfabric.FactValue{
				"repository_id":                        contextfabric.StringFactValue(repo.key),
				"pull_requests_opened_window":          contextfabric.IntegerFactValue(r.opened),
				"pull_requests_merged_window":          contextfabric.IntegerFactValue(r.merged),
				"pull_requests_closed_unmerged_window": contextfabric.IntegerFactValue(r.closedUnmerged),
			}
			if repo.name != "" {
				cells["repository_name"] = contextfabric.StringFactValue(repo.name)
			}
			breakdown = append(breakdown, contextfabric.FactValueRow{Fields: cells})
		}
		fields := map[string]contextfabric.FactValue{
			"rollup_basis":                    contextfabric.StringFactValue(teamRollupBasis),
			"window_basis":                    contextfabric.StringFactValue(window.basis),
			"owned_repository_count":          contextfabric.IntegerFactValue(int64(len(repos))),
			"repositories_with_data_count":    contextfabric.IntegerFactValue(withData),
			"repositories_without_data_count": contextfabric.IntegerFactValue(int64(len(repos)) - withData),
		}
		if value, ok := window.startValue(); ok {
			fields["window_start"] = value
		}
		if value, ok := window.endValue(); ok {
			fields["window_end"] = value
		}
		if table, omitted, ok := ownedRepositoriesFactValue(repos); ok {
			fields["owned_repositories"] = table
			if omitted > 0 {
				outcome.truncated = true
				fields["owned_repositories_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
			}
		}
		if withData > 0 {
			fields["pull_requests_opened_window"] = contextfabric.IntegerFactValue(opened)
			fields["pull_requests_merged_window"] = contextfabric.IntegerFactValue(merged)
			fields["pull_requests_closed_unmerged_window"] = contextfabric.IntegerFactValue(closedUnmerged)
			if table, omitted, ok := repositoryBreakdownFactValue(breakdown, grainExact,
				[]string{"pull_requests_opened_window", "pull_requests_merged_window", "pull_requests_closed_unmerged_window"}, []string{"repository_name"}); ok {
				fields["repository_breakdown"] = table
				if omitted > 0 {
					outcome.truncated = true
					fields["repository_breakdown_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
				}
			}
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactPullRequests, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, teamID)},
		})
	}
	return outcome, nil
}
