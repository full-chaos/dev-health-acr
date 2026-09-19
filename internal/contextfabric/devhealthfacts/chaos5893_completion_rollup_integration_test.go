package devhealthfacts_test

// CHAOS-5893: the roll-up's own unit tests use fakeClient, which returns
// pre-aggregated canned rows selected by a SQL text marker regardless of
// the statement's WHERE clause -- a predicate defect is invisible there by
// construction. These tests EXECUTE workItemProjectCompletionStatement
// against a real ClickHouse server over seeded work_items/projects/repos
// rows, so the numerator/denominator arithmetic, the identity join (id row
// and key row), and the authorization filter are proven by what the
// database actually computes, never by what a fixture was told to return.

import (
	"context"
	"net"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// readProjectCompletionFact drives the REAL ActualCompletionProvider (never
// a struct literal) for one project subject and returns its single served
// fact, or nil if the project served none (zero members, or every member
// cancelled).
func readProjectCompletionFact(t *testing.T, client *runtimeclickhouse.Client, principal storage.Principal, provider, projectID string) *contextfabric.CanonicalFact {
	t.Helper()
	facts := readProjectCompletionFacts(t, client, principal, []contextfabric.SubjectRef{projectSubject(provider, projectID)})
	if len(facts) == 0 {
		return nil
	}
	return &facts[0]
}

func readProjectCompletionFacts(t *testing.T, client *runtimeclickhouse.Client, principal storage.Principal, subjects []contextfabric.SubjectRef) []contextfabric.CanonicalFact {
	t.Helper()
	p := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactActualCompletion)
	result, err := p.ReadFacts(context.Background(), principal, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactActualCompletion, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	return result.Facts
}

func factInt(t *testing.T, fact contextfabric.CanonicalFact, field string) int64 {
	t.Helper()
	v, ok := fact.Fields[field]
	if !ok || v.Integer == nil {
		t.Fatalf("field %q missing or not an integer in %+v", field, fact.Fields)
	}
	return *v.Integer
}

func factNumber(t *testing.T, fact contextfabric.CanonicalFact, field string) float64 {
	t.Helper()
	v, ok := fact.Fields[field]
	if !ok || v.Number == nil {
		t.Fatalf("field %q missing or not a number in %+v", field, fact.Fields)
	}
	return *v.Number
}

// TestActualCompletionProjectRollupStatusMatrixAgainstRealClickHouse sweeps
// every work_items.status value this producer's own vocabulary comment
// names, crossed with completed_at present/absent, for ONE project, and
// checks the served roll-up's arithmetic against a hand-computed
// expectation. The cancelled+completed_at-present row proves a cancelled
// item that also carries a completed_at stays out of completed_count.
func TestActualCompletionProjectRollupStatusMatrixAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	client, direct := sharedClickHouseFixture(t)
	orgID := sharedTestOrgID(t)
	at := time.Now().UTC().Truncate(time.Millisecond)

	repoID := "d3c0ffee-0000-4000-8000-000000000001"
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		repoID, orgID, "acme/status-matrix", "github", at); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"PROJ-1", orgID, "linear", nil, "Status Matrix", uint8(1), "active", "https://example.test/PROJ-1", at); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	// Every work_items.status this package's own vocabulary comment names
	// (workitems.go's workItemCancelledStatus/workItemUnknownStatus doc
	// comment: {backlog, canceled, done, in_progress, todo, unknown}),
	// crossed with completed_at present/absent. id is unique per row so
	// ReplacingMergeTree never collapses two rows into one.
	type seedRow struct {
		id, status  string
		completedAt *time.Time
	}
	rows := []seedRow{
		{"WI-BACKLOG-OPEN", "backlog", nil},
		{"WI-TODO-OPEN", "todo", nil},
		{"WI-INPROGRESS-OPEN", "in_progress", nil},
		{"WI-DONE-COMPLETED", "done", &at},
		{"WI-DONE-OPEN", "done", nil},              // done but no completed_at: not counted as completed
		{"WI-CANCELED-COMPLETED", "canceled", &at}, // cancelled with a completed_at: must not inflate completed_count
		{"WI-CANCELED-OPEN", "canceled", nil},
		{"WI-UNKNOWN-COMPLETED", "unknown", &at},
		{"WI-UNKNOWN-OPEN", "unknown", nil},
	}
	for _, row := range rows {
		if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, created_at, updated_at, completed_at, project_id, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			row.id, repoID, orgID, "linear", "matrix "+row.id, row.status, at, at, row.completedAt, "PROJ-1", at); err != nil {
			t.Fatalf("seed work item %s: %v", row.id, err)
		}
	}

	fact := readProjectCompletionFact(t, client, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, "linear", "PROJ-1")
	if fact == nil {
		t.Fatal("facts = none, want one served roll-up")
	}

	// Hand-computed expectation over the 9 seeded rows. Only CANCELLED is
	// excluded (chris's ruling) -- unknown-status items are counted
	// normally, completed_at included, same as any other non-cancelled
	// status:
	//   work_item_count      = 9
	//   cancelled_count       = 2  (WI-CANCELED-COMPLETED, WI-CANCELED-OPEN)
	//   unknown_status_count  = 2  (WI-UNKNOWN-COMPLETED, WI-UNKNOWN-OPEN)
	//   counted_work_items    = 9 - 2 = 7
	//   completed_count       = 2  (WI-DONE-COMPLETED, WI-UNKNOWN-COMPLETED --
	//                                the cancelled completed item is the ONLY
	//                                one excluded by the fixed numerator;
	//                                WI-DONE-OPEN has no completed_at)
	//   completion_ratio      = 2/7
	if got := factInt(t, *fact, "work_item_count"); got != 9 {
		t.Errorf("work_item_count = %d, want 9", got)
	}
	if got := factInt(t, *fact, "cancelled_count"); got != 2 {
		t.Errorf("cancelled_count = %d, want 2", got)
	}
	if got := factInt(t, *fact, "unknown_status_count"); got != 2 {
		t.Errorf("unknown_status_count = %d, want 2", got)
	}
	if got := factInt(t, *fact, "counted_work_items"); got != 7 {
		t.Errorf("counted_work_items = %d, want 7", got)
	}
	if got := factInt(t, *fact, "completed_count"); got != 2 {
		t.Errorf("completed_count = %d, want 2 -- the cancelled item's completed_at must never inflate this, but an unknown-status one still counts", got)
	}
	if got := factNumber(t, *fact, "completion_ratio"); got != 2.0/7.0 {
		t.Errorf("completion_ratio = %v, want %v", got, 2.0/7.0)
	}
	if got := factInt(t, *fact, "completed_count"); got > factInt(t, *fact, "counted_work_items") {
		t.Errorf("completed_count = %d exceeds counted_work_items = %d -- the population partition invariant is violated", got, factInt(t, *fact, "counted_work_items"))
	}
}

