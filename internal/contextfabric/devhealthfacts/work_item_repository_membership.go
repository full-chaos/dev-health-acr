package devhealthfacts

import (
	"os"
	"regexp"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/dependencyrelation"
	"github.com/full-chaos/dev-health-go/readers"
)

// The work items of a repository are the issues that have a linked pull
// request of that repository: an issue -> pull request link row in
// work_item_dependencies whose pull-request end is a pull-request work item
// of the repository. An issue's own repository column is not the relation,
// and neither is an issue-key prefix.
//
// The link is the row the projector writes as the RELATES_TO edge between an
// issue and a pull-request work item (devhealthsource queryWorkItemDependencies:
// endpoints joined on work_item_id, relationship type keyed by
// dependencyrelation). Either end may hold the pull request, so both
// directions are read.

// workItemRepositoryPullRequestTypes are the stored types of a pull-request
// work item.
const workItemRepositoryPullRequestTypes = "('pr', 'merge_request')"

// workItemRepositoryRelatesKey is the relation key of a RELATES_TO link.
var workItemRepositoryRelatesKey = dependencyrelation.Key("relates_to")

// workItemRepositoryLinkedIssuesSQL is the repository's linked issues, one
// row per issue, before any member filter or authorization.
func workItemRepositoryLinkedIssuesSQL() string {
	key := dependencyrelation.KeySQL("d.relationship_type")
	links := `SELECT d.org_id AS org_id, d.target_work_item_id AS issue_id, d.source_work_item_id AS pull_request_id
    FROM work_item_dependencies AS d FINAL
    WHERE d.org_id = {org_id:String} AND ` + key + ` = '` + workItemRepositoryRelatesKey + `'
    UNION ALL
    SELECT d.org_id AS org_id, d.source_work_item_id AS issue_id, d.target_work_item_id AS pull_request_id
    FROM work_item_dependencies AS d FINAL
    WHERE d.org_id = {org_id:String} AND ` + key + ` = '` + workItemRepositoryRelatesKey + `'`
	return `(
  SELECT DISTINCT i.org_id AS org_id, i.repo_id AS repo_id, i.work_item_id AS work_item_id
  FROM (
    ` + links + `
  ) AS l
  INNER JOIN (
    SELECT work_item_id FROM work_items FINAL
    WHERE org_id = {org_id:String} AND toString(repo_id) = {anchor_repo_id:String} AND type IN ` + workItemRepositoryPullRequestTypes + `
  ) AS pr ON pr.work_item_id = l.pull_request_id
  INNER JOIN (
    SELECT org_id, repo_id, work_item_id FROM work_items FINAL
    WHERE org_id = {org_id:String} AND type NOT IN ` + workItemRepositoryPullRequestTypes + `
  ) AS i ON i.work_item_id = l.issue_id
)`
}

// workItemRepositoryMembershipStatementFor is the S1 statement for a
// repository anchor: the repository's linked issues, filtered and authorized
// exactly as a project's members are, inside the same census protocol. The
// anchor state carries whether the repository exists in the organization,
// how many pull requests it has and how many issues they link.
func workItemRepositoryMembershipStatementFor(scope readers.AuthorizationScope, k int, timeColumn string) (string, []readers.Binding) {
	timePredicate := workItemMembershipTimePredicate(timeColumn)
	rendered := readers.WorkItemScopeSQL(scope)
	linked := workItemRepositoryLinkedIssuesSQL()
	anchorResolution := `(
  SELECT
    toUInt8(if((SELECT count() FROM repos FINAL WHERE org_id = {org_id:String} AND toString(id) = {anchor_repo_id:String}) > 0, 1, 0)) AS anchor_resolved,
    (SELECT count() FROM work_items FINAL WHERE org_id = {org_id:String} AND toString(repo_id) = {anchor_repo_id:String} AND type IN ` + workItemRepositoryPullRequestTypes + `) AS repository_pull_requests,
    (SELECT count() FROM ` + linked + `) AS repository_linked_issues
)`
	from := `FROM ` + linked + ` AS m
INNER JOIN work_items AS w FINAL
  ON m.org_id = w.org_id AND m.repo_id = w.repo_id AND m.work_item_id = w.work_item_id`
	if rendered.JoinSQL != "" {
		from += "\n" + rendered.JoinSQL
	} else {
		from += "\nLEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id"
	}
	memberRows := `
  SELECT
    ` + workItemMembershipCanonicalKeySQL + ` AS canonical_key,
    toString(w.repo_id) AS repo_id,
    w.work_item_id AS work_item_id,
    ifNull(r.repo, '') AS repo_slug,
    toUInt8(if(` + rendered.AuthorizationExpr + `, 1, 0)) AS authorized_flag,
` + workItemMembershipPathFlagsSQL(rendered) + `
    toUInt8(NOT (toString(w.repo_id) != '' AND toString(w.repo_id) != '` + zeroRepositoryID + `')) AS repo_less,
    toUInt8(w.project_id = '') AS project_less,
    toUInt8(has(` + workItemMembershipExcludedLinksSQL(rendered) + `, 'explicit_text')) AS excluded_explicit_text_link,
    toUInt8(has(` + workItemMembershipExcludedLinksSQL(rendered) + `, 'heuristic')) AS excluded_heuristic_link,
    toUInt64(0) AS transition_assertions,
    toUInt64(0) AS future_boundaries
  ` + from + `
  WHERE w.org_id = {org_id:String}
    AND ({status_filter:String} = '' OR w.status = {status_filter:String})` + timePredicate + `
  GROUP BY canonical_key, repo_id, work_item_id, repo_slug, authorized_flag,
    ` + workItemMembershipPathColumns("path_") + `, repo_less, project_less, excluded_explicit_text_link, excluded_heuristic_link`
	return probeMutate(workItemMembershipEnvelope(memberRows, anchorResolution)), rendered.Bindings
}

