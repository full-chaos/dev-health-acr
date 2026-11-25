package devhealthfacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// chaos10430 pins the team link set read in candidate chunks: the chunk size,
// the split, the union's de-duplication, and the statement_sha each moved
// reader logs on the memory-capped client (sha256 of the statement, 6 bytes).

// chunkRecordingClient answers the candidate statement with ids, the link set
// statement with setRows (one call's rows each, in order), and records every
// statement it is sent.
type chunkRecordingClient struct {
	mu         sync.Mutex
	ids        []string
	setRows    [][][]any
	statements []string
	sets       int
}

func (c *chunkRecordingClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statements = append(c.statements, statement)
	switch {
	case strings.Contains(statement, "SELECT cp.work_item_id AS work_item_id FROM work_items AS cp WHERE"):
		return &linkScanner{rows: [][]any{{c.ids}}}, nil
	case strings.Contains(statement, "HAVING min(repo_id) != max(repo_id)"):
		return &linkScanner{rows: [][]any{{[]string{}}}}, nil
	case strings.Contains(statement, "groupArray(r), groupArray(n)"):
		var rows [][]any
		if c.sets < len(c.setRows) {
			rows = c.setRows[c.sets]
		}
		c.sets++
		return &linkScanner{rows: rows}, nil
	}
	return &linkScanner{}, nil
}

func setRow(rows ...linkedSetRow) [][]any {
	var repos, numbers, teams, issues []string
	var ranks []uint8
	for _, row := range rows {
		repos, numbers, teams, issues, ranks = append(repos, row.repo), append(numbers, row.number), append(teams, row.team), append(issues, row.issue), append(ranks, row.rank)
	}
	return [][]any{{repos, numbers, teams, issues, ranks}}
}

func candidateIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("ISSUE-%06d", i)
	}
	return ids
}

func TestCHAOS10430TheLinkSetChunkIs24000Candidates(t *testing.T) {
	if teamLinkCandidateChunk != 24_000 {
		t.Fatalf("teamLinkCandidateChunk = %d, want 24000 (the 58.1 MB measured chunk)", teamLinkCandidateChunk)
	}
}

// 24,001 candidates are two link set statements; 24,000 are one.
func TestCHAOS10430TheLinkSetSplitsPastOneChunk(t *testing.T) {
	for _, tc := range []struct {
		candidates, statements int
	}{{1, 1}, {24_000, 1}, {24_001, 2}, {48_000, 2}, {48_001, 3}, {96_000, 4}} {
		client := &chunkRecordingClient{ids: candidateIDs(tc.candidates)}
		if _, err := readTeamProjectLinkedSet(context.Background(), client, "org-1", []string{"t1"}, factTimeBound{}); err != nil {
			t.Fatal(err)
		}
		if client.sets != tc.statements {
			t.Errorf("%d candidates: %d link set statements, want %d", tc.candidates, client.sets, tc.statements)
		}
	}
}

// A link row two chunks both return is one row of the union; rows that differ
// in any column stay apart, as the statement's own DISTINCT keeps them.
func TestCHAOS10430TheLinkSetUnionKeepsEachRowOnce(t *testing.T) {
	native := linkedSetRow{repo: "r1", number: "7", team: "t1", rank: 3}
	fallback := linkedSetRow{repo: "r1", number: "7", team: "t1", issue: "ISSUE-024000", rank: 2}
	other := linkedSetRow{repo: "r1", number: "8", team: "t1", rank: 3}
	client := &chunkRecordingClient{ids: candidateIDs(24_001), setRows: [][][]any{setRow(native, other), setRow(native, fallback)}}
	rows, err := readTeamProjectLinkedSet(context.Background(), client, "org-1", []string{"t1"}, factTimeBound{})
	if err != nil {
		t.Fatal(err)
	}
	want := []linkedSetRow{native, other, fallback}
	if fmt.Sprint(rows) != fmt.Sprint(want) {
		t.Fatalf("union = %v, want %v", rows, want)
	}
	set := linkedPullRequestsOf(rows)
	wantKeys := []teamLinkedPullRequestKey{{"t1", "r1", "7"}, {"t1", "r1", "8"}}
	if fmt.Sprint(set.keys) != fmt.Sprint(wantKeys) || fmt.Sprint(set.ranks) != fmt.Sprint([]uint8{2, 3}) {
		t.Fatalf("folded set = %v %v, want %v [2 3] (one row per team pull request, its weakest rank)", set.keys, set.ranks, wantKeys)
	}
}

