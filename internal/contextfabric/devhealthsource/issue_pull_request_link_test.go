package devhealthsource_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The Issue <> Pull request link of record: work_graph_issue_pr projected as
// LINKS_PULL_REQUEST from the issue work item to the pull request.

const (
	linkTable    = "FROM work_graph_issue_pr AS l"
	zeroRepoUUID = "00000000-0000-0000-0000-000000000000"
)

// linkIssue is the work_items side of a link row: the ISSUE's own repo_id
// (the zero UUID for a repo-less issue), its type and its creation time.
type linkIssue struct {
	missing  bool
	repoID   string
	itemType string
	created  time.Time
}

// linkPullRequest is the git_pull_requests + repos side of a link row.
type linkPullRequest struct {
	missing bool
	slug    string
	created time.Time
}

// issuePullRequestLinkRow builds one fixture row in the column order
// queryIssuePullRequestLinks scans. rowRepoID is the PULL REQUEST's repository
// (work_graph_issue_pr.repo_id), never the issue's.
func issuePullRequestLinkRow(workItemID, rowRepoID string, number uint32, provenance string, at time.Time, issue linkIssue, pr linkPullRequest) []any {
	issueResolved, prExists := uint8(1), uint8(1)
	if issue.missing {
		issueResolved = 0
	}
	if pr.missing {
		prExists = 0
	}
	return []any{workItemID, rowRepoID, number, provenance, at,
		issueResolved, issue.repoID, issue.itemType, issue.created, uint8(0), zeroTime,
		prExists, pr.slug, pr.created, uint8(0), zeroTime}
}

func linkTablesOnly(at time.Time, rows [][]any) []fakeTable {
	tables := baseTables(at)
	for index := range tables {
		tables[index].rows = nil
	}
	return append(tables, fakeTable{match: linkTable, rows: rows, cursorOf: func(row []any) (time.Time, string) {
		return row[4].(time.Time), fmt.Sprintf("%s:%s:%d", row[1].(string), row[0].(string), row[2].(uint32))
	}})
}

// linkRun projects until the pass is caught up, returning every relationship
// and the ignored-row counts by reason (the ledger flushes when a call ends
// without publishing, so the pass must run to its end).
func linkRun(t *testing.T, tables []fakeTable, cursor string) ([]contractsv1.ContextFabricRelationshipProjection, map[string]int) {
	t.Helper()
	return linkRunCalls(t, tables, cursor, 10)
}

// linkRunCalls is linkRun with a bound on the calls. One call reads exactly
// the keyset page the cursor selects: a later call of a caught-up pass re-reads
// the trailing overlap window (CHAOS-7263) by design.
func linkRunCalls(t *testing.T, tables []fakeTable, cursor string, calls int) ([]contractsv1.ContextFabricRelationshipProjection, map[string]int) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: tables})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	source = source.WithLogger(logger)
	var relationships []contractsv1.ContextFabricRelationshipProjection
	for call := 0; call < calls; call++ {
		batch, available, err := source.NextProjectionBatch(context.Background(),
			contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: cursor})
		if err != nil {
			t.Fatalf("next projection batch: %v", err)
		}
		if !available {
			break
		}
		if err := batch.Validate(); err != nil {
			t.Fatalf("batch failed contract validation: %v", err)
		}
		relationships = append(relationships, batch.Relationships...)
		cursor = batch.NextCursor
	}
	counts := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		if msg, _ := entry["msg"].(string); strings.Contains(msg, ignoredLine) {
			counts[entry["ignored_relationship_type"].(string)] += int(entry["ignored_count"].(float64))
		}
		if entry["level"] == "WARN" {
			t.Fatalf("unexpected WARN (a link row was quarantined): %v", entry)
		}
	}
	var links []contractsv1.ContextFabricRelationshipProjection
	for _, relationship := range relationships {
		if relationship.Type == contractsv1.ContextFabricRelationshipLinksPullRequest {
			links = append(links, relationship)
		}
	}
	return links, counts
}

func linkCursor(t *testing.T, at time.Time) string { return testCursor(t, at.Add(-time.Hour), "") }

