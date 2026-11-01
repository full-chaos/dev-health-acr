package devhealthsource_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const linkSkipAmbiguous = "issue_pull_request_link:ambiguous_links"

// ambiguousLinkOrg seeds one organization: acme/local (the repository of the
// pull requests) and acme/foreign, and the two projects, for an issue provider.
type ambiguousLinkOrg struct {
	t              *testing.T
	ctx            context.Context
	direct         clickhousedriver.Conn
	org, provider  string
	local, foreign string
	events         int
}

// projectID is a project id as the provider spells it.
func (o *ambiguousLinkOrg) projectID(name string) string {
	if o.provider == "github" {
		return "ghprojv2:" + name
	}
	return name
}

func (o *ambiguousLinkOrg) exec(statement string, args ...any) {
	o.t.Helper()
	mustExec(o.t, o.ctx, o.direct, statement, args...)
}

func (o *ambiguousLinkOrg) repositories(pullRequestProvider string, at time.Time) {
	o.t.Helper()
	o.exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, 'acme/local', ?, ?)`, o.local, o.org, pullRequestProvider, at)
	o.exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, 'acme/foreign', ?, ?)`, o.foreign, o.org, pullRequestProvider, at)
	for _, name := range []string{"THEIRS", "OURS"} {
		o.exec(`INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, '', ?, 'started', '', 1, ?)`,
			o.projectID(name), o.org, "Project "+name, o.provider, at)
	}
}

// issue seeds one work_items row, in a project when one is named.
func (o *ambiguousLinkOrg) issue(id, repo, project string, at time.Time) {
	o.t.Helper()
	o.item(id, repo, project, "issue", at)
}

// item seeds one work_items row of a type.
func (o *ambiguousLinkOrg) item(id, repo, project, itemType string, at time.Time) {
	o.t.Helper()
	column := ""
	if project != "" && o.provider != "gitlab" {
		column = o.projectID(project)
	}
	o.exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, type, url, parent_id, provider, project_id, updated_at, created_at, last_synced) VALUES (?, ?, ?, ?, 'open', ?, '', '', ?, ?, ?, ?, ?)`,
		id, repo, o.org, "Item "+id, itemType, o.provider, column, at, at, at)
	if project != "" && o.provider == "gitlab" {
		o.events++
		o.exec(`INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at) VALUES (?, NULL, ?, 'work_item', ?, ?, '', ?, '', '', '', ?, ?, ?, ?)`,
			o.org, repo, id, o.provider, o.projectID(project), at, at, fmt.Sprintf("ev-%d", o.events), at)
	}
}

func (o *ambiguousLinkOrg) pullRequest(number uint32, at time.Time) {
	o.t.Helper()
	o.exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (?, ?, ?, 'PR', 'open', ?)`, o.local, o.org, number, at)
}