// The sent set is bounded in rows; over the bound the read fails by name.
func TestCHAOS10430TheSentSetIsBounded(t *testing.T) {
	keys := make([]teamLinkedPullRequestKey, teamLinkedPullRequestsMaxRows+1)
	if _, err := withTeamLinkedPullRequests(context.Background(), teamLinkedPullRequests{keys: keys, ranks: make([]uint8, len(keys))}); err == nil || !strings.Contains(err.Error(), "over the 400000 rows") {
		t.Fatalf("err = %v, want the named row bound", err)
	}
	big := strings.Repeat("x", 1<<20)
	keys = make([]teamLinkedPullRequestKey, 65)
	for i := range keys {
		keys[i] = teamLinkedPullRequestKey{team: big, repo: "r", number: fmt.Sprint(i)}
	}
	if _, err := withTeamLinkedPullRequests(context.Background(), teamLinkedPullRequests{keys: keys, ranks: make([]uint8, len(keys))}); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("err = %v, want the named byte bound", err)
	}
}

func statementSHA(statement string) string {
	sum := sha256.Sum256([]byte(statement))
	return hex.EncodeToString(sum[:6])
}

// mainReachedStatement and the two below are the three readers' statements at
// acr main a47904e8de93, first page, as they were rendered there.
func mainReachedStatement(bound factTimeBound, having string, limit int) string {
	return `SELECT lr.team_id, toString(lr.pr_repo_id), ifNull(any(r.repo), ''), toInt64(min(lr.link_rank)), toInt64(uniqExact(lr.pr_number))
FROM ` + teamProjectLinkRowsSQL("ids", bound) + ` AS lr
LEFT JOIN repos AS r FINAL ON r.id = lr.pr_repo_id AND r.org_id = {org_id:String}
GROUP BY lr.team_id, lr.pr_repo_id
` + having + `
ORDER BY lr.team_id, lr.pr_repo_id
LIMIT ` + fmt.Sprint(limit)
}

func mainCountsStatement(window rollupWindow, bound factTimeBound, having string, limit int) string {
	return `SELECT lr.team_id, toString(lr.pr_repo_id),
	toInt64(countIf(` + window.timestampExpr("pr.created_at") + `)),
	toInt64(countIf(pr.merged_at IS NOT NULL AND ` + window.timestampExpr("pr.merged_at") + `)),
	toInt64(countIf(pr.merged_at IS NULL AND pr.closed_at IS NOT NULL AND ` + window.timestampExpr("pr.closed_at") + `))
FROM (SELECT DISTINCT team_id, pr_repo_id, pr_number FROM ` + teamProjectLinkRowsSQL("ids", bound) + `) AS lr
INNER JOIN git_pull_requests AS pr FINAL ON pr.org_id = {org_id:String} AND pr.repo_id = lr.pr_repo_id AND pr.number = lr.pr_number
WHERE pr.repo_id IN (SELECT toUUIDOrZero(id) FROM (SELECT arrayJoin({reach_repo_ids:Array(String)}) AS id))
GROUP BY lr.team_id, lr.pr_repo_id
` + having + `
ORDER BY lr.team_id, lr.pr_repo_id
LIMIT ` + fmt.Sprint(limit)
}

