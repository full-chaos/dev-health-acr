package devhealthfacts

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4099 stage 2: the ClickHouse-backed contextfabric.FactScopeExpander
// for the three ratified project-origin policies.
//
// A ClickHouse implementation, deliberately NOT a FalkorDB graph traversal,
// even though the "activity proxy" chain this whole ticket is about was
// discovered and reasoned about in graph terms. Two independent reasons:
//
//  1. The chain's own producer -- devhealthsource/teams_projects_edges.go's
//     queryWorkItemProjects -- reads ClickHouse directly, and CHAOS-4108's
//     fix (the widened project-id/project-key join this file's own queries
//     mirror) landed there, not in any graph-side code. Querying ClickHouse
//     is querying the SAME source of truth the fix already proved correct,
//     with no second copy of the join logic to drift.
//  2. CHAOS-3916's graph rebuild is deliberately HELD until the current
//     re-measure run completes, so the STANDING FalkorDB graph still
//     carries the pre-CHAOS-4108 identity shape today -- a graph traversal
//     implementation would be provably inert (or silently wrong) against
//     live infrastructure for reasons that have nothing to do with whether
//     THIS code is correct. A ClickHouse implementation's correctness is
//     verifiable against a real, disposable, freshly-seeded container
//     (this file's own integration tests) independent of that hold.
//
// zeroRepositoryID mirrors devhealthsource/clickhouse.go's own constant of
// the identical name and value -- duplicated rather than exported across
// the package boundary, matching this repository's own established
// "same sentinel value, independently declared, doc-linked" convention
// (e.g. internal/runtime/hosted/open.go's priorHandleGrammarChecker mirrors
// falkorgraph.ConfigFromEnv's HandleGrammarChecker for the identical
// reason). A work_items.repo_id column carrying this value is repo-less BY
// DESIGN (a Linear-sourced work item), never an orphan -- see
// workItemAuthorization's own doc comment, devhealthsource/clickhouse.go --
// and must never expand to a fake repository.
const zeroRepositoryID = "00000000-0000-0000-0000-000000000000"

// ScopeExpander implements contextfabric.FactScopeExpander for the three
// ratified project-origin policies (project_work_item_repository_v1,
// project_work_item_pull_request_v1, project_work_item_pull_request_review_v1).
// Every traversal is authorized at the REPOSITORY hop: a pull request or
// review belongs to exactly one repository, so its authorization scope IS
// the repository's (repoAuthorization's own convention,
// devhealthsource/clickhouse.go), and this expander never queries a
// pull_request/pull_request_review row from a repository it has not first
// authorized -- fact CONTENT from an unauthorized repository must never
// even be READ, let alone disclosed (ruling invariant 9, amended 2026-08-22
// for existence only, never content).
type ScopeExpander struct {
	client contextpacket.ClickHouseQueryClient
}

// NewScopeExpander builds the stage-2 expander over the SAME ClickHouse
// query boundary every FactProvider in this package already shares
// (internal/contextpacket.ClickHouseQueryClient) -- no second database
// path.
func NewScopeExpander(client contextpacket.ClickHouseQueryClient) *ScopeExpander {
	return &ScopeExpander{client: client}
}

