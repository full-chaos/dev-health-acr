package devhealthsource_test

// CHAOS-7119 (K11 edge half): a team_repo_ownership row with a NULL repo_id
// is resolved BY NAME in the repository -> team OWNED_BY_TEAM edge, with the
// one rule the fact reads use (ownershipresolve; the fact-side cases are
// devhealthfacts/chaos7073_owned_repo_name_resolution_integration_test.go,
// whose seeds these mirror). Every case asserts the EDGES THE GRAPH RECEIVES
// from NextProjectionBatch on a real ClickHouse -- the state the projection
// exists to reach -- not the SQL text.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const (
	chaos7119RepoK      = "71190000-0000-4000-8000-00000000000a"
	chaos7119RepoB      = "71190000-0000-4000-8000-00000000000b"
	chaos7119Orphan     = "71190000-0000-4000-8000-0000000000ff"
	chaos7119ZeroRepoID = "00000000-0000-0000-0000-000000000000"
)

// chaos7119Seed writes one organization's K11 fixture:
//
//	repos (github): acme/repo-k, acme/repo-b, acme/zero (the ZERO UUID -- what
//	  an unmatched LEFT JOIN fills in, so a resolution that stops testing the
//	  join's `matched` sentinel lands an unmatched name on it).
//	ownership (all source=native, exact):
//	  team-name      github "ACME/Repo-K"  repo_id NULL    -> edge to repo-k
//	  team-ghost     github "acme/ghost"   repo_id NULL    -> no edge (no such repo)
//	  team-provider  gitlab "acme/repo-k"  repo_id NULL    -> no edge (provider differs)
//	  team-orphan    github "acme/orphan"  repo_id orphan  -> sentinel edge (kept default)
//	  team-id-wins   github "acme/repo-k"  repo_id repo-b  -> edge to repo-b only
//	  team-dup       github "ACME/REPO-B"  repo_id NULL  +
//	                 github "acme/repo-b"  repo_id repo-b  -> ONE edge to repo-b
func chaos7119Seed(t *testing.T, ctx context.Context, f *ownershipFixture, at time.Time) {
	t.Helper()
	for _, r := range []struct{ id, name string }{
		{chaos7119RepoK, "acme/repo-k"}, {chaos7119RepoB, "acme/repo-b"}, {chaos7119ZeroRepoID, "acme/zero"},
	} {
		mustExec(t, ctx, f.direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`,
			r.id, r.name, nil, at, nil, at, f.orgID, "github")
	}
	for _, team := range []string{"team-name", "team-ghost", "team-provider", "team-orphan", "team-id-wins", "team-dup"} {
		chaos7119Team(t, ctx, f, team, at)
	}
	chaos7119Own(t, ctx, f, "team-name", "github", "ACME/Repo-K", nil, at)
	chaos7119Own(t, ctx, f, "team-ghost", "github", "acme/ghost", nil, at)
	chaos7119Own(t, ctx, f, "team-provider", "gitlab", "acme/repo-k", nil, at)
	chaos7119Own(t, ctx, f, "team-orphan", "github", "acme/orphan", chaos7119Orphan, at)
	chaos7119Own(t, ctx, f, "team-id-wins", "github", "acme/repo-k", chaos7119RepoB, at)
	chaos7119Own(t, ctx, f, "team-dup", "github", "ACME/REPO-B", nil, at)
	chaos7119Own(t, ctx, f, "team-dup", "github", "acme/repo-b", chaos7119RepoB, at)
}

func chaos7119Team(t *testing.T, ctx context.Context, f *ownershipFixture, id string, at time.Time) {
	t.Helper()
	mustExec(t, ctx, f.direct, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, native_team_key, project_keys, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, id+" name", "", at, f.orgID, "github", id, []string{}, uint8(1))
}

func chaos7119Own(t *testing.T, ctx context.Context, f *ownershipFixture, teamID, provider, name string, repoID any, at time.Time) {
	t.Helper()
	mustExec(t, ctx, f.direct,
		`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.orgID, provider, teamID, repoID, name, "exact", "native", uint8(1), uint16(100), int32(0), at, nil, at)
}

// chaos7119Converge pages the source from cursor to convergence and returns
// every repository -> team edge, keyed by team id, plus the converged cursor.
// Each batch is checked for duplicate RelationshipIDs on its own (batch
// validation is per batch), and any duplicate across pages is reported too.
func chaos7119Converge(t *testing.T, ctx context.Context, f *ownershipFixture, cursor string) (map[string][]contractsv1.ContextFabricRelationshipProjection, string) {
	t.Helper()
	byTeam := map[string][]contractsv1.ContextFabricRelationshipProjection{}
	seen := map[string]int{}
	for page := 0; page < chaos6561ConvergePageBound; page++ {
		batch, available, err := f.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{
			OrgID: f.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if !available {
			for id, count := range seen {
				if count > 1 {
					t.Errorf("relationship %q emitted %d times across one convergence -- a name row and an id row for one repository must be ONE edge", id, count)
				}
			}
			return byTeam, cursor
		}
		assertUniqueRelationshipIDs(t, batch)
		cursor = batch.NextCursor
		for _, relationship := range batch.Relationships {
			if relationship.Type == contractsv1.ContextFabricRelationshipOwnedByTeam && relationship.From.Kind == contractsv1.ContextFabricSubjectRepository {
				team := strings.TrimPrefix(relationship.To.CanonicalID, "team:")
				byTeam[team] = append(byTeam[team], relationship)
				seen[relationship.RelationshipID]++
			}
		}
	}
	t.Fatalf("source did not converge within %d pages", chaos6561ConvergePageBound)
	return nil, ""
}

func chaos7119Edges(t *testing.T, ctx context.Context, f *ownershipFixture) map[string][]contractsv1.ContextFabricRelationshipProjection {
	t.Helper()
	edges, _ := chaos7119Converge(t, ctx, f, "")
	return edges
}

func chaos7119AssertOneEdge(t *testing.T, edges map[string][]contractsv1.ContextFabricRelationshipProjection, teamID, repoID, slug, why string) contractsv1.ContextFabricRelationshipProjection {
	t.Helper()
	got := edges[teamID]
	if len(got) != 1 {
		t.Fatalf("%s: %s has %d repository->team edges, want exactly 1 (to %s): %+v", why, teamID, len(got), repoID, got)
	}
	edge := got[0]
	if want := devhealthsource.RepositoryTeamRelationshipIDForTest(repoID, teamID, "github", "native"); edge.RelationshipID != want {
		t.Fatalf("%s: %s edge id %q, want %q", why, teamID, edge.RelationshipID, want)
	}
	if edge.From.CanonicalID != "repository:"+repoID {
		t.Fatalf("%s: %s edge From %q, want repository:%s", why, teamID, edge.From.CanonicalID, repoID)
	}
	if got := edge.Authorization.RepositorySlugs; len(got) != 1 || got[0] != slug {
		t.Fatalf("%s: %s edge RepositorySlugs %v, want [%s]", why, teamID, got, slug)
	}
	if got := edge.Authorization.TeamIDs; len(got) != 1 || got[0] != teamID {
		t.Fatalf("%s: %s edge TeamIDs %v, want [%s]", why, teamID, got, teamID)
	}
	return edge
}

func chaos7119AssertNoEdge(t *testing.T, edges map[string][]contractsv1.ContextFabricRelationshipProjection, teamID, why string) {
	t.Helper()
	if got := edges[teamID]; len(got) != 0 {
		t.Fatalf("%s: %s has repository->team edges %+v, want none", why, teamID, got)
	}
}

// (a) The headline: a NULL repo_id row whose name matches a repos row case-
// insensitively becomes an ordinary edge to that repository, scoped to the
// repository's canonical slug (not the raw, differently-cased name).
func subCHAOS7119NameResolvesCaseInsensitively(t *testing.T, ctx context.Context, f *ownershipFixture) {
	chaos7119Seed(t, ctx, f, time.Now().UTC().Truncate(time.Second))
	edge := chaos7119AssertOneEdge(t, chaos7119Edges(t, ctx, f), "team-name", chaos7119RepoK, "acme/repo-k",
		`ownership "ACME/Repo-K" with no repo_id must resolve to acme/repo-k`)
	if edge.From.Label != "acme/repo-k" {
		t.Fatalf("edge From label %q, want the repository's own slug acme/repo-k", edge.From.Label)
	}
}

// (b) The provider is part of the match: a gitlab row naming a github
// repository resolves to nothing.
func subCHAOS7119ProviderMismatchDoesNotResolve(t *testing.T, ctx context.Context, f *ownershipFixture) {
	chaos7119Seed(t, ctx, f, time.Now().UTC().Truncate(time.Second))
	chaos7119AssertNoEdge(t, chaos7119Edges(t, ctx, f), "team-provider", "a gitlab ownership row must not resolve to a github repository of the same name")
}

// (c) A name repos does not hold resolves to nothing -- and is counted, not
// silently dropped.
func subCHAOS7119GhostNameIsOmittedAndCounted(t *testing.T, ctx context.Context, f *ownershipFixture) {
	logged := &bytes.Buffer{}
	f.source.WithLogger(slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
	chaos7119Seed(t, ctx, f, time.Now().UTC().Truncate(time.Second))
	chaos7119AssertNoEdge(t, chaos7119Edges(t, ctx, f), "team-ghost", "acme/ghost names no repository")
	// Unresolved groups: team-ghost (github) and team-provider (gitlab).
	if !strings.Contains(logged.String(), "repository_team_groups_unresolved=2") {
		t.Fatalf("unresolved ownership rows were not counted (want repository_team_groups_unresolved=2):\n%s", logged.String())
	}
	if strings.Contains(logged.String(), "acme/ghost") {
		t.Fatalf("log leaks a repository name:\n%s", logged.String())
	}
}

// (d) A row's own repo_id wins over its name.
func subCHAOS7119OwnIDWinsOverName(t *testing.T, ctx context.Context, f *ownershipFixture) {
	chaos7119Seed(t, ctx, f, time.Now().UTC().Truncate(time.Second))
	chaos7119AssertOneEdge(t, chaos7119Edges(t, ctx, f), "team-id-wins", chaos7119RepoB, "acme/repo-b",
		"repo_id = repo-b must win over the name acme/repo-k")
}

// (e) The zero-UUID trap: an unmatched LEFT JOIN fills r.id with the zero
// UUID, and a repository carrying that id is seeded. No unresolved row may
// land on it.
func subCHAOS7119UnmatchedNameNeverLandsOnTheZeroUUID(t *testing.T, ctx context.Context, f *ownershipFixture) {
	chaos7119Seed(t, ctx, f, time.Now().UTC().Truncate(time.Second))
	edges := chaos7119Edges(t, ctx, f)
	for team, list := range edges {
		for _, edge := range list {
			if edge.From.CanonicalID == "repository:"+chaos7119ZeroRepoID {
				t.Fatalf("%s gained an edge to the zero-UUID repository %+v -- an unmatched name resolved through the LEFT JOIN's zero fill", team, edge)
			}
		}
	}
	chaos7119AssertNoEdge(t, edges, "team-ghost", "zero-UUID guard")
}

// (f) The duplicate-ID guard: a name row and an id row for the same
// (repository, team, source) are ONE edge, from ONE group. Two groups would
// carry the same RelationshipID. The CHAOS-4874 in-batch pass
// (dropDuplicateIdentities) already keeps that from rejecting the batch and
// wedging the organization, by dropping the second item with a
// duplicate_within_batch quarantine WARN -- so the served edge count alone
// cannot see a split group. The quarantine line is what does: a correct group
// key produces none.
func subCHAOS7119NameAndIDRowsForOneRepositoryAreOneEdge(t *testing.T, ctx context.Context, f *ownershipFixture) {
	logged := &bytes.Buffer{}
	f.source.WithLogger(slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelWarn})))
	chaos7119Seed(t, ctx, f, time.Now().UTC().Truncate(time.Second))
	chaos7119AssertOneEdge(t, chaos7119Edges(t, ctx, f), "team-dup", chaos7119RepoB, "acme/repo-b",
		"a name row and an id row for acme/repo-b")
	if strings.Contains(logged.String(), "duplicate_within_batch") {
		t.Fatalf("the source produced a duplicate relationship identity in one batch -- a name row and an id row for one repository split into two groups:\n%s", logged.String())
	}
	if !strings.Contains(logged.String(), "repository_team_groups_unresolved=") {
		t.Fatalf("no repository-ownership telemetry at WARN; the quarantine check above would be measuring a silent logger:\n%s", logged.String())
	}
}

