package devhealthfacts

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

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
		repos, counts, err := e.projectRepositories(ctx, orgID, request.Origins, limit, request.TimeContext)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		targets, targetBasis, targetRoot, authCounts := authorizeRepositories(request.Principal, repos)
		return contextfabric.FactScopeExpansionResult{Targets: targets, TargetBasis: targetBasis, TargetRoot: targetRoot, Counts: mergeCounts(counts, authCounts)}, nil

	case contextfabric.FactScopePolicyProjectWorkItemPullRequest:
		// One hop further than repository. Read the SAME work-item-scoped
		// repository set (no cap yet -- the cap belongs to THIS policy's
		// own target kind, pull requests, not to the intermediate
		// repository set), authorize it, and query pull requests ONLY from
		// repositories that survived authorization -- content from a
		// dropped repository is never read, let alone returned.
		repos, repoCounts, err := e.projectRepositories(ctx, orgID, request.Origins, maxFactScopeRepositoryFanout, request.TimeContext)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		authorizedRepos, dropped := splitRepositoriesByAuthorization(request.Principal, repos)
		if len(authorizedRepos) == 0 {
			return contextfabric.FactScopeExpansionResult{Counts: contextfabric.FactScopeExpansionCounts{
				CandidateCount: repoCounts.CandidateCount, AuthorizationDroppedCount: dropped,
				MissingNextHopCount: repoCounts.MissingNextHopCount, Truncated: repoCounts.Truncated,
				// CHAOS-4109 (codex xhigh review R1, MEDIUM, confirmed
				// real): the intermediate repository hop can itself carry
				// interval-miss/unbounded-fallback signal on a historical
				// axis -- see the identical fix's own comment below.
				TemporalDroppedCount: repoCounts.TemporalDroppedCount, UnboundedValidityCount: repoCounts.UnboundedValidityCount, MalformedTouchCount: repoCounts.MalformedTouchCount, DuplicateAddCount: repoCounts.DuplicateAddCount,
			}}, nil
		}
		targets, targetBasis, targetSource, targetRoot, prCounts, err := e.pullRequestsForRepositories(ctx, orgID, authorizedRepos, limit)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		prCounts.AuthorizationDroppedCount += dropped
		prCounts.MissingNextHopCount += repoCounts.MissingNextHopCount
		// CHAOS-4109 (codex xhigh review R1, MEDIUM, confirmed real): the
		// intermediate repository hop's OWN TemporalDroppedCount/
		// UnboundedValidityCount (from projectRepositoriesAsOf on a
		// historical axis) were being discarded here -- a historical
		// pull_request/review request could answer correctly while
		// telemetry falsely reported zero interval misses and zero
		// current-column fallbacks, exactly the class of gap the
		// Truncated fix immediately below already closed for a different
		// field.
		prCounts.TemporalDroppedCount += repoCounts.TemporalDroppedCount
		prCounts.UnboundedValidityCount += repoCounts.UnboundedValidityCount
		prCounts.MalformedTouchCount += repoCounts.MalformedTouchCount
		prCounts.DuplicateAddCount += repoCounts.DuplicateAddCount
		// codex xhigh review round 1 (confirmed real, MEDIUM): the
		// INTERMEDIATE repository set can itself be truncated at
		// maxFactScopeRepositoryFanout, which repoCounts.Truncated already
		// reports -- OR it into the final answer rather than discarding
		// it. Without this, a project reaching >200 repositories would
		// silently query pull requests from only the first 200 while
		// reporting a clean, non-truncated result.
		prCounts.Truncated = prCounts.Truncated || repoCounts.Truncated
		return contextfabric.FactScopeExpansionResult{Targets: targets, TargetBasis: targetBasis, TargetAttributionSource: targetSource, TargetRoot: targetRoot, Counts: prCounts}, nil

	case contextfabric.FactScopePolicyProjectWorkItemPullRequestReview:
		repos, repoCounts, err := e.projectRepositories(ctx, orgID, request.Origins, maxFactScopeRepositoryFanout, request.TimeContext)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		authorizedRepos, dropped := splitRepositoriesByAuthorization(request.Principal, repos)
		if len(authorizedRepos) == 0 {
			return contextfabric.FactScopeExpansionResult{Counts: contextfabric.FactScopeExpansionCounts{
				CandidateCount: repoCounts.CandidateCount, AuthorizationDroppedCount: dropped,
				MissingNextHopCount: repoCounts.MissingNextHopCount, Truncated: repoCounts.Truncated,
				TemporalDroppedCount: repoCounts.TemporalDroppedCount, UnboundedValidityCount: repoCounts.UnboundedValidityCount, MalformedTouchCount: repoCounts.MalformedTouchCount, DuplicateAddCount: repoCounts.DuplicateAddCount,
			}}, nil
		}
		targets, targetBasis, targetSource, targetRoot, reviewCounts, err := e.pullRequestReviewsForRepositories(ctx, orgID, authorizedRepos, limit)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		reviewCounts.AuthorizationDroppedCount += dropped
		reviewCounts.MissingNextHopCount += repoCounts.MissingNextHopCount
		// See the identical fix's own comment in the pull_request case
		// above.
		reviewCounts.TemporalDroppedCount += repoCounts.TemporalDroppedCount
		reviewCounts.UnboundedValidityCount += repoCounts.UnboundedValidityCount
		reviewCounts.MalformedTouchCount += repoCounts.MalformedTouchCount
		reviewCounts.DuplicateAddCount += repoCounts.DuplicateAddCount
		reviewCounts.Truncated = reviewCounts.Truncated || repoCounts.Truncated
		return contextfabric.FactScopeExpansionResult{Targets: targets, TargetBasis: targetBasis, TargetAttributionSource: targetSource, TargetRoot: targetRoot, Counts: reviewCounts}, nil

	// --- CHAOS-4101: team-origin policies, ruled 2026-08-24 ---
	//
	// Same shape as the three project cases above, ONE hop earlier
	// (team -OWNED_BY_TEAM<- work_item, instead of project
	// -BELONGS_TO_PROJECT<- work_item) and carrying a PER-REPOSITORY basis
	// derived from teamRepositories' own attribution-source read, rather
	// than the rule's fixed default. See teamRepositories' own doc comment.
	case contextfabric.FactScopePolicyTeamPrimaryAttributionRepository:
		repos, counts, err := e.teamRepositories(ctx, orgID, request.Origins, limit)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		targets, targetBasis, targetRoot, authCounts := authorizeRepositories(request.Principal, repos)
		merged := mergeCounts(counts, authCounts)
		targetSource := repositoryAttributionSources(repos, targets)
		return contextfabric.FactScopeExpansionResult{Targets: targets, TargetBasis: targetBasis, TargetAttributionSource: targetSource, TargetRoot: targetRoot, Counts: merged}, nil

	case contextfabric.FactScopePolicyTeamPrimaryAttributionPullRequest:
		repos, repoCounts, err := e.teamRepositories(ctx, orgID, request.Origins, maxFactScopeRepositoryFanout)
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
		targets, targetBasis, targetSource, targetRoot, prCounts, err := e.pullRequestsForRepositories(ctx, orgID, authorizedRepos, limit)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		prCounts.AuthorizationDroppedCount += dropped
		prCounts.MissingNextHopCount += repoCounts.MissingNextHopCount
		prCounts.Truncated = prCounts.Truncated || repoCounts.Truncated
		return contextfabric.FactScopeExpansionResult{Targets: targets, TargetBasis: targetBasis, TargetAttributionSource: targetSource, TargetRoot: targetRoot, Counts: prCounts}, nil

	case contextfabric.FactScopePolicyTeamPrimaryAttributionPullRequestReview:
		repos, repoCounts, err := e.teamRepositories(ctx, orgID, request.Origins, maxFactScopeRepositoryFanout)
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
		targets, targetBasis, targetSource, targetRoot, reviewCounts, err := e.pullRequestReviewsForRepositories(ctx, orgID, authorizedRepos, limit)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		reviewCounts.AuthorizationDroppedCount += dropped
		reviewCounts.MissingNextHopCount += repoCounts.MissingNextHopCount
		reviewCounts.Truncated = reviewCounts.Truncated || repoCounts.Truncated
		return contextfabric.FactScopeExpansionResult{Targets: targets, TargetBasis: targetBasis, TargetAttributionSource: targetSource, TargetRoot: targetRoot, Counts: reviewCounts}, nil

	// --- CHAOS-5405: the fourteen work-item-target policies ---
	//
	// All fourteen share ONE implementation and differ only in which origin
	// side they read from. They are listed as fourteen CASES rather than
	// collapsed behind a set membership test on purpose: the policy names are
	// the product commitments, and an exhaustive switch is what makes a
	// missing one a compile-visible gap instead of a silent fall-through to
	// the "does not implement" refusal below (which the resolver would then
	// report as `failed` -- a differently-shaped hollow answer, not a fix).
	case contextfabric.FactScopePolicyProjectWorkItemStatus,
		contextfabric.FactScopePolicyProjectWorkItemWork,
		contextfabric.FactScopePolicyProjectWorkItemActualCompletion,
		contextfabric.FactScopePolicyProjectWorkItemBlockers,
		contextfabric.FactScopePolicyProjectWorkItemRequiredChildren,
		contextfabric.FactScopePolicyProjectWorkItemIdentity,
		contextfabric.FactScopePolicyProjectWorkItemMembership:
		if unsupported := refuseNonCurrentWorkItemAxis(request); unsupported != nil {
			return contextfabric.FactScopeExpansionResult{}, unsupported
		}
		candidates, counts, err := e.projectWorkItems(ctx, request.Principal, orgID, request.Origins, limit)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		return workItemExpansionResult(request.Principal, candidates, counts), nil

	case contextfabric.FactScopePolicyTeamPrimaryAttributionWorkItemStatus,
		contextfabric.FactScopePolicyTeamPrimaryAttributionWorkItemWork,
		contextfabric.FactScopePolicyTeamPrimaryAttributionWorkItemActualCompletion,
		contextfabric.FactScopePolicyTeamPrimaryAttributionWorkItemBlockers,
		contextfabric.FactScopePolicyTeamPrimaryAttributionWorkItemRequiredChildren,
		contextfabric.FactScopePolicyTeamPrimaryAttributionWorkItemIdentity,
		contextfabric.FactScopePolicyTeamPrimaryAttributionWorkItemMembership:
		if unsupported := refuseNonCurrentWorkItemAxis(request); unsupported != nil {
			return contextfabric.FactScopeExpansionResult{}, unsupported
		}
		candidates, counts, err := e.teamWorkItems(ctx, request.Principal, orgID, request.Origins, limit)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		return workItemExpansionResult(request.Principal, candidates, counts), nil

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