// TestActualCompletionProjectRollupIdentityJoinAgainstRealClickHouse proves
// the project-identity join's two resolution arms against a real server in
// the SAME read: one project matched by its own id (the Linear convention),
// one matched by its project_key (the GitLab convention, work_items.project_id
// carrying the KEY rather than the id -- workitems.go's own doc comment).
// Both must resolve, and neither must contaminate the other's counts.
func TestActualCompletionProjectRollupIdentityJoinAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	client, direct := sharedClickHouseFixture(t)
	orgID := sharedTestOrgID(t)
	at := time.Now().UTC().Truncate(time.Millisecond)

	repoID := "d3c0ffee-0000-4000-8000-000000000002"
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		repoID, orgID, "acme/identity-join", "gitlab", at); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	// Linear project: resolved by ITS OWN id. project_key left NULL, as a
	// real Linear project's row is.
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"LIN-ID-1", orgID, "linear", nil, "Linear by id", uint8(1), "active", "https://example.test/LIN-ID-1", at); err != nil {
		t.Fatalf("seed linear project: %v", err)
	}
	// GitLab project: id is a numeric-looking internal id; work_items carry
	// the project_key ("grp/proj") in project_id, per
	// readers.ProjectIdentityMatchSQL's documented "work_scope_id"
	// convention -- the join must still resolve this to the SAME project
	// subject key ("gitlab:77").
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"77", orgID, "gitlab", "grp/proj", "GitLab by key", uint8(1), "active", "https://example.test/77", at); err != nil {
		t.Fatalf("seed gitlab project: %v", err)
	}

	seed := func(id, provider, projectID, status string, completed bool) {
		var completedAt *time.Time
		if completed {
			completedAt = &at
		}
		if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, created_at, updated_at, completed_at, project_id, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, repoID, orgID, provider, "identity "+id, status, at, at, completedAt, projectID, at); err != nil {
			t.Fatalf("seed work item %s: %v", id, err)
		}
	}
	// Linear project: 2 work items, project_id = the project's OWN id.
	seed("WI-LIN-1", "linear", "LIN-ID-1", "done", true)
	seed("WI-LIN-2", "linear", "LIN-ID-1", "todo", false)
	// GitLab project: 3 work items, project_id = the project's KEY, not
	// its id "77".
	seed("WI-GL-1", "gitlab", "grp/proj", "done", true)
	seed("WI-GL-2", "gitlab", "grp/proj", "done", true)
	seed("WI-GL-3", "gitlab", "grp/proj", "todo", false)

	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}
	linear := readProjectCompletionFact(t, client, principal, "linear", "LIN-ID-1")
	if linear == nil {
		t.Fatal("linear (id-resolved) project served no fact")
	}
	if got := factInt(t, *linear, "work_item_count"); got != 2 {
		t.Errorf("linear work_item_count = %d, want 2 -- the id-row join must not also match the gitlab project's rows", got)
	}
	if got := factInt(t, *linear, "completed_count"); got != 1 {
		t.Errorf("linear completed_count = %d, want 1", got)
	}

	gitlab := readProjectCompletionFact(t, client, principal, "gitlab", "77")
	if gitlab == nil {
		t.Fatal("gitlab (key-resolved) project served no fact -- the key-row join arm did not resolve project_id='grp/proj' to project id '77'")
	}
	if got := factInt(t, *gitlab, "work_item_count"); got != 3 {
		t.Errorf("gitlab work_item_count = %d, want 3", got)
	}
	if got := factInt(t, *gitlab, "completed_count"); got != 2 {
		t.Errorf("gitlab completed_count = %d, want 2", got)
	}
}

