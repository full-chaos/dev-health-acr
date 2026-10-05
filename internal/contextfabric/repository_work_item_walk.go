package contextfabric

import (
	"context"
	"slices"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// RepositoryWorkItemWalkOutcome is the closed outcome of one read of a
// repository's work items: the issues linked to the repository's pull
// requests.
type RepositoryWorkItemWalkOutcome string

const (
	// RepositoryWorkItemWalkMembers: at least one member was served.
	RepositoryWorkItemWalkMembers RepositoryWorkItemWalkOutcome = "members"
	// RepositoryWorkItemWalkNoPullRequests: no member, and the repository
	// has no pull request.
	RepositoryWorkItemWalkNoPullRequests RepositoryWorkItemWalkOutcome = "no_pull_requests"
	// RepositoryWorkItemWalkUnlinked: no member, and none of the
	// repository's pull requests links an issue.
	RepositoryWorkItemWalkUnlinked RepositoryWorkItemWalkOutcome = "unlinked"
	// RepositoryWorkItemWalkNoMatch: no member, linked issues exist and the
	// member filter or the caller's authorization left none.
	RepositoryWorkItemWalkNoMatch RepositoryWorkItemWalkOutcome = "no_match"
	// RepositoryWorkItemWalkDenied: no member for a repository-restricted
	// caller. The served reason is neutral; the counts stay on this line.
	RepositoryWorkItemWalkDenied RepositoryWorkItemWalkOutcome = "denied"
	// RepositoryWorkItemWalkReadFailed: the read failed or measured nothing.
	RepositoryWorkItemWalkReadFailed RepositoryWorkItemWalkOutcome = "read_failed"
)

// RepositoryWorkItemWalkOutcomeVocabulary returns every declared outcome, in
// declaration order.
func RepositoryWorkItemWalkOutcomeVocabulary() []string {
	return []string{
		string(RepositoryWorkItemWalkMembers), string(RepositoryWorkItemWalkNoPullRequests), string(RepositoryWorkItemWalkUnlinked),
		string(RepositoryWorkItemWalkNoMatch), string(RepositoryWorkItemWalkDenied), string(RepositoryWorkItemWalkReadFailed),
	}
}

// RepositoryWorkItemWalkEvent is one decision line of the read: counts and
// closed values only, never a name or an id. PullRequests and LinkedIssues
// are the repository's, before any filter or authorization; Denied is the
// distinct issues the link read returned that no authorized link reached, and
// is
// withheld (zero) on a filtered read, as the census line withholds it.
// Truncated: the walk or the filter did not read every member.
type RepositoryWorkItemWalkEvent struct {
	Outcome                                     RepositoryWorkItemWalkOutcome
	PullRequests, LinkedIssues, Members, Denied int
	Truncated, Filtered, Restricted, Measured   bool
	UnmeasuredReason                            WorkItemMembershipUnmeasuredReason
}

// repositoryWorkItemReading is what one measured read of a repository's work
// items found, kept beside the census for the disclosures of this request.
type repositoryWorkItemReading struct {
	Outcome                    RepositoryWorkItemWalkOutcome
	PullRequests, LinkedIssues int
	// Heuristic counts the served members linked only by the heuristic tier.
	Heuristic int
	// Cut: the walk or the filter did not read every member.
	Cut bool
}

// treeLinkTierStrongerThanHeuristic reports whether a tier is one of the two
// stronger tiers. A tier outside the closed set is read as the heuristic one:
// a member is never presented as more strongly linked than it is known to be.
func treeLinkTierStrongerThanHeuristic(tier string) bool {
	return tier == TreeLinkTierNative || tier == TreeLinkTierExplicitText
}

// workItemRepositoryRestricted reports whether the caller sees only some
// repositories: a grant without the organization wildcard, or a requested
// repository scope.
func workItemRepositoryRestricted(principal storage.Principal, requested []string) bool {
	if len(requested) > 0 {
		return true
	}
	if len(principal.RepositoryScopes) == 0 {
		return false
	}
	return !slices.ContainsFunc(principal.RepositoryScopes, func(scope string) bool { return strings.TrimSpace(scope) == "*" })
}

// repositoryWorkItemWalkOutcome classifies one finished read. A restricted
// caller with no member is denied whatever the counts say, so the served
// answer does not tell an unlinked repository from a hidden one.
func repositoryWorkItemWalkOutcome(census WorkItemMembershipCensus, measured bool, members int, restricted bool) RepositoryWorkItemWalkOutcome {
	switch {
	case !measured:
		return RepositoryWorkItemWalkReadFailed
	case members > 0:
		return RepositoryWorkItemWalkMembers
	case restricted:
		return RepositoryWorkItemWalkDenied
	case census.RepositoryPullRequests == 0:
		return RepositoryWorkItemWalkNoPullRequests
	case census.RepositoryLinkedIssues == 0:
		return RepositoryWorkItemWalkUnlinked
	default:
		return RepositoryWorkItemWalkNoMatch
	}
}

// withRepositoryWorkItemDisclosures adds what a repository work-item answer
// owes its reader: that the links come from the last link build (every
// answer), how many members are linked only by a heuristic match, that the
// read was partial, and why a read with no member has none. An unlinked
// repository (unrestricted caller, pull requests present, none linking an
// issue, a walk that read everything) also carries the coverage code, since
// its work items are unknown, not none. Nothing is added for a project.
func withRepositoryWorkItemDisclosures(result InvestigationResult, census *WorkItemTupleCensus) InvestigationResult {
	if census == nil || census.repository == nil {
		return result
	}
	reading := census.repository
	additions := []string{contractsv1.ContextFabricWorkItemRepositoryFreshnessLimitation}
	if reading.Heuristic > 0 {
		additions = append(additions, contractsv1.ContextFabricWorkItemRepositoryHeuristicLimitation(reading.Heuristic))
	}
	if census.incomplete {
		additions = append(additions, contractsv1.ContextFabricWorkItemRepositoryPartialLimitation)
	}
	if census.Retained < census.Value {
		additions = append(additions, contractsv1.ContextFabricWorkItemRepositoryStrongestFirstLimitation)
	}
	switch reading.Outcome {
	case RepositoryWorkItemWalkNoPullRequests:
		additions = append(additions, contractsv1.ContextFabricWorkItemRepositoryNoPullRequestsLimitation)
	case RepositoryWorkItemWalkUnlinked:
		additions = append(additions, contractsv1.ContextFabricWorkItemRepositoryUnlinkedLimitation)
	case RepositoryWorkItemWalkDenied:
		additions = append(additions, workItemStatusDeniedExclusion)
	}
	composed, displaced := appendBoundedLimitations(result.Limitations, additions)
	result.Limitations = composed
	result.LimitationsDisplaced += displaced
	if reading.Outcome == RepositoryWorkItemWalkUnlinked && !reading.Cut {
		result = withWorkItemRepositoryUnlinkedDetail(result, reading.PullRequests)
	}
	return result
}

// linkScopedFactSubjects are the work items of this request that its link
// predicate admitted under the requested repository scope: every member of a
// repository anchor's work-item walk, and every resolved work item the scoped
// census admitted on this call (WorkItemCensusLinkedSatisfiers). Nothing when
// the request names no repository scope.
func linkScopedFactSubjects(ctx context.Context, request InvestigationRequest, subjects []SubjectRef, tuple *WorkItemTupleCensus) []SubjectRef {
	if len(request.RequestedScope.RepositorySlugs) == 0 {
		return nil
	}
	walked := tuple != nil && tuple.repository != nil
	linked := WorkItemCensusLinkedSatisfiers(ctx)
	var out []SubjectRef
	for _, subject := range subjects {
		if subject.Kind != SubjectWorkItem {
			continue
		}
		if _, admitted := linked[subject.CanonicalID]; walked || admitted {
			out = append(out, subject)
		}
	}
	return out
}

// withWorkItemCensusScopeDisclosures adds what a census under the requested
// repository scope owes the answer: that the census searched through the links
// to the scoped repositories' pull requests, and, for each resolved work item
// the census admitted through a text or heuristic link, which tier it was (a
// text or heuristic link is never presented as native).
func withWorkItemCensusScopeDisclosures(ctx context.Context, result InvestigationResult) InvestigationResult {
	if !WorkItemCensusRepositoryScopeRecorded(ctx) {
		return result
	}
	additions := []string{contractsv1.ContextFabricWorkItemCensusRepositoryScopeLimitation}
	linked := WorkItemCensusLinkedSatisfiers(ctx)
	for _, subject := range result.SubjectResolution.Committed {
		tier, ok := linked[subject.CanonicalID]
		if !ok || subject.Kind != SubjectWorkItem {
			continue
		}
		if sentence := contractsv1.ContextFabricWorkItemCensusLinkTierLimitation(tier); sentence != "" && !slices.Contains(additions, sentence) {
			additions = append(additions, sentence)
		}
	}
	composed, displaced := appendBoundedLimitations(result.Limitations, additions)
	result.Limitations = composed
	result.LimitationsDisplaced += displaced
	return result
}
