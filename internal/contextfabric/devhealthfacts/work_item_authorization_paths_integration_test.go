package devhealthfacts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/full-chaos/dev-health-go/readers"
)

// The work-item authorization paths through every SQL reader of the shared
// rule: the S1 census (counts and its Info line through the real handler),
// the project -> work_item scope expander (statement, mask and second gate),
// and the status / title / actual-completion content readers. One fixture,
// production DDL, a repository-scoped principal.
//
// Project P is owned by a team that owns the granted repository; project Q
// by a team that owns only a non-granted one.
//
//	P members: direct-granted (repo G), direct-denied (repo N, owned project),
//	           p-project (repo-less), p-both (repo-less + native link to G)
//	Q members: q-link (repo-less + native link to G), q-none (repo-less),
//	           q-heuristic (repo-less + heuristic link to G),
//	           q-explicit (repo-less + explicit_text link to G),
//	           q-heuristic-2 (repo-less + heuristic link to G; two heuristic
//	           members against one explicit_text member keep the two
//	           excluded counts distinct),
//	           q-projectless (repo-less, no project_id, member by transition)
const (
	authzPathsOrg      = "authz-paths-org"
	authzPathsProjectP = "authz-proj-p"
	authzPathsProjectQ = "authz-proj-q"
	authzPathsRepoG    = "30000000-0000-4000-8000-000000000001"
	authzPathsRepoN    = "30000000-0000-4000-8000-000000000002"
	authzPathsSlugG    = "acme/granted"
	authzPathsSlugN    = "other/nongranted"
)

type authzPathsFixture struct {
	query  *runtimeclickhouse.Client
	direct clickhousedriver.Conn
	at     time.Time
}

