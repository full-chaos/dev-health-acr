package devhealthsource

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The Issue <> Pull request hop of the entity tree comes from the ops link
// table of record work_graph_issue_pr, projected as LINKS_PULL_REQUEST from
// the issue work_item node to the pull_request node. RELATES_TO from
// work_item_dependencies stays as it is for issue-to-issue relations.
//
// Facts about the table (ops main):
//   - repo_id is the PULL REQUEST's repository, never the issue's. The issue's
//     own repo_id (zero UUID for a repo-less issue) comes from work_items, and
//     only that one derives the issue's canonical id.
//   - one row per (org, repo_id, work_item_id, pr_number); ReplacingMergeTree
//     on version_rank keeps the strongest tier at merge, so it is read FINAL.
//   - rows are written by the ops workgraph.build job and never deleted, so
//     this producer has no retraction path.

// Edge property names. They are persisted on the graph edge by falkorgraph's
// projectRelationship, which is the only reason the tier survives projection.
const (
	// IssuePullRequestLinkTierProperty carries the link's provenance tier
	// exactly as ops spells it.
	IssuePullRequestLinkTierProperty = "link_provenance"
	// IssuePullRequestLinkRankProperty carries that tier's rank.
	IssuePullRequestLinkRankProperty = "link_provenance_rank"
)

// issuePullRequestLinkTierRank is the CLOSED acr vocabulary of ops' three
// link tiers and their ranks (native 3 > explicit_text 2 > heuristic 1), the
// same order as the table's version_rank. These are ops' tier names, not
// provider names. A provenance outside it is not projected and is counted.
var issuePullRequestLinkTierRank = map[string]int64{
	"native":        3,
	"explicit_text": 2,
	"heuristic":     1,
}

// The skip counters. Each skipped row advances the cursor, projects nothing
// and is counted at Info through the ignored ledger (ignored_relationship_type
// carries the value), never quarantined.
const (
	issuePullRequestLinkSkipUnknownProvenance = "issue_pull_request_link:unknown_provenance"
	issuePullRequestLinkSkipUnresolvedIssue   = "issue_pull_request_link:unresolved_work_item"
	issuePullRequestLinkSkipPullRequestTyped  = "issue_pull_request_link:pull_request_typed_work_item"
	issuePullRequestLinkSkipMissingPRNode     = "issue_pull_request_link:missing_pull_request_node"
)

// pullRequestWorkItemTypes are the work_items.type values of a work item that
// IS a pull request. A link of record targets an issue, so such a row is
// skipped.
var pullRequestWorkItemTypes = map[string]struct{}{"pr": {}, "merge_request": {}}

// pullRequestCanonicalID is the pull_request node id queryPullRequests mints.
func pullRequestCanonicalID(repoID string, number int64) string {
	return fmt.Sprintf("pull_request:%s:%d", repoID, number)
}