func tierProperty(t *testing.T, relationship contractsv1.ContextFabricRelationshipProjection) (string, int64) {
	t.Helper()
	tier, rank := relationship.Properties[devhealthsource.IssuePullRequestLinkTierProperty], relationship.Properties[devhealthsource.IssuePullRequestLinkRankProperty]
	if tier.String == nil || rank.Integer == nil {
		t.Fatalf("%s carries no tier/rank properties: %+v", relationship.RelationshipID, relationship.Properties)
	}
	return *tier.String, *rank.Integer
}

// TestIssuePullRequestLinksProjectEveryTierForEveryIssueProvider pins the
// edge for every tier and every issue id shape the writers produce (a
// repo-less issue with the zero repo UUID, and an issue with its own repo),
// onto pull requests of a GitHub repo and of a GitLab repo: endpoints, id,
// authorization, tier and rank.
func TestIssuePullRequestLinksProjectEveryTierForEveryIssueProvider(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const ghRepo, glRepo = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	const ghSlug, glSlug = "acme/widget", "group/sub/proj"

	linear := linkIssue{repoID: zeroRepoUUID, itemType: "issue", created: created}
	jira := linkIssue{repoID: zeroRepoUUID, itemType: "story", created: created}
	ghIssue := linkIssue{repoID: ghRepo, itemType: "issue", created: created}
	glIssue := linkIssue{repoID: glRepo, itemType: "issue", created: created}
	ghPR := linkPullRequest{slug: ghSlug, created: created}
	glPR := linkPullRequest{slug: glSlug, created: created}

	type want struct {
		issueID, issueRepo string
		prRepo             string
		number             uint32
		tier               string
		rank               int64
		slug               string
	}
	cases := []struct {
		want  want
		issue linkIssue
		pr    linkPullRequest
	}{
		{want{"linear:CHAOS-1", zeroRepoUUID, ghRepo, 42, "native", 3, ghSlug}, linear, ghPR},
		{want{"jira:PROJ-2", zeroRepoUUID, glRepo, 43, "explicit_text", 2, glSlug}, jira, glPR},
		{want{"gh:acme/widget#7", ghRepo, ghRepo, 44, "heuristic", 1, ghSlug}, ghIssue, ghPR},
		{want{"gitlab:group/sub/proj#9", glRepo, glRepo, 45, "native", 3, glSlug}, glIssue, glPR},
		{want{"linear:CHAOS-1", zeroRepoUUID, glRepo, 46, "heuristic", 1, glSlug}, linear, glPR},
		{want{"jira:PROJ-2", zeroRepoUUID, ghRepo, 47, "explicit_text", 2, ghSlug}, jira, ghPR},
	}
	rows := make([][]any, 0, len(cases))
	for i, c := range cases {
		rows = append(rows, issuePullRequestLinkRow(c.want.issueID, c.want.prRepo, c.want.number, c.want.tier, at.Add(time.Duration(i)*time.Second), c.issue, c.pr))
	}
	links, counts := linkRun(t, linkTablesOnly(at, rows), linkCursor(t, at))
	if len(counts) != 0 {
		t.Fatalf("no row should be skipped, got %v", counts)
	}
	if len(links) != len(cases) {
		t.Fatalf("LINKS_PULL_REQUEST edges = %d, want %d: %+v", len(links), len(cases), links)
	}
	byID := map[string]contractsv1.ContextFabricRelationshipProjection{}
	for _, link := range links {
		byID[link.RelationshipID] = link
	}
	for _, c := range cases {
		issueID, _, err := identity.Derive(identity.KindWorkItem, []string{c.want.issueRepo, c.want.issueID}, nil)
		if err != nil {
			t.Fatal(err)
		}
		pullRequestID := fmt.Sprintf("pull_request:%s:%d", c.want.prRepo, c.want.number)
		id := identity.DeriveRelationship(identity.RelationshipFamilyIssuePullRequestLink, issueID, pullRequestID, "LINKS_PULL_REQUEST")
		link, ok := byID[id]
		if !ok {
			t.Fatalf("no edge %s -> %s (id %s) in %+v", c.want.issueID, pullRequestID, id, links)
		}
		if link.From.Kind != contractsv1.ContextFabricSubjectWorkItem || link.From.CanonicalID != issueID {
			t.Errorf("%s: from = %+v, want work_item %s", c.want.issueID, link.From, issueID)
		}
		if link.To.Kind != contractsv1.ContextFabricSubjectPullRequest || link.To.CanonicalID != pullRequestID {
			t.Errorf("%s: to = %+v, want pull_request %s", c.want.issueID, link.To, pullRequestID)
		}
		if tier, rank := tierProperty(t, link); tier != c.want.tier || rank != c.want.rank {
			t.Errorf("%s -> %s: tier/rank = %s/%d, want %s/%d", c.want.issueID, pullRequestID, tier, rank, c.want.tier, c.want.rank)
		}
		if got := link.Authorization.RepositorySlugs; len(got) != 1 || got[0] != c.want.slug {
			t.Errorf("%s: authorization = %v, want the pull request's repository %q", c.want.issueID, got, c.want.slug)
		}
		if len(link.Properties) != 2 {
			t.Errorf("%s: properties = %v, want exactly tier and rank (no confidence, no evidence text)", c.want.issueID, link.Properties)
		}
		if len(link.EvidenceRefIDs) != 2 || link.SourceVersion != devhealthsource.ClickHouseSourceVersion {
			t.Errorf("%s: evidence refs %v / source version %q", c.want.issueID, link.EvidenceRefIDs, link.SourceVersion)
		}
	}
}

