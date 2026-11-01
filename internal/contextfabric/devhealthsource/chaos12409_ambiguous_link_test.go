package devhealthsource_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// A row whose id has several work item rows gives no edge and is counted. A
// native one names its pull request, and never an issue; a lower tier names
// nothing. The tier checks come first, as for every row: a row the tier rule
// drops is counted there. A pull-request-typed row counts as a row of the id.
func TestAnAmbiguousLinkRowGivesNoEdgeAndNamesOnlyItsPullRequest(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	const repo = "11111111-1111-1111-1111-111111111111"
	issue := linkIssue{repoID: repo, itemType: "issue", created: at}
	pr := linkPullRequest{slug: "acme/widget", created: at}
	outrankedAndAmbiguous := outrankedLinkRow("X-5", repo, 15, "explicit_text", at.Add(5*time.Second), issue, pr)
	outrankedAndAmbiguous[len(outrankedAndAmbiguous)-1] = uint8(1)
	rows := [][]any{
		ambiguousLinkRow("X-1", repo, 11, "native", at.Add(1*time.Second), issue, pr),
		ambiguousLinkRow("X-2", repo, 12, "heuristic", at.Add(2*time.Second), issue, pr),
		ambiguousLinkRow("X-3", repo, 13, "native", at.Add(3*time.Second), issue, linkPullRequest{missing: true}),
		ambiguousLinkRow("X-4", repo, 14, "native", at.Add(4*time.Second), linkIssue{repoID: repo, itemType: "pr", created: at}, pr),
		outrankedAndAmbiguous,
		ambiguousLinkRow("X-6", repo, 16, "guess", at.Add(6*time.Second), issue, pr),
		ambiguousLinkRow("X-7", repo, 17, "explicit_text", at.Add(7*time.Second), issue, pr),
		issuePullRequestLinkRow("Y-1", repo, 18, "native", at.Add(8*time.Second), issue, pr),
	}
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: linkTablesOnly(at, rows)})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	source = source.WithLogger(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	var edges, tombstones []string
	cursor := linkCursor(t, at)
	for call := 0; call < 10; call++ {
		batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if !available {
			break
		}
		if err := batch.Validate(); err != nil {
			t.Fatal(err)
		}
		for _, relationship := range batch.Relationships {
			edges = append(edges, relationship.To.CanonicalID)
		}
		for _, tombstone := range batch.Tombstones {
			tombstones = append(tombstones, tombstone.Kind+"|"+tombstone.CanonicalID)
		}
		cursor = batch.NextCursor
	}
	if want := "[pull_request:" + repo + ":18]"; fmt.Sprint(edges) != want {
		t.Errorf("edges = %v, want %s: only the id with one row", edges, want)
	}
	sort.Strings(tombstones)
	want := []string{
		contextfabric.TombstoneKindOutrankedPullRequestLinks + "|pull_request:" + repo + ":11",
		contextfabric.TombstoneKindOutrankedPullRequestLinks + "|pull_request:" + repo + ":14",
	}
	if fmt.Sprint(tombstones) != fmt.Sprint(want) {
		t.Errorf("tombstones = %v, want %v: a native row names its pull request and no issue, a lower tier names nothing", tombstones, want)
	}
	counts := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		if msg, _ := entry["msg"].(string); strings.Contains(msg, ignoredLine) {
			counts[entry["ignored_relationship_type"].(string)] += int(entry["ignored_count"].(float64))
		}
	}
	wantCounts := map[string]int{
		"issue_pull_request_link:ambiguous_links":     5,
		"issue_pull_request_link:outranked_by_native": 1,
		"issue_pull_request_link:unknown_provenance":  1,
	}
	if fmt.Sprint(counts) != fmt.Sprint(wantCounts) {
		t.Errorf("counts = %v, want %v", counts, wantCounts)
	}
}