// ExpandFactScope implements contextfabric.FactScopeExpander.
func (e *ScopeExpander) ExpandFactScope(ctx context.Context, request contextfabric.FactScopeExpansionRequest) (contextfabric.FactScopeExpansionResult, error) {
	if e == nil || e.client == nil {
		return contextfabric.FactScopeExpansionResult{}, errors.New("devhealthfacts: scope expander requires a ClickHouse query client")
	}
	orgID, err := requireOrgID(request.Principal.OrgID)
	if err != nil {
		return contextfabric.FactScopeExpansionResult{}, err
	}
	limit := request.Limit
	if limit <= 0 {
		return contextfabric.FactScopeExpansionResult{}, errors.New("devhealthfacts: scope expansion limit must be positive")
	}

	switch request.Policy {
	case contextfabric.FactScopePolicyProjectWorkItemRepository:
		repos, counts, err := e.projectRepositories(ctx, orgID, request.Origins, limit)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		targets, authCounts := authorizeRepositories(request.Principal, repos)
		return contextfabric.FactScopeExpansionResult{Targets: targets, Counts: mergeCounts(counts, authCounts)}, nil

	case contextfabric.FactScopePolicyProjectWorkItemPullRequest:
		// One hop further than repository. Read the SAME work-item-scoped
		// repository set (no cap yet -- the cap belongs to THIS policy's
		// own target kind, pull requests, not to the intermediate
		// repository set), authorize it, and query pull requests ONLY from
		// repositories that survived authorization -- content from a
		// dropped repository is never read, let alone returned.
		repos, repoCounts, err := e.projectRepositories(ctx, orgID, request.Origins, maxFactScopeRepositoryFanout)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		authorizedRepos, dropped := splitRepositoriesByAuthorization(request.Principal, repos)
		if len(authorizedRepos) == 0 {
			return contextfabric.FactScopeExpansionResult{Counts: contextfabric.FactScopeExpansionCounts{
				CandidateCount: repoCounts.CandidateCount, AuthorizationDroppedCount: dropped,
				MissingNextHopCount: repoCounts.MissingNextHopCount, Truncated: repoCounts.Truncated,
			}}, nil
		}
		targets, prCounts, err := e.pullRequestsForRepositories(ctx, orgID, authorizedRepos, limit)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		prCounts.AuthorizationDroppedCount += dropped
		prCounts.MissingNextHopCount += repoCounts.MissingNextHopCount
		// codex xhigh review round 1 (confirmed real, MEDIUM): the
		// INTERMEDIATE repository set can itself be truncated at
		// maxFactScopeRepositoryFanout, which repoCounts.Truncated already
		// reports -- OR it into the final answer rather than discarding
		// it. Without this, a project reaching >200 repositories would
		// silently query pull requests from only the first 200 while
		// reporting a clean, non-truncated result.
		prCounts.Truncated = prCounts.Truncated || repoCounts.Truncated
		return contextfabric.FactScopeExpansionResult{Targets: targets, Counts: prCounts}, nil

	case contextfabric.FactScopePolicyProjectWorkItemPullRequestReview:
		repos, repoCounts, err := e.projectRepositories(ctx, orgID, request.Origins, maxFactScopeRepositoryFanout)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		authorizedRepos, dropped := splitRepositoriesByAuthorization(request.Principal, repos)
		if len(authorizedRepos) == 0 {
			return contextfabric.FactScopeExpansionResult{Counts: contextfabric.FactScopeExpansionCounts{
				CandidateCount: repoCounts.CandidateCount, AuthorizationDroppedCount: dropped,
				MissingNextHopCount: repoCounts.MissingNextHopCount, Truncated: repoCounts.Truncated,
			}}, nil
		}
		targets, reviewCounts, err := e.pullRequestReviewsForRepositories(ctx, orgID, authorizedRepos, limit)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		reviewCounts.AuthorizationDroppedCount += dropped
		reviewCounts.MissingNextHopCount += repoCounts.MissingNextHopCount
		// See the identical fix's own comment in the pull_request case
		// above.
		reviewCounts.Truncated = reviewCounts.Truncated || repoCounts.Truncated
		return contextfabric.FactScopeExpansionResult{Targets: targets, Counts: reviewCounts}, nil

	default:
		return contextfabric.FactScopeExpansionResult{}, fmt.Errorf("devhealthfacts: scope expander does not implement policy %q", request.Policy)
	}
}

// maxFactScopeRepositoryFanout bounds the INTERMEDIATE repository set for
// the pull_request/pull_request_review policies -- generous relative to
// maxFactScopeTargets (contextfabric's own per-policy cap), since a
// project's own repository fan-out is typically small (the ratified
// policies' own Limit override on FactPullRequests/FactReviews governs the
// actual admitted-target cap; this only bounds how many repositories this
// expander will ever authorize-check and query pull requests from for ONE
// traversal).
const maxFactScopeRepositoryFanout = 200

