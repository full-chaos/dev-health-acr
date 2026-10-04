package contextfabric

import (
	"slices"
	"strings"

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
// are the repository's, before any filter or authorization; Denied is
// withheld (zero) on a filtered read, as the census line withholds it.
type RepositoryWorkItemWalkEvent struct {
	Outcome                                     RepositoryWorkItemWalkOutcome
	PullRequests, LinkedIssues, Members, Denied int
	Truncated, Filtered, Restricted, Measured   bool
	UnmeasuredReason                            WorkItemMembershipUnmeasuredReason
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
