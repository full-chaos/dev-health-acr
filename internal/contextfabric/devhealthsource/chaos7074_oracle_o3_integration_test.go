package devhealthsource_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Oracle O3 (CHAOS-7036 design J.4; CHAOS-7074).
//
//	Path 1: read_relationships(team, OWNED_BY_TEAM, in) -- every page,
//	        through the REAL reader, the REAL FalkorDB edge query and the
//	        REAL subject and edge gates, over a graph the REAL producer
//	        (devhealthsource.TeamsProjectsSource -> falkorgraph
//	        ApplyProjectionBatch) built from seeded ClickHouse rows.
//	Path 2: the team_repo_ownership population, read straight from
//	        ClickHouse: per (provider, repository, team, source) a fact is
//	        current when it has started and an open row exists for it, or,
//	        with no open row, when its latest closed row has not ended.
//	Contract: equal MULTISETS of repository ids (a repeated id on path 1 is
//	        a failure, not a set collapse), every leaf tagged {"t","v"}.
//
// NULL repo_id rows (CHAOS-7119 landed, CHAOS-7164). An ownership row with no
// repo_id resolves by (org, provider, lower-cased name) against repos, and the
// producer emits an ordinary repository -> team edge for it. Path 2 applies
// the same rule on its own (a repos lookup in Go, not the shared SQL): a NULL
// row whose name matches a repos row counts as that repository (fixture: T1
// "Acme/R11", mixed case on purpose), so the oracle compares the name-resolved
// edge. The remaining named class is null_repo_id: a NULL row whose name
// matches no repos row (fixture: T1 "acme/ghost") has no repository node, the
// producer omits it, and path 2 counts it apart; the test asserts both counts.
//
// Acceptance gate (planted by hand against the production code; see the PR
// body for each run): P1 a current read that returns ended edges; P2 a
// keyset of ">=" (repeats); P3 a LIMIT without the +1 probe (the walk ends
// early); P4 the in/out arms swapped.

const chaos7074FalkorImage = "falkordb/falkordb@sha256:ad09d5051bbda1cfee8cef9d7f41ffe1bcb1c5327b82c442c989e84ab8cc33d3"

func chaos7074FalkorAdapter(t *testing.T, ctx context.Context) *falkorgraph.Adapter {
	t.Helper()
	return chaos7074FalkorAdapterWith(t, ctx, nil)
}

func chaos7074FalkorAdapterWith(t *testing.T, ctx context.Context, configure func(*falkorgraph.Config)) *falkorgraph.Adapter {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: chaos7074FalkorImage, ExposedPorts: []string{"6379/tcp"},
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
	config := falkorgraph.Config{
		Addr: host + ":" + port.Port(), GraphPrefix: "acr-cf-o3", RequestTimeout: 15 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 10, AllowInsecure: true, TLS: false,
	}
	if configure != nil {
		configure(&config)
	}
	adapter, err := falkorgraph.New(config)
	if err != nil {
		t.Fatalf("falkorgraph.New: %v", err)
	}
	return adapter
}

type o3Leaf struct {
	T string `json:"t"`
	V string `json:"v"`
}

func o3Tagged(ids []string) string {
	sort.Strings(ids)
	leaves := make([]o3Leaf, 0, len(ids))
	for _, id := range ids {
		leaves = append(leaves, o3Leaf{T: "string", V: id})
	}
	encoded, _ := json.Marshal(leaves)
	return string(encoded)
}

func o3UUID(label string) string {
	sum := sha256.Sum256([]byte(label))
	h := hex.EncodeToString(sum[:16])
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-8" + h[17:20] + "-" + h[20:32]
}