// repositoryCandidate is one DISTINCT repository the project->work_item
// chain reached, before authorization.
type repositoryCandidate struct {
	repoID   string
	repoSlug string
}

// projectRepositories runs the activity-proxy chain's first hop: project ->
// work_item -> repository, widened to BOTH join arms CHAOS-4108 proved are
// live (projects.id, and projects.project_key for gitlab-sourced work
// items) -- scoped to the SPECIFIC project origins requested, never a
// whole-org scan. Returns DISTINCT real repository candidates (the
// zero-UUID sentinel and any orphaned, unmatched repo_id are counted in
// MissingNextHopCount, never returned as a candidate) and the raw
// CandidateCount/MissingNextHopCount/Truncated counts. Authorization is the
// caller's job (authorizeRepositories/splitRepositoriesByAuthorization) --
// this function never reads request.Principal.
func (e *ScopeExpander) projectRepositories(ctx context.Context, orgID string, origins []contextfabric.SubjectRef, limit int) ([]repositoryCandidate, contextfabric.FactScopeExpansionCounts, error) {
	projectIDs := decodeProjectOriginIDs(origins)
	if len(projectIDs) == 0 {
		// Every origin failed to decode as a project.v2 id -- an invariant
		// violation upstream (the resolver only ever calls this policy for
		// a SubjectProject origin), not a data statement about THIS
		// project. Reported as a genuinely empty traversal: no candidate
		// was ever queried for.
		return nil, contextfabric.FactScopeExpansionCounts{}, nil
	}
	limitPlusOne := limit + 1
	// The join-key ambiguity guard is computed across the WHOLE ORG (codex
	// xhigh review round 1, confirmed real, MEDIUM): scoping the
	// key_resolution_count computation to only the REQUESTED project ids
	// -- as an earlier version of this query did -- makes it blind to a
	// DIFFERENT, unrequested project elsewhere in the org whose OWN id
	// happens to equal the requested project's project_key (or vice
	// versa). That different project's work items would then be silently
	// attributed to the requested project's scope, exactly the ambiguity
	// devhealthsource/teams_projects_edges.go's own queryWorkItemProjects
	// exists to catch and OMIT rather than guess (its own
	// key_resolution_count, computed the identical way: DISTINCT (id,
	// join_key) pairs across every project in the org, partitioned by
	// join_key). This mirrors that computation exactly and only narrows to
	// the requested project_ids in the OUTER WHERE, after ambiguity has
	// already been decided over the full org -- so a work item whose
	// project_id resolves ambiguously is excluded here exactly as it would
	// be omitted (never guessed) by the real producer, not merely
	// filtered out of a set that never saw the ambiguity in the first
	// place.
	//
	// provider is carried into the DISTINCT/dedup step (codex xhigh review
	// round 2, confirmed real, MEDIUM), mirroring queryWorkItemProjects'
	// own `SELECT DISTINCT id, provider, join_key` exactly
	// (teams_projects_edges.go:109). Without it, two DIFFERENT providers'
	// projects that happen to share the identical raw `id` string (schema-
	// legal: projects' dedup key is (org_id, provider, id), so id is only
	// unique PER PROVIDER -- teams_projects.go's own doc comment records
	// this as a live-verified-absent-but-schema-permitted risk) would
	// collapse into ONE (id, join_key) row under a bare `DISTINCT id,
	// join_key`, silently HIDING the ambiguity: key_resolution_count would
	// read 1 instead of 2, and a work item using that shared id would be
	// confidently (and wrongly) attributed to whichever provider's row
	// ClickHouse happened to keep, exactly the "silently attribute to the
	// wrong project" failure mode this whole guard exists to prevent.
	// Carrying provider through restores the guard's own precondition:
	// PARTITION BY join_key still counts DISTINCT (id, provider) pairs
	// sharing that value, so a genuine cross-provider collision now
	// correctly yields key_resolution_count = 2 and is omitted, never
	// guessed, for EITHER provider's claim.
	statement := `SELECT DISTINCT toString(w.repo_id), ifNull(r.repo, '')
FROM work_items AS w FINAL
INNER JOIN (
  SELECT id, join_key, count() OVER (PARTITION BY join_key) AS key_resolution_count
  FROM (
    SELECT DISTINCT id, provider, join_key FROM (
      SELECT id, provider, id AS join_key FROM projects FINAL WHERE org_id = {org_id:String}
      UNION ALL
      SELECT id, provider, ifNull(project_key, '') AS join_key FROM projects FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != ''
    )
  )
) AS p ON p.join_key = w.project_id
LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
WHERE w.org_id = {org_id:String} AND p.key_resolution_count = 1 AND p.id IN {project_ids:Array(String)}
ORDER BY toString(w.repo_id)
LIMIT ` + strconv.Itoa(limitPlusOne)
	rows, err := e.client.Query(ctx, statement, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "project_ids", Value: projectIDs},
	})
	if err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	defer rows.Close()

	var candidates []repositoryCandidate
	counts := contextfabric.FactScopeExpansionCounts{}
	for rows.Next() {
		var repoID, repoSlug string
		if err := rows.Scan(&repoID, &repoSlug); err != nil {
			return nil, contextfabric.FactScopeExpansionCounts{}, err
		}
		switch {
		case repoID == zeroRepositoryID:
			// Repo-less by design (a Linear-sourced work item) -- never a
			// fake repository target.
			counts.MissingNextHopCount++
		case repoSlug == "":
			// A non-zero repo_id that matched no repos row: an orphan,
			// same treatment as the zero-UUID sentinel for THIS purpose --
			// there is no real repository entity to admit.
			counts.MissingNextHopCount++
		default:
			counts.CandidateCount++
			candidates = append(candidates, repositoryCandidate{repoID: repoID, repoSlug: repoSlug})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	totalRows := counts.CandidateCount + counts.MissingNextHopCount
	counts.Truncated = totalRows >= limitPlusOne
	// NOT trimmed to limit here (codex xhigh review round 1, confirmed
	// real, MEDIUM): FactScopeExpansionRequest.Limit's own doc comment
	// requires this port to "read up to Limit+1 rows and return ALL of
	// them" so the resolver's own overflow-row detection
	// (fact_scope.go's expand()) can independently confirm truncation
	// from the extra row, rather than trusting Counts.Truncated alone.
	// Pre-trimming here satisfied every existing test (this function's
	// own Truncated flag was always set correctly), but broke that
	// stated safety contract silently: if this flag were ever
	// underreported by a future change, the resolver's own overflow
	// check would have nothing left to catch it, because the overflow
	// row would already be gone.
	return candidates, counts, nil
}

// decodeProjectOriginIDs recovers the raw projects.id value for every
// project-kind origin subject, via identity.Segments (the exact inverse of
// the identity.Derive(identity.KindProject, []string{provider, id}, nil)
// call devhealthsource/teams_projects_edges.go already uses to MINT a
// project's canonical id). Deduplicated; a subject whose CanonicalID does
// not decode is skipped, not errored -- the resolver only ever calls this
// expander for subjects it already resolved as SubjectProject, so a
// decode failure here is unreachable in practice, not a data statement
// about this traversal.
func decodeProjectOriginIDs(origins []contextfabric.SubjectRef) []string {
	seen := make(map[string]struct{}, len(origins))
	ids := make([]string, 0, len(origins))
	for _, origin := range origins {
		segments, ok := identity.Segments(identity.KindProject, origin.CanonicalID)
		if !ok || len(segments) != 2 {
			continue
		}
		id := segments[1]
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// authorizeRepositories splits repository candidates by authorization and
// returns the admitted set as repository SubjectRefs (the target kind for
// FactScopePolicyProjectWorkItemRepository), plus the resulting
// AdmittedCount/AuthorizationDroppedCount.
func authorizeRepositories(principal storage.Principal, repos []repositoryCandidate) ([]contextfabric.SubjectRef, contextfabric.FactScopeExpansionCounts) {
	targets := make([]contextfabric.SubjectRef, 0, len(repos))
	dropped := 0
	for _, repo := range repos {
		if !authorizedForRepository(principal, repo.repoSlug) {
			dropped++
			continue
		}
		targets = append(targets, contextfabric.SubjectRef{
			Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repo.repoID, Label: repo.repoSlug,
		})
	}
	return targets, contextfabric.FactScopeExpansionCounts{AuthorizationDroppedCount: dropped}
}

// splitRepositoriesByAuthorization is authorizeRepositories' twin for the
// PR/review policies, which need the AUTHORIZED repositories themselves
// (to scope the next query) rather than repository SubjectRefs.
func splitRepositoriesByAuthorization(principal storage.Principal, repos []repositoryCandidate) (authorized []repositoryCandidate, droppedCount int) {
	authorized = make([]repositoryCandidate, 0, len(repos))
	for _, repo := range repos {
		if !authorizedForRepository(principal, repo.repoSlug) {
			droppedCount++
			continue
		}
		authorized = append(authorized, repo)
	}
	return authorized, droppedCount
}

// authorizedForRepository reuses graphrank.AuthorizedAttributes -- the SAME
// backend-neutral authorization primitive falkorgraph's own graph traversal
// uses -- rather than re-deriving repository-scope matching here. A
// repository's own authorization is EXACTLY the single-slug scope
// repoAuthorization(repoSlug) builds in devhealthsource/clickhouse.go
// (mirrored here as a one-entry attribute map: this package cannot import
// devhealthsource's unexported builder, and the encoding graphrank expects
// is the same either way -- a []string authorization_repositories entry).
func authorizedForRepository(principal storage.Principal, repoSlug string) bool {
	attributes := map[string]interface{}{"authorization_repositories": []string{repoSlug}}
	return graphrank.AuthorizedAttributes(principal, contextfabric.RequestedScope{}, attributes)
}

// mergeCounts combines the repository-hop counts with the authorization
// split's own AuthorizationDroppedCount.
func mergeCounts(base, addition contextfabric.FactScopeExpansionCounts) contextfabric.FactScopeExpansionCounts {
	base.AuthorizationDroppedCount += addition.AuthorizationDroppedCount
	return base
}

// pullRequestsForRepositories reads git_pull_requests from ALREADY
// AUTHORIZED repositories only -- content from a repository this caller
// may not read is never queried, let alone returned. Mirrors
// devhealthsource/tables.go's own queryPullRequests column/canonicalization
// choices for the fields this policy's target subject needs (repo_id,
// number), and PullRequestsProvider's own canonical id convention
// ("pull_request:" + repoID + ":" + number) exactly, since the derived
// subject must round-trip through that provider's own subjectIndex.
func (e *ScopeExpander) pullRequestsForRepositories(ctx context.Context, orgID string, repos []repositoryCandidate, limit int) ([]contextfabric.SubjectRef, contextfabric.FactScopeExpansionCounts, error) {
	if len(repos) == 0 {
		return nil, contextfabric.FactScopeExpansionCounts{}, nil
	}
	repoIDs := repositoryIDs(repos)
	limitPlusOne := limit + 1
	statement := `SELECT toString(p.repo_id), p.number
FROM git_pull_requests AS p FINAL
WHERE p.org_id = {org_id:String} AND toString(p.repo_id) IN {repo_ids:Array(String)}
ORDER BY toString(p.repo_id), p.number
LIMIT ` + strconv.Itoa(limitPlusOne)
	rows, err := e.client.Query(ctx, statement, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "repo_ids", Value: repoIDs},
	})
	if err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	defer rows.Close()

	var targets []contextfabric.SubjectRef
	for rows.Next() {
		var repoID string
		var number uint32
		if err := rows.Scan(&repoID, &number); err != nil {
			return nil, contextfabric.FactScopeExpansionCounts{}, err
		}
		targets = append(targets, contextfabric.SubjectRef{
			Kind:        contractsv1.ContextFabricSubjectPullRequest,
			CanonicalID: fmt.Sprintf("pull_request:%s:%d", repoID, number),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	counts := contextfabric.FactScopeExpansionCounts{CandidateCount: len(targets)}
	// NOT trimmed to limit (codex xhigh review round 1, confirmed real,
	// MEDIUM) -- see projectRepositories' own identical comment. Returning
	// all limit+1 targets lets the resolver's own overflow-row detection
	// independently confirm truncation rather than trusting this flag
	// alone.
	if len(targets) >= limitPlusOne {
		counts.Truncated = true
	}
	return targets, counts, nil
}

// pullRequestReviewsForRepositories is pullRequestsForRepositories' own
// twin, one hop further: git_pull_request_reviews INNER JOINed to
// git_pull_requests so a review can only ever be reached through a real
// pull request in an ALREADY AUTHORIZED repository. Canonical id via
// identity.Derive(identity.KindPullRequestReview, ...) -- the EXACT segment
// order (repo_id, number, review_id) devhealthsource/tables.go's
// queryPullRequestReviews already mints, so the derived subject decodes
// through ReviewsProvider's own v2Index unchanged.
func (e *ScopeExpander) pullRequestReviewsForRepositories(ctx context.Context, orgID string, repos []repositoryCandidate, limit int) ([]contextfabric.SubjectRef, contextfabric.FactScopeExpansionCounts, error) {
	if len(repos) == 0 {
		return nil, contextfabric.FactScopeExpansionCounts{}, nil
	}
	repoIDs := repositoryIDs(repos)
	limitPlusOne := limit + 1
	statement := `SELECT r.review_id, toString(r.repo_id), r.number
FROM git_pull_request_reviews AS r FINAL
INNER JOIN git_pull_requests AS p FINAL ON r.repo_id = p.repo_id AND r.number = p.number AND r.org_id = p.org_id
WHERE r.org_id = {org_id:String} AND toString(r.repo_id) IN {repo_ids:Array(String)}
ORDER BY toString(r.repo_id), r.number, r.review_id
LIMIT ` + strconv.Itoa(limitPlusOne)
	rows, err := e.client.Query(ctx, statement, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "repo_ids", Value: repoIDs},
	})
	if err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	defer rows.Close()

	var targets []contextfabric.SubjectRef
	var missingNextHop int
	for rows.Next() {
		var reviewID, repoID string
		var number uint32
		if err := rows.Scan(&reviewID, &repoID, &number); err != nil {
			return nil, contextfabric.FactScopeExpansionCounts{}, err
		}
		canonicalID, omitted, err := identity.Derive(identity.KindPullRequestReview, []string{repoID, strconv.FormatUint(uint64(number), 10), reviewID}, nil)
		if err != nil {
			return nil, contextfabric.FactScopeExpansionCounts{}, err
		}
		if omitted {
			// The natural key exceeded identity.MaxNaturalKeyBytes -- the
			// SAME whole-row omission devhealthsource's own producer
			// applies (a review this traversal cannot safely name at all,
			// not one this traversal decided is unreachable for a
			// resolvable reason).
			missingNextHop++
			continue
		}
		targets = append(targets, contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequestReview, CanonicalID: canonicalID})
	}
	if err := rows.Err(); err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	counts := contextfabric.FactScopeExpansionCounts{CandidateCount: len(targets) + missingNextHop, MissingNextHopCount: missingNextHop}
	// NOT trimmed to limit (codex xhigh review round 1, confirmed real,
	// MEDIUM) -- see projectRepositories' own identical comment.
	if len(targets)+missingNextHop >= limitPlusOne {
		counts.Truncated = true
	}
	return targets, counts, nil
}

func repositoryIDs(repos []repositoryCandidate) []string {
	ids := make([]string, len(repos))
	for i, repo := range repos {
		ids[i] = repo.repoID
	}
	sort.Strings(ids)
	return ids
}
