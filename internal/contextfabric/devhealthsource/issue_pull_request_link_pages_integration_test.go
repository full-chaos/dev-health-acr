package devhealthsource_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The skip reasons queryIssuePullRequestLinks counts (issue_pull_request_link.go).
const (
	linkSkipUnknownTier = "issue_pull_request_link:unknown_provenance"
	linkSkipUnresolved  = "issue_pull_request_link:unresolved_work_item"
	linkSkipPRTyped     = "issue_pull_request_link:pull_request_typed_work_item"
	linkSkipMissingPR   = "issue_pull_request_link:missing_pull_request_node"
)

// linkPageLimit is smaller than the 14 link rows the fixture holds after
// FINAL, so the producer reads five keyset pages (3+3+3+3+2).
const linkPageLimit = 3

type linkSeedRow struct {
	issue  string
	repo   string // the PULL REQUEST's repository
	number uint32
	tier   string
	at     time.Time
}

// requireLinkSeed is the seed guard: the table must hold exactly want rows for
// the org after FINAL (and rawWant before it). An empty or short store is an
// error, never a quiet pass.
func requireLinkSeed(ctx context.Context, direct clickhousedriver.Conn, orgID string, want, rawWant uint64) error {
	var final, raw uint64
	if err := direct.QueryRow(ctx, `SELECT count() FROM work_graph_issue_pr FINAL WHERE org_id = ?`, orgID).Scan(&final); err != nil {
		return fmt.Errorf("count work_graph_issue_pr FINAL: %w", err)
	}
	if err := direct.QueryRow(ctx, `SELECT count() FROM work_graph_issue_pr WHERE org_id = ?`, orgID).Scan(&raw); err != nil {
		return fmt.Errorf("count work_graph_issue_pr: %w", err)
	}
	if final != want || raw != rawWant {
		return fmt.Errorf("work_graph_issue_pr seed: FINAL rows = %d (want %d), physical rows = %d (want %d)", final, want, raw, rawWant)
	}
	return nil
}