// queryIssuePullRequestLinks projects work_graph_issue_pr as LINKS_PULL_REQUEST
// (work_item -> pull_request).
//
// Both joins are LEFT on purpose: an unresolved work item and a missing pull
// request node must be COUNTED, which an INNER join would hide. Existence is
// read from an explicit flag, so the statement is right whether or not the
// backend enables join_use_nulls. The edge is minted only when both ends
// exist, so it never points at an unwritten node.
//
// The edge carries no confidence and no evidence text: no sibling edge carries
// either, so the tier and its rank are the only properties.
//
// The edge window is the intersection of the two endpoints' windows (the
// table has no interval of its own), the rule every sibling edge follows.
// Authorization is the pull request's repository scope, the repository the
// link row names; each end is gated on its own scope as well. Evidence cites
// the issue's work item and the pull request.
func queryIssuePullRequestLinks(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	const rowKey = "concat(toString(l.repo_id), ':', l.work_item_id, ':', toString(l.pr_number))"
	statement := `SELECT l.work_item_id, toString(l.repo_id), l.pr_number, l.provenance, l.last_synced,
       toUInt8(ifNull(w.work_item_id, '') != ''), toString(w.repo_id), ifNull(w.type, ''),
       w.created_at, ` + nullableTimestamp("coalesce(w.completed_at, w.closed_at)") + `,
       toUInt8(ifNull(p.number, 0) >= 0), ifNull(r.repo, ''),
       p.created_at, ` + nullableTimestamp("coalesce(p.merged_at, p.closed_at)") + `
FROM work_graph_issue_pr AS l FINAL
LEFT JOIN work_items AS w FINAL ON w.org_id = l.org_id AND w.work_item_id = l.work_item_id
LEFT JOIN git_pull_requests AS p FINAL ON p.org_id = l.org_id AND p.repo_id = l.repo_id AND p.number = l.pr_number
LEFT JOIN repos AS r FINAL ON r.id = l.repo_id AND r.org_id = l.org_id
WHERE l.org_id = {org_id:String}` + sincePredicate(cursor, "l.last_synced", rowKey) + orderBy("l.last_synced", rowKey)
	return fetch(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
		var workItemID, prRepoID, provenance, issueRepoID, issueType, prRepoSlug string
		var rawNumber uint32
		var observedAt, issueCreatedAt, issueEndedAt, prCreatedAt, prEndedAt time.Time
		var issueResolved, issueHasEnded, prExists, prHasEnded uint8
		if err := r.Scan(&workItemID, &prRepoID, &rawNumber, &provenance, &observedAt,
			&issueResolved, &issueRepoID, &issueType, &issueCreatedAt, &issueHasEnded, &issueEndedAt,
			&prExists, &prRepoSlug, &prCreatedAt, &prHasEnded, &prEndedAt); err != nil {
			return nil, err
		}
		observedAt = observedAt.UTC()
		number := int64(rawNumber)
		rowSortKey := fmt.Sprintf("%s:%s:%d", prRepoID, workItemID, number)
		skip := func(reason string) ([]candidate, error) {
			return []candidate{{observedAt: observedAt, sortKey: rowSortKey, ignoredType: reason}}, nil
		}

		tier := strings.TrimSpace(provenance)
		rank, known := issuePullRequestLinkTierRank[tier]
		if !known {
			return skip(issuePullRequestLinkSkipUnknownProvenance)
		}
		if issueResolved == 0 {
			return skip(issuePullRequestLinkSkipUnresolvedIssue)
		}
		if _, isPullRequest := pullRequestWorkItemTypes[strings.ToLower(strings.TrimSpace(issueType))]; isPullRequest {
			return skip(issuePullRequestLinkSkipPullRequestTyped)
		}
		if prExists == 0 || strings.TrimSpace(prRepoSlug) == "" {
			return skip(issuePullRequestLinkSkipMissingPRNode)
		}

		// The issue's id uses the ISSUE's own repo_id from work_items. The
		// row's repo_id is the pull request's.
		issueCanonicalID, omitted, err := identity.Derive(identity.KindWorkItem, []string{issueRepoID, workItemID}, nil)
		if err != nil {
			return nil, err
		}
		if omitted {
			return []candidate{progressCandidate(observedAt, rowSortKey)}, nil
		}
		pullRequestID := pullRequestCanonicalID(prRepoID, number)

		validFrom, validTo := edgeValidity(
			requiredTime(issueCreatedAt), optionalTime(issueHasEnded, issueEndedAt),
			requiredTime(prCreatedAt), optionalTime(prHasEnded, prEndedAt))
		relationship := contractsv1.ContextFabricRelationshipProjection{
			RelationshipID: identity.DeriveRelationship(identity.RelationshipFamilyIssuePullRequestLink, issueCanonicalID, pullRequestID, string(contractsv1.ContextFabricRelationshipLinksPullRequest)),
			Type:           contractsv1.ContextFabricRelationshipLinksPullRequest,
			From:           contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: issueCanonicalID, Label: workItemID},
			To:             contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequest, CanonicalID: pullRequestID, Label: pullRequestID},
			Properties: map[string]contractsv1.ContextFabricScalarValue{
				IssuePullRequestLinkTierProperty: stringScalar(tier),
				IssuePullRequestLinkRankProperty: intScalar(rank),
			},
			Derivation: contractsv1.ContextFabricDerivationCanonicalStructured, EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
			Authorization: repoAuthorization(prRepoSlug),
			EvidenceRefIDs: []string{
				contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, issueRepoID+":"+workItemID),
				contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityPullRequest, prRepoID+":"+fmt.Sprint(number)),
			},
			ObservedAt: observedAt, ValidFrom: validFrom, ValidTo: validTo, SourceVersion: ClickHouseSourceVersion,
		}
		return []candidate{{observedAt: observedAt, sortKey: rowSortKey, relationship: &relationship}}, nil
	})
}