// TestActualCompletionProjectRollupAuthorizationAgainstRealClickHouse
// proves the repository-authorization filter is REAL SQL, not a fixture
// convenience: a project's work items span two repositories, the
// requesting principal is granted only one, and the served roll-up must
// count only the authorized repository's work items.
func TestActualCompletionProjectRollupAuthorizationAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	client, direct := sharedClickHouseFixture(t)
	orgID := sharedTestOrgID(t)
	at := time.Now().UTC().Truncate(time.Millisecond)

	repoAID := "d3c0ffee-0000-4000-8000-0000000000a1"
	repoBID := "d3c0ffee-0000-4000-8000-0000000000b1"
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?), (?, ?, ?, ?, ?)`,
		repoAID, orgID, "acme/authz-a", "github", at,
		repoBID, orgID, "acme/authz-b", "github", at); err != nil {
		t.Fatalf("seed repos: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"PROJ-AUTHZ", orgID, "linear", nil, "Authz", uint8(1), "active", "https://example.test/PROJ-AUTHZ", at); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	seed := func(id, repoID, status string, completed bool) {
		var completedAt *time.Time
		if completed {
			completedAt = &at
		}
		if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, created_at, updated_at, completed_at, project_id, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, repoID, orgID, "linear", "authz "+id, status, at, at, completedAt, "PROJ-AUTHZ", at); err != nil {
			t.Fatalf("seed work item %s: %v", id, err)
		}
	}
	seed("WI-A-1", repoAID, "done", true)
	seed("WI-A-2", repoAID, "todo", false)
	seed("WI-B-1", repoBID, "done", true)
	seed("WI-B-2", repoBID, "done", true)
	seed("WI-B-3", repoBID, "todo", false)

	// Granted only repo A: the roll-up must see ONLY WI-A-1/WI-A-2.
	scoped := readProjectCompletionFact(t, client, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/authz-a"}}, "linear", "PROJ-AUTHZ")
	if scoped == nil {
		t.Fatal("scoped principal served no fact")
	}
	if got := factInt(t, *scoped, "work_item_count"); got != 2 {
		t.Errorf("scoped work_item_count = %d, want 2 -- repo B's work items must not be counted for a principal not authorized to see them", got)
	}
	if got := factInt(t, *scoped, "completed_count"); got != 1 {
		t.Errorf("scoped completed_count = %d, want 1", got)
	}

	// Granted everything: all 5 must be visible.
	unscoped := readProjectCompletionFact(t, client, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, "linear", "PROJ-AUTHZ")
	if unscoped == nil {
		t.Fatal("unscoped principal served no fact")
	}
	if got := factInt(t, *unscoped, "work_item_count"); got != 5 {
		t.Errorf("unscoped work_item_count = %d, want 5", got)
	}
}

// TestActualCompletionProjectRollupLimitProbeAgainstRealClickHouse
// executes the exact-limit and limit+1 boundary, not asserted from a
// canned row count. maxFactRowsPerQuery projects (each with one completed
// work item) must serve all of them with Truncated=false; one more must
// serve only maxFactRowsPerQuery of them with Truncated=true -- a LIMIT
// 200 read alone cannot tell "exactly 200" from "201 and the probe row
// was cut".
//
// A dedicated container, not the shared package fixture: the shared
// fixture's default database accumulates rows from every other test in
// this package, and a precise row-count boundary needs a clean corpus the
// same way work_item_read_limits_integration_test.go's own fixture does.
func TestActualCompletionProjectRollupLimitProbeAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	client, direct := newCompletionLimitProbeFixture(t, ctx)
	orgID := "org-completion-limit-probe"
	at := time.Now().UTC().Truncate(time.Millisecond)

	repoID := "d3c0ffee-0000-4000-8000-00000000c001"
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		repoID, orgID, "acme/limit-probe", "linear", at); err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	const exactLimit = 200
	// seedProjects seeds count NEW projects (each with one completed work
	// item) starting at a distinct id offset, so two calls never collide on
	// the same project id -- every project across both subtests below is
	// unique.
	seedProjects := func(offset, count int) []contextfabric.SubjectRef {
		subjects := make([]contextfabric.SubjectRef, 0, count)
		for i := offset; i < offset+count; i++ {
			projectID := "LIMIT-" + paddedIndex(i)
			if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				projectID, orgID, "linear", nil, "limit "+projectID, uint8(1), "active", "https://example.test/"+projectID, at); err != nil {
				t.Fatalf("seed project %s: %v", projectID, err)
			}
			if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, created_at, updated_at, completed_at, project_id, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				"WI-"+projectID, repoID, orgID, "linear", "limit item "+projectID, "done", at, at, at, projectID, at); err != nil {
				t.Fatalf("seed work item for %s: %v", projectID, err)
			}
			subjects = append(subjects, projectSubject("linear", projectID))
		}
		return subjects
	}

	exactSubjects := seedProjects(0, exactLimit)

	t.Run("exact cap is not reported truncated", func(t *testing.T) {
		p := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactActualCompletion)
		result, err := p.ReadFacts(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactActualCompletion, Subjects: exactSubjects,
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		if len(result.Facts) != exactLimit {
			t.Fatalf("facts = %d, want exactly %d", len(result.Facts), exactLimit)
		}
		if result.Truncated {
			t.Fatalf("Truncated = true for exactly %d projects, want false -- an exact-cap population must not read as truncated", exactLimit)
		}
	})

	t.Run("one over the cap is reported truncated and capped", func(t *testing.T) {
		overflowSubjects := seedProjects(exactLimit, 1)
		subjects := append(append([]contextfabric.SubjectRef{}, exactSubjects...), overflowSubjects...)

		p := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactActualCompletion)
		result, err := p.ReadFacts(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactActualCompletion, Subjects: subjects,
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		if len(result.Facts) != exactLimit {
			t.Fatalf("facts = %d, want capped at %d even though %d projects were requested", len(result.Facts), exactLimit, len(subjects))
		}
		if !result.Truncated {
			t.Fatalf("Truncated = false for %d requested projects (cap %d), want true -- the LIMIT+1 probe row must be detected", len(subjects), exactLimit)
		}
	})
}

func paddedIndex(i int) string {
	digits := "0123456789"
	out := make([]byte, 4)
	for pos := 3; pos >= 0; pos-- {
		out[pos] = digits[i%10]
		i /= 10
	}
	return string(out)
}

// newCompletionLimitProbeFixture gives the limit-probe test its own
// ClickHouse server, mirroring work_item_read_limits_integration_test.go's
// newWorkItemReadLimitFixture for the same reason: a 200/201-row boundary
// needs a corpus with no rows from any other test.
func newCompletionLimitProbeFixture(t *testing.T, ctx context.Context) (*runtimeclickhouse.Client, clickhousedriver.Conn) {
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
		t.Fatalf("start completion limit-probe ClickHouse container: %v", err)
	}
	containerID := container.GetContainerID()
	var query *runtimeclickhouse.Client
	var direct clickhousedriver.Conn
	t.Cleanup(func() {
		if query != nil {
			if err := query.Close(); err != nil {
				t.Errorf("close completion limit-probe query client (container_id=%q): %v", containerID, err)
			}
		}
		if direct != nil {
			if err := direct.Close(); err != nil {
				t.Errorf("close completion limit-probe native client (container_id=%q): %v", containerID, err)
			}
		}
		if err := container.Terminate(context.Background()); err != nil {
			t.Errorf("terminate completion limit-probe ClickHouse container (container_id=%q): %v", containerID, err)
		}
	})
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("resolve completion limit-probe ClickHouse host: %v", err)
	}
	port, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatalf("resolve completion limit-probe ClickHouse port: %v", err)
	}
	addr := net.JoinHostPort(host, port.Port())
	direct, err = clickhousedriver.Open(&clickhousedriver.Options{
		Addr: []string{addr}, Auth: clickhousedriver.Auth{Database: "default", Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open completion limit-probe native ClickHouse client: %v", err)
	}
	pingDeadline := time.Now().Add(30 * time.Second)
	for {
		if pingErr := direct.Ping(ctx); pingErr == nil {
			break
		} else if time.Now().After(pingDeadline) {
			t.Fatalf("completion limit-probe ClickHouse did not accept a ping: %v", pingErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
	query, err = runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/default", DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open completion limit-probe production query client: %v", err)
	}
	for _, statement := range devhealthschema.DDL("repos", "projects", "work_items", "team_project_ownership", "team_repo_ownership", "work_graph_issue_pr") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create completion limit-probe production table: %v\n%s", err, statement)
		}
	}
	return query, direct
}
