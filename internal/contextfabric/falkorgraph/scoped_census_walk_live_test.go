//go:build sizeproof

package falkorgraph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/full-chaos/dev-health-acr/internal/chfixture/startterm"
	"net"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/chfixture/falkorfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// The scoped census and the scoped deployment walk on real stores: seeded
// ClickHouse rows, projected by the REAL producers (the clickhouse source and
// the teams/projects source) into a REAL FalkorDB, read by the REAL resolver,
// census and walks. Needs Docker (ClickHouse and FalkorDB containers); run by
// CI.
//
// The relation under test is the entity tree: a work item is in a requested
// repository scope when it is linked to a pull request of a named repository,
// never by its own repository. Issue side: Linear, Jira and GitHub issues;
// pull request side: GitHub pull requests and GitLab merge requests; the three
// link tiers.

// scopedTables are the tables the two producers and the census read. Naming a
// subset picks WHICH declared tables to render; every column type comes from
// devhealthschema.DDL.
// devhealthschema:not-a-production-replica -- this list only picks which declared tables to render through devhealthschema.DDL; it declares no column.
var scopedTables = []string{
	"repos", "work_items", "git_pull_requests", "git_pull_request_reviews",
	"ci_pipeline_runs", "deployments", "operational_incidents",
	"operational_service_repository_mappings", "work_item_dependencies", "work_item_relations_read",
	"work_graph_deployment_incident_edges", "work_graph_issue_pr",
	"teams", "projects", "work_item_team_attributions", "team_project_ownership",
	"project_membership_transitions", "team_repo_ownership",
	"project_team_ownership_groups_log", "project_team_ownership_groups_current",
}

const scopedZeroRepo = "00000000-0000-0000-0000-000000000000"

func scopedUUID(label string) string {
	sum := sha256.Sum256([]byte("scoped-census:" + label))
	h := hex.EncodeToString(sum[:16])
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-8" + h[17:20] + "-" + h[20:32]
}

func scopedClickHouse(t *testing.T, ctx context.Context) (*runtimeclickhouse.Client, clickhousedriver.Conn) {
	t.Helper()
	chfixture.RequireContainers(t)
	container, err := startterm.Generic(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: chfixture.Image, ExposedPorts: []string{"9000/tcp"},
			Env:        map[string]string{"CLICKHOUSE_USER": "acr", "CLICKHOUSE_PASSWORD": "acr", "CLICKHOUSE_DB": "default"},
			WaitingFor: wait.ForListeningPort("9000/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start ClickHouse: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	address := net.JoinHostPort(host, port.Port())
	direct, err := clickhousedriver.Open(&clickhousedriver.Options{
		Addr: []string{address}, Auth: clickhousedriver.Auth{Database: "default", Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open ClickHouse: %v", err)
	}
	t.Cleanup(func() { _ = direct.Close() })
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := direct.Ping(ctx); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("ping ClickHouse: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	query, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{DSN: "clickhouse://acr:acr@" + address + "/default", DialTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("open query client: %v", err)
	}
	t.Cleanup(func() { _ = query.Close() })
	for _, statement := range devhealthschema.DDL(scopedTables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	if err := direct.Exec(ctx, devhealthschema.ProjectMembershipPresenceViewDDL); err != nil {
		t.Fatalf("create membership view: %v", err)
	}
	for _, statement := range devhealthschema.OwnershipGroupsViewsDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create stored ownership group view: %v\n%s", err, statement)
		}
	}
	return query, direct
}

// scopedRoundTracer keeps the evidence_round events of a resolution.
type scopedRoundTracer struct {
	mu     sync.Mutex
	rounds []graphrank.ResolutionTraceEvent
}

func (r *scopedRoundTracer) Trace(event graphrank.ResolutionTraceEvent) {
	if event.Stage != "evidence_round" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rounds = append(r.rounds, event)
}

func (r *scopedRoundTracer) take() []graphrank.ResolutionTraceEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.rounds
	r.rounds = nil
	return out
}

func scopedFalkor(t *testing.T, ctx context.Context, query *runtimeclickhouse.Client, tracer graphrank.ResolutionTracer) *Adapter {
	t.Helper()
	chfixture.RequireContainers(t)
	container := falkorfixture.Start(ctx, t, codexRoundFalkordbImage)
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "6379/tcp")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(Config{
		Addr: host + ":" + port.Port(), GraphPrefix: "acr-cf-scoped-census", RequestTimeout: 15 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 10, AllowInsecure: true, TLS: false,
		CensusFunc: devhealthsource.NewCensusFunc(query), WorkItemKeyPrefix: devhealthsource.NewWorkItemKeyPrefixFunc(query), ResolutionTracer: tracer,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return adapter
}

func scopedDrain(t *testing.T, ctx context.Context, source contextfabric.ProjectionSource, name, orgID string, adapter *Adapter) {
	t.Helper()
	cursor := ""
	for page := 0; page < 400; page++ {
		batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: orgID, Source: name, Cursor: cursor})
		if err != nil {
			t.Fatalf("%s page %d: %v", name, page, err)
		}
		if !available {
			return
		}
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("apply %s page %d: %v", name, page, err)
		}
		if batch.NextCursor == cursor {
			t.Fatalf("%s page %d made no cursor progress", name, page)
		}
		cursor = batch.NextCursor
	}
	t.Fatalf("%s did not drain", name)
}