func mainStatesStatement(bound factTimeBound, having string, limit int) string {
	return `SELECT team_id, repo, ` + statePairsExpression() + `
FROM (
	SELECT lr.team_id AS team_id, toString(lr.pr_repo_id) AS repo, ` + pullRequestStateExpression("lp", bound) + ` AS st, toInt64(count()) AS c
	FROM (SELECT DISTINCT team_id, pr_repo_id, pr_number FROM ` + teamProjectLinkRowsSQL("ids", bound) + `) AS lr
	INNER JOIN git_pull_requests AS lp FINAL ON lp.org_id = {org_id:String} AND lp.repo_id = lr.pr_repo_id AND lp.number = lr.pr_number
	WHERE lp.repo_id IN (SELECT toUUIDOrZero(id) FROM (SELECT arrayJoin({reach_repo_ids:Array(String)}) AS id))` + bound.neutral().ExistencePredicate("lp.created_at") + `
	GROUP BY team_id, repo, st
)
GROUP BY team_id, repo
` + having + `
ORDER BY team_id, repo
LIMIT ` + fmt.Sprint(limit)
}

// The statement_sha of each moved reader before and after, for the prod
// memory watch: unbounded read, default trailing window, first page.
func TestCHAOS10430StatementSHAs(t *testing.T) {
	bound := factTimeBound{}
	window := resolveRollupWindow(bound, nil, clock())
	client := &chunkRecordingClient{}
	set := teamLinkedPullRequests{keys: []teamLinkedPullRequestKey{{"t1", "00000000-0000-4000-8000-000000000042", "1"}}, ranks: []uint8{3}}
	reached := map[string][]teamReachedRepo{"t1": {{key: "00000000-0000-4000-8000-000000000042"}}}
	ctx := context.Background()
	if _, err := teamProjectReachedRepositoriesOf(ctx, client, "org-1", []string{"t1"}, set); err != nil {
		t.Fatal(err)
	}
	if _, err := teamLinkedPullRequestCounts(ctx, client, "org-1", []string{"t1"}, set, reached, window); err != nil {
		t.Fatal(err)
	}
	if _, err := teamLinkedPullRequestStateCounts(ctx, client, "org-1", []string{"t1"}, set, reached, bound); err != nil {
		t.Fatal(err)
	}
	if len(client.statements) != 3 {
		t.Fatalf("recorded %d statements, want 3", len(client.statements))
	}
	limit := teamReadPageRows + 1
	for i, c := range []struct {
		reader, main, wantMain, wantTip string
	}{
		{"ReadTeamProjectReachedRepositories", mainReachedStatement(bound, "", limit), "1c4dc4885ee4", "9b1d6cc79c60"},
		{"ReadTeamProjectLinkedPullRequests", mainCountsStatement(window, bound, "", limit), "36161a79b233", "f7c9165ba8ac"},
		{"ReadTeamProjectLinkedPullRequestStates", mainStatesStatement(bound, "", limit), "f4b7509900b9", "4b1d620043c6"},
	} {
		gotMain, gotTip := statementSHA(c.main), statementSHA(client.statements[i])
		t.Logf("statement_sha %s main=%s tip=%s", c.reader, gotMain, gotTip)
		if gotMain != c.wantMain || gotTip != c.wantTip {
			t.Errorf("WANT %s statement_sha main %s tip %s, got %s %s", c.reader, c.wantMain, c.wantTip, gotMain, gotTip)
		}
	}
	for reader, c := range map[string]struct{ statement, want string }{
		"ReadTeamLinkCandidates":       {teamLinkCandidatesStatement(bound), "818b10d043e8"},
		"ReadTeamLinkedPullRequestSet": {linkedPullRequestSetStatement(bound), "e87b21ab50f7"},
	} {
		if got := statementSHA(c.statement); got != c.want {
			t.Errorf("WANT %s statement_sha unchanged %s, got %s", reader, c.want, got)
		}
	}
}