// TestIssuePullRequestLinkIssueEndUsesTheIssuesOwnRepo pins the trap in the
// table: work_graph_issue_pr.repo_id is the PULL REQUEST's repository. An
// issue of one repository linked to a pull request of another gets its
// canonical id from the issue's repo_id in work_items, never the row's.
func TestIssuePullRequestLinkIssueEndUsesTheIssuesOwnRepo(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	const issueRepo, prRepo = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	rows := [][]any{issuePullRequestLinkRow("gh:acme/issues#3", prRepo, 8, "explicit_text", at,
		linkIssue{repoID: issueRepo, itemType: "issue", created: at}, linkPullRequest{slug: "acme/code", created: at})}
	links, _ := linkRun(t, linkTablesOnly(at, rows), linkCursor(t, at))
	if len(links) != 1 {
		t.Fatalf("edges = %d, want 1", len(links))
	}
	wantFrom, _, _ := identity.Derive(identity.KindWorkItem, []string{issueRepo, "gh:acme/issues#3"}, nil)
	if links[0].From.CanonicalID != wantFrom {
		t.Fatalf("issue end = %q, want %q (the issue's own repo)", links[0].From.CanonicalID, wantFrom)
	}
	if strings.Contains(links[0].From.CanonicalID, prRepo) {
		t.Fatalf("issue end %q carries the pull request's repo_id", links[0].From.CanonicalID)
	}
	if links[0].To.CanonicalID != "pull_request:"+prRepo+":8" {
		t.Fatalf("pull request end = %q", links[0].To.CanonicalID)
	}
}

// TestIssuePullRequestLinkSkipsAreCountedByReason pins every skip: an
// unresolved work item, a pull-request-typed work item (pr and merge_request),
// a missing pull request node and an unknown provenance project nothing and
// are counted under their own reason, beside one good row that still projects.
func TestIssuePullRequestLinkSkipsAreCountedByReason(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	const repo = "11111111-1111-1111-1111-111111111111"
	issue := linkIssue{repoID: repo, itemType: "issue", created: at}
	pr := linkPullRequest{slug: "acme/widget", created: at}
	rows := [][]any{
		issuePullRequestLinkRow("gh:acme/widget#1", repo, 1, "native", at.Add(0*time.Second), issue, pr),
		issuePullRequestLinkRow("gh:acme/widget#2", repo, 2, "native", at.Add(1*time.Second), linkIssue{missing: true}, pr),
		issuePullRequestLinkRow("gh:acme/widget#3", repo, 3, "native", at.Add(2*time.Second), linkIssue{repoID: repo, itemType: "pr", created: at}, pr),
		issuePullRequestLinkRow("gh:acme/widget#4", repo, 4, "native", at.Add(3*time.Second), linkIssue{repoID: repo, itemType: "merge_request", created: at}, pr),
		issuePullRequestLinkRow("gh:acme/widget#5", repo, 5, "native", at.Add(4*time.Second), issue, linkPullRequest{missing: true}),
		issuePullRequestLinkRow("gh:acme/widget#6", repo, 6, "guess", at.Add(5*time.Second), issue, pr),
		issuePullRequestLinkRow("gh:acme/widget#7", repo, 7, "NATIVE", at.Add(6*time.Second), issue, pr),
	}
	links, counts := linkRun(t, linkTablesOnly(at, rows), linkCursor(t, at))
	if len(links) != 1 || links[0].To.CanonicalID != "pull_request:"+repo+":1" {
		t.Fatalf("edges = %+v, want only the good row's", links)
	}
	want := map[string]int{
		"issue_pull_request_link:unresolved_work_item":         1,
		"issue_pull_request_link:pull_request_typed_work_item": 2,
		"issue_pull_request_link:missing_pull_request_node":    1,
		"issue_pull_request_link:unknown_provenance":           2,
	}
	for reason, n := range want {
		if counts[reason] != n {
			t.Errorf("%s count = %d, want %d (all counts %v)", reason, counts[reason], n, counts)
		}
	}
	if len(counts) != len(want) {
		t.Errorf("counts = %v, want exactly %v", counts, want)
	}
}