// repositoryCandidate is one DISTINCT repository the project->work_item OR
// team->work_item chain reached, before authorization.
type repositoryCandidate struct {
	repoID   string
	repoSlug string
	// basis and attributionSource are CHAOS-4101's additions, set ONLY by
	// teamRepositories -- the zero value ("") for every project-origin
	// candidate, which is exactly "no override" (fact_scope.go's expand()
	// falls through to the rule's own Basis when a target is absent from
	// TargetBasis) and "no source to report" respectively. basis is the
	// per-repository FactScopeBasis override (FactScopeBasisActivityProxy
	// for a repo reached by at least one native_team-sourced work item,
	// FactScopeBasisAttributedPrimaryTeam otherwise); attributionSource is
	// the winning work_item_team_attributions.source value that basis was
	// computed from, carried separately for the telemetry breakdown
	// (FactScopeExpansionEvent.AttributionSourceCounts), which needs the
	// exact enum value rather than the two-way basis split.
	basis             contextfabric.FactScopeBasis
	attributionSource string
	// originRoot is CHAOS-4260's addition: the SPECIFIC origin (project or
	// team) whose own edge reached this repository, when the underlying
	// query could determine one deterministically among possibly-several
	// requested origins of the same kind (projectRepositories/
	// projectRepositoriesAsOf/teamRepositories each pick the lexicographically
	// smallest contributing origin id via a SQL aggregate that does NOT
	// change which repositories are returned or how many -- see their own
	// comments). Zero value (empty CanonicalID) when the query could not
	// resolve the raw origin id back to one of the requested SubjectRefs
	// (should not happen in practice, since the aggregate only ever returns
	// an id that was in the request) -- callers only promote this into
	// FactScopeExpansionResult.TargetRoot when it is non-zero, mirroring
	// basis/attributionSource's own "only set on a real value" discipline.
	originRoot contextfabric.SubjectRef
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
//
// CHAOS-4109: on TemporalCurrent (the zero TimeContext included), this runs
// the ORIGINAL, byte-identical statement below -- no risk of regressing the
// well-exercised current-axis path. On TemporalValidTime/TemporalRange, it
// delegates to projectRepositoriesAsOf, which binds the SAME chain's first
// hop to the requested instant/window using
// project_membership_transitions' full touch history (mirroring
// devhealthsource/teams_projects_edges.go's membershipIntervalsSubquery --
// duplicated rather than shared across the package boundary, this file's
// own established convention, see this function's file-level doc comment).
func (e *ScopeExpander) projectRepositories(ctx context.Context, orgID string, origins []contextfabric.SubjectRef, limit int, timeContext contextfabric.TimeContext) ([]repositoryCandidate, contextfabric.FactScopeExpansionCounts, error) {
	// EXPLICIT axis switch, not an if/else fallthrough (codex xhigh review
	// R1, MEDIUM, confirmed real): an if/else that only special-cases
	// ValidTime/Range and falls through to the current-column query for
	// "anything else" would silently answer TemporalObservedTime -- or any
	// future axis this package has never heard of -- with TODAY's
	// membership, exactly the false-historical-answer failure CHAOS-3781's
	// H6 refusal and this ticket's own axis gate exist to prevent. The
	// resolver already blocks ObservedTime from reaching here through the
	// normal engine path (fact_scope.go's resolveRequirement), but this
	// exported method is also this package's own contract boundary, and a
	// boundary must not depend on every caller already having checked.
	//
	// The empty axis is treated as TemporalCurrent, not rejected: every
	// pre-CHAOS-4109 caller (this package's own existing tests included)
	// builds a request with no TimeContext at all, which is the Go zero
	// value (Axis == "") -- rejecting it would be a breaking change to a
	// contract nothing about this ticket touches.
	switch timeContext.Axis {
	case "", contractsv1.ContextFabricTemporalCurrent:
	case contractsv1.ContextFabricTemporalValidTime, contractsv1.ContextFabricTemporalRange:
		return e.projectRepositoriesAsOf(ctx, orgID, origins, limit, timeContext)
	default:
		return nil, contextfabric.FactScopeExpansionCounts{}, fmt.Errorf("devhealthfacts: projectRepositories does not support axis %q", timeContext.Axis)
	}
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
	// CHAOS-4260: the third SELECT column, min(p.id), attributes each
	// repository to the lexicographically smallest requested project id that
	// reaches it -- an aggregate ADDED to the same GROUP BY the prior
	// DISTINCT already implied for (repo_id, repo_slug), so it changes
	// neither which repositories are returned nor how many (CandidateCount/
	// Truncated/the resolver's own overflow-row detection are all unaffected).
	// "Smallest id" is an arbitrary but stable tiebreak among several
	// project origins reaching the SAME repository -- the same "arbitrary
	// among an exact tie, but stable" discipline this package's own
	// investment.go/source_health.go tiebreakers already use -- chosen over
	// "first origin in the caller's slice order" because origins order is
	// not part of any documented contract and SQL has no visibility into it
	// anyway.
	statement := `SELECT toString(w.repo_id), ifNull(r.repo, ''), min(p.id)
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
GROUP BY toString(w.repo_id), ifNull(r.repo, '')
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

	originsByID := projectOriginsByRawID(origins)
	var candidates []repositoryCandidate
	counts := contextfabric.FactScopeExpansionCounts{}
	for rows.Next() {
		var repoID, repoSlug, originProjectID string
		if err := rows.Scan(&repoID, &repoSlug, &originProjectID); err != nil {
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
			candidates = append(candidates, repositoryCandidate{
				repoID: repoID, repoSlug: repoSlug, originRoot: originsByID[originProjectID],
			})
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

// asOfWindow resolves the half-open [start, end) a historical traversal
// must bind to from the interpreted TimeContext -- a point-in-time AsOf is
// the degenerate window [AsOf, AsOf], the same "one shape, one place"
// reduction falkorgraph/temporal.go's own predicate comment documents for
// graph reads. Re-validates the axis and its required bounds rather than
// trusting the caller: fact_scope.go's resolver already checked both before
// reaching here, but this function is a second, independent boundary (the
// same reasoning ExpandFactScope's own target-kind re-check gives for not
// trusting an upstream invariant blindly).
func asOfWindow(timeContext contextfabric.TimeContext) (start, end time.Time, err error) {
	switch timeContext.Axis {
	case contractsv1.ContextFabricTemporalValidTime:
		if timeContext.AsOf == nil {
			return time.Time{}, time.Time{}, errors.New("devhealthfacts: valid_time scope expansion requires AsOf")
		}
		return *timeContext.AsOf, *timeContext.AsOf, nil
	case contractsv1.ContextFabricTemporalRange:
		if timeContext.Start == nil || timeContext.End == nil {
			return time.Time{}, time.Time{}, errors.New("devhealthfacts: range scope expansion requires Start and End")
		}
		return *timeContext.Start, *timeContext.End, nil
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("devhealthfacts: projectRepositoriesAsOf does not support axis %q", timeContext.Axis)
	}
}

// membershipTouchesAsOfSQL renders the work_item-scoped touch/interval
// derivation project_membership_transitions' full history supports
// (CHAOS-4109). MIRRORS devhealthsource/teams_projects_edges.go's
// membershipIntervalsSubquery exactly (same touch polarization, same
// leadInFrame window, same ROWS BETWEEN frame -- see that constant's own
// doc comment for why the frame clause is load-bearing) -- duplicated
// rather than shared across the package boundary, per this file's own
// established convention (this function's own doc comment; see
// projectRepositories' ambiguity-guard citation of the identical producer
// pattern). Narrowed to subject_kind = 'work_item' (the only subject kind
// this expansion chain ever traverses) and to the columns this caller
// needs (repo_id, provider, project_id, observed_at, valid_to) -- no
// subject_id in the output, since a distinct-REPOSITORY answer never needs
// to distinguish which work item on that repository carried the touch.
// is_malformed is projected THROUGH, not filtered out here (CHAOS-4109
// codex xhigh review R1, MEDIUM, confirmed real): every caller of this
// subquery must decide for itself whether to exclude a malformed row from
// its ANSWER (always) and whether to COUNT it for telemetry (only
// historicalDropCount does, today -- see its own doc comment). Filtering
// malformed rows out at this shared subquery, as an earlier version did,
// meant no caller could ever see them again to report a
// MalformedTouchCount, and the scope-expansion telemetry silently dropped
// exactly the same signal devhealthsource's own presenceTelemetryLedger
// already surfaces for the producer path.
// membershipTouchesAsOfSQL mirrors devhealthsource/teams_projects_edges.go's
// membershipIntervalsSubquery exactly -- same two-stage classify-then-close
// window-function state machine, same interval rule (CHAOS-4109, team-lead
// ruling 2026-08-25): a duplicate ADD (immediately preceded by another ADD,
// no REMOVE between) is a continuation of the already-open interval, not a
// new one, and is excluded from `closed`'s row set before recomputing
// row_number/touch_count/leadInFrame there; a dangling REMOVE (no prior ADD
// to close) is the one case still flagged is_malformed. See that
// subquery's own doc comment for the full rule and the CTE-column-naming
// trap (`dup_flag`/`dangling_flag`, never reusing this query's own output
// alias names `is_duplicate_add`/`is_malformed`, which silently duplicated
// rows across UNION ALL branches when tried).
//
// Narrowed from the producer's shape to what THIS caller needs: no
// event_id (no RelationshipID minted here), no subject_id (a distinct-
// REPOSITORY answer never needs to know which work item on it carried the
// touch). is_duplicate_add is projected through for MalformedTouchCount's
// own telemetry sibling, DuplicateAddCount -- duplicate rows carry no
// interval of their own (valid_to is always NULL) and callers must
// exclude them from admission the same way they already exclude
// is_malformed rows, but still count them.
const membershipTouchesAsOfSQL = `(
  WITH touches AS (
    SELECT repo_id, subject_id, provider, to_project_id AS project_id, occurred_at, event_id, 1 AS is_add
    FROM project_membership_transitions FINAL
    WHERE org_id = {org_id:String} AND subject_kind = 'work_item' AND to_project_id != '' AND to_project_id != from_project_id
    UNION ALL
    SELECT repo_id, subject_id, provider, from_project_id AS project_id, occurred_at, event_id, 0 AS is_add
    FROM project_membership_transitions FINAL
    WHERE org_id = {org_id:String} AND subject_kind = 'work_item' AND from_project_id != '' AND from_project_id != to_project_id
  ),
  classified AS (
    SELECT repo_id, subject_id, provider, project_id, occurred_at, event_id, is_add,
      (is_add = 1 AND lagInFrame(is_add, 1, 2) OVER w = 1) AS dup_flag,
      (is_add = 0 AND lagInFrame(is_add, 1, 2) OVER w != 1) AS dangling_flag
    FROM touches
    WINDOW w AS (PARTITION BY repo_id, subject_id, provider, project_id ORDER BY occurred_at, event_id ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)
  ),
  closed AS (
    SELECT repo_id, subject_id, provider, project_id, occurred_at, event_id, is_add,
      row_number() OVER w2 AS rn,
      count() OVER (PARTITION BY repo_id, subject_id, provider, project_id) AS touch_count,
      leadInFrame(occurred_at, 1, occurred_at) OVER w2 AS next_occurred_at,
      leadInFrame(is_add, 1, is_add) OVER w2 AS next_is_add
    FROM classified
    WHERE NOT dup_flag
    WINDOW w2 AS (PARTITION BY repo_id, subject_id, provider, project_id ORDER BY occurred_at, event_id ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)
  )
  SELECT repo_id, provider, project_id, occurred_at AS observed_at,
    if(rn < touch_count AND next_is_add = 0, next_occurred_at, NULL) AS valid_to,
    0 AS is_malformed, 0 AS is_duplicate_add
  FROM closed
  WHERE is_add = 1
  UNION ALL
  SELECT repo_id, provider, project_id, occurred_at AS observed_at,
    NULL AS valid_to,
    1 AS is_malformed, 0 AS is_duplicate_add
  FROM classified
  WHERE dangling_flag
  UNION ALL
  SELECT repo_id, provider, project_id, occurred_at AS observed_at,
    NULL AS valid_to,
    0 AS is_malformed, 1 AS is_duplicate_add
  FROM classified
  WHERE dup_flag
)`

// projectRepositoriesAsOf binds projectRepositories' first hop to
// asOfWindow(timeContext) instead of reading work_items.project_id
// unconditionally, using project_membership_transitions' full touch
// history the same way devhealthsource's producer derives edge validity
// intervals (membershipTouchesAsOfSQL above).
//
// TWO ARMS, mirroring project_membership_presence's own column-arm/
// transition-arm split (ops migration 077):
//
//   - a work item WITH transition history is answered from that history:
//     admitted only if its interval overlaps the requested window --
//     `observed_at <= window_end AND (valid_to IS NULL OR valid_to >
//     window_start)`, the identical half-open overlap predicate
//     falkorgraph/temporal.go's temporalFilter.predicate applies to graph
//     reads (one rule, two read sites).
//   - a work item with NO transition history has no historical precision
//     at all -- its only signal is the plain current-value column
//     (work_items.project_id) -- so it is admitted UNCONDITIONALLY,
//     regardless of the requested window, the same "unbounded means valid
//     at every time" approximation falkorgraph.hasUnboundedValidity
//     already applies to an edge with no validity bound. Every repository
//     reached ONLY through this arm increments UnboundedValidityCount, so
//     a caller can see how much of a historical answer rests on this
//     approximation rather than a genuine as-of resolution (a repository
//     reached through BOTH arms is not counted here -- it also has a
//     genuine as-of resolution, so the approximation added nothing that
//     resolution did not already justify).
//
// TemporalDroppedCount is populated by a second, small aggregate query
// (historicalDropCount) rather than folded into this one: it answers a
// different question -- how many repositories this project's history
// reaches at SOME point but not during the requested window -- and
// answering it inline would have meant carrying an unfiltered copy of the
// interval arm through the same UNION this function already uses for a
// filtered one.
func (e *ScopeExpander) projectRepositoriesAsOf(ctx context.Context, orgID string, origins []contextfabric.SubjectRef, limit int, timeContext contextfabric.TimeContext) ([]repositoryCandidate, contextfabric.FactScopeExpansionCounts, error) {
	projectIDs := decodeProjectOriginIDs(origins)
	if len(projectIDs) == 0 {
		return nil, contextfabric.FactScopeExpansionCounts{}, nil
	}
	windowStart, windowEnd, err := asOfWindow(timeContext)
	if err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	limitPlusOne := limit + 1
	// CHAOS-4260: p.id is threaded through BOTH UNION ALL branches (each
	// already joins the SAME resolvedProjectsSQL aliased p) as
	// origin_project_id, and the outer min(origin_project_id) is an
	// aggregate ADDED alongside the existing max(via_history) -- the GROUP
	// BY key (repo_id_str, repo_slug) is unchanged, so this changes neither
	// which repositories are returned nor how many. See projectRepositories'
	// own identical comment for why "smallest id" is the tiebreak.
	statement := `SELECT repo_id_str, repo_slug, max(via_history) AS via_history, min(origin_project_id) AS origin_project_id
FROM (
  SELECT toString(iv.repo_id) AS repo_id_str, ifNull(r.repo, '') AS repo_slug, 1 AS via_history, p.id AS origin_project_id
  FROM ` + membershipTouchesAsOfSQL + ` AS iv
  INNER JOIN ` + resolvedProjectsSQL + ` AS p ON p.provider = iv.provider AND p.join_key = iv.project_id
  LEFT JOIN repos AS r FINAL ON r.id = iv.repo_id AND r.org_id = {org_id:String}
  WHERE p.key_resolution_count = 1 AND p.id IN {project_ids:Array(String)}
    AND NOT iv.is_malformed AND NOT iv.is_duplicate_add
    AND iv.observed_at <= {window_end:DateTime64(6,'UTC')}
    AND (iv.valid_to IS NULL OR iv.valid_to > {window_start:DateTime64(6,'UTC')})
  UNION ALL
  SELECT toString(w.repo_id) AS repo_id_str, ifNull(r.repo, '') AS repo_slug, 0 AS via_history, p.id AS origin_project_id
  FROM work_items AS w FINAL
  INNER JOIN ` + resolvedProjectsSQL + ` AS p ON p.provider = w.provider AND p.join_key = w.project_id
  LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
  WHERE w.org_id = {org_id:String} AND p.key_resolution_count = 1 AND p.id IN {project_ids:Array(String)}
    AND (w.repo_id, w.work_item_id) NOT IN (
      SELECT repo_id, subject_id FROM project_membership_transitions FINAL WHERE org_id = {org_id:String} AND subject_kind = 'work_item'
    )
)
GROUP BY repo_id_str, repo_slug
ORDER BY repo_id_str
LIMIT ` + strconv.Itoa(limitPlusOne)
	rows, err := e.client.Query(ctx, statement, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "project_ids", Value: projectIDs},
		{Name: "window_start", Value: windowStart},
		{Name: "window_end", Value: windowEnd},
	})
	if err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	defer rows.Close()

	originsByID := projectOriginsByRawID(origins)
	var candidates []repositoryCandidate
	counts := contextfabric.FactScopeExpansionCounts{}
	for rows.Next() {
		var repoID, repoSlug, originProjectID string
		var viaHistory uint8
		if err := rows.Scan(&repoID, &repoSlug, &viaHistory, &originProjectID); err != nil {
			return nil, contextfabric.FactScopeExpansionCounts{}, err
		}
		switch {
		case repoID == zeroRepositoryID:
			counts.MissingNextHopCount++
		case repoSlug == "":
			counts.MissingNextHopCount++
		default:
			counts.CandidateCount++
			candidates = append(candidates, repositoryCandidate{
				repoID: repoID, repoSlug: repoSlug, originRoot: originsByID[originProjectID],
			})
			if viaHistory == 0 {
				counts.UnboundedValidityCount++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	totalRows := counts.CandidateCount + counts.MissingNextHopCount
	counts.Truncated = totalRows >= limitPlusOne
	dropped, malformed, duplicateAdd, err := e.historicalDropCount(ctx, orgID, projectIDs, windowStart, windowEnd)
	if err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	counts.TemporalDroppedCount = dropped
	counts.MalformedTouchCount = malformed
	counts.DuplicateAddCount = duplicateAdd
	return candidates, counts, nil
}

// resolvedProjectsSQL is the widened project-id/project-key join arm
// CHAOS-4108 proved live, in the shape projectRepositoriesAsOf/
// historicalDropCount need (id, provider, key_resolution_count) --
// deliberately the SAME computation as projectRepositories' own inline
// join above and devhealthsource's resolvedProjectsSubquery, duplicated a
// third time rather than shared, per this file's established convention.
const resolvedProjectsSQL = `(
  SELECT provider, id, join_key, count() OVER (PARTITION BY provider, join_key) AS key_resolution_count
  FROM (
    SELECT DISTINCT provider, id, join_key FROM (
      SELECT provider, id, id AS join_key FROM projects FINAL WHERE org_id = {org_id:String}
      UNION ALL
      SELECT provider, id, ifNull(project_key, '') AS join_key FROM projects FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != ''
    )
  )
  LIMIT 1 BY provider, join_key
)`

// historicalDropCount answers two decision-basis questions in one aggregate
// query (CHAOS-4109 telemetry requirement):
//
//   - TemporalDroppedCount ("interval-miss"): how many DISTINCT
//     repositories does this project's transition history reach at SOME
//     point that the requested window then excludes. uniqExactIf keeps
//     this a single query rather than two round trips: ever_matched is the
//     interval arm with NO window predicate, window_matched is the same
//     set restricted to rows that DO overlap the window, and the
//     difference is exactly the repositories whose history-derived
//     membership existed but not during this question's window.
//   - MalformedTouchCount (codex xhigh review R1, MEDIUM, confirmed real):
//     how many touches membershipTouchesAsOfSQL flagged is_malformed (a
//     REMOVE touch with no prior ADD to close -- team-lead ruling
//     2026-08-25; see that constant's own doc comment). The main answer
//     query excludes malformed rows from
//     what it admits (`AND NOT iv.is_malformed`), and until this fix
//     nothing counted them either: a malformed touch could silently drop
//     an interval from the answer with zero telemetry signal that
//     anything was skipped, unlike devhealthsource's own producer-side
//     presenceTelemetryLedger, which has always counted this. Counted
//     here (NOT excluded) rather than in the ever_matched/window_matched
//     pair, which both explicitly exclude malformed rows via
//     `NOT is_malformed` so a malformed touch cannot inflate either.
func (e *ScopeExpander) historicalDropCount(ctx context.Context, orgID string, projectIDs []string, windowStart, windowEnd time.Time) (dropped, malformed, duplicateAdd int, err error) {
	// The repos JOIN + zero-UUID/empty-slug exclusion (codex xhigh review
	// R1, LOW, confirmed real) mirrors the main query's own MissingNextHopCount
	// classification exactly: without it, a repo-less-by-design work item
	// (the zero-UUID sentinel) or an orphaned repo_id with no matching
	// repos row counts as "ever matched" here even though the main query
	// never treats it as a real repository candidate at all -- so a
	// history-touched but never-a-real-repository row was double-counted
	// as BOTH MissingNextHopCount (main query) AND TemporalDroppedCount
	// (this one), though no repository target was actually excluded by
	// the requested window.
	//
	// ever_matched/window_matched both exclude is_duplicate_add rows too
	// (CHAOS-4109, team-lead ruling 2026-08-25), the same way they already
	// excluded is_malformed: a duplicate ADD's own row carries valid_to=
	// NULL, the same shape as a genuinely open interval, and admitting it
	// here would let a redundant touch masquerade as independent evidence
	// of a match/miss the ORIGINAL (first-add) interval already decides on
	// its own.
	statement := `SELECT uniqExactIf(repo_id_str, NOT is_malformed AND NOT is_duplicate_add) AS ever_matched,
  uniqExactIf(repo_id_str, in_window AND NOT is_malformed AND NOT is_duplicate_add) AS window_matched,
  countIf(is_malformed) AS malformed_touches,
  countIf(is_duplicate_add) AS duplicate_add_touches
FROM (
  SELECT toString(iv.repo_id) AS repo_id_str, iv.is_malformed AS is_malformed, iv.is_duplicate_add AS is_duplicate_add,
    (iv.observed_at <= {window_end:DateTime64(6,'UTC')} AND (iv.valid_to IS NULL OR iv.valid_to > {window_start:DateTime64(6,'UTC')})) AS in_window
  FROM ` + membershipTouchesAsOfSQL + ` AS iv
  INNER JOIN ` + resolvedProjectsSQL + ` AS p ON p.provider = iv.provider AND p.join_key = iv.project_id
  LEFT JOIN repos AS r FINAL ON r.id = iv.repo_id AND r.org_id = {org_id:String}
  WHERE p.key_resolution_count = 1 AND p.id IN {project_ids:Array(String)}
    AND toString(iv.repo_id) != {zero_repository_id:String} AND ifNull(r.repo, '') != ''
)`
	rows, queryErr := e.client.Query(ctx, statement, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "project_ids", Value: projectIDs},
		{Name: "window_start", Value: windowStart},
		{Name: "window_end", Value: windowEnd},
		{Name: "zero_repository_id", Value: zeroRepositoryID},
	})
	if queryErr != nil {
		return 0, 0, 0, queryErr
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, 0, 0, err
		}
		return 0, 0, 0, nil
	}
	var everMatched, windowMatched, malformedTouches, duplicateAddTouches uint64
	if err := rows.Scan(&everMatched, &windowMatched, &malformedTouches, &duplicateAddTouches); err != nil {
		return 0, 0, 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, 0, 0, err
	}
	return int(everMatched - windowMatched), int(malformedTouches), int(duplicateAddTouches), nil
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
// decodeProjectOriginIDs renders each project origin as its BARE id.
//
// Used by the two CHAOS-4099 repository chains, which bind `p.id IN
// {project_ids}`. It is deliberately NOT the decoder the work-item chain uses
// -- see decodeProjectOriginKeys, and the note there about why the difference
// was not flattened here.
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

// projectOriginKeySeparator joins a project origin's provider to its id. It is
// ':' because a canonical project id is already provider-qualified with the
// same separator upstream, so the composite reads the same way the identity
// does.
const projectOriginKeySeparator = ":"

// projectOriginKey is the one place the composite is built, so the binding
// below and the origin map above cannot drift on a separator.
func projectOriginKey(provider, id string) string {
	return provider + projectOriginKeySeparator + id
}

// decodeProjectOriginKeys renders each project origin as a PROVIDER-QUALIFIED
// key, "<provider>:<id>".
//
// The work-item chain used decodeProjectOriginIDs and dropped segments[0]
// entirely (codex r2 F4, reproduced against real ClickHouse on bigboy). The
// provider is half of a project's identity: two providers can mint the same
// project id, and a request for the LINEAR project SHARED-PROJ was answered
// with a JIRA work item whose project_id was the same string:
//
//	REPRO F4: targets=1 candidate_count=1 authorized=1
//	REPRO F4: admitted target work_item.v2:...:JIRA-1
//
// The org-wide ambiguity guard does not catch this. It excludes a join key
// that resolves to more than one PROJECT; here exactly one project carried the
// key, so key_resolution_count = 1 and the row was admitted as unambiguous. It
// was unambiguous -- and it was the wrong provider's.
//
// WHY THIS IS A SECOND DECODER rather than a fix to the shared one: the two
// repository chains (projectRepositories and its history twin) bind `p.id IN
// {project_ids}` and are CHAOS-4099 code this change does not otherwise
// touch. Changing the shared decoder would silently change their bindings
// too. They look provider-blind in the same way and that is reported
// separately; widening this fix into them without a round of its own is the
// move that turns a scoped fix into an unreviewed one.
func decodeProjectOriginKeys(origins []contextfabric.SubjectRef) []string {
	seen := make(map[string]struct{}, len(origins))
	keys := make([]string, 0, len(origins))
	for _, origin := range origins {
		segments, ok := identity.Segments(identity.KindProject, origin.CanonicalID)
		if !ok || len(segments) != 2 {
			continue
		}
		provider, id := segments[0], segments[1]
		// BOTH halves required. An origin missing its provider cannot be
		// matched provider-exactly, and admitting it under "any provider"
		// would reopen exactly this hole for the one input most likely to
		// be malformed. Dropped, and counted upstream as unresolved.
		if provider == "" || id == "" {
			continue
		}
		key := projectOriginKey(provider, id)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

// projectOriginsByKey maps a PROVIDER-QUALIFIED project key back to the origin
// that produced it, the work-item chain's twin of projectOriginsByRawID.
//
// The bare-id map cannot be reused: with two providers' projects among the
// origins, two different origins collide on one raw id and the traversal
// attributes a work item to whichever landed first.
func projectOriginsByKey(origins []contextfabric.SubjectRef) map[string]contextfabric.SubjectRef {
	byKey := make(map[string]contextfabric.SubjectRef, len(origins))
	for _, origin := range origins {
		segments, ok := identity.Segments(identity.KindProject, origin.CanonicalID)
		if !ok || len(segments) != 2 || segments[0] == "" || segments[1] == "" {
			continue
		}
		key := projectOriginKey(segments[0], segments[1])
		if _, exists := byKey[key]; !exists {
			byKey[key] = origin
		}
	}
	return byKey
}

// projectOriginsByRawID is decodeProjectOriginIDs' own companion (CHAOS-4260):
// the SAME raw projects.id decode, kept as a map back to the ORIGINAL
// SubjectRef (Label included) rather than a bare id list, so a per-repo
// origin id a query returns (projectRepositories/projectRepositoriesAsOf's
// own `min(p.id)` aggregate) can be resolved back to the exact SubjectRef the
// resolver passed in as one of origins, with no re-derivation of a canonical
// id (and no second chance to get that derivation wrong). First origin wins
// on a duplicate id, matching decodeProjectOriginIDs' own dedup.
func projectOriginsByRawID(origins []contextfabric.SubjectRef) map[string]contextfabric.SubjectRef {
	byID := make(map[string]contextfabric.SubjectRef, len(origins))
	for _, origin := range origins {
		segments, ok := identity.Segments(identity.KindProject, origin.CanonicalID)
		if !ok || len(segments) != 2 || segments[1] == "" {
			continue
		}
		if _, exists := byID[segments[1]]; !exists {
			byID[segments[1]] = origin
		}
	}
	return byID
}

// authorizeRepositories splits repository candidates by authorization and
// returns the admitted set as repository SubjectRefs (the target kind for
// FactScopePolicyProjectWorkItemRepository / FactScopePolicyTeamPrimary
// AttributionRepository), the resulting AdmittedCount/
// AuthorizationDroppedCount, and (CHAOS-4101) a per-target basis override map
// built from each admitted candidate's own repositoryCandidate.basis --
// empty for every project-origin candidate (zero-value basis), which
// fact_scope.go's expand() treats as "no override, use the rule's default".
func authorizeRepositories(principal storage.Principal, repos []repositoryCandidate) ([]contextfabric.SubjectRef, map[string]contextfabric.FactScopeBasis, map[string]contextfabric.SubjectRef, contextfabric.FactScopeExpansionCounts) {
	targets := make([]contextfabric.SubjectRef, 0, len(repos))
	var targetBasis map[string]contextfabric.FactScopeBasis
	var targetRoot map[string]contextfabric.SubjectRef
	dropped := 0
	for _, repo := range repos {
		if !authorizedForRepository(principal, repo.repoSlug) {
			dropped++
			continue
		}
		target := contextfabric.SubjectRef{
			Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repo.repoID, Label: repo.repoSlug,
		}
		targets = append(targets, target)
		if repo.basis != "" {
			if targetBasis == nil {
				targetBasis = map[string]contextfabric.FactScopeBasis{}
			}
			targetBasis[contextfabric.FactSubjectKey(target)] = repo.basis
		}
		// CHAOS-4260: targetRoot mirrors targetBasis's own "only set on a
		// real value" discipline -- repo.originRoot is the zero SubjectRef
		// when the underlying query could not resolve one (see
		// repositoryCandidate.originRoot's own doc comment).
		if repo.originRoot.CanonicalID != "" {
			if targetRoot == nil {
				targetRoot = map[string]contextfabric.SubjectRef{}
			}
			targetRoot[contextfabric.FactSubjectKey(target)] = repo.originRoot
		}
	}
	return targets, targetBasis, targetRoot, contextfabric.FactScopeExpansionCounts{AuthorizationDroppedCount: dropped}
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

// repoCandidateIndex maps repositoryCandidate.repoID -> the candidate
// itself, so a query one hop further (pull requests, reviews) can look up
// the repository a returned row belongs to and inherit its basis/source
// (CHAOS-4101) -- a PR or review has no attribution of its own; it carries
// the basis of the repository it was reached through.
func repoCandidateIndex(repos []repositoryCandidate) map[string]repositoryCandidate {
	index := make(map[string]repositoryCandidate, len(repos))
	for _, repo := range repos {
		index[repo.repoID] = repo
	}
	return index
}

// pullRequestsForRepositories reads git_pull_requests from ALREADY
// AUTHORIZED repositories only -- content from a repository this caller
// may not read is never queried, let alone returned. Mirrors
// devhealthsource/tables.go's own queryPullRequests column/canonicalization
// choices for the fields this policy's target subject needs (repo_id,
// number), and PullRequestsProvider's own canonical id convention
// ("pull_request:" + repoID + ":" + number) exactly, since the derived
// subject must round-trip through that provider's own subjectIndex.
//
// Returns a per-target basis override map alongside targets/counts
// (CHAOS-4101): each PR inherits the basis of the repository it belongs to,
// via repos' own basis field -- empty/no-op for the project policy, since
// projectRepositories never sets it.
func (e *ScopeExpander) pullRequestsForRepositories(ctx context.Context, orgID string, repos []repositoryCandidate, limit int) ([]contextfabric.SubjectRef, map[string]contextfabric.FactScopeBasis, map[string]string, map[string]contextfabric.SubjectRef, contextfabric.FactScopeExpansionCounts, error) {
	if len(repos) == 0 {
		return nil, nil, nil, nil, contextfabric.FactScopeExpansionCounts{}, nil
	}
	repoIDs := repositoryIDs(repos)
	byRepoID := repoCandidateIndex(repos)
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
		return nil, nil, nil, nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	defer rows.Close()

	var targets []contextfabric.SubjectRef
	var targetBasis map[string]contextfabric.FactScopeBasis
	var targetSource map[string]string
	var targetRoot map[string]contextfabric.SubjectRef
	for rows.Next() {
		var repoID string
		var number uint32
		if err := rows.Scan(&repoID, &number); err != nil {
			return nil, nil, nil, nil, contextfabric.FactScopeExpansionCounts{}, err
		}
		target := contextfabric.SubjectRef{
			Kind:        contractsv1.ContextFabricSubjectPullRequest,
			CanonicalID: fmt.Sprintf("pull_request:%s:%d", repoID, number),
		}
		targets = append(targets, target)
		if repo, ok := byRepoID[repoID]; ok {
			if repo.basis != "" {
				if targetBasis == nil {
					targetBasis = map[string]contextfabric.FactScopeBasis{}
				}
				targetBasis[contextfabric.FactSubjectKey(target)] = repo.basis
				if repo.attributionSource != "" {
					if targetSource == nil {
						targetSource = map[string]string{}
					}
					targetSource[contextfabric.FactSubjectKey(target)] = repo.attributionSource
				}
			}
			// CHAOS-4260: a PR inherits the ROOT of the repository it
			// belongs to, the same way it already inherits basis/source --
			// a PR has no origin of its own, it belongs to exactly one
			// repository, and that repository's own origin attribution
			// (set by projectRepositories/teamRepositories) is the correct
			// root for every PR reached through it.
			if repo.originRoot.CanonicalID != "" {
				if targetRoot == nil {
					targetRoot = map[string]contextfabric.SubjectRef{}
				}
				targetRoot[contextfabric.FactSubjectKey(target)] = repo.originRoot
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	// AttributionSourceCounts is NOT built here (codex xhigh review round 1,
	// confirmed real, MEDIUM): this loop sees every row up to limitPlusOne,
	// including the overflow row the resolver uses to detect truncation, so
	// a count built from it would include a target that is never actually
	// admitted. TargetAttributionSource above carries the per-target source
	// instead; the resolver derives the count from `kept`, its own
	// requirement-level admitted set, in the SAME place it derives
	// TargetBasis's own mix summary.
	counts := contextfabric.FactScopeExpansionCounts{CandidateCount: len(targets)}
	// NOT trimmed to limit (codex xhigh review round 1, confirmed real,
	// MEDIUM) -- see projectRepositories' own identical comment. Returning
	// all limit+1 targets lets the resolver's own overflow-row detection
	// independently confirm truncation rather than trusting this flag
	// alone.
	if len(targets) >= limitPlusOne {
		counts.Truncated = true
	}
	return targets, targetBasis, targetSource, targetRoot, counts, nil
}

// pullRequestReviewsForRepositories is pullRequestsForRepositories' own
// twin, one hop further: git_pull_request_reviews INNER JOINed to
// git_pull_requests so a review can only ever be reached through a real
// pull request in an ALREADY AUTHORIZED repository. Canonical id via
// identity.Derive(identity.KindPullRequestReview, ...) -- the EXACT segment
// order (repo_id, number, review_id) devhealthsource/tables.go's
// queryPullRequestReviews already mints, so the derived subject decodes
// through ReviewsProvider's own v2Index unchanged.
// Returns a per-target basis override map alongside targets/counts
// (CHAOS-4101), the same way pullRequestsForRepositories does: each review
// inherits the basis of the repository its pull request belongs to.
func (e *ScopeExpander) pullRequestReviewsForRepositories(ctx context.Context, orgID string, repos []repositoryCandidate, limit int) ([]contextfabric.SubjectRef, map[string]contextfabric.FactScopeBasis, map[string]string, map[string]contextfabric.SubjectRef, contextfabric.FactScopeExpansionCounts, error) {
	if len(repos) == 0 {
		return nil, nil, nil, nil, contextfabric.FactScopeExpansionCounts{}, nil
	}
	repoIDs := repositoryIDs(repos)
	byRepoID := repoCandidateIndex(repos)
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
		return nil, nil, nil, nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	defer rows.Close()

	var targets []contextfabric.SubjectRef
	var targetBasis map[string]contextfabric.FactScopeBasis
	var targetSource map[string]string
	var targetRoot map[string]contextfabric.SubjectRef
	var missingNextHop int
	for rows.Next() {
		var reviewID, repoID string
		var number uint32
		if err := rows.Scan(&reviewID, &repoID, &number); err != nil {
			return nil, nil, nil, nil, contextfabric.FactScopeExpansionCounts{}, err
		}
		canonicalID, omitted, err := identity.Derive(identity.KindPullRequestReview, []string{repoID, strconv.FormatUint(uint64(number), 10), reviewID}, nil)
		if err != nil {
			return nil, nil, nil, nil, contextfabric.FactScopeExpansionCounts{}, err
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
		target := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequestReview, CanonicalID: canonicalID}
		targets = append(targets, target)
		if repo, ok := byRepoID[repoID]; ok {
			if repo.basis != "" {
				if targetBasis == nil {
					targetBasis = map[string]contextfabric.FactScopeBasis{}
				}
				targetBasis[contextfabric.FactSubjectKey(target)] = repo.basis
				if repo.attributionSource != "" {
					if targetSource == nil {
						targetSource = map[string]string{}
					}
					targetSource[contextfabric.FactSubjectKey(target)] = repo.attributionSource
				}
			}
			// CHAOS-4260: a review inherits the ROOT of the repository its
			// pull request belongs to -- see pullRequestsForRepositories'
			// own identical comment.
			if repo.originRoot.CanonicalID != "" {
				if targetRoot == nil {
					targetRoot = map[string]contextfabric.SubjectRef{}
				}
				targetRoot[contextfabric.FactSubjectKey(target)] = repo.originRoot
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	// AttributionSourceCounts is NOT built here -- see
	// pullRequestsForRepositories' own identical comment (codex xhigh
	// review round 1, confirmed real, MEDIUM); TargetAttributionSource
	// carries the per-target source and the resolver derives the count
	// from its own admitted set.
	counts := contextfabric.FactScopeExpansionCounts{CandidateCount: len(targets) + missingNextHop, MissingNextHopCount: missingNextHop}
	// NOT trimmed to limit (codex xhigh review round 1, confirmed real,
	// MEDIUM) -- see projectRepositories' own identical comment.
	if len(targets)+missingNextHop >= limitPlusOne {
		counts.Truncated = true
	}
	return targets, targetBasis, targetSource, targetRoot, counts, nil
}

func repositoryIDs(repos []repositoryCandidate) []string {
	ids := make([]string, len(repos))
	for i, repo := range repos {
		ids[i] = repo.repoID
	}
	sort.Strings(ids)
	return ids
}

// repositoryAttributionSources builds pullRequestsForRepositories' own
// TargetAttributionSource map for the direct team_primary_attribution_repository_v1
// policy: for each authorized target, the attributionSource of the
// repositoryCandidate it came from, keyed by FactSubjectKey(target). This
// is deliberately per-target, not a pre-aggregated count (codex xhigh
// review round 1, confirmed real, MEDIUM x2 -- see fact_scope.go's own
// comment on TargetAttributionSource): the resolver derives
// AttributionSourceCounts from `kept`, its OWN requirement-level cap-and-
// dedup-applied admitted set, not from this policy's authorized-but-not-yet
// cap-checked target list. An authorization-dropped repository's source
// never reaches this map at all, matching AuthorizationDroppedCount's own
// discipline.
func repositoryAttributionSources(repos []repositoryCandidate, targets []contextfabric.SubjectRef) map[string]string {
	byRepoID := repoCandidateIndex(repos)
	var source map[string]string
	for _, target := range targets {
		repoID := strings.TrimPrefix(target.CanonicalID, "repository:")
		repo, ok := byRepoID[repoID]
		if !ok || repo.attributionSource == "" {
			continue
		}
		if source == nil {
			source = map[string]string{}
		}
		source[contextfabric.FactSubjectKey(target)] = repo.attributionSource
	}
	return source
}

// teamAttributionSourceRank mirrors ops' own precedence order
// (ops/src/dev_health_ops/metrics/compute_work_items.py's _SOURCE_ORDER)
// for the ONE purpose this expander needs it: when several work items with
// DIFFERENT attribution sources reach the SAME repository, which source
// determines that repository's basis. Lower ranks win. unassigned never
// reaches this query (queryWorkItemTeams' own WHERE clause excludes empty
// team_id, and this query's WHERE a.team_id IN {team_ids} does identically),
// so it is not represented here; an unrecognised value ranks last (7),
// failing toward the WEAKER classification (FactScopeBasisAttributedPrimaryTeam,
// never the stronger activity_proxy) for a future enum addition this
// producer has not been updated for.
const teamAttributionSourceRankSQL = `multiIf(` +
	`a.source = 'native_team', 0, ` +
	`a.source = 'issue_project', 1, ` +
	`a.source = 'project_ownership', 2, ` +
	`a.source = 'repo_ownership', 3, ` +
	`a.source = 'assignee_membership', 4, ` +
	`a.source = 'linked_issue', 5, ` +
	`a.source = 'manual_fallback', 6, ` +
	`7)`

// teamRepositories runs CHAOS-4101's activity-proxy chain: team ->
// work_item (primary attribution only) -> repository, scoped to the
// SPECIFIC team origins requested. Mirrors projectRepositories' own
// zero-UUID/orphan handling exactly (repos.go).
//
// ONE ROW PER DISTINCT REPOSITORY (GROUP BY), matching projectRepositories'
// own "row count == candidate count" contract that the Limit+1 overflow
// detection (FactScopeExpansionRequest.Limit's own doc comment) relies on --
// a repository reached by several work items with different sources must
// not multiply the candidate count merely because it has several
// contributing attributions. best_source is the WINNING source among all of
// a repository's contributing primary attributions, by teamAttributionSourceRank
// (argMin picks the source at the minimum rank): native_team if ANY
// contributing work item carries it, otherwise the highest-precedence
// heuristic source present. That winning source decides the repository's
// basis (FactScopeBasisActivityProxy for native_team -- the SAME class the
// project chain uses, since reaching a repo via a work item is activity
// either way; FactScopeBasisAttributedPrimaryTeam for every other value,
// since the team-attribution hop itself is then also an inference) and is
// carried onto repositoryCandidate.attributionSource for the telemetry
// breakdown.
func (e *ScopeExpander) teamRepositories(ctx context.Context, orgID string, origins []contextfabric.SubjectRef, limit int) ([]repositoryCandidate, contextfabric.FactScopeExpansionCounts, error) {
	teamIDs := decodeTeamOriginIDs(origins)
	if len(teamIDs) == 0 {
		// Every origin failed to decode as a team.v1 id -- an invariant
		// violation upstream (the resolver only ever calls a team policy for
		// a SubjectTeam origin), not a data statement about THIS team.
		// Reported as a genuinely empty traversal: no candidate was ever
		// queried for.
		return nil, contextfabric.FactScopeExpansionCounts{}, nil
	}
	limitPlusOne := limit + 1
	// The join carries repo_id, not just work_item_id (codex xhigh review
	// round 2, confirmed real, MEDIUM): work_items' declared sort key is
	// (org_id, repo_id, work_item_id) -- devhealthschema.go's
	// "ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id,
	// work_item_id)", and devhealthsource's own census registry documents
	// work_item_id as NOT the table's natural key on its own: a bare
	// work_item_id is cross-repo-collidable. work_item_team_attributions
	// carries its OWN repo_id column (insert_work_item_team_attributions,
	// ops/storage/clickhouse.py), written from the SAME work item the
	// attribution was computed for, so joining on work_item_id alone could
	// match a work_items row from a DIFFERENT repository that happens to
	// reuse the same bare id -- attributing the wrong repository to the
	// team, or (worse) attributing an unauthorized repository the team was
	// never connected to.
	// CHAOS-4260: min(a.team_id) is an aggregate ADDED alongside the existing
	// argMin(source) -- the GROUP BY key (repo_id, repo_slug) is unchanged,
	// so this changes neither which repositories are returned nor how many.
	// See projectRepositories' own identical comment for why "smallest id"
	// is the tiebreak among several team origins reaching the same repo.
	statement := `SELECT toString(w.repo_id), ifNull(r.repo, ''), argMin(toString(a.source), ` + teamAttributionSourceRankSQL + `) AS best_source, min(a.team_id) AS origin_team_id
FROM work_item_team_attributions AS a FINAL
INNER JOIN (SELECT work_item_id, repo_id, org_id FROM work_items FINAL WHERE org_id = {org_id:String}) AS w ON w.work_item_id = a.work_item_id AND w.repo_id = a.repo_id AND w.org_id = a.org_id
LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
WHERE a.org_id = {org_id:String} AND a.is_primary = 1 AND a.team_id IN {team_ids:Array(String)}
GROUP BY toString(w.repo_id), ifNull(r.repo, '')
ORDER BY toString(w.repo_id)
LIMIT ` + strconv.Itoa(limitPlusOne)
	rows, err := e.client.Query(ctx, statement, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "team_ids", Value: teamIDs},
	})
	if err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	defer rows.Close()

	originsByID := teamOriginsByRawID(origins)
	var candidates []repositoryCandidate
	counts := contextfabric.FactScopeExpansionCounts{}
	for rows.Next() {
		var repoID, repoSlug, bestSource, originTeamID string
		if err := rows.Scan(&repoID, &repoSlug, &bestSource, &originTeamID); err != nil {
			return nil, contextfabric.FactScopeExpansionCounts{}, err
		}
		switch {
		case repoID == zeroRepositoryID:
			// Repo-less by design (a Linear-sourced work item) -- never a
			// fake repository target.
			counts.MissingNextHopCount++
		case repoSlug == "":
			// A non-zero repo_id that matched no repos row: an orphan, same
			// treatment as the zero-UUID sentinel for THIS purpose -- there
			// is no real repository entity to admit.
			counts.MissingNextHopCount++
		default:
			counts.CandidateCount++
			basis := contextfabric.FactScopeBasisAttributedPrimaryTeam
			if bestSource == attributionSourceNativeTeamForScope {
				basis = contextfabric.FactScopeBasisActivityProxy
			}
			candidates = append(candidates, repositoryCandidate{
				repoID: repoID, repoSlug: repoSlug, basis: basis, attributionSource: bestSource,
				originRoot: originsByID[originTeamID],
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	totalRows := counts.CandidateCount + counts.MissingNextHopCount
	counts.Truncated = totalRows >= limitPlusOne
	// NOT trimmed to limit here -- see projectRepositories' own identical
	// comment; the resolver's own overflow-row detection needs the extra
	// row intact.
	return candidates, counts, nil
}

// attributionSourceNativeTeamForScope mirrors devhealthsource's
// attributionSourceNativeTeam constant (teams_projects_edges.go). Duplicated
// rather than imported: devhealthfacts already depends on devhealthsource
// for nothing else, and importing a whole projection-source package for one
// string constant would be a heavier coupling than restating a value this
// stable. Both spots are doc-linked to each other.
const attributionSourceNativeTeamForScope = "native_team"

// teamCanonicalIDPrefix mirrors devhealthsource's own teamCanonicalID
// producer (teams_projects.go: `"team:" + teamID`) -- the exact inverse,
// duplicated for the same reason attributionSourceNativeTeamForScope is:
// a one-line format this package cannot import without a heavier coupling.
const teamCanonicalIDPrefix = "team:"

// decodeTeamOriginIDs recovers the raw teams.id value for every team-kind
// origin subject. UNLIKE decodeProjectOriginIDs, a team's canonical id is a
// plain string-concatenation ("team:" + id, teams_projects.go's
// teamCanonicalID), never routed through identity.Derive/identity.Segments,
// so decoding is a prefix strip rather than a segment split. A subject whose
// CanonicalID does not carry the prefix is skipped, not errored -- the
// resolver only ever calls this expander for subjects it already resolved
// as SubjectTeam, so a decode failure here is unreachable in practice.
func decodeTeamOriginIDs(origins []contextfabric.SubjectRef) []string {
	seen := make(map[string]struct{}, len(origins))
	ids := make([]string, 0, len(origins))
	for _, origin := range origins {
		id, ok := strings.CutPrefix(origin.CanonicalID, teamCanonicalIDPrefix)
		if !ok || id == "" {
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

// teamOriginsByRawID is decodeTeamOriginIDs' own companion, the exact team-
// side twin of projectOriginsByRawID (CHAOS-4260) -- see that function's own
// doc comment.
func teamOriginsByRawID(origins []contextfabric.SubjectRef) map[string]contextfabric.SubjectRef {
	byID := make(map[string]contextfabric.SubjectRef, len(origins))
	for _, origin := range origins {
		id, ok := strings.CutPrefix(origin.CanonicalID, teamCanonicalIDPrefix)
		if !ok || id == "" {
			continue
		}
		if _, exists := byID[id]; !exists {
			byID[id] = origin
		}
	}
	return byID
}

// ---------------------------------------------------------------------------
// CHAOS-5405: the work-item traversal
// ---------------------------------------------------------------------------

// workItemCandidate is one DISTINCT work item the project->work_item or
// team->work_item chain reached, BEFORE Go-side authorization.
//
// It differs from repositoryCandidate in the one way that matters: a
// zero-UUID repo_id is a FIRST-CLASS target here, not a missing next hop.
// The repository chain has to refuse it because there is no repository entity
// to admit; this chain stops AT the work item, and a Linear-sourced item that
// is repo-less by design is a perfectly real work item. Which principals may
// see it is an authorization question (D-c), not an existence one.
type workItemCandidate struct {
	repoID     string
	repoSlug   string
	workItemID string
	// authorizationSlug is what the principal is actually checked against:
	// the real repo slug, or one of the two sentinels for a repo-less or
	// orphaned row. It mirrors devhealthsource's workItemAuthorization
	// exactly -- see workItemAuthorizationSlug.
	authorizationSlug string
	repoLess          bool
	orphaned          bool
	basis             contextfabric.FactScopeBasis
	attributionSource string
	originRoot        contextfabric.SubjectRef
}

// The two sentinels devhealthsource/clickhouse.go's workItemAuthorization
// builds for a work item whose repository did not resolve. Duplicated rather
// than imported for the same reason attributionSourceNativeTeamForScope is:
// devhealthfacts does not otherwise depend on devhealthsource, and importing
// a whole projection-source package for two string constants would be a
// heavier coupling than restating values this stable. Both spots are
// doc-linked to each other.
//
// The split is load-bearing and must NOT be collapsed: zeroRepositoryID means
// "repo-less BY DESIGN" (every Linear-sourced row), while a nonzero repo_id
// that matches no repos row means "named a repository that did not resolve".
// Both are invisible to a repository-restricted principal and visible to an
// organization-wide one, but they are different facts about the source and
// D-e reports them as separate counts.
const (
	noRepositorySentinelForScope       = "acr-context-fabric:no-repository"
	orphanedRepositorySentinelForScope = "acr-context-fabric:orphaned-repository"
)

// workItemAuthorizationSlug mirrors devhealthsource/clickhouse.go's
// workItemAuthorization: the resolved slug when there is one, else the
// sentinel that says WHY there is not.
func workItemAuthorizationSlug(repoID, repoSlug string) string {
	if repoSlug != "" {
		return repoSlug
	}
	if repoID == zeroRepositoryID {
		return noRepositorySentinelForScope
	}
	return orphanedRepositorySentinelForScope
}

// workItemScopeSelectionColumns documents the TWELVE columns every work-item
// selection statement returns, in order, because the scanner depends on that
// exact shape and a silent drift between the two is a mis-scan, not a compile
// error:
//
//	repo_id, work_item_id, repo_slug, origin_id, attribution_source,
//	authorized, repo_less,
//	scoped_population, authorized_population, repo_less_population,
//	repo_less_denied, orphaned_population
//
// The first five are MASKED to ” on an unauthorized row (see
// workItemAuthorizationExprSQL). The last FIVE are WINDOW aggregates over the
// same relation, so the census and the page can never come from two different
// observations -- D-a's "a count and page obtained from unrelated
// observations cannot constitute a complete census", made mechanical.
const workItemScopeSelectionColumns = "repo_id, work_item_id, repo_slug, origin_id, attribution_source, authorized, repo_less, scoped_population, authorized_population, repo_less_population, repo_less_denied, orphaned_population"

// authorizedRepositoryScopesFor renders the principal's repository scope for
// binding INTO the selection relation (D-c step 5: authorization is applied
// before content projection, not as a Go filter afterwards).
//
// IT CARRIES graphrank.ScopeMatch'S RULE; IT DOES NOT RE-DERIVE ONE. The first
// version bound the scope strings VERBATIM against an exact `IN` match, which
// silently disagreed with the layer that owns repository authorization for
// every wildcard scope: a principal scoped `*` is UNRESTRICTED there, and here
// matched nothing, so every row was masked and the caller was served an empty
// answer with no error and no disclosure. Found by codex r1 (graded P2),
// reproduced in both arms before this fix, and pinned as a cross-layer
// agreement test in both directions.
//
// The mapping, one line per rule in ScopeMatch/auth.RepositoryAllowed:
//
//   - no scopes at all -> unrestricted. Both lists empty.
//   - any `*` scope -> unrestricted, whatever else is present. `*` admits
//     unconditionally there, so a list carrying it can only be widened by its
//     other members, never narrowed.
//   - `owner/*` -> the OWNER, lowercased, into the owners list. The SQL
//     compares it against the owner segment of the row's own slug.
//   - anything else -> an exact slug, trimmed and LOWERCASED, into the slugs
//     list. ScopeMatch's exact arm normalises a repository slug on both sides
//     (auth.NormalizeRepositorySlug lowercases), so this is what makes the two
//     gates agree on a slug that differs only in case.
//
// The two sentinel populations (repo-less, orphaned) carry no slug and so
// authorize ONLY under the unrestricted arm -- unchanged, and the reason the
// predicate cannot simply drop its empty-list special case.
func authorizedRepositoryScopesFor(principal storage.Principal) (slugs []string, owners []string) {
	for _, raw := range principal.RepositoryScopes {
		// TRIMMED AND LOWERCASED, both sides (chris, 2026-09-09: "Case
		// insensitive is the only way to search"). ScopeMatch's exact arm now
		// normalises a repository slug on both sides through
		// auth.NormalizeRepositorySlug, which lowercases -- so lowercasing
		// here is what makes this predicate agree with it rather than
		// disagree.
		//
		// The order this arrived in is worth keeping: the first version
		// lowercased, the agreement test caught it disagreeing with a
		// then-case-SENSITIVE ScopeMatch, and the resolution was that
		// ScopeMatch was the side that was wrong. The test did its job twice
		// -- once against the predicate, once against the rule.
		scope := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case scope == "":
			// An empty entry is not a scope. Dropping it rather than binding
			// it keeps a malformed list from accidentally matching a row
			// whose own slug failed to resolve.
			continue
		case scope == "*":
			return nil, nil
		default:
			if owner, ok := strings.CutSuffix(scope, "/*"); ok && owner != "" {
				// Already lowercased with the whole scope above; ScopeMatch
				// lowercases the scope's owner and compares it against the
				// owner of a NORMALIZED entry, so this arm is
				// case-insensitive on both sides too.
				owners = append(owners, owner)
				continue
			}
			slugs = append(slugs, scope)
		}
	}
	return slugs, owners
}

// projectWorkItems runs the ONE-HOP project chain: project -> work_item,
// stopping at the work item rather than continuing to its repository.
//
// ONE STATEMENT, and everything the ruling pushes down is in it: the census
// (a window aggregate), authorization (bound into the relation), dedup
// (DISTINCT on the composite identity) and the bounded selection (LIMIT
// limit+1). Nothing here reads a per-item row in a loop, and the complete ID
// array of the scoped population is never returned to Go or built here --
// only the count of it crosses the boundary.
//
// The join-key ambiguity guard is computed over the WHOLE ORG and only
// narrowed to the requested project ids in the OUTER WHERE, byte-for-byte the
// same discipline projectRepositories uses -- see its own long comment for
// why an org-scoped guard is the only one that catches a DIFFERENT project
// whose id collides with this project's project_key. An ambiguous row is
// EXCLUDED and counted, never guessed into a scope.
func (e *ScopeExpander) projectWorkItems(ctx context.Context, principal storage.Principal, orgID string, origins []contextfabric.SubjectRef, limit int) ([]workItemCandidate, contextfabric.FactScopeExpansionCounts, error) {
	projectKeys := decodeProjectOriginKeys(origins)
	if len(projectKeys) == 0 {
		// Every origin failed to decode -- an upstream invariant violation,
		// not a data statement about this project. Reported as a genuinely
		// empty traversal that queried for nothing, and named as such so the
		// decision record can say origin_unresolved rather than "empty".
		return nil, contextfabric.FactScopeExpansionCounts{AmbiguousOriginCount: len(origins)}, nil
	}
	statement := projectWorkItemSelectionSQL(limit)
	return e.scanWorkItemCandidates(ctx, statement, orgID, principal, []contextpacket.ClickHouseBinding{
		{Name: "project_ids", Value: projectKeys},
	}, projectOriginsByKey(origins), limit, false)
}

// teamWorkItems is projectWorkItems' team twin, one hop earlier on the same
// shape: team -OWNED_BY_TEAM<- work_item.
//
// The join carries repo_id as well as work_item_id, for exactly the reason
// teamRepositories' own comment gives at length: work_items' sort key is
// (org_id, repo_id, work_item_id), so a bare work_item_id is
// cross-repo-collidable and joining on it alone can attribute a DIFFERENT
// repository's work item to this team.
//
// PER-TARGET BASIS, and it is the OPPOSITE of the repository chain's. There,
// a native_team-sourced row still only earns activity_proxy, because reaching
// a REPOSITORY through a work item is activity regardless of how solid the
// attribution is. Here the traversal STOPS at the work item, so a
// provider-asserted native_team attribution is a direct edge about that very
// item, and only a computed source layers an inference on top.
func (e *ScopeExpander) teamWorkItems(ctx context.Context, principal storage.Principal, orgID string, origins []contextfabric.SubjectRef, limit int) ([]workItemCandidate, contextfabric.FactScopeExpansionCounts, error) {
	teamIDs := decodeTeamOriginIDs(origins)
	if len(teamIDs) == 0 {
		return nil, contextfabric.FactScopeExpansionCounts{AmbiguousOriginCount: len(origins)}, nil
	}
	statement := workItemScopeProjection(`  SELECT toString(w.repo_id) AS repo_id, w.work_item_id AS work_item_id, ifNull(r.repo, '') AS repo_slug, min(a.team_id) AS origin_id, argMin(toString(a.source), `+teamAttributionSourceRankSQL+`) AS attribution_source,
    `+workItemAuthorizationExprSQL+` AS authorized,
    toUInt8(toString(w.repo_id) = '`+zeroRepositoryID+`') AS repo_less,
    toUInt8(toString(w.repo_id) != '`+zeroRepositoryID+`' AND ifNull(r.repo, '') = '') AS orphaned
FROM work_item_team_attributions AS a FINAL
INNER JOIN (SELECT work_item_id, repo_id, org_id FROM work_items FINAL WHERE org_id = {org_id:String}) AS w ON w.work_item_id = a.work_item_id AND w.repo_id = a.repo_id AND w.org_id = a.org_id
LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
WHERE a.org_id = {org_id:String} AND a.is_primary = 1 AND a.team_id IN {team_ids:Array(String)}
GROUP BY toString(w.repo_id), w.work_item_id, ifNull(r.repo, '')`, limit)
	return e.scanWorkItemCandidates(ctx, statement, orgID, principal, []contextpacket.ClickHouseBinding{
		{Name: "team_ids", Value: teamIDs},
	}, teamOriginsByRawID(origins), limit, true)
}

// workItemAuthorizationExprSQL is D-c step 5 in SQL -- and it is a PROJECTION
// MASK, never a row filter. That distinction is the whole design.
//
// The obvious reading of "apply target authorization inside the selection
// relation before content projection" is a WHERE clause. That was implemented
// first and it is WRONG, in two ways that only showed up under the D-e
// counters:
//
//  1. A filtered-out row is invisible to Go, so AuthorizationDroppedCount
//     reads 0 for a principal that demonstrably had rows denied -- exactly
//     the "the filter dropped nothing" versus "nobody ever counted"
//     ambiguity D-e exists to remove, reintroduced by the push-down D-c
//     asked for.
//  2. WHERE is applied BEFORE window functions, so filtering also removes
//     the denied rows from the census aggregate. In the all-denied case NO
//     rows come back at all, the census never reaches Go, and
//     matched_unauthorized loses the population count that is the entire
//     point of D-c's widened count-only existence exception.
//
// So the row STAYS in the relation and every identifying column is masked
// instead. An unauthorized work item's id, slug, origin and attribution
// source never cross into Go -- only the fact that one more row existed. That
// is count-only disclosure and honest telemetry at the same time, rather than
// trading one for the other.
//
// An EMPTY bound array means organization-wide, matching
// graphrank.AuthorizedAttributes' own rule that a principal naming no
// repositories is unrestricted. The two sentinel populations have no slug to
// compare, so they authorize only under that empty-array arm -- which is
// precisely the ruled behaviour: neither an authorized project nor an
// authorized team upgrades a repository-restricted principal.
const workItemAuthorizationExprSQL = `toUInt8(
    (empty({authorized_repository_slugs:Array(String)}) AND empty({authorized_repository_owners:Array(String)}))
    OR (ifNull(r.repo, '') != '' AND lower(ifNull(r.repo, '')) IN {authorized_repository_slugs:Array(String)})
    OR (ifNull(r.repo, '') != '' AND lower(splitByChar('/', ifNull(r.repo, ''))[1]) IN {authorized_repository_owners:Array(String)})
  )`

// workItemScopeProjection wraps one inner selection in the masking outer
// projection plus the three window aggregates the census needs.
//
// ORDER BY authorized DESC comes first so the bounded page is filled with
// ADMISSIBLE rows rather than spent on masked ones -- the counts come from
// the window aggregates, which are computed over the whole relation before
// LIMIT, so ordering cannot cost the census anything.
func workItemScopeProjection(inner string, limit int) string {
	return `SELECT
  if(authorized = 1, repo_id, '') AS repo_id,
  if(authorized = 1, work_item_id, '') AS work_item_id,
  if(authorized = 1, repo_slug, '') AS repo_slug,
  if(authorized = 1, origin_id, '') AS origin_id,
  if(authorized = 1, attribution_source, '') AS attribution_source,
  authorized,
  repo_less,
  count() OVER () AS scoped_population,
  countIf(authorized = 1) OVER () AS authorized_population,
  -- THE REPO-LESS CANDIDATE POPULATION, authorized or not, over the WHOLE
  -- relation. Go used to derive this by counting repo_less rows as it read
  -- them, which counted only the rows that fit inside LIMIT limit+1 (codex r2
  -- F2, reproduced before this fix):
  --
  --   candidate=2 repo_less_candidate=0 repo_less_denied=1 auth_dropped=1
  --
  -- CandidateCount comes from scoped_population, a whole-relation aggregate,
  -- so a page-bounded subset compared against it is not a subset of anything
  -- -- the served census contradicted itself. Every other count on this
  -- record is already an aggregate for exactly that reason. This one was the
  -- odd one out.
  countIf(repo_less = 1) OVER () AS repo_less_population,
  countIf(repo_less = 1 AND authorized = 0) OVER () AS repo_less_denied,
  countIf(orphaned = 1) OVER () AS orphaned_population
FROM (
` + inner + `
)
ORDER BY authorized DESC, repo_id, work_item_id
LIMIT ` + strconv.Itoa(limit+1)
}

// scanWorkItemCandidates executes ONE selection statement and folds it into
// candidates plus the D-e counts.
//
// It is deliberately the ONLY place a work-item selection row is read, so the
// project and team arms cannot drift apart on the two things most likely to
// diverge: which populations count as what, and the second authorization gate.
//
// teamOrigin selects the per-target basis rule. See teamWorkItems' doc comment
// for why the team work-item arm resolves native_team to `direct` while the
// team REPOSITORY arm resolves the same source to activity_proxy.
func (e *ScopeExpander) scanWorkItemCandidates(
	ctx context.Context,
	statement string,
	orgID string,
	principal storage.Principal,
	extraBindings []contextpacket.ClickHouseBinding,
	originsByID map[string]contextfabric.SubjectRef,
	limit int,
	teamOrigin bool,
) ([]workItemCandidate, contextfabric.FactScopeExpansionCounts, error) {
	authorizedSlugs, authorizedOwners := authorizedRepositoryScopesFor(principal)
	bindings := append([]contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "authorized_repository_slugs", Value: authorizedSlugs},
		{Name: "authorized_repository_owners", Value: authorizedOwners},
	}, extraBindings...)

	counts := contextfabric.FactScopeExpansionCounts{}
	rows, err := e.client.Query(ctx, statement, bindings)
	if err != nil {
		return nil, counts, err
	}
	defer rows.Close()

	var candidates []workItemCandidate
	returned := 0
	for rows.Next() {
		var repoID, workItemID, repoSlug, originID, attributionSource string
		var authorized, repoLess uint8
		var scopedPopulation, authorizedPopulation, repoLessPopulation, repoLessDenied, orphanedPopulation uint64
		if err := rows.Scan(&repoID, &workItemID, &repoSlug, &originID, &attributionSource,
			&authorized, &repoLess, &scopedPopulation, &authorizedPopulation, &repoLessPopulation, &repoLessDenied, &orphanedPopulation); err != nil {
			return nil, contextfabric.FactScopeExpansionCounts{}, err
		}
		returned++
		// The census rides on EVERY row as a window aggregate over the whole
		// relation, so it is identical on each. Read per row rather than only
		// from the first, so a future change to row order cannot silently
		// change which row the census is taken from.
		//
		// CensusComplete is NOT set here -- see below the loop. It is a
		// property of the QUERY having succeeded, not of any row existing.
		counts.AuthorizedCount = int(authorizedPopulation)
		// FROM THE AGGREGATE, never from counting repo_less rows on this page
		// (codex r2 F2). See repo_less_population in workItemScopeProjection.
		counts.RepoLessCandidateCount = int(repoLessPopulation)
		counts.RepoLessAuthorizationDroppedCount = int(repoLessDenied)
		counts.OrphanedRepositoryCount = int(orphanedPopulation)
		// AuthorizationDroppedCount comes from the AGGREGATES, never from
		// counting masked rows on this page: the projection orders admissible
		// rows first, so a denied row may not be on the page at all, and the
		// caller is owed the count of every denial rather than of the ones
		// that happened to fit.
		counts.AuthorizationDroppedCount = int(scopedPopulation) - int(authorizedPopulation)
		// CandidateCount is the population BEFORE any filtering -- its
		// documented meaning on FactScopeExpansionCounts, and what the
		// repository hop already reports (it counts a candidate before
		// authorization runs). Taking it from the census rather than from
		// len(candidates) is what keeps the all-denied case honest: with a
		// post-authorization count, a fully denied traversal would report
		// ZERO candidates and become indistinguishable from one that found
		// nothing at all -- attempted_empty wearing matched_unauthorized's
		// clothes, which is a proof-of-absence the caller never earned.
		counts.CandidateCount = int(scopedPopulation)

		if authorized != 1 {
			// A masked row carries no identity at all -- only the fact that
			// one more row existed, which the aggregates above already
			// recorded. This is D-c's count-only disclosure in practice.
			continue
		}

		candidate := workItemCandidate{
			repoID:            repoID,
			repoSlug:          repoSlug,
			workItemID:        workItemID,
			authorizationSlug: workItemAuthorizationSlug(repoID, repoSlug),
			repoLess:          repoLess == 1,
			orphaned:          repoID != zeroRepositoryID && repoSlug == "",
			attributionSource: attributionSource,
			originRoot:        originsByID[originID],
		}
		if teamOrigin {
			// A closed vocabulary, and an unrecognised source is a REFUSAL,
			// never an `else => computed` admission: the point of the
			// per-target basis is that a reader can tell asserted rows from
			// inferred ones, and admitting an unknown source under either
			// label destroys that.
			switch {
			case attributionSource == attributionSourceNativeTeamForScope:
				candidate.basis = contextfabric.FactScopeBasisDirect
			case knownComputedAttributionSource(attributionSource):
				candidate.basis = contextfabric.FactScopeBasisAttributedPrimaryTeam
			default:
				counts.UnknownAttributionSourceCount++
				continue
			}
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	// THE CENSUS COMPLETED BECAUSE THE QUERY DID, not because a row came
	// back (codex r1 P1, reproduced before it was fixed: an empty successful
	// selection reported census_complete=false, so a MEASURED ZERO was served
	// as an UNMEASURED population -- the one distinction D-d exists to carry,
	// reported backwards in the commonest empty case).
	//
	// Set here rather than in the loop, and only after rows.Err() has been
	// checked: a scan or iteration failure returns above with a zero-valued
	// counts, so reaching this line means the whole relation was read. A
	// relation with no rows has a scoped population of zero and an authorized
	// population of zero, and both are MEASUREMENTS -- the aggregates are
	// absent because there was nothing to aggregate, not because nobody
	// counted.
	counts.CensusComplete = true
	counts.ScopeQueryCount = 1
	counts.ScopeRowsReturned = returned
	// TRUNCATION IS ABOUT THE AUTHORIZED POPULATION, not the returned rows.
	// A masked row occupies no admission slot, so a page padded with them is
	// not evidence that anything admissible was left behind; conversely a
	// caller whose authorized population exceeds the cap IS owed the signal
	// even when the page is short. The census knows this; the row count does
	// not.
	counts.Truncated = counts.AuthorizedCount > limit
	return candidates, counts, nil
}

// knownComputedAttributionSource is the closed computed-source allow-list the
// ruling names: the sources teamAttributionSourceRankSQL already ranks, minus
// native_team, which is classified separately as asserted.
func knownComputedAttributionSource(source string) bool {
	switch source {
	case "issue_project", "project_ownership", "repo_ownership",
		"assignee_membership", "linked_issue", "manual_fallback":
		return true
	}
	return false
}

// authorizeWorkItems is the SECOND authorization gate (D-c step 5's
// "defensively check returned authorization metadata before admitting
// SubjectRefs"). The projection already masked unauthorized rows; this
// re-checks what came back, so a statement edited wrongly cannot leak a
// target on the strength of one gate alone.
//
// It re-uses the SAME primitive the repository hop uses --
// graphrank.AuthorizedAttributes via authorizedForRepository -- applied to the
// candidate's authorization slug, which for a repo-less or orphaned row is the
// sentinel rather than a manufactured repository.
func authorizeWorkItems(principal storage.Principal, candidates []workItemCandidate) ([]contextfabric.SubjectRef, map[string]contextfabric.FactScopeBasis, map[string]string, map[string]contextfabric.SubjectRef, contextfabric.FactScopeExpansionCounts) {
	targets := make([]contextfabric.SubjectRef, 0, len(candidates))
	var targetBasis map[string]contextfabric.FactScopeBasis
	var targetSource map[string]string
	var targetRoot map[string]contextfabric.SubjectRef
	counts := contextfabric.FactScopeExpansionCounts{}

	for _, candidate := range candidates {
		if !authorizedForRepository(principal, candidate.authorizationSlug) {
			// The mask should already have caught this. Reaching here means
			// the two gates DISAGREE, which is a defect in the statement --
			// counted as a drop rather than admitted on the SQL's word.
			counts.AuthorizationDroppedCount++
			if candidate.repoLess {
				counts.RepoLessAuthorizationDroppedCount++
			}
			continue
		}
		canonicalID, omitted, err := identity.Derive(identity.KindWorkItem, []string{candidate.repoID, candidate.workItemID}, nil)
		if err != nil || omitted {
			// An id this package cannot mint is one no provider could match
			// back to a row, so admitting it would produce a subject that
			// silently answers nothing.
			counts.MalformedTouchCount++
			continue
		}
		target := contextfabric.SubjectRef{
			Kind: contextfabric.SubjectWorkItem, CanonicalID: canonicalID, Label: candidate.workItemID,
		}
		targets = append(targets, target)
		if candidate.repoLess {
			counts.RepoLessAdmittedCount++
		}
		key := contextfabric.FactSubjectKey(target)
		if candidate.basis != "" {
			if targetBasis == nil {
				targetBasis = map[string]contextfabric.FactScopeBasis{}
			}
			targetBasis[key] = candidate.basis
		}
		if candidate.attributionSource != "" {
			if targetSource == nil {
				targetSource = map[string]string{}
			}
			targetSource[key] = candidate.attributionSource
		}
		if candidate.originRoot.CanonicalID != "" {
			if targetRoot == nil {
				targetRoot = map[string]contextfabric.SubjectRef{}
			}
			targetRoot[key] = candidate.originRoot
		}
	}
	return targets, targetBasis, targetSource, targetRoot, counts
}

// refuseNonCurrentWorkItemAxis is this package's OWN contract boundary for the
// fourteen `_v1` work-item policies, which support the current axis only.
//
// The resolver already refuses a historical axis for them, so in the normal
// engine path this never fires. It exists for the same reason
// projectRepositories' explicit axis switch does: ExpandFactScope is an
// EXPORTED method, and a boundary must not depend on every caller having
// already checked. Answering a valid_time request with TODAY's membership
// would be a false historical answer, which is worse than a refusal.
//
// The empty axis is treated as current, not rejected -- it is the Go zero
// value of TimeContext, which every pre-CHAOS-4109 caller still constructs.
func refuseNonCurrentWorkItemAxis(request contextfabric.FactScopeExpansionRequest) error {
	switch request.TimeContext.Axis {
	case "", contractsv1.ContextFabricTemporalCurrent:
		return nil
	default:
		// WRAPPED, not a bare error: the classifier matches by errors.Is, so
		// the refusal keeps its meaning through this package's own context
		// instead of degrading into a generic backend fault.
		return fmt.Errorf("devhealthfacts: work-item scope policy %q supports the current axis only, got %q: %w",
			request.Policy, request.TimeContext.Axis, contextfabric.ErrFactScopeAxisUnsupported)
	}
}

// workItemExpansionResult applies the second authorization gate and folds its
// counts into the traversal's, so both work-item arms return an identically
// shaped result and neither can forget the gate.
func workItemExpansionResult(principal storage.Principal, candidates []workItemCandidate, counts contextfabric.FactScopeExpansionCounts) contextfabric.FactScopeExpansionResult {
	targets, targetBasis, targetSource, targetRoot, authCounts := authorizeWorkItems(principal, candidates)
	counts.AuthorizationDroppedCount += authCounts.AuthorizationDroppedCount
	counts.RepoLessAdmittedCount += authCounts.RepoLessAdmittedCount
	counts.RepoLessAuthorizationDroppedCount += authCounts.RepoLessAuthorizationDroppedCount
	counts.MalformedTouchCount += authCounts.MalformedTouchCount
	return contextfabric.FactScopeExpansionResult{
		Targets: targets, TargetBasis: targetBasis,
		TargetAttributionSource: targetSource, TargetRoot: targetRoot,
		Counts: counts,
	}
}

// projectWorkItemSelectionSQL renders the project -> work_item selection for
// one bounded page.
//
// A NAMED PURE FUNCTION rather than a string built inline in projectWorkItems,
// so the statement the database actually executes can be handed to a test and
// RUN (codex r3 P3). The properties this statement decides -- the identity
// mask and the admissible-rows-first ordering -- have no consequence any fake
// client can observe, and the substring guards that stood in for them were
// satisfied by a mutant that put the expected text in an SQL comment. The
// answer is to execute it, which needs the string to be reachable.
func projectWorkItemSelectionSQL(limit int) string {
	return workItemScopeProjection(`  SELECT toString(w.repo_id) AS repo_id, w.work_item_id AS work_item_id, ifNull(r.repo, '') AS repo_slug, min(concat(p.provider, '`+projectOriginKeySeparator+`', p.id)) AS origin_id, '' AS attribution_source,
    `+workItemAuthorizationExprSQL+` AS authorized,
    toUInt8(toString(w.repo_id) = '`+zeroRepositoryID+`') AS repo_less,
    toUInt8(toString(w.repo_id) != '`+zeroRepositoryID+`' AND ifNull(r.repo, '') = '') AS orphaned
FROM work_items AS w FINAL
INNER JOIN (
  -- PARTITIONED BY (provider, join_key), not by join_key alone. Two providers
  -- reusing one project id string are not an ambiguity: the join below relates
  -- the work item to a project of ITS OWN provider, so each (provider,
  -- join_key) resolves independently. Partitioning by the key alone made BOTH
  -- providers' projects unresolvable the moment either one existed. This is
  -- the shape CHAOS-4108 proved live in devhealthsource's
  -- resolvedProjectsSubquery. The work-item chain was the arm not carrying it.
  SELECT id, provider, join_key, count() OVER (PARTITION BY provider, join_key) AS key_resolution_count
  FROM (
    SELECT DISTINCT id, provider, join_key FROM (
      SELECT id, provider, id AS join_key FROM projects FINAL WHERE org_id = {org_id:String}
      UNION ALL
      SELECT id, provider, ifNull(project_key, '') AS join_key FROM projects FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != ''
    )
  )
) AS p
  -- THE PROVIDER IS PART OF THE JOIN, not an afterthought in the WHERE (codex
  -- r2 F4). w.project_id is a foreign key into ONE provider's project
  -- namespace, so matching it against a project from a DIFFERENT provider is
  -- not a weak match, it is a match on two unrelated identifiers that happen
  -- to be the same string. Joining on the key alone admitted a jira work item
  -- as scope for a linear project.
  ON p.join_key = w.project_id AND p.provider = w.provider
LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
-- The requested origins are bound PROVIDER-QUALIFIED for the same reason (see
-- decodeProjectOriginKeys): the caller named a canonical project, and its
-- provider is half of that name.
WHERE w.org_id = {org_id:String} AND p.key_resolution_count = 1 AND concat(p.provider, '`+projectOriginKeySeparator+`', p.id) IN {project_ids:Array(String)}
GROUP BY toString(w.repo_id), w.work_item_id, ifNull(r.repo, '')`, limit)
}