// TestIssuePullRequestLinkProducerPagesOnPopulatedStore drives
// queryIssuePullRequestLinks page by page, with a page limit smaller than the
// row count, over a populated ClickHouse (all three tiers, one issue with two
// links of different tiers, one pair written twice with different tiers, a
// repo-less issue, a link whose pull request row is absent, an unresolved
// issue, pull-request-typed work items and unknown tiers). It then projects
// the same store into a real FalkorDB and reads the edges back.
//
// It has no Skip: a missing container runtime fails inside the fixture
// (t.Fatalf), an empty or short table fails the seed guard, and a missing
// table fails the producer. Needs Docker; run by CI, not by the authoring
// sandbox.
func TestIssuePullRequestLinkProducerPagesOnPopulatedStore(t *testing.T) {
	ctx := context.Background()
	const zeroRepo = "00000000-0000-0000-0000-000000000000"
	orgID := "12000000-0000-4000-8000-000000000002"
	repoGH, repoGL := o3UUID(orgID+"gh"), o3UUID(orgID+"gl")
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := base.Add(-90 * 24 * time.Hour)

	t.Run("populated store: keyset pages return every row once", func(t *testing.T) {
		query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
		applyProductionSchema(t, ctx, direct, "")
		adapter := chaos7074FalkorAdapter(t, ctx)
		exec := func(label, statement string, args ...any) {
			t.Helper()
			if err := direct.Exec(ctx, statement, args...); err != nil {
				t.Fatalf("seed %s: %v", label, err)
			}
		}
		exec("repo gh", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repoGH, orgID, "acme/widget", "github", base)
		exec("repo gl", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repoGL, orgID, "group/proj", "gitlab", base)

		type issue struct{ id, repo, itemType, provider string }
		issues := []issue{
			{"linear:CHAOS-1", zeroRepo, "issue", "linear"}, // repository-less
			{"jira:PROJ-2", zeroRepo, "story", "jira"},      // repository-less
			{"gh:acme/widget#7", repoGH, "issue", "github"},
			{"gitlab:group/proj#9", repoGL, "issue", "gitlab"},
			{"gh:acme/widget#50", repoGH, "pr", "github"},
			{"gitlab:group/proj#51", repoGL, "merge_request", "gitlab"},
		}
		issueRepo := map[string]string{}
		for _, i := range issues {
			issueRepo[i.id] = i.repo
			exec("work item "+i.id, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, provider, created_at, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				i.id, i.repo, orgID, i.id, i.itemType, "open", i.provider, created, base, base)
		}
		// Pull request rows exist for GH 42,44,47,48 and GL 43,45,46. GH 999 has none.
		for _, p := range []struct {
			repo   string
			number uint32
		}{{repoGH, 42}, {repoGH, 44}, {repoGH, 47}, {repoGH, 48}, {repoGL, 43}, {repoGL, 45}, {repoGL, 46}} {
			exec(fmt.Sprintf("pull request %s#%d", p.repo, p.number), `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, created_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				p.repo, orgID, p.number, fmt.Sprintf("PR %d", p.number), "open", created, base)
		}

		at := func(minute int) time.Time { return base.Add(time.Duration(minute) * time.Minute) }
		// Every row has a distinct last_synced, so the keyset order is defined.
		rows := []linkSeedRow{
			// projectable: 8 distinct (issue, pull request) pairs
			{"linear:CHAOS-1", repoGH, 42, "native", at(1)},
			{"jira:PROJ-2", repoGL, 43, "explicit_text", at(2)},
			{"gh:acme/widget#7", repoGH, 44, "native", at(3)}, // written twice, see below
			{"gitlab:group/proj#9", repoGL, 45, "native", at(4)},
			{"linear:CHAOS-1", repoGL, 46, "heuristic", at(5)}, // same issue, different tier than its first link
			{"jira:PROJ-2", repoGH, 47, "explicit_text", at(6)},
			{"gh:acme/widget#7", repoGH, 48, "native", at(7)},
			{"gitlab:group/proj#9", repoGL, 43, "heuristic", at(8)},
			// the same pair again at a LATER stamp and a LOWER tier: FINAL keeps native
			{"gh:acme/widget#7", repoGH, 44, "heuristic", at(9)},
			// skipped rows
			{"linear:GHOST-1", repoGH, 42, "native", at(10)},       // no work_items row
			{"gh:acme/widget#50", repoGH, 42, "native", at(11)},    // type pr
			{"gitlab:group/proj#51", repoGL, 43, "native", at(12)}, // type merge_request
			{"linear:CHAOS-1", repoGH, 999, "native", at(13)},      // no git_pull_requests row
			{"jira:PROJ-2", repoGH, 42, "guess", at(14)},           // tier outside the vocabulary
			{"gh:acme/widget#7", repoGL, 43, "NOT_A_TIER", at(15)}, // tier outside the vocabulary
		}
		// Background merges would collapse the duplicated pair before the
		// read and hide whether the producer reads FINAL: stop them for this
		// table while the test runs.
		exec("stop merges", `SYSTEM STOP MERGES work_graph_issue_pr`)
		t.Cleanup(func() { _ = direct.Exec(context.Background(), `SYSTEM START MERGES work_graph_issue_pr`) })
		for i, r := range rows {
			exec(fmt.Sprintf("link %d", i), `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				r.repo, r.issue, r.number, float32(0.9), r.tier, "", r.at, orgID)
		}
		// 15 rows written, 14 distinct keys: the duplicated pair collapses.
		if err := requireLinkSeed(ctx, direct, orgID, 14, 15); err != nil {
			t.Fatal(err)
		}

		// Expected edges: relationship id -> tier. The duplicated pair is native.
		wantTier := map[string]string{}
		wantRank := map[string]int64{"native": 3, "explicit_text": 2, "heuristic": 1}
		wantFrom := map[string]string{}
		wantTo := map[string]string{}
		for _, r := range rows[:8] {
			issueID, _, err := identity.Derive(identity.KindWorkItem, []string{issueRepo[r.issue], r.issue}, nil)
			if err != nil {
				t.Fatal(err)
			}
			prID := fmt.Sprintf("pull_request:%s:%d", r.repo, r.number)
			id := identity.DeriveRelationship(identity.RelationshipFamilyIssuePullRequestLink, issueID, prID, "LINKS_PULL_REQUEST")
			wantTier[id], wantFrom[id], wantTo[id] = r.tier, issueID, prID
		}
		if len(wantTier) != 8 {
			t.Fatalf("fixture holds %d projectable pairs, want 8", len(wantTier))
		}
		wantSkips := map[string]int{linkSkipUnknownTier: 2, linkSkipUnresolved: 1, linkSkipPRTyped: 2, linkSkipMissingPR: 1}

		// Walk the keyset pages the way the projection source does: the cursor
		// is the last row's position and row key.
		gotRel := map[string]devhealthsource.IssuePullRequestLinkRowForTest{}
		gotSkips := map[string]int{}
		seenKeys := map[string]bool{}
		var since time.Time
		var after string
		pages, total := 0, 0
		for {
			page, more, err := devhealthsource.IssuePullRequestLinkPageForTest(ctx, query, orgID, since, after, linkPageLimit)
			if err != nil {
				t.Fatalf("page %d: %v", pages, err)
			}
			pages++
			if pages > 20 {
				t.Fatal("keyset did not terminate")
			}
			if len(page) == 0 || len(page) > linkPageLimit {
				t.Fatalf("page %d holds %d rows, want 1..%d", pages, len(page), linkPageLimit)
			}
			for i, row := range page {
				if seenKeys[row.SortKey] {
					t.Errorf("row %s returned twice across pages", row.SortKey)
				}
				seenKeys[row.SortKey] = true
				total++
				// strictly after the cursor, and ascending within the page
				if !since.IsZero() && (row.Position.Before(since) || (row.Position.Equal(since) && row.SortKey <= after)) {
					t.Errorf("page %d row %s is not after the cursor (%s, %s)", pages, row.SortKey, since, after)
				}
				if i > 0 && !(page[i-1].Position.Before(row.Position) || (page[i-1].Position.Equal(row.Position) && page[i-1].SortKey < row.SortKey)) {
					t.Errorf("page %d rows out of keyset order at %d", pages, i)
				}
				switch {
				case row.IgnoredReason != "":
					gotSkips[row.IgnoredReason]++
				case row.RelationshipID != "":
					if _, dup := gotRel[row.RelationshipID]; dup {
						t.Errorf("relationship %s emitted twice", row.RelationshipID)
					}
					gotRel[row.RelationshipID] = row
				default:
					t.Errorf("row %s is neither an edge nor a counted skip", row.SortKey)
				}
			}
			last := page[len(page)-1]
			if !last.Position.After(since) && !(last.Position.Equal(since) && last.SortKey > after) {
				t.Fatalf("page %d did not advance the cursor", pages)
			}
			since, after = last.Position, last.SortKey
			if !more {
				break
			}
			if len(page) != linkPageLimit {
				t.Fatalf("page %d reports more rows but holds %d (< %d)", pages, len(page), linkPageLimit)
			}
		}
		if pages < 2 {
			t.Fatalf("read %d page(s); the fixture must cross at least one cursor page", pages)
		}
		if total != 14 {
			t.Errorf("pages returned %d rows in all, want 14 (every row once)", total)
		}
		// The page after the last one is empty, so the cursor really ended.
		tail, tailMore, err := devhealthsource.IssuePullRequestLinkPageForTest(ctx, query, orgID, since, after, linkPageLimit)
		if err != nil || len(tail) != 0 || tailMore {
			t.Errorf("page past the end = %d rows, more=%v, err=%v; want empty", len(tail), tailMore, err)
		}

		if len(gotRel) != len(wantTier) {
			t.Errorf("edges = %d, want %d", len(gotRel), len(wantTier))
		}
		for id, tier := range wantTier {
			row, ok := gotRel[id]
			if !ok {
				t.Errorf("edge %s -> %s missing", wantFrom[id], wantTo[id])
				continue
			}
			if row.Tier != tier || row.Rank != wantRank[tier] {
				t.Errorf("edge %s -> %s: tier/rank = %s/%d, want %s/%d", wantFrom[id], wantTo[id], row.Tier, row.Rank, tier, wantRank[tier])
			}
			if row.From != wantFrom[id] || row.To != wantTo[id] {
				t.Errorf("edge %s ends = %s -> %s, want %s -> %s", id, row.From, row.To, wantFrom[id], wantTo[id])
			}
		}
		for id := range gotRel {
			if _, ok := wantTier[id]; !ok {
				t.Errorf("unexpected edge %s (a skipped row projected)", id)
			}
		}
		for reason, want := range wantSkips {
			if gotSkips[reason] != want {
				t.Errorf("skip %s = %d, want %d", reason, gotSkips[reason], want)
			}
		}
		if len(gotSkips) != len(wantSkips) {
			t.Errorf("skip reasons = %v, want %v", gotSkips, wantSkips)
		}

		// The same store through the real projection source and a real graph.
		source, err := devhealthsource.NewClickHouseProjectionSource(query)
		if err != nil {
			t.Fatal(err)
		}
		drainSource(t, ctx, source, adapter, orgID, devhealthsource.SourceName)
		principal := storage.Principal{OrgID: orgID, Subject: "link-pages", CredentialID: "link-pages"}
		binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
		if err != nil {
			t.Fatalf("resolve graph binding: %v", err)
		}
		var origins []contextfabric.SubjectRef
		for _, p := range []struct {
			repo   string
			number int
		}{{repoGH, 42}, {repoGH, 44}, {repoGH, 47}, {repoGH, 48}, {repoGL, 43}, {repoGL, 45}, {repoGL, 46}} {
			origins = append(origins, contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: fmt.Sprintf("pull_request:%s:%d", p.repo, p.number)})
		}
		edges, err := adapter.DirectEdgePage(ctx, principal, binding, directread.EdgePageQuery{
			Origins: origins, Types: []string{"LINKS_PULL_REQUEST"}, Direction: directread.EdgeDirectionIn, Limit: 100, ValidAt: base.Add(24 * time.Hour),
		})
		if err != nil {
			t.Fatalf("direct edge page: %v", err)
		}
		if edges.More {
			t.Fatal("more edges than the page held")
		}
		gotGraph := map[string]string{}
		for _, e := range edges.Edges {
			key := e.From.Subject.CanonicalID + " -> " + e.To.Subject.CanonicalID
			if _, dup := gotGraph[key]; dup {
				t.Errorf("graph edge %s stored twice", key)
			}
			// persisted edge properties carry the node property prefix
			gotGraph[key] = fmt.Sprint(e.Attributes["property_link_provenance"]) + "/" + fmt.Sprint(e.Attributes["property_link_provenance_rank"])
		}
		wantGraph := map[string]string{}
		for id, tier := range wantTier {
			wantGraph[wantFrom[id]+" -> "+wantTo[id]] = fmt.Sprintf("%s/%d", tier, wantRank[tier])
		}
		if len(gotGraph) != len(wantGraph) {
			t.Errorf("graph LINKS_PULL_REQUEST edges = %d, want %d\n got  %v\n want %v", len(gotGraph), len(wantGraph), sortedPairs(gotGraph), sortedPairs(wantGraph))
		}
		for key, want := range wantGraph {
			if gotGraph[key] != want {
				t.Errorf("graph edge %s: tier/rank = %q, want %q", key, gotGraph[key], want)
			}
		}
		for key := range gotGraph {
			if _, ok := wantGraph[key]; !ok {
				t.Errorf("unexpected graph edge %s", key)
			}
		}
		// The issue with two links of different tiers holds two edges, one per tier.
		chaos1, _, _ := identity.Derive(identity.KindWorkItem, []string{zeroRepo, "linear:CHAOS-1"}, nil)
		tiers := []string{}
		for key, v := range gotGraph {
			if strings.HasPrefix(key, chaos1+" -> ") {
				tiers = append(tiers, v)
			}
		}
		sort.Strings(tiers)
		if strings.Join(tiers, ",") != "heuristic/1,native/3" {
			t.Errorf("linear:CHAOS-1 edge tiers = %v, want [heuristic/1 native/3]", tiers)
		}
	})

	t.Run("seed guard fails on an empty table; producer returns nothing", func(t *testing.T) {
		query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
		applyProductionSchema(t, ctx, direct, "")
		if err := requireLinkSeed(ctx, direct, orgID, 14, 15); err == nil {
			t.Fatal("seed guard passed on an EMPTY work_graph_issue_pr; an empty store must fail the test")
		}
		page, more, err := devhealthsource.IssuePullRequestLinkPageForTest(ctx, query, orgID, time.Time{}, "", linkPageLimit)
		if err != nil || len(page) != 0 || more {
			t.Fatalf("empty table: rows=%d more=%v err=%v, want 0 rows and no error", len(page), more, err)
		}
	})

	t.Run("seed guard fails on a short table", func(t *testing.T) {
		_, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
		applyProductionSchema(t, ctx, direct, "")
		if err := direct.Exec(ctx, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoGH, "linear:CHAOS-1", uint32(42), float32(0.9), "native", "", base, orgID); err != nil {
			t.Fatal(err)
		}
		if err := requireLinkSeed(ctx, direct, orgID, 14, 15); err == nil {
			t.Fatal("seed guard passed on a 1-row table")
		}
	})

	t.Run("dropped table is a producer error, not an empty success", func(t *testing.T) {
		query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
		applyProductionSchema(t, ctx, direct, "")
		if err := direct.Exec(ctx, `DROP TABLE work_graph_issue_pr SYNC`); err != nil {
			t.Fatal(err)
		}
		page, _, err := devhealthsource.IssuePullRequestLinkPageForTest(ctx, query, orgID, time.Time{}, "", linkPageLimit)
		if err == nil {
			t.Fatalf("producer returned %d rows and no error for a missing work_graph_issue_pr", len(page))
		}
	})

	t.Run("database without the table is a producer error", func(t *testing.T) {
		query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
		applyProductionSchema(t, ctx, direct, "work_graph_issue_pr")
		page, _, err := devhealthsource.IssuePullRequestLinkPageForTest(ctx, query, orgID, time.Time{}, "", linkPageLimit)
		if err == nil {
			t.Fatalf("producer returned %d rows and no error for a database without work_graph_issue_pr", len(page))
		}
	})
}

// applyProductionSchema applies the shared production DDL, leaving out every
// statement that mentions omit (when non-empty).
func applyProductionSchema(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, omit string) {
	t.Helper()
	for _, statement := range productionSchemaDDL() {
		if omit != "" && strings.Contains(statement, omit) {
			continue
		}
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("apply rendered schema statement: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
}