// scopedSeed is the seeded organization: what the rows are, and the graph ids
// the producers mint for them.
type scopedSeed struct {
	orgID   string
	repoIDs map[string]string // slug -> repo id
	items   map[string]string // work item id -> its own repo id
	project contextfabric.SubjectRef
}

func (s scopedSeed) workItem(t *testing.T, id string) string {
	t.Helper()
	canonical, omitted, err := identity.Derive(identity.KindWorkItem, []string{s.items[id], id}, nil)
	if err != nil || omitted {
		t.Fatalf("derive work item id for %s: omitted=%t err=%v", id, omitted, err)
	}
	return canonical
}

func (s scopedSeed) repository(slug string) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + s.repoIDs[slug], Label: slug}
}

// scopedFillers is how many filler repositories the seed carries beyond the
// three named ones: together more than one requested scope may walk
// (maxLinkScopedRepositories), so "acme/*" is a cut walk.
const scopedFillers = maxLinkScopedRepositories

func seedScopedOrganization(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, query *runtimeclickhouse.Client, adapter *Adapter) scopedSeed {
	t.Helper()
	s := scopedSeed{orgID: scopedUUID("org"), repoIDs: map[string]string{}, items: map[string]string{}}
	now := time.Now().UTC().Truncate(time.Millisecond)
	created := now.Add(-20 * 24 * time.Hour)
	exec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	repos := []struct{ slug, provider string }{{"acme/svc", "github"}, {"acme/other", "github"}, {"acme/gl", "gitlab"}}
	for i := 0; i < scopedFillers; i++ {
		repos = append(repos, struct{ slug, provider string }{fmt.Sprintf("acme/fill-%02d", i), "github"})
	}
	for _, r := range repos {
		s.repoIDs[r.slug] = scopedUUID("repo " + r.slug)
		exec("repo "+r.slug, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, s.repoIDs[r.slug], s.orgID, r.slug, r.provider, now)
	}
	// Pull requests and merge requests: three of acme/svc, one of acme/other,
	// one merge request of acme/gl. acme/svc#3 has no native link: explicit_text
	// and heuristic links are a fallback only, so the lower-tier links target it.
	pulls := []struct {
		slug   string
		number uint32
	}{{"acme/svc", 1}, {"acme/svc", 2}, {"acme/svc", 3}, {"acme/other", 1}, {"acme/gl", 1}}
	for _, p := range pulls {
		exec(fmt.Sprintf("pull request %s#%d", p.slug, p.number), `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, created_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			s.repoIDs[p.slug], s.orgID, p.number, fmt.Sprintf("Change %d", p.number), "merged", created, now)
	}
	// One production deployment for each of acme/svc and acme/other.
	for _, slug := range []string{"acme/svc", "acme/other"} {
		exec("deployment "+slug, `INSERT INTO deployments (repo_id, deployment_id, status, environment, started_at, deployed_at, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			s.repoIDs[slug], "dep-"+strings.TrimPrefix(slug, "acme/"), "success", "production", created, created, now, s.orgID)
	}
	// A GitHub project holding one issue of acme/other.
	exec("project", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, 'github', NULL, 'Checkout', 1, 'open', '', ?)`, "ghprojv2:PVT_checkout", s.orgID, now)
	// CHAOS-12505: three teams for the requested team narrowing. TSVC owns
	// acme/svc, TPROJ owns the project, TNONE owns nothing; acme/other and
	// acme/gl have no owner.
	for _, team := range []string{"TSVC", "TPROJ", "TNONE"} {
		exec("team "+team, `INSERT INTO teams (id, name, description, updated_at, last_synced, org_id, provider, native_team_key, project_keys, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			team, "team "+team, "", created, created, s.orgID, "linear", team, []string{}, uint8(1))
	}
	exec("ownership acme/svc", `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.orgID, "github", "TSVC", s.repoIDs["acme/svc"], "acme/svc", "exact", "native", uint8(1), uint16(1), int32(1), created, nil, created)
	exec("ownership project", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		s.orgID, "github", "TPROJ", "ghprojv2:PVT_checkout", nil, "native", created, nil, created)

	type item struct{ id, slug, provider, itemType, project, title string }
	items := []item{
		{"linear:CHAOS-10", "", "linear", "issue", "", "Retry the payment call"},                  // repository-less, native to a GitHub pull request of acme/svc
		{"jira:CHAOS-11", "", "jira", "story", "", "Index the ledger"},                            // repository-less, native to a GitLab merge request of acme/gl
		{"linear:CHAOS-12", "", "linear", "issue", "", "Rename the queue"},                        // repository-less, explicit_text only, to acme/svc
		{"jira:CHAOS-13", "", "jira", "story", "", "Trim the logs"},                               // repository-less, native to a pull request of acme/other only
		{"linear:CHAOS-14", "", "linear", "issue", "", "Split the job"},                           // two items share the key, both native to acme/svc
		{"jira:CHAOS-14", "", "jira", "story", "", "Split the job again"},                         //
		{"gh:acme/other#5", "acme/other", "github", "issue", "ghprojv2:PVT_checkout", "Checkout"}, // own repository acme/other, in the project, native to acme/svc
		{"gh:acme/svc#6", "acme/svc", "github", "issue", "", "Heuristic neighbour"},               // own repository acme/svc, heuristic to acme/svc
		{"gitlab:acme/gl#7", "acme/gl", "gitlab", "issue", "", "Bump the runner"},                 // own repository acme/gl, native to acme/svc
		{"gh:acme/svc#8", "acme/svc", "github", "issue", "", "Own column only"},                   // CHAOS-12706: own repository acme/svc, no link
	}
	// Fillers: work items the search for any of the keys finds, so the
	// resolution stalls (more hits than the search returns) and the census
	// runs, as it does on a large organization. None carries a key the census
	// counts.
	for i := 0; i < 30; i++ {
		items = append(items, item{fmt.Sprintf("linear:CHAOS-9%02d", i), "", "linear", "issue", "", "CHAOS-10 CHAOS-11 CHAOS-12 CHAOS-13 CHAOS-14 follow-up"})
	}
	for _, i := range items {
		repoID := scopedZeroRepo
		if i.slug != "" {
			repoID = s.repoIDs[i.slug]
		}
		s.items[i.id] = repoID
		exec("work item "+i.id, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, provider, project_id, created_at, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			i.id, repoID, s.orgID, i.title, i.itemType, "open", i.provider, i.project, created, now, now)
	}
	links := []struct {
		issue, slug string
		number      uint32
		tier        string
	}{
		{"linear:CHAOS-10", "acme/svc", 1, "native"},
		{"jira:CHAOS-11", "acme/gl", 1, "native"},
		{"linear:CHAOS-12", "acme/svc", 3, "explicit_text"},
		{"jira:CHAOS-13", "acme/other", 1, "native"},
		{"linear:CHAOS-14", "acme/svc", 1, "native"},
		{"jira:CHAOS-14", "acme/svc", 2, "native"},
		{"gh:acme/other#5", "acme/svc", 1, "native"},
		{"gh:acme/svc#6", "acme/svc", 3, "heuristic"},
		{"gitlab:acme/gl#7", "acme/svc", 2, "native"},
	}
	for i, l := range links {
		exec(fmt.Sprintf("link %d", i), `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			s.repoIDs[l.slug], l.issue, l.number, float32(0.9), l.tier, "", now, s.orgID)
	}

	main, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	scopedDrain(t, ctx, main, devhealthsource.SourceName, s.orgID, adapter)
	// The ownership edges come from the stored groups: the second refresh
	// starts after the seed even when a scheduled one was running during it.
	for range 2 {
		for _, statement := range []string{"SYSTEM REFRESH VIEW project_team_ownership_groups_refresh", "SYSTEM WAIT VIEW project_team_ownership_groups_refresh"} {
			if err := direct.Exec(ctx, statement); err != nil {
				t.Fatalf("%s: %v", statement, err)
			}
		}
	}
	teams, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatal(err)
	}
	scopedDrain(t, ctx, teams, devhealthsource.TeamsProjectsSourceName, s.orgID, adapter)

	// The graph must be the seed: one link edge per link row, and the
	// project's presence edge.
	probe := storage.Principal{OrgID: s.orgID, Subject: "probe", CredentialID: "probe"}
	binding, err := adapter.ResolveInvestigationBinding(ctx, probe)
	if err != nil {
		t.Fatal(err)
	}
	var origins []contextfabric.SubjectRef
	for _, p := range pulls {
		origins = append(origins, contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: fmt.Sprintf("pull_request:%s:%d", s.repoIDs[p.slug], p.number)})
	}
	page, err := adapter.DirectEdgePage(ctx, probe, binding, directread.EdgePageQuery{
		Origins: origins, Types: []string{"LINKS_PULL_REQUEST"}, Direction: directread.EdgeDirectionIn, Limit: 100, ValidAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("link edges: %v", err)
	}
	if page.More || len(page.Edges) != len(links) {
		t.Fatalf("projection produced %d LINKS_PULL_REQUEST edges (more=%t), want %d: the graph is not the seed", len(page.Edges), page.More, len(links))
	}
	member := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: s.workItem(t, "gh:acme/other#5")}
	presence, err := adapter.DirectEdgePage(ctx, probe, binding, directread.EdgePageQuery{
		Origins: []contextfabric.SubjectRef{member}, Types: []string{"BELONGS_TO_PROJECT"}, Direction: directread.EdgeDirectionOut, Limit: 10, ValidAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("presence edges: %v", err)
	}
	if len(presence.Edges) != 1 {
		t.Fatalf("projection produced %d BELONGS_TO_PROJECT edges for the project's issue, want 1: the graph is not the seed", len(presence.Edges))
	}
	s.project = presence.Edges[0].To.Subject
	return s
}