// o3Seed writes one organization's rows. Times are relative to now so
// "current" means the same thing on both paths.
func o3Seed(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string, now time.Time) {
	t.Helper()
	longAgo, past, pastLater := now.Add(-90*24*time.Hour), now.Add(-30*24*time.Hour), now.Add(-10*24*time.Hour)
	exec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	for _, team := range []string{"T1", "T2"} {
		exec("team "+team, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, native_team_key, project_keys, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, team, team+" name", "", now, orgID, "github", team, []string{}, uint8(1))
	}
	for i := 1; i <= 12; i++ {
		if i == 9 {
			continue // R9: an ownership row whose repository has no repos row
		}
		exec(fmt.Sprintf("repo R%d", i), `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
			o3UUID(orgID+"R"+fmt.Sprint(i)), orgID, fmt.Sprintf("acme/r%d", i), "github", now)
	}
	own := func(team string, repo int, repoName string, matchType, source string, from time.Time, to any) {
		var repoID any
		if repo > 0 {
			repoID = o3UUID(orgID + "R" + fmt.Sprint(repo))
		}
		exec(fmt.Sprintf("ownership %s R%d %s", team, repo, source),
			`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "github", team, repoID, repoName, matchType, source, uint8(1), uint16(1), int32(1), from, to, now)
	}
	own("T1", 1, "acme/r1", "exact", "native", longAgo, nil)                   // open
	own("T1", 2, "acme/r2", "exact", "native", longAgo, nil)                   // open, two sources:
	own("T1", 2, "acme/r2", "exact", "manual", longAgo, nil)                   //   two edges, one id
	own("T1", 3, "acme/r3", "exact", "native", longAgo, past)                  // ended
	own("T1", 4, "acme/r4", "exact", "native", longAgo, nil)                   // older open ...
	own("T1", 4, "acme/r4", "exact", "native", pastLater, now.Add(-time.Hour)) // ... a later duplicate closed: still open
	own("T1", 5, "acme/r5", "exact", "native", longAgo, past)                  // older ended ...
	own("T1", 5, "acme/r5", "exact", "native", pastLater, nil)                 // ... reopened by a new open row
	own("T1", 6, "acme/*", "pattern", "inferred", longAgo, nil)                // pattern match, open
	own("T1", 7, "acme/r7", "exact", "native", longAgo, nil)                   // open
	own("T1", 8, "acme/r8", "exact", "provider_access", longAgo, nil)          // open
	own("T1", 9, "acme/r9", "exact", "native", longAgo, nil)                   // open, no repos row (orphan slug)
	own("T1", 0, "acme/ghost", "exact", "native", longAgo, nil)                // NULL repo_id, no repos row: named class (unresolvable)
	own("T1", 0, "Acme/R11", "exact", "native", longAgo, nil)                  // NULL repo_id, resolves by name to R11 (case-insensitive)
	own("T2", 1, "acme/r1", "exact", "native", longAgo, nil)                   // another team
	own("T2", 10, "acme/r10", "exact", "native", longAgo, nil)                 // another team
}

// o3Population is path 2.
func o3Population(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID, teamID string) (ids []string, nullRepoID, nameResolved int) {
	t.Helper()
	repoRows, err := direct.Query(ctx, `SELECT provider, lower(repo), toString(id) FROM repos FINAL WHERE org_id = ?`, orgID)
	if err != nil {
		t.Fatalf("path 2 repos query: %v", err)
	}
	byName := map[string]string{}
	for repoRows.Next() {
		var provider, name, id string
		if err := repoRows.Scan(&provider, &name, &id); err != nil {
			t.Fatal(err)
		}
		byName[provider+"\x00"+name] = id
	}
	repoRows.Close()
	rows, err := direct.Query(ctx, `
SELECT provider, repo_key, repo_name FROM (
  SELECT provider, ifNull(toString(repo_id), '') AS repo_key,
         if(isNull(repo_id), repo_full_name, '') AS repo_name,
         min(valid_from) AS first_from,
         countIf(isNull(valid_to)) > 0 AS open_exists,
         maxIf(valid_to, isNotNull(valid_to)) AS latest_closed_to
  FROM team_repo_ownership FINAL
  WHERE org_id = ? AND team_id = ?
  GROUP BY provider, repo_id, team_id, source, if(isNull(repo_id), repo_full_name, '')
)
WHERE first_from <= now64(3) AND (open_exists OR latest_closed_to > now64(3))`, orgID, teamID)
	if err != nil {
		t.Fatalf("path 2 query: %v", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var provider, key, name string
		if err := rows.Scan(&provider, &key, &name); err != nil {
			t.Fatal(err)
		}
		if key == "" {
			resolved, ok := byName[provider+"\x00"+strings.ToLower(name)]
			if !ok {
				nullRepoID++
				continue
			}
			key = resolved
			nameResolved++
		}
		id := "repository:" + key
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids, nullRepoID, nameResolved
}

// o3Project drains the real producer into the real graph.
func o3Project(t *testing.T, ctx context.Context, source *devhealthsource.TeamsProjectsSource, adapter *falkorgraph.Adapter, orgID string) {
	t.Helper()
	cursor := ""
	replays := map[string]bool{}
	for page := 0; page < 50; page++ {
		batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor})
		if err != nil {
			t.Fatalf("producer page %d: %v", page, err)
		}
		if !available {
			return
		}
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("apply producer page %d: %v", page, err)
		}
		requireCursorProgress(t, fmt.Sprintf("producer page %d", page), cursor, batch, replays)
		cursor = batch.NextCursor
	}
	t.Fatal("producer did not drain in 50 pages")
}