func newAuthzPathsFixture(t *testing.T) authzPathsFixture {
	t.Helper()
	ctx := context.Background()
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
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Errorf("terminate ClickHouse: %v", err)
		}
	})
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("ClickHouse host: %v", err)
	}
	port, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatalf("ClickHouse port: %v", err)
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
	for _, statement := range devhealthschema.DDL("repos", "work_items", "projects", "project_membership_transitions", "team_project_ownership", "team_repo_ownership", "work_graph_issue_pr", "work_item_dependencies") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create fixture table: %v\n%s", err, statement)
		}
	}
	if err := direct.Exec(ctx, devhealthschema.ProjectMembershipPresenceViewDDL); err != nil {
		t.Fatalf("create membership view: %v", err)
	}

	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	seed := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed fixture: %v\n%s", err, statement)
		}
	}
	seed(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?), (?, ?, ?, ?, ?)`,
		authzPathsRepoG, authzPathsOrg, authzPathsSlugG, "github", at,
		authzPathsRepoN, authzPathsOrg, authzPathsSlugN, "github", at)
	seed(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, ?, NULL, ?, 1, 'started', '', ?), (?, ?, ?, NULL, ?, 1, 'started', '', ?)`,
		authzPathsProjectP, authzPathsOrg, "linear", "P", at,
		authzPathsProjectQ, authzPathsOrg, "linear", "Q", at)
	seed(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?, 'linear', 'team-g', ?, NULL, 'native', ?, NULL, ?), (?, 'linear', 'team-n', ?, NULL, 'native', ?, NULL, ?)`,
		authzPathsOrg, authzPathsProjectP, at, at,
		authzPathsOrg, authzPathsProjectQ, at, at)
	seed(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?, 'github', 'team-g', ?, ?, 'exact', 'inferred', 0, 0, 0, ?, NULL, ?), (?, 'github', 'team-n', ?, ?, 'exact', 'inferred', 0, 0, 0, ?, NULL, ?)`,
		authzPathsOrg, authzPathsRepoG, authzPathsSlugG, at, at,
		authzPathsOrg, authzPathsRepoN, authzPathsSlugN, at, at)
	item := func(id, repoID, projectID string) {
		seed(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'done', '', ?, '', 'linear', ?, ?)`,
			id, repoID, authzPathsOrg, "title "+id, at, projectID, at)
	}
	item("linear:direct-granted", authzPathsRepoG, authzPathsProjectP)
	item("linear:direct-denied", authzPathsRepoN, authzPathsProjectP)
	item("linear:p-project", zeroRepositoryID, authzPathsProjectP)
	item("linear:p-both", zeroRepositoryID, authzPathsProjectP)
	item("linear:q-link", zeroRepositoryID, authzPathsProjectQ)
	item("linear:q-none", zeroRepositoryID, authzPathsProjectQ)
	item("linear:q-heuristic", zeroRepositoryID, authzPathsProjectQ)
	item("linear:q-explicit", zeroRepositoryID, authzPathsProjectQ)
	item("linear:q-heuristic-2", zeroRepositoryID, authzPathsProjectQ)
	item("linear:q-projectless", zeroRepositoryID, "")
	// q-projectless is a member of Q by a transition only; its own row names
	// no project, so no project path can reach it.
	seed(`INSERT INTO project_membership_transitions (org_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, ?, 'work_item', ?, 'linear', '', ?, '', 'Q', 'fixture', ?, ?, 'q-projectless-add')`,
		authzPathsOrg, zeroRepositoryID, "linear:q-projectless", authzPathsProjectQ, at.Add(-time.Hour), at)
	link := func(workItemID, repoID, provenance string, pr uint32) {
		seed(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, 1, ?, 'fixture', ?, ?)`,
			repoID, workItemID, pr, provenance, at, authzPathsOrg)
	}
	// linear:secret sits in the non-granted repository; linear:shared-id is
	// stored twice, once authorized and once not.
	item("linear:secret", authzPathsRepoN, "")
	item("linear:shared-id", authzPathsRepoG, "")
	item("linear:shared-id", authzPathsRepoN, "")
	dependency := func(source, target, relationship string) {
		seed(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, last_synced) VALUES (?, ?, ?, ?, ?)`,
			authzPathsOrg, source, target, relationship, at)
	}
	dependency("linear:secret", "linear:p-project", "blocks")
	dependency("linear:p-both", "linear:p-project", "blocks")
	dependency("linear:shared-id", "linear:p-project", "blocks")
	dependency("linear:p-project", "linear:secret", "parent_of")
	dependency("linear:p-project", "linear:p-both", "parent_of")
	dependency("linear:p-project", "linear:shared-id", "parent_of")
	link("linear:p-both", authzPathsRepoG, "native", 1)
	link("linear:q-link", authzPathsRepoG, "native", 2)
	link("linear:q-heuristic", authzPathsRepoG, "heuristic", 3)
	link("linear:q-explicit", authzPathsRepoG, "explicit_text", 4)
	link("linear:q-heuristic-2", authzPathsRepoG, "heuristic", 5)
	return authzPathsFixture{query: query, direct: direct, at: at}
}

func authzPathsPrincipal(scopes ...string) storage.Principal {
	return storage.Principal{OrgID: authzPathsOrg, RepositoryScopes: scopes}
}

func TestWorkItemAuthorizationPathsThroughEverySQLReader(t *testing.T) {
	fixture := newAuthzPathsFixture(t)
	ctx := context.Background()
	scoped := authzPathsPrincipal(authzPathsSlugG)

	t.Run("S1 census counts each path and the Info line carries them", func(t *testing.T) {
		gate, err := contextfabric.NewWorkItemMembershipGate(1, 1)
		if err != nil {
			t.Fatalf("create gate: %v", err)
		}
		var buffer bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
		reader, err := NewWorkItemMembershipReader(fixture.query, WorkItemMembershipReaderOptions{
			Gate: gate, Telemetry: contextfabric.NewSlogWorkItemMembershipTelemetry(logger),
		})
		if err != nil {
			t.Fatalf("create membership reader: %v", err)
		}
		type want struct {
			authorized, denied int
			members            []string
			paths              contextfabric.WorkItemMembershipPathCensus
		}
		for _, tc := range []struct {
			name      string
			principal storage.Principal
			project   string
			want      want
		}{
			{"P scoped", scoped, authzPathsProjectP, want{3, 1, []string{"linear:direct-granted", "linear:p-both", "linear:p-project"},
				contextfabric.WorkItemMembershipPathCensus{DirectRepository: 1, ProjectOwnership: 2, PullRequestLink: 1, RepoLess: 2}}},
			{"Q scoped", scoped, authzPathsProjectQ, want{1, 5, []string{"linear:q-link"},
				contextfabric.WorkItemMembershipPathCensus{PullRequestLink: 1, RepoLess: 6, RepoLessDenied: 5, DeniedProjectLess: 1, ExcludedExplicitTextLink: 1, ExcludedHeuristicLink: 2}}},
			// The organization grant names no repository, so no link row is
			// matched against a grant and nothing is disclosed as excluded.
			{"Q organization-wide", authzPathsPrincipal(), authzPathsProjectQ, want{6, 0, []string{"linear:q-explicit", "linear:q-heuristic", "linear:q-heuristic-2", "linear:q-link", "linear:q-none", "linear:q-projectless"},
				contextfabric.WorkItemMembershipPathCensus{OrganizationGrant: 6, RepoLess: 6}}},
		} {
			buffer.Reset()
			lease, result, err := reader.BeginWorkItemMembership(ctx, tc.principal, contextfabric.WorkItemMembershipRequest{
				Anchor: workItemMembershipTestAnchor(t, "linear", tc.project), S1Instant: fixture.at,
			})
			if err != nil || lease == nil {
				t.Fatalf("%s: BeginWorkItemMembership lease=%v err=%v", tc.name, lease, err)
			}
			lease.Release()
			census := result.Census
			if census.State != contextfabric.WorkItemMembershipCensusExact || census.AuthorizedPopulation != tc.want.authorized || census.DeniedPopulation != tc.want.denied || census.Paths != tc.want.paths {
				t.Fatalf("%s: census = %+v, want authorized=%d denied=%d paths=%+v", tc.name, census, tc.want.authorized, tc.want.denied, tc.want.paths)
			}
			var members []string
			for _, member := range result.Members {
				members = append(members, member.WorkItemID)
			}
			sort.Strings(members)
			if strings.Join(members, ",") != strings.Join(tc.want.members, ",") {
				t.Fatalf("%s: members = %v, want %v", tc.name, members, tc.want.members)
			}

			var line map[string]any
			if err := json.Unmarshal(buffer.Bytes(), &line); err != nil {
				t.Fatalf("%s: S1 Info line is not one JSON record: %v\n%s", tc.name, err, buffer.String())
			}
			if line["msg"] != "context fabric work item membership s1" || line["level"] != "INFO" {
				t.Fatalf("%s: S1 line = %v", tc.name, line)
			}
			organizationWide := len(tc.principal.RepositoryScopes) == 0
			exact := 1
			if organizationWide {
				exact = 0
			}
			for key, value := range map[string]any{
				"grant_organization_wide":                organizationWide,
				"grant_exact_selectors":                  float64(exact),
				"grant_owner_selectors":                  float64(0),
				"grant_requested_selectors":              false,
				"authorized_population":                  float64(tc.want.authorized),
				"denied_population":                      float64(tc.want.denied),
				"organization_grant_population":          float64(tc.want.paths.OrganizationGrant),
				"direct_repo_population":                 float64(tc.want.paths.DirectRepository),
				"project_ownership_population":           float64(tc.want.paths.ProjectOwnership),
				"pr_link_population":                     float64(tc.want.paths.PullRequestLink),
				"repo_less_population":                   float64(tc.want.paths.RepoLess),
				"repo_less_denied_population":            float64(tc.want.paths.RepoLessDenied),
				"denied_project_less_population":         float64(tc.want.paths.DeniedProjectLess),
				"excluded_explicit_text_link_population": float64(tc.want.paths.ExcludedExplicitTextLink),
				"excluded_heuristic_link_population":     float64(tc.want.paths.ExcludedHeuristicLink),
			} {
				if got, ok := line[key]; !ok || got != value {
					t.Errorf("%s: S1 line %s = %v (present=%t), want %v", tc.name, key, got, ok, value)
				}
			}
		}
	})

	t.Run("read caps cover the measured trial read and the statement shapes they were derived from", func(t *testing.T) {
		// The caps were derived from system.query_log on the trial store's
		// 1675-item project: S1 32,504 rows over 17 ReadFromMergeTree
		// passes; the content readers at most 23,948 rows over 11 passes
		// (status 10, roll-up 11); each x60 for a large organization, x2
		// margin. A cap below that, or a statement that grew passes (and so
		// reads more per organization than the cap was sized for), fails
		// here rather than surfacing later as an unmeasured census.
		const scale = 60 * 2
		if workItemMembershipMaxRowsToRead < 32_504*scale {
			t.Fatalf("workItemMembershipMaxRowsToRead = %d, below the measured S1 read x%d", workItemMembershipMaxRowsToRead, scale)
		}
		if workItemReaderMaxRowsToRead < 23_948*scale {
			t.Fatalf("workItemReaderMaxRowsToRead = %d, below the measured content read x%d", workItemReaderMaxRowsToRead, scale)
		}
		passes := func(name, statement string, bindings []readers.Binding) int {
			// EXPLAIN goes through the native connection: the production
			// client refuses any statement that is not a plain read.
			parameters := clickhousedriver.Parameters{"org_id": authzPathsOrg}
			for _, binding := range bindings {
				switch value := binding.Value.(type) {
				case string:
					parameters[binding.Name] = value
				case []string:
					quoted := make([]string, 0, len(value))
					for _, item := range value {
						quoted = append(quoted, "'"+strings.ReplaceAll(item, "'", "\\'")+"'")
					}
					parameters[binding.Name] = "[" + strings.Join(quoted, ",") + "]"
				case uint8:
					parameters[binding.Name] = strconv.Itoa(int(value))
				case uint32:
					parameters[binding.Name] = strconv.Itoa(int(value))
				case time.Time:
					parameters[binding.Name] = value.UTC().Format("2006-01-02 15:04:05")
				default:
					t.Fatalf("%s binding %s has unhandled type %T", name, binding.Name, binding.Value)
				}
			}
			rows, err := fixture.direct.Query(clickhousedriver.Context(ctx, clickhousedriver.WithParameters(parameters)), "EXPLAIN indexes = 1 "+statement)
			if err != nil {
				t.Fatalf("%s EXPLAIN: %v", name, err)
			}
			defer rows.Close()
			count := 0
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatalf("%s EXPLAIN scan: %v", name, err)
				}
				if strings.Contains(line, "ReadFromMergeTree") {
					count++
				}
			}
			return count
		}
		scope := workItemRepositoryAuthorization(scoped, nil)
		s1, s1Bindings := workItemMembershipS1Statement(scope, 200)
		s1Bindings = append(s1Bindings,
			readers.Binding{Name: "anchor_provider", Value: "linear"}, readers.Binding{Name: "anchor_project_id", Value: authzPathsProjectP},
			readers.Binding{Name: "s1_instant", Value: fixture.at}, readers.Binding{Name: "serve_limit", Value: uint32(200)})
		rollup, rollupBindings := workItemProjectCompletionStatement(scope)
		rollupBindings = append(rollupBindings, readers.Binding{Name: "ids", Value: []string{"linear:" + authzPathsProjectP}})
		for _, tc := range []struct {
			name      string
			statement string
			bindings  []readers.Binding
			max       int
		}{
			{"S1", s1, s1Bindings, 17},
			{"project roll-up", rollup, rollupBindings, 11},
		} {
			got := passes(tc.name, tc.statement, tc.bindings)
			if got == 0 || got > tc.max {
				t.Fatalf("%s reads %d ReadFromMergeTree passes, derivation assumed at most %d -- re-derive the cap", tc.name, got, tc.max)
			}
			t.Logf("%s: %d ReadFromMergeTree passes (derivation: %d)", tc.name, got, tc.max)
		}
	})

	t.Run("S1 counts every member past the served row bound", func(t *testing.T) {
		// bound+1 authorized members rank ahead of every denied one, so a
		// count taken after the row bound would see no denied member at all.
		const projectR = "authz-proj-r"
		bound := contextfabric.WorkItemMembershipCensusLimit + 1
		for _, statement := range []string{
			`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES ('` + projectR + `', '` + authzPathsOrg + `', 'linear', NULL, 'R', 1, 'started', '', now64(3))`,
			`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id) SELECT concat('linear:r-granted-', toString(number)), '` + authzPathsRepoG + `', '` + authzPathsOrg + `', 't', 'open', '', now64(3), '', 'linear', '` + projectR + `' FROM numbers(` + strconv.Itoa(bound+1) + `)`,
			`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id) SELECT concat('linear:r-denied-', toString(number)), '` + authzPathsRepoN + `', '` + authzPathsOrg + `', 't', 'open', '', now64(3), '', 'linear', '` + projectR + `' FROM numbers(3)`,
		} {
			if err := fixture.direct.Exec(ctx, statement); err != nil {
				t.Fatalf("seed bound fixture: %v", err)
			}
		}
		gate, err := contextfabric.NewWorkItemMembershipGate(1, 1)
		if err != nil {
			t.Fatalf("create gate: %v", err)
		}
		reader, err := NewWorkItemMembershipReader(fixture.query, WorkItemMembershipReaderOptions{Gate: gate, Telemetry: contextfabric.NoopWorkItemMembershipTelemetry{}})
		if err != nil {
			t.Fatalf("create membership reader: %v", err)
		}
		lease, result, err := reader.BeginWorkItemMembership(ctx, scoped, contextfabric.WorkItemMembershipRequest{
			Anchor: workItemMembershipTestAnchor(t, "linear", projectR), S1Instant: fixture.at,
		})
		if err != nil || lease == nil {
			t.Fatalf("BeginWorkItemMembership lease=%v err=%v", lease, err)
		}
		lease.Release()
		census := result.Census
		if census.DeniedPopulation != 3 || census.Paths.DirectRepository != bound+1 || census.State != contextfabric.WorkItemMembershipCensusFloor {
			t.Fatalf("census past the bound = %+v, want denied=3 direct_repo=%d state=floor", census, bound+1)
		}
	})

	t.Run("scope expander admits repo-less items on a re-checked granted repository", func(t *testing.T) {
		expander := NewScopeExpander(fixture.query)
		for _, tc := range []struct {
			name                            string
			project                         string
			admitted                        []string
			projectAdmitted                 int
			linkAdmitted                    int
			excludedText, excludedHeuristic int
			projectPop, linkPop             int
			directPop, authorized           int
		}{
			{"P", authzPathsProjectP, []string{"linear:direct-granted", "linear:p-both", "linear:p-project"}, 2, 1, 0, 0, 2, 1, 1, 3},
			{"Q", authzPathsProjectQ, []string{"linear:q-link"}, 0, 1, 1, 2, 0, 1, 0, 1},
		} {
			origin := workItemMembershipTestAnchor(t, "linear", tc.project).Subject
			candidates, counts, err := expander.projectWorkItems(ctx, scoped, authzPathsOrg, []contextfabric.SubjectRef{origin}, 50)
			if err != nil {
				t.Fatalf("%s: projectWorkItems: %v", tc.name, err)
			}
			result := workItemExpansionResult(scoped, candidates, counts)
			var admitted []string
			for _, target := range result.Targets {
				admitted = append(admitted, target.Label)
			}
			sort.Strings(admitted)
			if strings.Join(admitted, ",") != strings.Join(tc.admitted, ",") {
				t.Fatalf("%s: admitted = %v, want %v", tc.name, admitted, tc.admitted)
			}
			c := result.Counts
			if c.ProjectOwnershipAdmittedCount != tc.projectAdmitted || c.PullRequestLinkAdmittedCount != tc.linkAdmitted ||
				c.ProjectOwnershipAuthorizedCount != tc.projectPop || c.PullRequestLinkAuthorizedCount != tc.linkPop ||
				c.DirectRepositoryAuthorizedCount != tc.directPop || c.AuthorizedCount != tc.authorized || c.OrganizationGrantAuthorizedCount != 0 ||
				c.ExcludedExplicitTextLinkCount != tc.excludedText || c.ExcludedHeuristicLinkCount != tc.excludedHeuristic ||
				!c.AuthorizationGrantMeasured || c.AuthorizationGrantOrganizationWide || c.AuthorizationGrantExactSelectors != 1 || c.AuthorizationGrantOwnerSelectors != 0 {
				t.Fatalf("%s: counts = %+v", tc.name, c)
			}
			for _, candidate := range candidates {
				if candidate.repoLess && len(candidate.authorizationRepositories) != 1 || candidate.repoLess && candidate.authorizationRepositories[0] != authzPathsSlugG {
					t.Fatalf("%s: repo-less candidate %s carries repositories %v, want [%s]", tc.name, candidate.workItemID, candidate.authorizationRepositories, authzPathsSlugG)
				}
			}
		}

		// THE SECOND GATE RE-CHECKS THE RETURNED REPOSITORIES. A candidate
		// that names a derived path but a repository the principal does not
		// hold is dropped, whatever the statement decided.
		forged := []workItemCandidate{{
			repoID: zeroRepositoryID, workItemID: "linear:forged", authorizationSlug: noRepositorySentinelForScope, repoLess: true,
			authorizationPaths: []string{"project_ownership"}, authorizationRepositories: []string{authzPathsSlugN},
		}}
		gated := workItemExpansionResult(scoped, forged, contextfabric.FactScopeExpansionCounts{})
		if len(gated.Targets) != 0 || gated.Counts.AuthorizationDroppedCount != 1 || gated.Counts.RepoLessAuthorizationDroppedCount != 1 || gated.Counts.ProjectOwnershipAdmittedCount != 0 {
			t.Fatalf("forged candidate: targets=%v counts=%+v, want dropped", gated.Targets, gated.Counts)
		}
		// Nor does a granted repository admit a row with a real repository,
		// or a row whose paths name no derived path.
		for _, candidate := range []workItemCandidate{
			{repoID: authzPathsRepoN, workItemID: "linear:real-repo", authorizationSlug: authzPathsSlugN, authorizationPaths: []string{"project_ownership"}, authorizationRepositories: []string{authzPathsSlugG}},
			{repoID: zeroRepositoryID, workItemID: "linear:no-derived-path", authorizationSlug: noRepositorySentinelForScope, repoLess: true, authorizationPaths: []string{"organization_grant", "direct_repo"}, authorizationRepositories: []string{authzPathsSlugG}},
		} {
			gated := workItemExpansionResult(scoped, []workItemCandidate{candidate}, contextfabric.FactScopeExpansionCounts{})
			if len(gated.Targets) != 0 {
				t.Fatalf("candidate %s admitted by the second gate: %+v", candidate.workItemID, gated.Targets)
			}
		}
	})

	t.Run("dependency facts name only work items the principal may see", func(t *testing.T) {
		canonicalID, _, err := identity.Derive(identity.KindWorkItem, []string{zeroRepositoryID, "linear:p-project"}, nil)
		if err != nil {
			t.Fatalf("derive subject: %v", err)
		}
		subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: canonicalID}
		current := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
		named := func(principal storage.Principal, kind contextfabric.FactKind, field string) string {
			var provider interface {
				ReadFacts(context.Context, storage.Principal, contextfabric.FactQuery) (contextfabric.FactProviderResult, error)
			} = newBlockersProvider(fixture.query)
			if kind == contextfabric.FactRequiredChildren {
				provider = newRequiredChildrenProvider(fixture.query)
			}
			result, err := provider.ReadFacts(ctx, principal, contextfabric.FactQuery{Time: current, Kind: kind, Subjects: []contextfabric.SubjectRef{subject}})
			if err != nil {
				t.Fatalf("%s ReadFacts: %v", kind, err)
			}
			var ids []string
			for _, fact := range result.Facts {
				ids = append(ids, *fact.Fields[field].String)
			}
			sort.Strings(ids)
			return strings.Join(ids, ",")
		}
		for _, tc := range []struct {
			principal storage.Principal
			kind      contextfabric.FactKind
			field     string
			want      string
		}{
			{scoped, contextfabric.FactBlockers, "blocked_by_work_item_id", "linear:p-both"},
			{scoped, contextfabric.FactRequiredChildren, "required_child_work_item_id", "linear:p-both"},
			{authzPathsPrincipal(), contextfabric.FactBlockers, "blocked_by_work_item_id", "linear:p-both,linear:secret,linear:shared-id"},
			{authzPathsPrincipal(), contextfabric.FactRequiredChildren, "required_child_work_item_id", "linear:p-both,linear:secret,linear:shared-id"},
			// A principal who may not see the subject gets nothing about it,
			// even when it may see the item on the other end (linear:secret
			// lives in the repository this principal holds).
			{authzPathsPrincipal(authzPathsSlugN), contextfabric.FactBlockers, "blocked_by_work_item_id", ""},
			{authzPathsPrincipal(authzPathsSlugN), contextfabric.FactRequiredChildren, "required_child_work_item_id", ""},
			{authzPathsPrincipal("zzz/none"), contextfabric.FactBlockers, "blocked_by_work_item_id", ""},
			{authzPathsPrincipal("zzz/none"), contextfabric.FactRequiredChildren, "required_child_work_item_id", ""},
		} {
			if got := named(tc.principal, tc.kind, tc.field); got != tc.want {
				t.Fatalf("%s for %v = %q, want %q", tc.kind, tc.principal.RepositoryScopes, got, tc.want)
			}
		}
	})

	t.Run("the scope expansion's read ceiling is enforced and classified", func(t *testing.T) {
		expander := NewScopeExpander(fixture.query)
		origin := workItemMembershipTestAnchor(t, "linear", authzPathsProjectP).Subject
		saved := workItemScopeSelectionMaxRowsToRead
		t.Cleanup(func() { workItemScopeSelectionMaxRowsToRead = saved })
		workItemScopeSelectionMaxRowsToRead = 5
		_, _, err := expander.projectWorkItems(ctx, scoped, authzPathsOrg, []contextfabric.SubjectRef{origin}, 50)
		if err == nil || !errors.Is(err, contextfabric.ErrFactScopeReadLimitExceeded) {
			t.Fatalf("lowered ceiling error = %v, want ErrFactScopeReadLimitExceeded", err)
		}
		workItemScopeSelectionMaxRowsToRead = saved
		if _, _, err := expander.projectWorkItems(ctx, scoped, authzPathsOrg, []contextfabric.SubjectRef{origin}, 50); err != nil {
			t.Fatalf("shipped ceiling error = %v", err)
		}
	})

	t.Run("content readers serve exactly the authorized members", func(t *testing.T) {
		subjects := []contextfabric.SubjectRef{}
		labels := map[string]string{}
		for _, id := range []struct{ repo, item string }{
			{authzPathsRepoG, "linear:direct-granted"}, {authzPathsRepoN, "linear:direct-denied"},
			{zeroRepositoryID, "linear:p-project"}, {zeroRepositoryID, "linear:p-both"},
			{zeroRepositoryID, "linear:q-link"}, {zeroRepositoryID, "linear:q-none"},
			{zeroRepositoryID, "linear:q-heuristic"}, {zeroRepositoryID, "linear:q-projectless"},
			{zeroRepositoryID, "linear:q-explicit"},
		} {
			canonicalID, omitted, err := identity.Derive(identity.KindWorkItem, []string{id.repo, id.item}, nil)
			if err != nil || omitted {
				t.Fatalf("derive %s: %v", id.item, err)
			}
			subjects = append(subjects, contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: canonicalID, Label: id.item})
			labels[canonicalID] = id.item
		}
		want := "linear:direct-granted,linear:p-both,linear:p-project,linear:q-link"
		current := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
		for _, reader := range []struct {
			kind     contextfabric.FactKind
			provider interface {
				ReadFacts(context.Context, storage.Principal, contextfabric.FactQuery) (contextfabric.FactProviderResult, error)
			}
		}{
			{contextfabric.FactStatus, newStatusProvider(fixture.query)},
			{contextfabric.FactWork, newWorkProvider(fixture.query)},
			{contextfabric.FactActualCompletion, newActualCompletionProvider(fixture.query)},
		} {
			name := string(reader.kind)
			result, err := reader.provider.ReadFacts(ctx, scoped, contextfabric.FactQuery{Time: current, Kind: reader.kind, Subjects: subjects})
			if err != nil {
				t.Fatalf("%s ReadFacts: %v", name, err)
			}
			seen := map[string]bool{}
			for _, fact := range result.Facts {
				seen[labels[fact.Subject.CanonicalID]] = true
			}
			if len(result.Facts) == 0 {
				t.Logf("%s result: %+v", name, result)
			}
			var got []string
			for label := range seen {
				got = append(got, label)
			}
			sort.Strings(got)
			if strings.Join(got, ",") != want {
				t.Fatalf("%s served %v, want %s", name, got, want)
			}
		}

		// The project roll-up counts the members the rule admits.
		result, err := newActualCompletionProvider(fixture.query).ReadFacts(ctx, scoped, contextfabric.FactQuery{
			Time: current, Kind: contextfabric.FactActualCompletion,
			Subjects: []contextfabric.SubjectRef{workItemMembershipTestAnchor(t, "linear", authzPathsProjectP).Subject},
		})
		if err != nil {
			t.Fatalf("project actual_completion: %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("project actual_completion facts = %+v, want one roll-up", result.Facts)
		}
		if counted := result.Facts[0].Fields["counted_work_items"]; counted.Integer == nil || *counted.Integer != 3 {
			t.Fatalf("project roll-up counted_work_items = %+v, want 3 (direct-granted, p-project, p-both)", counted)
		}
	})
}