// TestTheScopedCensusAndTheDeploymentWalkFollowTheLinkOnRealStores. Rows that
// fail on the code before the change (the census filtered the work item's own
// repository; the deployment walk tested the issue's own repository against
// the scope): every row that expects a committed work item, and the deployment
// rows that expect acme/svc's deployment under a requested scope.
func TestTheScopedCensusAndTheDeploymentWalkFollowTheLinkOnRealStores(t *testing.T) {
	ctx := context.Background()
	query, direct := scopedClickHouse(t, ctx)
	tracer := &scopedRoundTracer{}
	adapter := scopedFalkor(t, ctx, query, tracer)
	s := seedScopedOrganization(t, ctx, direct, query, adapter)
	org := storage.Principal{OrgID: s.orgID, Subject: "u", CredentialID: "c"}

	resolve := func(t *testing.T, key string, principal storage.Principal, slugs []string) ([]string, []graphrank.ResolutionTraceEvent) {
		t.Helper()
		tracer.take()
		binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
		if err != nil {
			t.Fatal(err)
		}
		request := contextfabric.InvestigationRequest{
			SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_scoped_census", Question: "What is the status of " + key + "?",
			TimeContext:    contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			RequestedScope: contextfabric.RequestedScope{RepositorySlugs: slugs},
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
				MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
			},
			Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
		}
		interpreted := contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: []string{key},
			TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
		}
		resolution, _, _, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, binding, nil, nil, nil, "")
		if err != nil {
			t.Fatalf("ResolveSubjects(%s, %v): %v", key, slugs, err)
		}
		var committed []string
		for _, subject := range resolution.Committed {
			if subject.Kind == contextfabric.SubjectWorkItem {
				committed = append(committed, subject.CanonicalID)
			}
		}
		sort.Strings(committed)
		return committed, tracer.take()
	}
	reasons := func(rounds []graphrank.ResolutionTraceEvent) string {
		out := make([]string, 0, len(rounds))
		for _, r := range rounds {
			out = append(out, r.ShadowOutcome+"/"+r.ShadowReason)
		}
		return strings.Join(out, ",")
	}

	t.Run("census: a key is in a requested repository scope through its link", func(t *testing.T) {
		for _, c := range []struct {
			name, key string
			scope     []string
			want      []string
		}{
			{"Linear issue, native link to a GitHub pull request in scope", "CHAOS-10", []string{"acme/svc"}, []string{"linear:CHAOS-10"}},
			{"Jira issue, native link to a GitLab merge request in scope", "CHAOS-11", []string{"acme/gl"}, []string{"jira:CHAOS-11"}},
			{"Jira issue, native link to a pull request of the other scoped repository", "CHAOS-13", []string{"acme/other"}, []string{"jira:CHAOS-13"}},
			{"case-folded scope", "CHAOS-10", []string{"ACME/Svc"}, []string{"linear:CHAOS-10"}},
			{"link to a pull request outside the scope", "CHAOS-13", []string{"acme/svc"}, nil},
			{"repository-less issue with a text link only: a link grants authority only when native", "CHAOS-12", []string{"acme/svc"}, nil},
			{"two issues share the key inside the scope", "CHAOS-14", []string{"acme/svc"}, nil},
			{"neither issue of the key is linked into the scope", "CHAOS-14", []string{"acme/other"}, nil},
		} {
			committed, rounds := resolve(t, c.key, org, c.scope)
			want := make([]string, 0, len(c.want))
			for _, id := range c.want {
				want = append(want, s.workItem(t, id))
			}
			if strings.Join(committed, ",") != strings.Join(want, ",") {
				t.Errorf("%s: %s under %v committed %v, want %v (rounds %s)", c.name, c.key, c.scope, committed, want, reasons(rounds))
			}
			if len(rounds) == 0 {
				t.Errorf("%s: no evidence round ran: the census was not measured", c.name)
			}
		}
	})

	// CHAOS-12505: the census is scoped by a read keyed on its satisfiers, so a
	// scope the population walk would cut is read whole. acme/* names more
	// repositories than one walk takes (maxLinkScopedRepositories); before the
	// change this row ended in census_error with nothing committed.
	t.Run("census: a scope past the walk's bound is read keyed on the satisfiers", func(t *testing.T) {
		for _, c := range []struct {
			name      string
			principal storage.Principal
			want      []string
		}{
			{"organization-wide", org, []string{"linear:CHAOS-10"}},
			{"restricted, granted the pull request's repository", storage.Principal{OrgID: s.orgID, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/svc"}}, []string{"linear:CHAOS-10"}},
			{"restricted, granted another repository only", storage.Principal{OrgID: s.orgID, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/other"}}, nil},
		} {
			committed, rounds := resolve(t, "CHAOS-10", c.principal, []string{"acme/*"})
			want := make([]string, 0, len(c.want))
			for _, id := range c.want {
				want = append(want, s.workItem(t, id))
			}
			if strings.Join(committed, ",") != strings.Join(want, ",") {
				t.Errorf("%s: committed %v under acme/*, want %v (rounds %s)", c.name, committed, want, reasons(rounds))
			}
			if len(rounds) == 0 {
				t.Errorf("%s: no evidence round ran: the census was not measured", c.name)
			}
			if c.want != nil && strings.Contains(reasons(rounds), string(graphrank.ReasonCensusError)) {
				t.Errorf("%s: rounds %s, want no census error", c.name, reasons(rounds))
			}
		}
	})

	// CHAOS-12456: a restricted caller reaches a repo-less work item through a
	// native link to a pull request of a granted repository, on the real stores:
	// the census reads the link table, the node gate reads the link edge.
	// CHAOS-12592: every web user reaches the resolver with a "*" grant the
	// access read kept (userprincipal.WebUser), and reads the whole
	// organization. A credential that names a repository keeps the restricted
	// rules: a repository-less item needs a native link into its grant.
	t.Run("census: a web user reads the whole organization, a repository-bound credential does not", func(t *testing.T) {
		web := userprincipal.WebUser(t, s.orgID)
		bound := userprincipal.Narrowed(t, s.orgID, []string{"acme/svc"}, []string{"acme/svc"})
		for _, c := range []struct {
			name, key  string
			web, bound []string
		}{
			{"native link into the bound grant", "CHAOS-10", []string{"linear:CHAOS-10"}, []string{"linear:CHAOS-10"}},
			{"native link outside the bound grant", "CHAOS-13", []string{"jira:CHAOS-13"}, nil},
			{"text link only", "CHAOS-12", []string{"linear:CHAOS-12"}, nil},
		} {
			for who, run := range map[string]struct {
				principal storage.Principal
				want      []string
			}{"web user": {web, c.web}, "repository-bound": {bound, c.bound}} {
				committed, rounds := resolve(t, c.key, run.principal, nil)
				want := make([]string, 0, len(run.want))
				for _, id := range run.want {
					want = append(want, s.workItem(t, id))
				}
				if strings.Join(committed, ",") != strings.Join(want, ",") {
					t.Errorf("%s, %s: %s committed %v, want %v (rounds %s)", c.name, who, c.key, committed, want, reasons(rounds))
				}
				if len(rounds) == 0 {
					t.Errorf("%s, %s: no round ran, so this case measured nothing", c.name, who)
				}
			}
		}
	})

	t.Run("census: a restricted caller resolves a key through a native link", func(t *testing.T) {
		restricted := storage.Principal{OrgID: s.orgID, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/svc"}}
		for _, c := range []struct {
			name, key string
			scope     []string
			want      []string
		}{
			{"native link into the grant, no narrowing", "CHAOS-10", nil, []string{"linear:CHAOS-10"}},
			{"native link into the grant, requested repository", "CHAOS-10", []string{"acme/svc"}, []string{"linear:CHAOS-10"}},
			{"native link outside the grant", "CHAOS-11", nil, nil},
			{"text link into the grant: only a native link grants", "CHAOS-12", nil, nil},
		} {
			committed, rounds := resolve(t, c.key, restricted, c.scope)
			want := make([]string, 0, len(c.want))
			for _, id := range c.want {
				want = append(want, s.workItem(t, id))
			}
			if strings.Join(committed, ",") != strings.Join(want, ",") {
				t.Errorf("%s: %s committed %v, want %v (rounds %s)", c.name, c.key, committed, want, reasons(rounds))
			}
			if len(rounds) == 0 {
				t.Errorf("%s: no round ran, so this case measured nothing", c.name)
			}
		}
	})

	binding, err := adapter.ResolveInvestigationBinding(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	key, err := adapter.effectiveKey(ctx, s.orgID, binding)
	if err != nil {
		t.Fatal(err)
	}

	// CHAOS-12706: a receipt for a work item under a requested repository
	// scope is admitted through its link, on the real stores and the real
	// adapter: exactly where the keyed link read admits it, committed on the
	// caller's canonical id. Before the change the hint loop tested the
	// issue's own repository against the scope, so the GitHub and GitLab
	// issues of other repositories linked into acme/svc were dropped, and the
	// GitHub issue of acme/svc with no link was admitted.
	t.Run("receipt: a work item hint is admitted where the keyed link read admits it", func(t *testing.T) {
		resolveReceipt := func(t *testing.T, id string, principal storage.Principal, slugs []string) (bool, contextfabric.CommitBasis) {
			t.Helper()
			subjectBinding, err := adapter.ResolveInvestigationBinding(ctx, principal)
			if err != nil {
				t.Fatal(err)
			}
			request := contextfabric.InvestigationRequest{
				SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_scoped_receipt", Question: "What is its status now?",
				TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				RequestedScope: contextfabric.RequestedScope{RepositorySlugs: slugs, SubjectHints: []contextfabric.SubjectHint{
					{Kind: contextfabric.SubjectWorkItem, ID: id, Source: string(hintsource.PriorSubjectReceipt)},
				}},
				Options: contextfabric.InvestigationOptions{
					MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
					MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
				},
				Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
			}
			interpreted := contextfabric.InterpretedQuestion{
				Shape: contextfabric.ShapeOpen, RequestedJudgment: "status",
				TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
			}
			resolution, _, bases, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, subjectBinding, nil, nil, nil, "")
			if err != nil {
				t.Fatalf("ResolveSubjects(receipt %s, %v): %v", id, slugs, err)
			}
			subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: id}
			for _, committed := range resolution.Committed {
				if committed.Kind == contextfabric.SubjectWorkItem && committed.CanonicalID == id {
					return true, bases[graphrank.SubjectKey(subject)]
				}
			}
			return false, ""
		}
		org := storage.Principal{OrgID: s.orgID, Subject: "u", CredentialID: "c"}
		// Pinned rows, so the differential below cannot pass on an empty read.
		for _, c := range []struct {
			name, item string
			scope      []string
			want       bool
		}{
			{"Linear issue, native link into the scope", "linear:CHAOS-10", []string{"acme/svc"}, true},
			{"Jira issue, native link to a GitLab merge request in the scope", "jira:CHAOS-11", []string{"acme/gl"}, true},
			{"GitHub issue of another repository, native link into the scope", "gh:acme/other#5", []string{"acme/svc"}, true},
			{"GitLab issue of another repository, native link into the scope", "gitlab:acme/gl#7", []string{"acme/svc"}, true},
			{"GitHub issue of the scoped repository with no link: its own column admits nothing", "gh:acme/svc#8", []string{"acme/svc"}, false},
			{"Jira issue linked outside the scope", "jira:CHAOS-13", []string{"acme/svc"}, false},
			{"repository-less issue with a text link only", "linear:CHAOS-12", []string{"acme/svc"}, false},
			{"no scope: unchanged", "gh:acme/svc#8", nil, true},
		} {
			committed, basis := resolveReceipt(t, s.workItem(t, c.item), org, c.scope)
			if committed != c.want || (committed && basis != contextfabric.CommitBasisCallerCanonicalID) {
				t.Errorf("%s: committed %t basis %q, want committed %t on the caller's canonical id", c.name, committed, basis, c.want)
			}
		}
		// The differential: for every named item, scope and caller, the receipt
		// commits exactly when the real keyed link read lists the item.
		var named []string
		for id := range s.items {
			if !strings.HasPrefix(id, "linear:CHAOS-9") {
				named = append(named, id)
			}
		}
		sort.Strings(named)
		principals := []storage.Principal{
			org,
			{OrgID: s.orgID, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/svc"}},
			{OrgID: s.orgID, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/svc", "acme/other"}},
			{OrgID: s.orgID, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/gl"}},
		}
		admitted, refused := 0, 0
		for _, principal := range principals {
			for _, slugs := range [][]string{{"acme/svc"}, {"acme/gl"}, {"acme/other"}} {
				reachCtx, finish, err := adapter.withProjectReach(ctx, key, principal)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range named {
					id := s.workItem(t, item)
					tiers, complete, err := adapter.linkScopedWorkItemsOf(reachCtx, key, principal, contextfabric.RequestedScope{RepositorySlugs: slugs}, []string{id})
					if err != nil || !complete {
						t.Fatalf("keyed read of %s: complete %t err %v", item, complete, err)
					}
					want := tiers[id] != ""
					committed, basis := resolveReceipt(t, id, principal, slugs)
					if committed != want || (committed && basis != contextfabric.CommitBasisCallerCanonicalID) {
						t.Errorf("grants %v scope %v %s: receipt committed %t basis %q, keyed read %v: want the receipt committed exactly where the read admits the item", principal.RepositoryScopes, slugs, item, committed, basis, tiers)
					}
					if want {
						admitted++
					} else {
						refused++
					}
				}
				finish()
			}
		}
		t.Logf("receipt differential: %d admitted, %d refused", admitted, refused)
		if admitted < 8 || refused < 20 {
			t.Fatalf("receipt differential: %d admitted, %d refused: too few to stand for the rule", admitted, refused)
		}
	})

	t.Run("the census population is the repository walk's members", func(t *testing.T) {
		for _, principal := range []storage.Principal{
			{OrgID: s.orgID, Subject: "u", CredentialID: "c"},
			{OrgID: s.orgID, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/svc"}},
			{OrgID: s.orgID, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/svc", "acme/other"}},
		} {
			scope := contextfabric.RequestedScope{RepositorySlugs: []string{"acme/svc"}}
			reachCtx, _, err := adapter.withProjectReach(ctx, key, principal)
			if err != nil {
				t.Fatal(err)
			}
			population, complete, err := adapter.linkScopedIssues(reachCtx, key, principal, scope, contextfabric.WorkItemMembershipCensusLimit)
			if err != nil || !complete {
				t.Fatalf("grants %v: population complete=%t err=%v", principal.RepositoryScopes, complete, err)
			}
			walk, err := adapter.TreeWorkItemMembers(ctx, principal, binding, scope, s.repository("acme/svc"), contextfabric.WorkItemMembershipCensusLimit+1)
			if err != nil {
				t.Fatal(err)
			}
			members := make([]string, 0, len(walk.Members))
			for _, m := range walk.Members {
				members = append(members, m.Subject.CanonicalID+"="+m.Tier)
			}
			sort.Strings(members)
			walked := make([]string, 0, len(population))
			for id, tier := range population {
				walked = append(walked, id+"="+tier)
			}
			sort.Strings(walked)
			if strings.Join(walked, ",") != strings.Join(members, ",") || len(members) == 0 {
				t.Errorf("grants %v: census population %v, repository walk members %v: want the same non-empty set, with the same link tiers", principal.RepositoryScopes, walked, members)
			}
			// The census of the walk is the count of the members it returns.
			gate, err := contextfabric.NewWorkItemMembershipGate(1, 1)
			if err != nil {
				t.Fatal(err)
			}
			lease, read, err := contextfabric.NewTreeWorkItemMembership(adapter, nil, gate).Begin(ctx, principal, binding, scope, contextfabric.WorkItemMembershipRequest{
				Anchor: contextfabric.WorkItemMembershipAnchor{Subject: s.repository("acme/svc")},
			})
			if err != nil {
				t.Fatal(err)
			}
			if lease != nil {
				lease.Release()
			}
			if read.Census.AuthorizedPopulation != len(members) || !read.Census.PopulationComplete {
				t.Errorf("grants %v: census %d (complete %t), walk members %d: want the census to be the count of the members", principal.RepositoryScopes, read.Census.AuthorizedPopulation, read.Census.PopulationComplete, len(members))
			}
		}
	})

	// CHAOS-12505, the differential oracle: the keyed link read and the
	// repository walk are two readers of one rule, so wherever the walk is not
	// cut, the keyed read's answer for any ids is the walk's members intersected
	// with those ids, tiers included: every tier, every provider (Linear, Jira,
	// GitHub and GitLab issues; GitHub pull requests and GitLab merge requests),
	// every team narrowing, organization-wide, repository-restricted and
	// project-only callers.
	t.Run("the keyed link read is the repository walk's members intersected with the ids", func(t *testing.T) {
		names := make([]string, 0, len(s.items))
		for id := range s.items {
			names = append(names, id)
		}
		sort.Strings(names)
		var all []string
		for _, id := range names {
			all = append(all, s.workItem(t, id))
		}
		all = append(all, "work_item.v2:"+scopedZeroRepo+":linear:CHAOS-99999")
		principals := []storage.Principal{
			org,
			{OrgID: s.orgID, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/svc"}},
			{OrgID: s.orgID, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/svc", "acme/other"}},
			{OrgID: s.orgID, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/gl"}},
			{OrgID: s.orgID, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/other"}},
			{OrgID: s.orgID, Subject: "u", CredentialID: "c", ProjectScopes: []string{s.project.CanonicalID}},
		}
		scopes := [][]string{{"acme/svc"}, {"acme/other"}, {"acme/gl"}, {"acme/svc", "acme/gl"}, {"ACME/Svc"}}
		teams := [][]string{nil, {"TSVC"}, {"TPROJ"}, {"TNONE"}}
		render := func(tiers map[string]string) string {
			out := make([]string, 0, len(tiers))
			for id, tier := range tiers {
				out = append(out, id+"="+tier)
			}
			sort.Strings(out)
			return strings.Join(out, ",")
		}
		compared, teamCompared, teamExcluding, nonEmpty, bothCut := 0, 0, 0, 0, 0
		for _, principal := range principals {
			for _, slugs := range scopes {
				for _, teamIDs := range teams {
					scope := contextfabric.RequestedScope{RepositorySlugs: slugs, TeamIDs: teamIDs}
					label := fmt.Sprintf("grants %v projects %v scope %v teams %v", principal.RepositoryScopes, principal.ProjectScopes, slugs, teamIDs)
					reachCtx, finish, err := adapter.withProjectReach(ctx, key, principal)
					if err != nil {
						t.Fatal(err)
					}
					walked, walkComplete, err := adapter.linkScopedIssues(reachCtx, key, principal, scope, contextfabric.WorkItemMembershipCensusLimit)
					if err != nil {
						t.Fatalf("%s: walk: %v", label, err)
					}
					keyed, keyedComplete, err := adapter.linkScopedWorkItemsOf(reachCtx, key, principal, scope, all)
					if err != nil {
						t.Fatalf("%s: keyed read: %v", label, err)
					}
					if !walkComplete {
						// Every scope here is under the walk's bounds, so only a team
						// narrowing it could not decide cuts it, and the ids are every
						// seeded item: the keyed read meets the same undecided item and
						// must be cut too, never a smaller answer.
						if keyedComplete {
							t.Errorf("%s: the walk is cut and the keyed read answered %q as complete", label, render(keyed))
						}
						bothCut++
						finish()
						continue
					}
					want := map[string]string{}
					for _, id := range all {
						if tier, ok := walked[id]; ok {
							want[id] = tier
						}
					}
					if !keyedComplete || render(keyed) != render(want) {
						t.Errorf("%s: keyed read %q (complete %t), want the walk's members intersected with the ids %q", label, render(keyed), keyedComplete, render(want))
					}
					// Each id alone: the answer for one id does not depend on the others.
					for _, id := range all {
						one, oneComplete, err := adapter.linkScopedWorkItemsOf(reachCtx, key, principal, scope, []string{id})
						if err != nil || !oneComplete {
							t.Fatalf("%s: keyed read of %s: complete %t err %v", label, id, oneComplete, err)
						}
						if one[id] != want[id] || len(one) > 1 {
							t.Errorf("%s: keyed read of %s alone %v, want %q", label, id, one, want[id])
						}
					}
					finish()
					compared++
					if len(want) > 0 {
						nonEmpty++
					}
					if teamIDs != nil {
						teamCompared++
						unnarrowed, _, err := adapter.linkScopedWorkItemsOf(reachCtx, key, principal, contextfabric.RequestedScope{RepositorySlugs: slugs}, all)
						if err != nil {
							t.Fatal(err)
						}
						if len(unnarrowed) > len(keyed) {
							teamExcluding++
						}
					}
				}
			}
		}
		t.Logf("differential: %d cases compared (%d team-narrowed, %d where the team narrowing excluded an item, %d non-empty), %d cut by both", compared, teamCompared, teamExcluding, nonEmpty, bothCut)
		// A differential that compared nothing proves nothing.
		if compared < 60 || teamCompared < 30 || teamExcluding == 0 || nonEmpty < 20 || bothCut == 0 {
			t.Fatalf("differential compared %d cases (%d team-narrowed, %d excluding, %d non-empty, %d cut by both): too few to stand for the rule", compared, teamCompared, teamExcluding, nonEmpty, bothCut)
		}
	})

	t.Run("deployment walk: a requested scope follows the link", func(t *testing.T) {
		deployment := func(slug string) string {
			id, omitted, err := identity.Derive(identity.KindDeployment, []string{s.repoIDs[slug], "dep-" + strings.TrimPrefix(slug, "acme/")}, nil)
			if err != nil || omitted {
				t.Fatalf("derive deployment id for %s: omitted=%t err=%v", slug, omitted, err)
			}
			return id
		}
		for _, c := range []struct {
			name   string
			grants []string
			scope  []string
			want   []string
		}{
			{"unrestricted, no scope", nil, nil, []string{"acme/svc"}},
			{"unrestricted, scope on the pull request's repository", nil, []string{"acme/svc"}, []string{"acme/svc"}},
			{"unrestricted, scope on the issue's own repository only", nil, []string{"acme/other"}, nil},
			{"granted both, scope on the pull request's repository", []string{"acme/svc", "acme/other"}, []string{"acme/svc"}, []string{"acme/svc"}},
			{"granted the pull request's repository only: the grants still apply to the issue", []string{"acme/svc"}, nil, nil},
		} {
			principal := storage.Principal{OrgID: s.orgID, Subject: "u", CredentialID: "c", RepositoryScopes: c.grants}
			reachCtx, _, err := adapter.withProjectReach(ctx, key, principal)
			if err != nil {
				t.Fatal(err)
			}
			walk, err := adapter.anchorDeploymentMembers(reachCtx, key, s.orgID, principal, contextfabric.RequestedScope{RepositorySlugs: c.scope}, s.project, 25, newTemporalFilter(contextfabric.TimeContext{}))
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			got := walkMemberIDs(walk)
			sort.Strings(got)
			want := make([]string, 0, len(c.want))
			for _, slug := range c.want {
				want = append(want, deployment(slug))
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s: deployments %v, want %v", c.name, got, want)
			}
		}
	})

	// The result-by-id read (api/context_fabric_result_routes.go): the
	// store's row, then the stored-result gate on the caller's CURRENT grants,
	// then its serving error. A result whose committed subject is a work item
	// the scoped census admitted through its link is read back by its
	// computing caller and by narrower callers. A denied read is the same
	// classification as an unknown id.
	t.Run("by id: a link-admitted work item is read back under the reader's current grants", func(t *testing.T) {
		member := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: s.workItem(t, "linear:CHAOS-10"), Label: "CHAOS-10"}
		stored := func(computedBy storage.Principal) contextfabric.StoredInvestigationResult {
			return contextfabric.StoredInvestigationResult{
				Result: contextfabric.InvestigationResult{
					ResultID:          "result_scoped_census_by_id",
					SubjectResolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{member}},
				},
				GrantDigest: contextfabric.StoredResultGrantDigest(computedBy),
			}
		}
		gate := contextfabric.NewStoredResultGate(adapter)
		restricted := func(grants ...string) storage.Principal {
			return storage.Principal{OrgID: s.orgID, Subject: "u", CredentialID: "c", RepositoryScopes: grants}
		}
		for _, c := range []struct {
			name       string
			computedBy storage.Principal
			readBy     storage.Principal
			wantServed bool
		}{
			{"(a) the org-wide caller that computed it", org, org, true},
			// CHAOS-12456: the gate reads the item's native link, or the recheck
			// would remove the root the census just committed.
			{"(a) a restricted caller that computed it: the gate reads the native link to its repository", restricted("acme/svc", "acme/other"), restricted("acme/svc", "acme/other"), true},
			{"(b) a narrower grant than the computing caller's", org, restricted("acme/svc"), false},
			{"(c) a grant that no longer covers the linked pull request's repository", restricted("acme/svc", "acme/other"), restricted("acme/other"), false},
		} {
			decision := gate.Authorize(ctx, c.readBy, stored(c.computedBy), contextfabric.StoredResultSurfaceResultByID)
			err := decision.ServingError()
			if c.wantServed {
				if err != nil {
					t.Errorf("%s: %v (decision %s %s), want the stored result served as stored", c.name, err, decision.Decision, decision.Reason)
				}
				continue
			}
			if !errors.Is(err, contextfabric.ErrInvestigationResultNotFound) || err != contextfabric.ErrInvestigationResultNotFound {
				t.Errorf("%s: %v (decision %s %s), want exactly the not-found an unknown id gets", c.name, err, decision.Decision, decision.Reason)
			}
		}
		if _, err := memoryinvestigation.NewStore().Get(ctx, org, "result_never_saved"); !errors.Is(err, contextfabric.ErrInvestigationResultNotFound) {
			t.Fatalf("unknown id: %v, want the not-found classification", err)
		}
	})
}
