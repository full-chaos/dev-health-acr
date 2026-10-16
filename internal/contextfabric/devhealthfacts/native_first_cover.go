package devhealthfacts

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/full-chaos/dev-health-go/readers"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// The fallback-only rule of the issue <> pull request link of record (ops
// issueprlinks.NativeLinks.Outranks): an explicit_text or heuristic row counts
// only when no native row covers its pull request (repo_id, pr_number) or its
// issue. The linked set of the team linked mix is built in Go from rows, so the
// rule is applied there, once per read, from one native-cover statement that
// looks only for the fallback rows' issues and pull requests anywhere in the
// organization (every repository). A set without a fallback row reads nothing.

// linkedSetRow is one distinct row of the linked set. issue is empty for a
// native row (its issue is not needed); rank is the tier rank, 3 for native.
type linkedSetRow struct {
	repo, number, team, issue string
	rank                      uint8
}

const nativeRank = 3

// nativeCover is what the organization's native rows cover: issues by work
// item id and pull requests by pullRequestKey.
type nativeCover struct {
	issues       map[string]struct{}
	pullRequests map[string]struct{}
}

func coverPullRequestKey(repo, number string) string { return repo + "|" + number }

func fallbackRows(links []linkedSetRow) int {
	n := 0
	for _, l := range links {
		if l.rank < nativeRank {
			n++
		}
	}
	return n
}

// nativeCoverStatement is the organization's native rows among the fallback
// rows' issues ({ids}) and pull requests ({pull_requests}), as two arrays in
// one row. A native row found by either arm is a native link of its issue and
// of its pull request, so both arrays cover.
const nativeCoverStatement = `SELECT groupUniqArray(work_item_id), groupUniqArray(concat(toString(repo_id), '|', toString(pr_number)))
FROM work_graph_issue_pr
WHERE org_id = {org_id:String} AND provenance = 'native'
  AND (work_item_id IN {ids:Array(String)} OR concat(toString(repo_id), '|', toString(pr_number)) IN {pull_requests:Array(String)})`

// nativeCover reads the native cover of the fallback rows of links; it reads
// nothing when there is none.
func (p *InvestmentProvider) nativeCover(ctx context.Context, orgID string, links []linkedSetRow) (nativeCover, error) {
	cover := nativeCover{issues: map[string]struct{}{}, pullRequests: map[string]struct{}{}}
	issues, pullRequests := map[string]struct{}{}, map[string]struct{}{}
	for _, l := range links {
		if l.rank >= nativeRank {
			continue
		}
		issues[l.issue] = struct{}{}
		pullRequests[coverPullRequestKey(l.repo, l.number)] = struct{}{}
	}
	if len(issues) == 0 {
		return cover, nil
	}
	rows := 0
	var coveredIssues, coveredPullRequests []string
	err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadTeamLinkedNativeCover", nativeCoverStatement, orgID, sortedKeys(issues), func(row contextpacket.ClickHouseRowScanner) error {
		rows++
		return row.Scan(&coveredIssues, &coveredPullRequests)
	}, readers.Binding{Name: "pull_requests", Value: sortedKeys(pullRequests)})
	if err != nil {
		return nativeCover{}, err
	}
	if rows != 1 {
		return nativeCover{}, fmt.Errorf("team linked native cover returned %d rows", rows)
	}
	for _, id := range coveredIssues {
		cover.issues[id] = struct{}{}
	}
	for _, key := range coveredPullRequests {
		cover.pullRequests[key] = struct{}{}
	}
	return cover, nil
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// linkedSet is the kept links as the parallel arrays the external table takes,
// one entry per distinct (repository, PR number, team).
type linkedSet struct {
	repoIDs, prNumbers, teamIDs []string
}

func (s linkedSet) repos() []string   { return s.repoIDs }
func (s linkedSet) numbers() []string { return s.prNumbers }
func (s linkedSet) teams() []string   { return s.teamIDs }

// dropCoveredLinks drops every fallback row whose issue or pull request a
// native row covers, then reduces the rest to the distinct (repository, PR
// number, team) the linked set carries; dropped counts the rows removed. The
// order of the first row of each key is kept.
func dropCoveredLinks(links []linkedSetRow, cover nativeCover) (linkedSet, int) {
	var out linkedSet
	seen := map[string]struct{}{}
	dropped := 0
	for _, l := range links {
		if l.rank < nativeRank {
			_, issueCovered := cover.issues[l.issue]
			_, prCovered := cover.pullRequests[coverPullRequestKey(l.repo, l.number)]
			if issueCovered || prCovered {
				dropped++
				continue
			}
		}
		key := strconv.Quote(l.repo) + strconv.Quote(l.number) + strconv.Quote(l.team)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out.repoIDs = append(out.repoIDs, l.repo)
		out.prNumbers = append(out.prNumbers, l.number)
		out.teamIDs = append(out.teamIDs, l.team)
	}
	return out, dropped
}

// nativeFirstOutcome names the closed outcome of the rule for the read's log.
func nativeFirstOutcome(links []linkedSetRow, dropped int) string {
	switch {
	case fallbackRows(links) == 0:
		return "no_fallback_rows"
	case dropped == 0:
		return "no_fallback_row_covered"
	default:
		return "covered_fallback_rows_dropped"
	}
}
