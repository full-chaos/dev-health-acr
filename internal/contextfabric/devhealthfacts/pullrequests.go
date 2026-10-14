package devhealthfacts

import (
	"context"
	"fmt"
	"strconv"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/evidenceref"
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
	capability := newCapability(contextfabric.FactPullRequests, "devhealthfacts.pull_requests", []contextfabric.SubjectKind{contextfabric.SubjectPullRequest, contextfabric.SubjectTeam, contextfabric.SubjectProject})
	capability.Tables = map[contextfabric.SubjectKind][]contextfabric.FactTableShape{
		contextfabric.SubjectTeam:        {contextfabric.FactTableBreakdown},
		contextfabric.SubjectProject:     {contextfabric.FactTableBreakdown},
		contextfabric.SubjectPullRequest: {contextfabric.FactTableBreakdown},
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
	defer func() {
		attachOwnershipNoteToFailure(ctx, p.facts.client, orgID, query.Subjects, teamOwnedOrReachedRepositories, timeBound, err)
	}()
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
	var projectLinksUnwindowed []contextfabric.SubjectRef
	if projectSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectProject); len(projectSubjects) > 0 {
		projectRejected, unwindowed, projectErr := p.readProjectRollup(ctx, orgID, projectSubjects, &facts, timeBound, query.Time.EvidenceWindow)
		if projectErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query project pull requests", projectErr)
		}
		rejected += projectRejected
		projectLinksUnwindowed = unwindowed
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
	var projectsByPullRequest map[string][]pullRequestProjectRow
	var projectLinksTruncated bool
	if len(rows) > 0 {
		var projectErr error
		projectsByPullRequest, projectLinksTruncated, projectErr = p.readPullRequestProjects(ctx, orgID, ids)
		if projectErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query pull request projects", projectErr)
		}
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
		fields := map[string]contextfabric.FactValue{"state": stringOrNull(row.State)}
		evidence := []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, row.RepoID+":"+strconv.FormatInt(number, 10))}
		// The projects this pull request reaches through the issue link of
		// record, with the link tier on each row. Absent when no link row is
		// admitted: a missing link is not an empty project list.
		if table, projectEvidence, omitted, ok := pullRequestProjectBreakdown(projectsByPullRequest[key]); ok {
			fields["project_breakdown"] = table
			if omitted > 0 {
				projectLinksTruncated = true
			}
			evidence = append(evidence, projectEvidence...)
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactPullRequests, Subject: subject,
			Fields:         fields,
			EvidenceRefIDs: evidence,
		})
	}
	state, retentionReason := timeBound.retentionState(len(rows) + teamFactCount)
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainExact), Truncated: len(rows) >= maxFactRowsPerQuery || teamOutcome.truncated || projectLinksTruncated}
	applyTeamsOwningNothing(&result, query.Subjects, teamOutcome.teamsRequested, teamOutcome.teamsWithoutRepos)
	unwindowedLinkSubjects := append(append([]contextfabric.SubjectRef(nil), teamOutcome.linkUnwindowedTeams...), projectLinksUnwindowed...)
	addScopedReason(&result, query.Subjects, unwindowedLinkSubjects, teamProjectLinksUnwindowedReason)
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
		number := strconv.FormatUint(uint64(row.Number), 10)
		subject, ok := bySubject[row.RepoID+":"+number+":"+row.ReviewID]
		if !ok {
			continue
		}
		ref, _ := evidenceref.Mint(contractsv1.ContextFabricEvidenceEntityReviewV2, row.RepoID, number, row.ReviewID)
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactReviews, Subject: subject,
			Fields:         map[string]contextfabric.FactValue{"state": stringOrNull(row.State)},
			EvidenceRefIDs: []string{ref},
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
	outcome.teamsRequested = len(teamIDs)
	if len(teamIDs) == 0 {
		return outcome, nil
	}
	owned, err := teamOwnedRepositories(ctx, p.facts.client, orgID, teamIDs, timeBound)
	if err != nil {
		return outcome, err
	}
	reached, err := teamProjectReachedRepositories(ctx, p.facts.client, orgID, teamIDs, timeBound)
	if err != nil {
		return outcome, err
	}
	reachedOnly := reachedOnlyRepositories(reached, owned)
	window := resolveRollupWindow(timeBound, evidence, clock())
	byRepo := map[string]pullRequestRollupRepo{}
	// A repository reached only through an owned project counts the project's
	// linked pull requests alone, never the repository's whole activity.
	reachedCounts, err := teamLinkedPullRequestCounts(ctx, p.facts.client, orgID, teamIDs, reachedOnly, window, timeBound)
	if err != nil {
		return outcome, err
	}
	if repoKeys := repoKeysOf(owned); len(repoKeys) > 0 {
		statement := `SELECT toString(repo_id),
	toInt64(countIf(` + window.timestampExpr("created_at") + `)),
	toInt64(countIf(merged_at IS NOT NULL AND ` + window.timestampExpr("merged_at") + `)),
	toInt64(countIf(merged_at IS NULL AND closed_at IS NOT NULL AND ` + window.timestampExpr("closed_at") + `))
FROM git_pull_requests FINAL
WHERE org_id = {org_id:String} AND toString(repo_id) IN {ids:Array(String)}
GROUP BY repo_id
ORDER BY repo_id`
		if scanErr := forEachRepoKeyChunk(repoKeys, func(chunk []string) error {
			return readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadTeamPullRequestRollup", statement, orgID, chunk, func(row contextpacket.ClickHouseRowScanner) error {
				var repoID string
				var r pullRequestRollupRepo
				if err := row.Scan(&repoID, &r.opened, &r.merged, &r.closedUnmerged); err != nil {
					return err
				}
				byRepo[repoID] = r
				return nil
			}, window.bindings()...)
		}); scanErr != nil {
			return outcome, scanErr
		}
	}
	tables := teamRollupTablesIncluded(len(teamIDs))
	var ownedStates map[string]pullRequestStateCounts
	var reachedStates map[string]pullRequestStateCounts
	if tables {
		ownedStates, err = ownedPullRequestStateCounts(ctx, p.facts.client, orgID, repoKeysOf(owned), timeBound)
		if err != nil {
			return outcome, err
		}
		reachedStates, err = teamLinkedPullRequestStateCounts(ctx, p.facts.client, orgID, teamIDs, reachedOnly, timeBound)
		if err != nil {
			return outcome, err
		}
	}
	for _, teamID := range teamIDs {
		subject := bySubject[teamID]
		repos := owned[teamID]
		if len(repos) == 0 && len(reachedOnly[teamID]) == 0 {
			outcome.teamsWithoutRepos = append(outcome.teamsWithoutRepos, subject)
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
			if tables {
				cells := map[string]contextfabric.FactValue{
					"repository_id":                        contextfabric.StringFactValue(repo.key),
					"pull_requests_opened_window":          contextfabric.IntegerFactValue(r.opened),
					"pull_requests_merged_window":          contextfabric.IntegerFactValue(r.merged),
					"pull_requests_closed_unmerged_window": contextfabric.IntegerFactValue(r.closedUnmerged),
				}
				if repo.name != "" {
					cells["repository_name"] = contextfabric.StringFactValue(repo.name)
				}
				cells["reach_basis"] = contextfabric.StringFactValue(repositoryReachOwned)
				breakdown = append(breakdown, contextfabric.FactValueRow{Fields: cells})
			}
		}
		var byState []contextfabric.FactValueRow
		if tables {
			for _, repo := range repos {
				byState = append(byState, pullRequestsByStateRows(repo.key, repo.name, repositoryReachOwned, "", ownedStates[repo.key])...)
			}
		}
		var reachedWithData int64
		teamLinkUnwindowed := false
		weakestRank := int64(0)
		for _, repo := range reachedOnly[teamID] {
			r, ok := reachedCounts[teamID+blockerPairSeparator+repo.key]
			if !ok {
				continue
			}
			outcome.contributed++
			reachedWithData++
			if timeBound.active && !teamLinkUnwindowed {
				teamLinkUnwindowed = true
				outcome.linkUnwindowedTeams = append(outcome.linkUnwindowedTeams, subject)
			}
			opened += r.opened
			merged += r.merged
			closedUnmerged += r.closedUnmerged
			if weakestRank == 0 || repo.linkRank < weakestRank {
				weakestRank = repo.linkRank
			}
			if tables {
				byState = append(byState, pullRequestsByStateRows(repo.key, repo.name, repositoryReachOwnedProject, blockerLinkTierName(repo.linkRank), reachedStates[teamID+blockerPairSeparator+repo.key])...)
				cells := map[string]contextfabric.FactValue{
					"repository_id":                        contextfabric.StringFactValue(repo.key),
					"pull_requests_opened_window":          contextfabric.IntegerFactValue(r.opened),
					"pull_requests_merged_window":          contextfabric.IntegerFactValue(r.merged),
					"pull_requests_closed_unmerged_window": contextfabric.IntegerFactValue(r.closedUnmerged),
					"reach_basis":                          contextfabric.StringFactValue(repositoryReachOwnedProject),
				}
				if tier := blockerLinkTierName(repo.linkRank); tier != "" {
					cells["link_tier"] = contextfabric.StringFactValue(tier)
				}
				if repo.name != "" {
					cells["repository_name"] = contextfabric.StringFactValue(repo.name)
				}
				breakdown = append(breakdown, contextfabric.FactValueRow{Fields: cells})
			}
		}
		fields := map[string]contextfabric.FactValue{
			"rollup_basis":                    contextfabric.StringFactValue(teamRollupBasis),
			"repository_tables_basis":         contextfabric.StringFactValue(teamRollupTablesBasis(tables)),
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
		if n := len(reachedOnly[teamID]); n > 0 {
			fields["project_reached_repository_count"] = contextfabric.IntegerFactValue(int64(n))
		}
		if tier := blockerLinkTierName(weakestRank); tier != "" {
			fields["repository_link_basis"] = contextfabric.StringFactValue(tier)
		}
		if table, omitted, ok := ownedRepositoriesFactValue(repos); ok && tables {
			fields["owned_repositories"] = table
			if omitted > 0 {
				outcome.truncated = true
				fields["owned_repositories_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
			}
		}
		if table, omitted, ok := pullRequestsByStateFactValue(byState, grainExact); ok && tables {
			fields["pull_requests_by_state"] = table
			if omitted > 0 {
				outcome.truncated = true
				fields["pull_requests_by_state_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
			}
		}
		if withData+reachedWithData > 0 {
			fields["pull_requests_opened_window"] = contextfabric.IntegerFactValue(opened)
			fields["pull_requests_merged_window"] = contextfabric.IntegerFactValue(merged)
			fields["pull_requests_closed_unmerged_window"] = contextfabric.IntegerFactValue(closedUnmerged)
			if table, omitted, ok := repositoryBreakdownFactValue(breakdown, grainExact,
				[]string{"pull_requests_opened_window", "pull_requests_merged_window", "pull_requests_closed_unmerged_window"}, []string{"repository_name", "reach_basis", "link_tier"}); ok && tables {
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

// teamLinkedPullRequestCounts counts, per (team, project-reached repository),
// the distinct pull requests linked to an issue of a project the team owns,
// by the same opened / merged / closed-unmerged rule the owned arm applies.
// The key is team id + blockerPairSeparator + repository id. A repository
// whose linked pull requests have no git_pull_requests row is absent.
func teamLinkedPullRequestCounts(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, teamIDs []string, reachedOnly map[string][]teamReachedRepo, window rollupWindow, bound factTimeBound) (map[string]pullRequestRollupRepo, error) {
	counts := map[string]pullRequestRollupRepo{}
	if reachedRepositoryCount(reachedOnly) == 0 {
		return counts, nil
	}
	err := pagedOrgScopedTeamRepoRead(ctx, client, "ReadTeamProjectLinkedPullRequests", orgID, teamIDs,
		teamRepoCursor{teamExpr: "lr.team_id", repoExpr: "lr.pr_repo_id", repoType: "UUID"},
		append(window.bindings(), boundReaderBindings(bound)...),
		func(having string, limit int) string {
			return `SELECT lr.team_id, toString(lr.pr_repo_id),
	toInt64(countIf(` + window.timestampExpr("pr.created_at") + `)),
	toInt64(countIf(pr.merged_at IS NOT NULL AND ` + window.timestampExpr("pr.merged_at") + `)),
	toInt64(countIf(pr.merged_at IS NULL AND pr.closed_at IS NOT NULL AND ` + window.timestampExpr("pr.closed_at") + `))
FROM (SELECT DISTINCT team_id, pr_repo_id, pr_number FROM ` + teamProjectLinkRowsSQL("ids", bound) + `) AS lr
INNER JOIN git_pull_requests AS pr FINAL ON pr.org_id = {org_id:String} AND pr.repo_id = lr.pr_repo_id AND pr.number = lr.pr_number
GROUP BY lr.team_id, lr.pr_repo_id
` + having + `
ORDER BY lr.team_id, lr.pr_repo_id
LIMIT ` + fmt.Sprint(limit)
		},
		func(row contextpacket.ClickHouseRowScanner) (string, string, error) {
			var teamID, repoID string
			var r pullRequestRollupRepo
			if err := row.Scan(&teamID, &repoID, &r.opened, &r.merged, &r.closedUnmerged); err != nil {
				return "", "", err
			}
			counts[teamID+blockerPairSeparator+repoID] = r
			return teamID, repoID, nil
		})
	if err != nil {
		return nil, err
	}
	return counts, nil
}