// (g) DEFAULT pending chris: a non-NULL repo_id with no repos row keeps its
// CHAOS-6561 edge, scoped fail-closed to the orphaned-repository sentinel.
// The fact reads drop the same row (ops rule); this divergence is deliberate
// until ruled.
func subCHAOS7119OrphanIDKeepsTheSentinelEdge(t *testing.T, ctx context.Context, f *ownershipFixture) {
	chaos7119Seed(t, ctx, f, time.Now().UTC().Truncate(time.Second))
	got := chaos7119Edges(t, ctx, f)["team-orphan"]
	if len(got) != 1 {
		t.Fatalf("team-orphan has %d edges, want 1 (the orphan-sentinel edge, kept default): %+v", len(got), got)
	}
	if got[0].From.CanonicalID != "repository:"+chaos7119Orphan {
		t.Fatalf("orphan edge From %q, want repository:%s", got[0].From.CanonicalID, chaos7119Orphan)
	}
	if slugs := got[0].Authorization.RepositorySlugs; len(slugs) != 1 || slugs[0] != "acr-context-fabric:orphaned-repository" {
		t.Fatalf("orphan edge scoped %v, want the orphaned-repository sentinel", slugs)
	}
}

// (h) Late repos arrival: a name-only row with no repos row yet resolves to
// nothing; when the repos row arrives with a later last_synced (the
// ownership row untouched), the edge appears exactly once, then converges.
func subCHAOS7119LateReposRowResolvesTheNameOnce(t *testing.T, ctx context.Context, f *ownershipFixture) {
	ownedAt := time.Now().UTC().Truncate(time.Second)
	chaos7119Team(t, ctx, f, "team-late", ownedAt)
	chaos7119Own(t, ctx, f, "team-late", "github", "Acme/Late", nil, ownedAt)

	first, cursor := chaos7119Converge(t, ctx, f, "")
	chaos7119AssertNoEdge(t, first, "team-late", "before the repos row exists")

	const lateRepo = "71190000-0000-4000-8000-0000000000aa"
	syncedAt := ownedAt.Add(time.Minute)
	mustExec(t, ctx, f.direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`,
		lateRepo, "acme/late", nil, ownedAt, nil, syncedAt, f.orgID, "github")

	second, cursor := chaos7119Converge(t, ctx, f, cursor)
	edge := chaos7119AssertOneEdge(t, second, "team-late", lateRepo, "acme/late", "after the repos row arrived")
	if !edge.ObservedAt.Equal(syncedAt) {
		t.Fatalf("edge ObservedAt %s, want the repos row's last_synced %s", edge.ObservedAt, syncedAt)
	}
	third, _ := chaos7119Converge(t, ctx, f, cursor)
	chaos7119AssertNoEdge(t, third, "team-late", "a read after convergence")
}

// (i) Row-key parity under pagination. Every group below shares ONE
// watermark, so page boundaries are decided by the row key alone: a row key
// that disagrees with the GROUP BY (or a GROUP BY split by a name column)
// skips, replays or duplicates groups. 450 repositories, each owned by a
// name-only row and, for every other one, also by an id row -- well past one
// page (incrementalBatchCap 200). Exactly 450 edges must arrive, each once.
//
// The repositories are spread over five teams (90 each) on purpose. queryTeams'
// authorization_repositories list (untouched here; CHAOS-7130) counts raw
// repo_full_name values, so 90 name rows plus 45 differently-cased id rows is
// 135 entries. Before CHAOS-7139 a team over the generic contract bound (200)
// had its team ENTITY quarantined and every edge on the same page dropped
// with it (endpoint_entity_quarantined); the entity bound is now 5000, and
// this case keeps its 90-per-team spread only to stay independent of that.
func subCHAOS7119PaginationOverNameAndIDRowsIsExact(t *testing.T, ctx context.Context, f *ownershipFixture) {
	const n = 450
	at := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		chaos7119Team(t, ctx, f, fmt.Sprintf("team-bulk-%d", i), at)
	}
	mustExec(t, ctx, f.direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider)
SELECT toUUID(concat('71190000-0000-4000-9000-', leftPad(toString(number), 12, '0'))), concat('acme/bulk-', toString(number)), NULL, ?, NULL, ?, ?, 'github'
FROM numbers(?)`, at, at, f.orgID, n)
	mustExec(t, ctx, f.direct, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at)
SELECT ?, 'github', concat('team-bulk-', toString(number % 5)), NULL, concat('ACME/Bulk-', toString(number)), 'exact', 'native', 1, 100, 0, ?, NULL, ?
FROM numbers(?)`, f.orgID, at, at, n)
	mustExec(t, ctx, f.direct, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at)
SELECT ?, 'github', concat('team-bulk-', toString(number % 5)), toUUID(concat('71190000-0000-4000-9000-', leftPad(toString(number), 12, '0'))), concat('acme/bulk-', toString(number)), 'exact', 'native', 1, 100, 0, ?, NULL, ?
FROM numbers(?) WHERE number % 2 = 0`, f.orgID, at, at, n)

	edges := chaos7119Edges(t, ctx, f)
	got := map[string]bool{}
	total := 0
	for i := 0; i < 5; i++ {
		team := fmt.Sprintf("team-bulk-%d", i)
		for _, edge := range edges[team] {
			total++
			got[team+"|"+edge.From.CanonicalID] = true
		}
	}
	if total != n {
		t.Fatalf("bulk teams: %d repository->team edges across pages, want exactly %d", total, n)
	}
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("team-bulk-%d|repository:71190000-0000-4000-9000-%012d", i%5, i)
		if !got[key] {
			t.Fatalf("no edge %s -- a page boundary skipped its group", key)
		}
	}
}