func (o *ambiguousLinkOrg) link(id string, number uint32, tier string, at time.Time) {
	o.t.Helper()
	o.exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (toUUID(?), ?, ?, 0.9, ?, '', ?, ?)`,
		o.local, id, number, tier, at, o.org)
}

func (o *ambiguousLinkOrg) projectIdentity(name string) string {
	o.t.Helper()
	canonical, omitted, err := identity.Derive(identity.KindProject, []string{o.provider, o.projectID(name)}, nil)
	if err != nil || omitted {
		o.t.Fatalf("derive project: %v %v", err, omitted)
	}
	return canonical
}

// ambiguousLinkRun is what one pass of the real producer did.
type ambiguousLinkRun struct {
	cursor      string
	edges       []string
	ignored     map[string]int
	pullRequest map[uint32]string
	anchor      contextfabric.SubjectRef
	stated      []string
}

// project runs the real producer from cursor into the real graph.
func (o *ambiguousLinkOrg) project(query contextpacket.ClickHouseQueryClient, adapter *falkorgraph.Adapter, cursor string) ambiguousLinkRun {
	o.t.Helper()
	var buf bytes.Buffer
	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		o.t.Fatal(err)
	}
	source = source.WithLogger(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	source.WithWorkItemProjectMembership(true)
	run := ambiguousLinkRun{ignored: map[string]int{}, pullRequest: map[uint32]string{}}
	for page := 0; ; page++ {
		if page == 40 {
			o.t.Fatal("the producer did not catch up in 40 pages")
		}
		batch, available, err := source.NextProjectionBatch(o.ctx, contextfabric.ProjectionCheckpoint{OrgID: o.org, Source: devhealthsource.SourceName, Cursor: cursor})
		if err != nil {
			o.t.Fatalf("page %d: %v", page, err)
		}
		if !available {
			break
		}
		if err := batch.Validate(); err != nil {
			o.t.Fatalf("page %d: %v", page, err)
		}
		for _, entity := range batch.Entities {
			switch {
			case entity.Subject.Kind == contractsv1.ContextFabricSubjectRepository && entity.Subject.Label == "acme/local":
				run.anchor = entity.Subject
			case entity.Subject.Kind == contractsv1.ContextFabricSubjectPullRequest:
				cut := strings.LastIndexByte(entity.Subject.CanonicalID, ':')
				var number uint32
				if _, err := fmt.Sscan(entity.Subject.CanonicalID[cut+1:], &number); err != nil {
					o.t.Fatal(err)
				}
				run.pullRequest[number] = entity.Subject.CanonicalID
				if entity.ProjectIdentities != nil {
					run.stated = *entity.ProjectIdentities
				}
			}
		}
		for _, relationship := range batch.Relationships {
			if relationship.Type == contractsv1.ContextFabricRelationshipLinksPullRequest {
				run.edges = append(run.edges, relationship.From.CanonicalID)
			}
		}
		if _, err := adapter.ApplyProjectionBatch(o.ctx, batch); err != nil {
			o.t.Fatalf("apply page %d: %v", page, err)
		}
		cursor = batch.NextCursor
	}
	run.cursor = cursor
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			o.t.Fatalf("decode log line %q: %v", line, err)
		}
		if msg, _ := entry["msg"].(string); strings.Contains(msg, ignoredLine) {
			run.ignored[entry["ignored_relationship_type"].(string)] += int(entry["ignored_count"].(float64))
		}
	}
	sort.Strings(run.edges)
	return run
}

// The matrix of AC3: pull requests of github and gitlab, issues of every
// provider.
func eachAmbiguousLinkPair(t *testing.T, run func(t *testing.T, pullRequestProvider, provider string)) {
	t.Helper()
	for _, pullRequestProvider := range []string{"github", "gitlab"} {
		for _, provider := range []string{"github", "gitlab", "jira", "linear"} {
			t.Run(pullRequestProvider+"/"+provider, func(t *testing.T) { run(t, pullRequestProvider, provider) })
		}
	}
}

// work_items is keyed by (org_id, repo_id, work_item_id) and a link row names
// no issue repository, so nothing can say which of two rows a link means, in
// either direction: the swapped shape is the case where the link means the
// repository-less row. So an id with several rows gives no edge at all and is
// counted in ambiguous_links, and the edge count takes the same rule. Before
// this rule the wrong edge reached the graph and the walk, and the walk's rule
// for a repository-less issue gave a repository-restricted caller an issue that
// no grant and no true link gave it. An id with one row stays, in any
// repository: the one-row shape is the walk's rule on a true link.
func TestLiveAnAmbiguousLinkIdGivesNoEdge(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	applyProductionSchema(t, ctx, direct, "")
	adapter := chaos7074FalkorAdapter(t, ctx)
	keyring := directread.CursorKeyring{ActiveKID: "k1", Keys: map[string][]byte{"k1": []byte("0123456789abcdef0123456789abcdef-test-cursor-key")}}
	reader, err := directread.NewRelationshipsReader(directread.NewSubjectGate(adapter, nil), adapter, nil, keyring)
	if err != nil {
		t.Fatal(err)
	}
	const local, foreign = "77777777-7777-7777-7777-777777777777", "88888888-8888-8888-8888-888888888888"
	at := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	workItem := func(t *testing.T, repo, id string) string {
		t.Helper()
		canonical, omitted, err := identity.Derive(identity.KindWorkItem, []string{repo, id}, nil)
		if err != nil || omitted {
			t.Fatalf("derive work item: %v %v", err, omitted)
		}
		return canonical
	}
	calls := 0
	for _, shape := range []struct {
		name string
		// rows are the (repository, project, type) rows of X-1.
		rows      [][3]string
		tier      string
		ambiguous int
	}{
		{name: "two-repositories", rows: [][3]string{{foreign, "THEIRS", "issue"}, {local, "OURS", "issue"}}, tier: "native", ambiguous: 1},
		{name: "repository-less", rows: [][3]string{{foreign, "THEIRS", "issue"}, {zeroRepositoryUUID, "OURS", "issue"}}, tier: "native", ambiguous: 1},
		{name: "swapped", rows: [][3]string{{zeroRepositoryUUID, "THEIRS", "issue"}, {foreign, "OURS", "issue"}}, tier: "native", ambiguous: 1},
		{name: "explicit-text", rows: [][3]string{{foreign, "THEIRS", "issue"}, {zeroRepositoryUUID, "OURS", "issue"}}, tier: "explicit_text", ambiguous: 1},
		// A pull-request-typed row counts: it can be what the link names.
		{name: "pull-request-typed", rows: [][3]string{{foreign, "THEIRS", "pr"}, {zeroRepositoryUUID, "OURS", "issue"}}, tier: "native", ambiguous: 1},
		{name: "one-row", rows: [][3]string{{zeroRepositoryUUID, "OURS", "issue"}}, tier: "native"},
	} {
		t.Run(shape.name, func(t *testing.T) {
			eachAmbiguousLinkPair(t, func(t *testing.T, pullRequestProvider, provider string) {
				org := &ambiguousLinkOrg{t: t, ctx: ctx, direct: direct, org: strings.Join([]string{"org-12409", shape.name, pullRequestProvider, provider}, "-"), provider: provider, local: local, foreign: foreign}
				org.repositories(pullRequestProvider, at)
				for _, row := range shape.rows {
					org.item("X-1", row[0], row[1], row[2], at)
				}
				// A control: one row, so the pull request has a project and a
				// walk that reads it returns a member.
				org.issue("Y-1", local, "OURS", at)
				org.pullRequest(1, at)
				org.link("X-1", 1, shape.tier, at)
				org.link("Y-1", 1, shape.tier, at)

				run := org.project(query, adapter, "")
				if run.anchor.CanonicalID == "" || run.pullRequest[1] == "" {
					t.Fatalf("the seed did not project: anchor %q, pull request %q", run.anchor.CanonicalID, run.pullRequest[1])
				}
				want := []string{workItem(t, local, "Y-1")}
				if shape.ambiguous == 0 {
					want = append(want, workItem(t, shape.rows[0][0], "X-1"))
				}
				sort.Strings(want)
				if !slices.Equal(run.edges, want) {
					t.Errorf("edges = %v, want %v", run.edges, want)
				}
				if got := run.ignored[linkSkipAmbiguous]; got != shape.ambiguous {
					t.Errorf("ambiguous_links = %d, want %d (ignored %v)", got, shape.ambiguous, run.ignored)
				}

				// The count and the graph hold the same edges.
				source, err := devhealthsource.NewClickHouseProjectionSource(query)
				if err != nil {
					t.Fatal(err)
				}
				counts, err := source.ProjectionSourceEdgeCounts(ctx, org.org)
				if err != nil {
					t.Fatal(err)
				}
				counted, stored := int64(0), int64(0)
				for key, total := range counts {
					if key.Relation != contractsv1.ContextFabricRelationshipLinksPullRequest {
						continue
					}
					counted += total
					inGraph, err := adapter.CountEdges(ctx, org.org, key)
					if err != nil {
						t.Fatal(err)
					}
					stored += inGraph
				}
				if counted != int64(len(want)) || stored != int64(len(want)) {
					t.Errorf("edge count = %d, graph = %d, want both %d", counted, stored, len(want))
				}

				// The walk: an unrestricted and a repository-restricted caller.
				for name, principal := range map[string]storage.Principal{
					"unrestricted":          {OrgID: org.org},
					"repository-restricted": {OrgID: org.org, RepositoryScopes: []string{"acme/local"}},
				} {
					binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
					if err != nil {
						t.Fatal(err)
					}
					walk, err := adapter.TreeWorkItemMembers(ctx, principal, binding, contextfabric.RequestedScope{}, run.anchor, 25)
					if err != nil {
						t.Fatal(err)
					}
					members := make([]string, 0, len(walk.Members))
					for _, member := range walk.Members {
						members = append(members, member.Subject.CanonicalID)
					}
					sort.Strings(members)
					if !slices.Equal(members, want) {
						t.Errorf("%s walk members = %v, want %v", name, members, want)
					}
				}

				// A project-only caller reads a link through read_relationships:
				// its repository walk returns nothing with or without this rule.
				// A lower tier grants it no authority, so that pull request is denied.
				if shape.tier != "native" {
					return
				}
				if !slices.Contains(run.stated, org.projectIdentity("OURS")) {
					t.Fatalf("control: the pull request states %v, not OURS, so this read measures nothing", run.stated)
				}
				calls++
				sum := sha256.Sum256([]byte(fmt.Sprintf("12409-%d", calls)))
				response, err := reader.Read(observability.WithRequestID(ctx, "req_"+hex.EncodeToString(sum[:16])), storage.Principal{OrgID: org.org, ProjectScopes: []string{org.projectIdentity("OURS")}}, directread.RelationshipsRequest{
					Subject: directread.RelationshipsSubject{Kind: "pull_request", CanonicalID: run.pullRequest[1]},
					Types:   []string{"LINKS_PULL_REQUEST"}, Direction: "in", Limit: 100,
				})
				if err != nil {
					t.Fatal(err)
				}
				served := make([]string, 0, len(response.Edges))
				for _, edge := range response.Edges {
					served = append(served, edge.From.CanonicalID)
				}
				sort.Strings(served)
				if !slices.Equal(served, want) {
					t.Errorf("project-only read = %v (%s), want %v", served, response.Status, want)
				}
			})
		})
	}
}

// An ambiguous native row still shows that the provider attached an issue to
// that pull request, so its lower-tier links are retracted (the entity-tree
// rule). It names no issue: covering one of the two rows could retract a link
// of an unrelated issue.
func TestLiveAnAmbiguousNativeLinkRetractsThePullRequestsLowerTierLinksAndNoIssuesLink(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	applyProductionSchema(t, ctx, direct, "")
	adapter := chaos7074FalkorAdapter(t, ctx)
	const local, foreign = "77777777-7777-7777-7777-777777777777", "88888888-8888-8888-8888-888888888888"
	at := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	eachAmbiguousLinkPair(t, func(t *testing.T, pullRequestProvider, provider string) {
		org := &ambiguousLinkOrg{t: t, ctx: ctx, direct: direct, org: strings.Join([]string{"org-12409-retract", pullRequestProvider, provider}, "-"), provider: provider, local: local, foreign: foreign}
		org.repositories(pullRequestProvider, at)
		org.issue("Y-1", local, "", at)
		org.issue("X-1", local, "", at)
		org.pullRequest(1, at)
		org.pullRequest(2, at)
		org.pullRequest(3, at)
		org.link("Y-1", 1, "explicit_text", at)
		org.link("X-1", 2, "heuristic", at)
		first := org.project(query, adapter, "")

		// Each row of X-1 holds a lower-tier link, each written while it was
		// the only row: the read cannot say which row it would name.
		mid := at.Add(time.Hour)
		org.exec(`ALTER TABLE work_items DELETE WHERE org_id = ? AND work_item_id = 'X-1' SETTINGS mutations_sync = 2`, org.org)
		org.issue("X-1", foreign, "", mid)
		org.link("X-1", 3, "heuristic", mid)
		second := org.project(query, adapter, first.cursor)
		if len(first.edges) != 2 || len(second.edges) == 0 {
			t.Fatalf("premise: %d then %d lower-tier edges, want 2 and the second row's", len(first.edges), len(second.edges))
		}

		// Both rows now exist, and a native link to pull request 1 arrives.
		later := mid.Add(time.Hour)
		org.issue("X-1", local, "", later)
		org.link("X-1", 1, "native", later)
		third := org.project(query, adapter, second.cursor)
		if got := third.ignored[linkSkipAmbiguous]; got != 1 {
			t.Errorf("ambiguous_links = %d, want 1 (ignored %v)", got, third.ignored)
		}

		principal := storage.Principal{OrgID: org.org}
		binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
		if err != nil {
			t.Fatal(err)
		}
		linksOf := func(number uint32) []string {
			t.Helper()
			page, err := adapter.DirectEdgePage(ctx, principal, binding, directread.EdgePageQuery{
				Origins: []contextfabric.SubjectRef{{Kind: contextfabric.SubjectPullRequest, CanonicalID: first.pullRequest[number]}},
				Types:   []string{"LINKS_PULL_REQUEST"}, Direction: directread.EdgeDirectionIn, Limit: 10, ValidAt: time.Now().UTC(),
			})
			if err != nil {
				t.Fatal(err)
			}
			out := make([]string, 0, len(page.Edges))
			for _, edge := range page.Edges {
				out = append(out, edge.From.Subject.CanonicalID)
			}
			return out
		}
		if got := linksOf(1); len(got) != 0 {
			t.Errorf("pull request 1 keeps %v: the ambiguous native row must retract its lower-tier link and give no edge", got)
		}
		for _, number := range []uint32{2, 3} {
			if got := linksOf(number); len(got) != 1 {
				t.Errorf("pull request %d holds %v, want the heuristic link of X-1: an ambiguous row names no issue, so it retracts no link of one", number, got)
			}
		}
	})
}