// probeMutations: one clause each, applied only under ACR_PROBE_MUTATION.
var probeMutations = map[string][2]string{
	"S1_pr_type":         {"AND toString(repo_id) = {anchor_repo_id:String} AND type IN ('pr', 'merge_request')\n  ) AS pr", "AND toString(repo_id) = {anchor_repo_id:String}\n  ) AS pr"},
	"S2_pr_repository":   {"WHERE org_id = {org_id:String} AND toString(repo_id) = {anchor_repo_id:String} AND type IN ('pr', 'merge_request')\n  ) AS pr", "WHERE org_id = {org_id:String} AND type IN ('pr', 'merge_request')\n  ) AS pr"},
	"S3_issue_type":      {"WHERE org_id = {org_id:String} AND type NOT IN ('pr', 'merge_request')\n  ) AS i", "WHERE org_id = {org_id:String}\n  ) AS i"},
	"S6_anchor_exists":   {"WHERE org_id = {org_id:String} AND toString(id) = {anchor_repo_id:String}) > 0, 1, 0)) AS anchor_resolved", "WHERE org_id = {org_id:String} AND toString(id) = {anchor_repo_id:String}) >= 0, 1, 0)) AS anchor_resolved"},
	"S7_pull_count_type": {"AND toString(repo_id) = {anchor_repo_id:String} AND type IN ('pr', 'merge_request')) AS repository_pull_requests", "AND toString(repo_id) = {anchor_repo_id:String}) AS repository_pull_requests"},
	"S8_status":          {"AND ({status_filter:String} = '' OR w.status = {status_filter:String})", "AND 1"},
}

func probeMutate(statement string) string {
	id := os.Getenv("ACR_PROBE_MUTATION")
	switch id {
	case "":
		return statement
	case "S4a_key_first", "S4b_key_second", "S5_reverse_branch":
		return probeLinkMutate(statement, id)
	case "S9_time":
		return regexp.MustCompile(`\n    AND w\.(created_at|completed_at|updated_at) >= \{time_start:DateTime64\(6, 'UTC'\)\} AND w\.(created_at|completed_at|updated_at) < \{time_end:DateTime64\(6, 'UTC'\)\}`).ReplaceAllString(statement, "")
	case "S10_authorization":
		out := regexp.MustCompile(`(?s)toUInt8\(if\(.*?, 1, 0\)\) AS authorized_flag`).ReplaceAllString(statement, "toUInt8(1) AS authorized_flag")
		if out == statement {
			panic("probe mutation not applicable: " + id)
		}
		return out
	}
	m, ok := probeMutations[id]
	if !ok || !strings.Contains(statement, m[0]) {
		panic("probe mutation not applicable: " + id)
	}
	return strings.Replace(statement, m[0], m[1], -1)
}

func probeLinkMutate(statement, id string) string {
	key := " AND " + dependencyrelation.KeySQL("d.relationship_type") + " = '" + workItemRepositoryRelatesKey + "'"
	first := "AS pull_request_id\n    FROM work_item_dependencies AS d FINAL\n    WHERE d.org_id = {org_id:String}" + key + "\n    UNION ALL"
	if !strings.Contains(statement, first) {
		panic("probe link mutation not applicable: " + id)
	}
	switch id {
	case "S4a_key_first":
		return strings.Replace(statement, first, "AS pull_request_id\n    FROM work_item_dependencies AS d FINAL\n    WHERE d.org_id = {org_id:String}\n    UNION ALL", -1)
	case "S4b_key_second":
		second := "AS pull_request_id\n    FROM work_item_dependencies AS d FINAL\n    WHERE d.org_id = {org_id:String}" + key + "\n  ) AS l"
		if !strings.Contains(statement, second) {
			panic("probe link mutation not applicable: " + id)
		}
		return strings.Replace(statement, second, "AS pull_request_id\n    FROM work_item_dependencies AS d FINAL\n    WHERE d.org_id = {org_id:String}\n  ) AS l", -1)
	default:
		start := strings.Index(statement, "\n    UNION ALL\n    SELECT d.org_id AS org_id, d.source_work_item_id AS issue_id")
		end := strings.Index(statement, "\n  ) AS l")
		if start < 0 || end < start {
			panic("probe link mutation not applicable: " + id)
		}
		var b strings.Builder
		for {
			start = strings.Index(statement, "\n    UNION ALL\n    SELECT d.org_id AS org_id, d.source_work_item_id AS issue_id")
			if start < 0 {
				break
			}
			end = strings.Index(statement[start:], "\n  ) AS l")
			b.WriteString(statement[:start])
			statement = statement[start+end:]
		}
		b.WriteString(statement)
		return b.String()
	}
}