// o3Path1 is read_relationships, every page, limit 3.
func o3Path1(t *testing.T, reader *directread.RelationshipsReader, principal storage.Principal, teamID string) []string {
	t.Helper()
	request := directread.RelationshipsRequest{
		Subject: directread.RelationshipsSubject{Kind: "team", CanonicalID: contextfabric.TeamCanonicalID(teamID)},
		Types:   []string{"OWNED_BY_TEAM"}, Direction: "in", Limit: 3,
	}
	var ids []string
	seen := map[string]bool{}
	for page := 0; page < 100; page++ {
		sum := sha256.Sum256([]byte(fmt.Sprintf("o3-%s-%d", teamID, page)))
		ctx := observability.WithRequestID(context.Background(), "req_"+hex.EncodeToString(sum[:16]))
		response, err := reader.Read(ctx, principal, request)
		if err != nil {
			t.Fatalf("path 1 page %d: %v", page, err)
		}
		if response.Status == directread.RelationshipsDenied {
			t.Fatalf("path 1 root denied")
		}
		for _, e := range response.Edges {
			// OWNED_BY_TEAM in-edges of a team come from repositories
			// (team_repo_ownership), projects (team_project_ownership) and
			// work items (work_item_team_attributions); O3 compares the
			// repository side (on the local org's real data all three
			// occur).
			if e.To.CanonicalID != request.Subject.CanonicalID || e.Type != "OWNED_BY_TEAM" {
				t.Fatalf("path 1 served a non-ownership edge: %+v", e)
			}
			if e.From.Kind != "repository" {
				continue
			}
			// Two sources on one repository are two edges, one repository.
			// A repeated EDGE is a failure; a repeated repository is not.
			if seen[e.RelationshipID] {
				t.Fatalf("path 1 served edge %s twice", e.RelationshipID)
			}
			seen[e.RelationshipID] = true
			ids = append(ids, e.From.CanonicalID)
		}
		if response.Page.Complete {
			return dedupSorted(ids)
		}
		request.Cursor = response.Page.NextCursor
	}
	t.Fatal("path 1 did not end in 100 pages")
	return nil
}

func dedupSorted(ids []string) []string {
	sort.Strings(ids)
	out := ids[:0]
	for i, id := range ids {
		if i == 0 || id != ids[i-1] {
			out = append(out, id)
		}
	}
	return out
}

func TestChaos7074OracleO3OwnedRepositories(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("apply rendered schema statement: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	adapter := chaos7074FalkorAdapter(t, ctx)

	now := time.Now().UTC()
	orgID := "o3000000-0000-4000-8000-000000000001"
	otherOrg := "o3000000-0000-4000-8000-000000000002"
	o3Seed(t, ctx, direct, orgID, now)
	o3Seed(t, ctx, direct, otherOrg, now) // same team ids, other organization
	source, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatal(err)
	}
	o3Project(t, ctx, source, adapter, orgID)
	o3Project(t, ctx, source, adapter, otherOrg)

	reader, err := directread.NewRelationshipsReader(directread.NewSubjectGate(adapter, nil), adapter, nil, o3CursorKeyring())
	if err != nil {
		t.Fatal(err)
	}
	principal := storage.Principal{OrgID: orgID, Subject: "oracle", CredentialID: "oracle"}
	for _, team := range []string{"T1", "T2"} {
		t.Run(team, func(t *testing.T) {
			want, nullRepoID, nameResolved := o3Population(t, ctx, direct, orgID, team)
			got := o3Path1(t, reader, principal, team)
			if o3Tagged(got) != o3Tagged(want) {
				t.Fatalf("O3 unnamed difference for %s:\n path 1 read_relationships = %s\n path 2 team_repo_ownership = %s", team, o3Tagged(got), o3Tagged(want))
			}
			// The named class, asserted so it is exercised (T1 seeds one row).
			wantNull := map[string]int{"T1": 1, "T2": 0}[team]
			if nullRepoID != wantNull {
				t.Fatalf("null_repo_id rows for %s = %d, want %d", team, nullRepoID, wantNull)
			}
			// The name-resolved rows, asserted so the by-name rule is exercised.
			wantNamed := map[string]int{"T1": 1, "T2": 0}[team]
			if nameResolved != wantNamed {
				t.Fatalf("name-resolved NULL repo_id rows for %s = %d, want %d", team, nameResolved, wantNamed)
			}
			if len(want) == 0 {
				t.Fatalf("path 2 is empty for %s: the oracle compared nothing", team)
			}
			t.Logf("O3 %s: %d repositories equal on both paths; name_resolved=%d null_repo_id=%d (named)", team, len(want), nameResolved, nullRepoID)
		})
	}
	// The fixture must make the comparison non-trivial: T1 must hold ended
	// and superseded ownership that path 2 excludes, and more repositories
	// than one page.
	all, _, _ := o3Population(t, ctx, direct, orgID, "T1")
	if len(all) != 9 {
		t.Fatalf("T1 current population = %d, want 9 (R1 R2 R4 R5 R6 R7 R8 R9 + R11 by name; not R3); fixture drifted", len(all))
	}
}

func o3CursorKeyring() directread.CursorKeyring {
	return directread.CursorKeyring{ActiveKID: "k1", Keys: map[string][]byte{"k1": []byte("0123456789abcdef0123456789abcdef-test-cursor-key")}}
}