// TestIssuePullRequestLinkCursorPagesOnLastSyncedWithADeterministicRowKey pins
// the keyset: rows at or before the cursor are not re-read, and rows sharing
// one last_synced resume strictly after the (repo:issue:number) row key.
func TestIssuePullRequestLinkCursorPagesOnLastSyncedWithADeterministicRowKey(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	const repo = "11111111-1111-1111-1111-111111111111"
	issue := linkIssue{repoID: repo, itemType: "issue", created: at.Add(-time.Hour)}
	pr := linkPullRequest{slug: "acme/widget", created: at.Add(-time.Hour)}
	rows := [][]any{
		issuePullRequestLinkRow("gh:acme/widget#1", repo, 1, "native", at, issue, pr),
		issuePullRequestLinkRow("gh:acme/widget#1", repo, 2, "native", at, issue, pr),
		issuePullRequestLinkRow("gh:acme/widget#1", repo, 3, "heuristic", at.Add(time.Second), issue, pr),
	}
	numbers := func(links []contractsv1.ContextFabricRelationshipProjection) []string {
		out := make([]string, 0, len(links))
		for _, link := range links {
			out = append(out, link.To.CanonicalID[strings.LastIndex(link.To.CanonicalID, ":")+1:])
		}
		return out
	}
	all, _ := linkRunCalls(t, linkTablesOnly(at, rows), devhealthsource.IngestCursorForTest(at.Add(-time.Hour), ""), 1)
	if got := strings.Join(numbers(all), ","); got != "1,2,3" {
		t.Fatalf("from the start: %s, want 1,2,3 in last_synced then row key order", got)
	}
	afterFirst, _ := linkRunCalls(t, linkTablesOnly(at, rows), devhealthsource.IngestCursorForTest(at, repo+":gh:acme/widget#1:1"), 1)
	if got := strings.Join(numbers(afterFirst), ","); got != "2,3" {
		t.Fatalf("after the first row key: %s, want 2,3 (the same-stamp row after the key, then the later stamp)", got)
	}
	for _, link := range all {
		if link.ObservedAt.IsZero() {
			t.Fatalf("edge %s has no observed_at", link.RelationshipID)
		}
	}
	if all[2].ObservedAt != at.Add(time.Second) {
		t.Fatalf("observed_at = %s, want the row's last_synced", all[2].ObservedAt)
	}
}

// TestIssuePullRequestLinkOnlyPageAdvancesPastSkippedRows pins progress: a
// page whose rows are ALL skipped publishes nothing, and the consumed-progress
// memo is the only way the checkpoint moves past them.
func TestIssuePullRequestLinkOnlyPageAdvancesPastSkippedRows(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	rows := [][]any{issuePullRequestLinkRow("gh:acme/widget#1", "11111111-1111-1111-1111-111111111111", 1, "guess", at, linkIssue{missing: true}, linkPullRequest{missing: true})}
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: linkTablesOnly(at, rows)})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: linkCursor(t, at)}
	if _, available, err := source.NextProjectionBatch(context.Background(), checkpoint); err != nil || available {
		t.Fatalf("err=%v available=%v, want no batch and no error", err, available)
	}
	if _, ok, err := source.ConsumedWithoutPublishing(context.Background(), checkpoint); err != nil || !ok {
		t.Fatalf("consumed progress ok=%v err=%v: the checkpoint cannot advance past skipped rows", ok, err)
	}
}
