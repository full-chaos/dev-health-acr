package falkorgraph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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
	"operational_service_repository_mappings", "work_item_dependencies",
	"work_graph_deployment_incident_edges", "work_graph_issue_pr",
	"teams", "projects", "work_item_team_attributions", "team_project_ownership",
	"project_membership_transitions", "team_repo_ownership",
}

const scopedZeroRepo = "00000000-0000-0000-0000-000000000000"

func scopedUUID(label string) string {
	sum := sha256.Sum256([]byte("scoped-census:" + label))
	h := hex.EncodeToString(sum[:16])
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-8" + h[17:20] + "-" + h[20:32]
}

func scopedClickHouse(t *testing.T, ctx context.Context) (*runtimeclickhouse.Client, clickhousedriver.Conn) {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
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
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: codexRoundFalkordbImage, ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForListeningPort("6379/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start FalkorDB container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
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
		CensusFunc: devhealthsource.NewCensusFunc(query), ResolutionTracer: tracer,
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
const scopedFillers = 25

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
	// Pull requests and merge requests: two of acme/svc, one of acme/other,
	// one merge request of acme/gl.
	pulls := []struct {
		slug   string
		number uint32
	}{{"acme/svc", 1}, {"acme/svc", 2}, {"acme/other", 1}, {"acme/gl", 1}}
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

	type item struct{ id, slug, provider, itemType, project, title string }
	items := []item{
		{"linear:CHAOS-10", "", "linear", "issue", "", "Retry the payment call"},         // repository-less, native to a GitHub pull request of acme/svc
		{"jira:CHAOS-11", "", "jira", "story", "", "Index the ledger"},                   // repository-less, native to a GitLab merge request of acme/gl
		{"linear:CHAOS-12", "", "linear", "issue", "", "Rename the queue"},               // repository-less, explicit_text only, to acme/svc
		{"jira:CHAOS-13", "", "jira", "story", "", "Trim the logs"},                      // repository-less, native to a pull request of acme/other only
		{"linear:CHAOS-14", "", "linear", "issue", "", "Split the job"},                  // two items share the key, both native to acme/svc
		{"jira:CHAOS-14", "", "jira", "story", "", "Split the job again"},                //
		{"gh:acme/other#5", "acme/other", "github", "issue", "ghprojv2:PVT_checkout", "Checkout"}, // own repository acme/other, in the project, native to acme/svc
		{"gh:acme/svc#6", "acme/svc", "github", "issue", "", "Heuristic neighbour"},      // own repository acme/svc, heuristic to acme/svc
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
		{"linear:CHAOS-12", "acme/svc", 2, "explicit_text"},
		{"jira:CHAOS-13", "acme/other", 1, "native"},
		{"linear:CHAOS-14", "acme/svc", 1, "native"},
		{"jira:CHAOS-14", "acme/svc", 2, "native"},
		{"gh:acme/other#5", "acme/svc", 1, "native"},
		{"gh:acme/svc#6", "acme/svc", 2, "heuristic"},
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

	t.Run("census: a cut link walk is not a census", func(t *testing.T) {
		// acme/* names more repositories than one scope walks.
		committed, rounds := resolve(t, "CHAOS-10", org, []string{"acme/*"})
		if len(committed) != 0 {
			t.Fatalf("committed %v under a cut walk, want none", committed)
		}
		if got := reasons(rounds); !strings.Contains(got, string(graphrank.ReasonCensusError)) {
			t.Fatalf("rounds %s, want the census reported incomplete (%s)", got, graphrank.ReasonCensusError)
		}
	})

	t.Run("census: a restricted caller gets no census", func(t *testing.T) {
		restricted := storage.Principal{OrgID: s.orgID, Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/svc"}}
		committed, rounds := resolve(t, "CHAOS-10", restricted, []string{"acme/svc"})
		if len(committed) != 0 {
			t.Fatalf("committed %v for a restricted caller through the census, want none", committed)
		}
		for _, r := range rounds {
			if r.ShadowReason != string(graphrank.ReasonScopedVisibility) {
				t.Fatalf("round %s/%s, want the scoped-visibility refusal", r.ShadowOutcome, r.ShadowReason)
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
			reachCtx, err := adapter.withProjectReach(ctx, key, principal)
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
}
