package devhealthsource_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// TERMINATION RULE for every loop in this file (four of the branch's nine
// early-exit sites live here). A loop that stops on an OBSERVED SIGNAL can be
// ended by the very defect it exists to catch, and then passes -- this branch
// produced three instances of that before it was named, including one written
// to fix another. So: prefer a bound derived from the fixture's own size with
// no early exit; if a loop does exit on a signal, it MUST carry a post-loop
// assertion keyed to a fixture-known quantity, so the signal firing early
// becomes a failure rather than a pass. Full statement, carve-out and the
// nine-site evidence table: docs/design/context-fabric-team-project-subjects.md §8c.
//
// The ownership aggregation is the one producer in this issue whose
// correctness lives entirely in SQL -- a GROUP BY, an argMax and two joins --
// so the package's fake cannot test it at all: fakeClient returns canned rows
// for a statement without executing it. These tests run the real statement
// against a real, production-typed ClickHouse (codex round-1 F1/F2).

// TestOwnershipCollapseDoesNotMergeAcrossProviders is codex round-1 F1.
// team_project_ownership joins projects on project_key, and project_key is
// only unique WITHIN a provider. Collapsing without carrying provider merges
// two providers' independent ownership assertions, and the join then
// fabricates cross-provider project->team edges -- an edge asserting an
// ownership nobody ever recorded -- and can emit two rows that resolve to the
// same projects.id, duplicating a RelationshipID and wedging the batch.
//
// Live today there are zero cross-provider project_key collisions (verified
// across every organization: 0 keys under >1 provider in projects, 0 in
// team_project_ownership), so this is a latent defect rather than an active
// one. That is exactly why it needs a test: nothing in the schema prevents
// the collision, and the failure is silent fabrication rather than an error.
func subOwnershipCollapseDoesNotMergeAcrossProviders(t *testing.T, ctx context.Context, fixture *ownershipFixture) {

	batch := fixture.project(t, ctx)
	for _, forbidden := range []struct{ provider, project, team string }{
		{"github", "PROJ-GITHUB", "TEAM-GITLAB"},
		{"gitlab", "PROJ-GITLAB", "TEAM-GITHUB"},
	} {
		id := devhealthsource.ProjectTeamRelationshipIDForTest(t, forbidden.provider, forbidden.project, forbidden.team, "native")
		if hasRelationship(batch, id) {
			t.Errorf("fabricated a cross-provider ownership edge %q -- the two providers' assertions were merged on a shared project_key", id)
		}
	}
	for _, wanted := range []struct{ provider, project, team string }{
		{"github", "PROJ-GITHUB", "TEAM-GITHUB"},
		{"gitlab", "PROJ-GITLAB", "TEAM-GITLAB"},
	} {
		id := devhealthsource.ProjectTeamRelationshipIDForTest(t, wanted.provider, wanted.project, wanted.team, "native")
		if !hasRelationship(batch, id) {
			t.Errorf("lost the genuine same-provider ownership edge %q while scoping by provider", id)
		}
	}
	assertUniqueRelationshipIDs(t, batch)
}

// TestOwnershipWindowTakesTheLatestAssertion is codex round-1 F2. The
// approved semantics is "the latest assertion wins", keyed on valid_from --
// not "the largest valid_to", which is what max() gives.
//
// Both directions are asserted because each fails a different way and a
// producer can easily get one right and the other wrong:
//   - later assertion CLOSES EARLIER: max() reports the older, later date, so
//     an ownership that ended in March looks live until December.
//   - later assertion is OPEN: the NULL must win. This is the ClickHouse trap
//     codex flagged, and it is real -- verified directly against this server:
//     plain argMax(valid_to, valid_from) SKIPS the NULL and returns the older
//     row's date, holding a closed edge open. argMax(tuple(valid_to),
//     valid_from).1 preserves it.
func subOwnershipWindowTakesTheLatestAssertion(t *testing.T, ctx context.Context, fixture *ownershipFixture) {

	batch := fixture.project(t, ctx)

	closedLate := relationshipByID(t, batch, devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-CLOSED", "TEAM-GITHUB", "native"))
	if closedLate.ValidTo == nil {
		t.Fatal("PROJ-CLOSED: the latest assertion closed the ownership, so the edge must carry an end")
	}
	if !closedLate.ValidTo.Equal(ownershipLatestClose) {
		t.Errorf("PROJ-CLOSED: ValidTo = %v, want the LATEST assertion's valid_to %v (max() would wrongly report the older assertion's later date %v)",
			closedLate.ValidTo, ownershipLatestClose, ownershipStaleFarFutureClose)
	}

	stillOpen := relationshipByID(t, batch, devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-OPEN", "TEAM-GITHUB", "native"))
	if stillOpen.ValidTo != nil {
		t.Errorf("PROJ-OPEN: ValidTo = %v, want nil -- the latest assertion left the window open, and a NULL valid_to must not be skipped in favour of an older closed row", stillOpen.ValidTo)
	}
	if stillOpen.ValidFrom == nil || !stillOpen.ValidFrom.Equal(ownershipFirstSeen) {
		t.Errorf("PROJ-OPEN: ValidFrom = %v, want the EARLIEST observed assertion %v", stillOpen.ValidFrom, ownershipFirstSeen)
	}
}

// subTeamAuthorizationCarriesCurrentOwnedRepositories is CHAOS-4390.
// queryTeams previously left Authorization.RepositorySlugs empty for every
// team, which falkorgraph's shared authorizationValue convention encodes as
// the wildcard "*" (unrestricted) -- so ANY repository-scoped principal
// could see EVERY team in an organization, whether or not that team owns
// anything the principal is scoped to. This proves the real fix: the team's
// CURRENT team_repo_ownership rows (CHAOS-4321 -- ownership only, never
// team_memberships) populate RepositorySlugs, and only currently-open
// ownership counts -- a closed or not-yet-started assertion must not leak
// into the authorization list, exactly like ownershipValidityPredicate's
// "currently active" rule everywhere else in this package.
func subTeamAuthorizationCarriesCurrentOwnedRepositories(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	seedRepoOwnership := func(repoFullName string, validFrom time.Time, validTo any) {
		if err := fixture.direct.Exec(ctx,
			`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			fixture.orgID, "github", "TEAM-GITHUB", nil, repoFullName, "exact", "native", uint8(1), uint16(1), int32(1), validFrom, validTo, ownershipLaterAssertion); err != nil {
			t.Fatalf("seed team_repo_ownership %s: %v", repoFullName, err)
		}
	}
	// Currently open: must be authorized.
	seedRepoOwnership("acme/repo-open", ownershipFirstSeen, nil)
	// Closed in the past (before "now"): must NOT be authorized.
	seedRepoOwnership("acme/repo-closed", ownershipFirstSeen, ownershipLatestClose)
	// Not yet started (valid_from in the future): must NOT be authorized.
	seedRepoOwnership("acme/repo-future", time.Now().UTC().Add(365*24*time.Hour), nil)

	batch := fixture.project(t, ctx)
	entity := entityByCanonicalID(t, batch, "team:TEAM-GITHUB")
	if len(entity.Authorization.TeamIDs) != 1 || entity.Authorization.TeamIDs[0] != "TEAM-GITHUB" {
		t.Fatalf("TeamIDs = %v, want [TEAM-GITHUB] unchanged", entity.Authorization.TeamIDs)
	}
	got := append([]string(nil), entity.Authorization.RepositorySlugs...)
	sort.Strings(got)
	want := []string{"acme/repo-open"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RepositorySlugs = %v, want %v (closed and not-yet-started ownership rows must be excluded)", got, want)
	}
}

// subTeamAuthorizationCollapsesStaleOpenAssertion is codex round-1's HIGH
// finding on this PR: team_repo_ownership's ReplacingMergeTree key includes
// valid_from, so FINAL keeps an OLDER open assertion (valid_from=t1,
// valid_to=NULL) and a LATER assertion that actually closed the SAME
// (team, repo, source) (valid_from=t2>t1, valid_to=<past>) as two DISTINCT
// rows -- they differ only in valid_from. A naive `valid_to IS NULL`
// filter after FINAL would still surface the repository through the stale
// open row even though the org's most recent assertion revoked it. This
// seeds exactly that sequence and proves the repository is excluded.
func subTeamAuthorizationCollapsesStaleOpenAssertion(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	seed := func(validFrom time.Time, validTo any) {
		if err := fixture.direct.Exec(ctx,
			`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			fixture.orgID, "github", "TEAM-GITHUB", nil, "acme/revoked-repo", "exact", "native", uint8(1), uint16(1), int32(1), validFrom, validTo, validFrom); err != nil {
			t.Fatalf("seed team_repo_ownership: %v", err)
		}
	}
	// Older assertion: open.
	seed(ownershipFirstSeen, nil)
	// LATER assertion (different valid_from -- FINAL keeps both rows):
	// closes it, before "now".
	seed(ownershipLaterAssertion, ownershipLatestClose)

	batch := fixture.project(t, ctx)
	entity := entityByCanonicalID(t, batch, "team:TEAM-GITHUB")
	for _, repo := range entity.Authorization.RepositorySlugs {
		if repo == "acme/revoked-repo" {
			t.Fatalf("RepositorySlugs = %v, want acme/revoked-repo excluded -- the LATEST assertion closed it, but a stale FINAL-surviving open row leaked it back in", entity.Authorization.RepositorySlugs)
		}
	}
}

// subTeamAuthorizationRefreshedByOwnershipOnlyChange is codex round-1's
// other HIGH finding: queryTeams' cursor/watermark used to be bare
// tm.updated_at, so a grant or revocation that touches ONLY
// team_repo_ownership (the team's own teams-table row left untouched)
// would never advance the watermark incremental catch-up compares
// against, and the team would never be re-selected -- the graph would
// keep serving a STALE authorization scope indefinitely. This proves the
// fix: page through to convergence, then write a NEW team_repo_ownership
// row with NO corresponding change to the teams table, and prove a
// subsequent incremental page (starting from the converged cursor)
// re-selects the team with the new repository present.
func subTeamAuthorizationRefreshedByOwnershipOnlyChange(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	cursor := ""
	for page := 0; page < 10; page++ {
		batch, available, err := fixture.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{
			OrgID: fixture.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if !available {
			break
		}
		cursor = batch.NextCursor
	}

	// Ownership-only change: team_repo_ownership gets a brand-new row,
	// timestamped strictly after everything already consumed; the teams
	// table itself is not touched at all.
	refreshedAt := time.Now().UTC()
	if err := fixture.direct.Exec(ctx,
		`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		fixture.orgID, "github", "TEAM-GITHUB", nil, "acme/freshly-granted-repo", "exact", "native", uint8(1), uint16(1), int32(1), refreshedAt, nil, refreshedAt); err != nil {
		t.Fatalf("seed ownership-only change: %v", err)
	}

	found := false
	for page := 0; page < 10; page++ {
		batch, available, err := fixture.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{
			OrgID: fixture.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("post-refresh page %d: %v", page, err)
		}
		if !available {
			break
		}
		cursor = batch.NextCursor
		for _, e := range batch.Entities {
			if e.Subject.CanonicalID != "team:TEAM-GITHUB" {
				continue
			}
			for _, repo := range e.Authorization.RepositorySlugs {
				if repo == "acme/freshly-granted-repo" {
					found = true
				}
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Fatal("TEAM-GITHUB was never re-emitted after an ownership-only change -- the watermark did not advance for a team_repo_ownership-only update")
	}
}

// subTeamAuthorizationRefreshedByRevokingLastOpenRepository is codex
// round-2's HIGH finding: the outer watermark aggregation used to filter
// to `WHERE latest_is_open` BEFORE computing `max(updated_at)`, so
// revoking a team's ONLY open repository removed that repository's row
// from the aggregation entirely -- taking its own updated_at with it.
// The watermark then regressed (or fell back to the epoch), which can sit
// BELOW what an earlier page already consumed, and sincePredicate's
// strict `>` then skips the team forever: the revocation itself is what
// never gets picked up, leaving the now-invalid repository authorized
// indefinitely. This seeds exactly that sequence -- grant, converge,
// revoke the team's ONLY open repository, converge again -- and proves
// the team is re-emitted with the repository excluded.
func subTeamAuthorizationRefreshedByRevokingLastOpenRepository(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	seed := func(validFrom time.Time, validTo any, updatedAt time.Time) {
		if err := fixture.direct.Exec(ctx,
			`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			fixture.orgID, "github", "TEAM-GITHUB", nil, "acme/only-repo", "exact", "native", uint8(1), uint16(1), int32(1), validFrom, validTo, updatedAt); err != nil {
			t.Fatalf("seed team_repo_ownership: %v", err)
		}
	}
	grantedAt := ownershipFirstSeen
	seed(grantedAt, nil, grantedAt)

	cursor := ""
	converge := func() {
		for page := 0; page < 10; page++ {
			batch, available, err := fixture.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{
				OrgID: fixture.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor,
			})
			if err != nil {
				t.Fatalf("converge page %d: %v", page, err)
			}
			if !available {
				return
			}
			cursor = batch.NextCursor
		}
	}
	converge()

	// Revoke the team's ONLY open repository: a NEW assertion (later
	// valid_from, later updated_at) that closes it.
	revokedAt := time.Now().UTC()
	seed(revokedAt, revokedAt, revokedAt)

	found := false
	excluded := false
	for page := 0; page < 10; page++ {
		batch, available, err := fixture.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{
			OrgID: fixture.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("post-revoke page %d: %v", page, err)
		}
		if !available {
			break
		}
		cursor = batch.NextCursor
		for _, e := range batch.Entities {
			if e.Subject.CanonicalID != "team:TEAM-GITHUB" {
				continue
			}
			found = true
			excluded = true
			for _, repo := range e.Authorization.RepositorySlugs {
				if repo == "acme/only-repo" {
					excluded = false
				}
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Fatal("TEAM-GITHUB was never re-emitted after its only open repository was revoked -- the watermark regressed when the last open row dropped out of the aggregation")
	}
	if !excluded {
		t.Fatal("TEAM-GITHUB was re-emitted but acme/only-repo is still in RepositorySlugs after revocation")
	}
}

// subTeamAuthorizationOwnershipJoinScopedToOneOrganization proves the
// team_repo_ownership join itself carries the same org_id isolation as
// every other producer in this package (CHAOS-3642/3649 discipline): a
// same-named repository owned by a DIFFERENT organization's team must
// never leak into this organization's team authorization.
func subTeamAuthorizationOwnershipJoinScopedToOneOrganization(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	otherOrg := "40000000-0000-4000-8000-00000000000b"
	mustSeed := func(orgID string, statement string, args ...any) {
		t.Helper()
		if err := fixture.direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed in org %s: %v", orgID, err)
		}
	}
	at := ownershipLaterAssertion
	// A DIFFERENT organization, with a team carrying the SAME id and a
	// repository owned only there.
	mustSeed(otherOrg, `INSERT INTO teams VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"TEAM-GITHUB", "TEAM-GITHUB name", "", at, otherOrg, "github", "TEAM-GITHUB", []string{}, uint8(1))
	mustSeed(otherOrg, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		otherOrg, "github", "TEAM-GITHUB", nil, "acme/other-org-repo", "exact", "native", uint8(1), uint16(1), int32(1), ownershipFirstSeen, nil, ownershipFirstSeen)

	batch := fixture.project(t, ctx)
	entity := entityByCanonicalID(t, batch, "team:TEAM-GITHUB")
	for _, repo := range entity.Authorization.RepositorySlugs {
		if repo == "acme/other-org-repo" {
			t.Fatalf("RepositorySlugs = %v, leaked a DIFFERENT organization's ownership row -- team_repo_ownership join is not org-scoped", entity.Authorization.RepositorySlugs)
		}
	}
}

var (
	ownershipFirstSeen            = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ownershipLaterAssertion       = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	ownershipStaleFarFutureClose  = time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	ownershipLatestClose          = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	ownershipSupersededEarlyClose = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
)

type ownershipFixture struct {
	orgID  string
	source *devhealthsource.TeamsProjectsSource
	direct clickhousedriver.Conn
	// query is the SAME production query client the source reads through
	// (CHAOS-4635). The row-key agreement test runs its expression through it
	// rather than the raw driver, so it exercises the path production uses.
	query contextpacket.ClickHouseQueryClient
}

// TestOwnershipProducerAgainstRealClickHouse is the single entry point for
// every ownership assertion that needs real SQL, and it exists in this shape
// for a measured reason: these started as six separate top-level tests, each
// spawning its own ClickHouse container. Six extra containers under `-race`
// starved the Postgres-backed packages in the same run -- pginvestigation and
// storage/postgres both blew past their deadlines in `make verify` while
// passing comfortably in isolation. One container, one schema, and a distinct
// ORGANIZATION per subtest gives the same isolation the source itself
// guarantees, at a fraction of the cost.
func TestOwnershipProducerAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			// Deliberately not the phrase "create table" here, and the
			// wording is load-bearing rather than stylistic.
			// devhealthschema's TestNoTestAuthorsDeclaredTableDDL has a
			// second pass that flags any file containing that phrase which
			// also names a declared table, unless the file literally
			// contains "devhealthschema.DDL(". This file renders from the
			// shared declaration -- productionSchemaDDL() is exactly that
			// call, one indirection away -- so it authors no DDL at all,
			// but the substring check cannot see through the helper. The
			// phrase appeared only in this error string, so removing it
			// removes a FALSE positive rather than silencing a real one:
			// the guard's first pass still catches any actual
			// `CREATE TABLE <name>` added to this file. Renaming the
			// message is also simply more accurate -- what failed is
			// applying an already-rendered statement, which is printed
			// beside the error.
			t.Fatalf("apply rendered schema statement: %v\n%s", err, statement)
		}
	}
	// CHAOS-4193: NextProjectionBatch now always queries
	// project_membership_presence too (teamsProjectsTables), even for a
	// case that only cares about team_project_ownership -- the view must
	// exist or every case here fails on "dependency unavailable" before
	// its own assertion is even reached.
	createProjectMembershipPresenceView(t, ctx, direct)
	cases := []struct {
		name  string
		orgID string
		run   func(*testing.T, context.Context, *ownershipFixture)
	}{
		{"collapse does not merge across providers", "30000000-0000-4000-8000-000000000001", subOwnershipCollapseDoesNotMergeAcrossProviders},
		{"window takes the latest assertion", "30000000-0000-4000-8000-000000000002", subOwnershipWindowTakesTheLatestAssertion},
		{"ambiguous project key resolves an id and omits a key", "30000000-0000-4000-8000-000000000003", subAmbiguousProjectKeyResolvesAnIDAndOmitsAKey},
		{"ambiguous rows do not stall pagination", "30000000-0000-4000-8000-000000000004", subAmbiguousRowsDoNotStallPagination},
		{"tied assertions resolve deterministically", "30000000-0000-4000-8000-000000000005", subTiedOwnershipAssertionsResolveDeterministically},
		{"ambiguity guard is scoped to one organization", "30000000-0000-4000-8000-000000000006", subAmbiguityGuardIsScopedToOneOrganization},
		{"omitted rows beyond the skip bound still converge", "30000000-0000-4000-8000-000000000007", subOmittedRowsBeyondTheSkipBoundStillConverge},
		{"team authorization carries current owned repositories", "30000000-0000-4000-8000-000000000008", subTeamAuthorizationCarriesCurrentOwnedRepositories},
		{"team authorization collapses a stale open assertion superseded by a later close", "30000000-0000-4000-8000-000000000009", subTeamAuthorizationCollapsesStaleOpenAssertion},
		{"team authorization is refreshed by an ownership-only change", "30000000-0000-4000-8000-00000000000a", subTeamAuthorizationRefreshedByOwnershipOnlyChange},
		{"team authorization is refreshed by revoking the last open repository", "30000000-0000-4000-8000-00000000000c", subTeamAuthorizationRefreshedByRevokingLastOpenRepository},
		{"team authorization ownership join is scoped to one organization", "30000000-0000-4000-8000-00000000000b", subTeamAuthorizationOwnershipJoinScopedToOneOrganization},
		{"two id-space ownership rows for one project yield exactly one edge", "30000000-0000-4000-8000-00000000000d", subTwoIDSpaceRowsYieldExactlyOneEdge},
		{"empty-key projects each keep their own edge", "30000000-0000-4000-8000-00000000000e", subEmptyKeyProjectsEachKeepTheirOwnEdge},
		{"a projected edge is retracted when its key becomes ambiguous", "30000000-0000-4000-8000-00000000000f", subRetractsAnEdgeWhoseKeyBecomesAmbiguous},
		{"a projected edge is retracted when its ambiguous key arrives via project_ref", "30000000-0000-4000-8000-000000000015", subRetractsAnEdgeWhoseAmbiguousKeyArrivesViaProjectRef},
		{"a projected edge is retracted when its identity starts conflicting", "30000000-0000-4000-8000-000000000010", subRetractsAnEdgeWhoseIdentityStartsConflicting},
		{"retraction is idempotent across a re-run", "30000000-0000-4000-8000-000000000011", subRetractionIsIdempotentAcrossAReRun},
		{"retraction only follows max-raising project writes", "30000000-0000-4000-8000-000000000012", subRetractionOnlyFollowsMaxRaisingProjectWrites},
		{"the row-key SQL agrees with Go byte for byte", "30000000-0000-4000-8000-000000000013", subRowKeySQLAgreesWithGoByteForByte},
		{"two groups sharing a project id get distinct cursor keys", "30000000-0000-4000-8000-000000000014", subTwoGroupsSharingAProjectIDGetDistinctCursorKeys},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.run(t, ctx, newOwnershipFixture(t, ctx, query, direct, testCase.orgID))
		})
	}
}

// newOwnershipFixture seeds one organization's baseline rows into the SHARED
// schema. Organization scoping is what keeps the subtests independent, which
// is the same boundary the producer enforces -- so a leak between subtests
// would itself be a real finding.
func newOwnershipFixture(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, direct clickhousedriver.Conn, orgID string) *ownershipFixture {
	t.Helper()
	at := ownershipLaterAssertion
	mustSeed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	seedTeam := func(id, provider string) {
		mustSeed("teams "+id, `INSERT INTO teams VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, id+" name", "", at, orgID, provider, id, []string{}, uint8(1))
	}
	seedProject := func(id, provider, key string) {
		mustSeed("projects "+id, `INSERT INTO projects VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, orgID, provider, key, id+" name", uint8(1), "started", "", at)
	}
	seedOwnership := func(provider, teamID, projectID, key string, validFrom time.Time, validTo any) {
		mustSeed("team_project_ownership "+projectID, `INSERT INTO team_project_ownership VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			orgID, provider, teamID, projectID, key, "native", validFrom, validTo, at)
	}

	// AMBIGUOUS: two projects sharing one (provider, project_key). The join
	// fans out to two DISTINCT projects.id, so the RelationshipIDs differ and
	// batch.Validate stays silent -- the producer must catch this itself.
	seedProject("PROJ-AMBIG-A", "github", "AMBIG-KEY")
	seedProject("PROJ-AMBIG-B", "github", "AMBIG-KEY")

	seedTeam("TEAM-GITHUB", "github")
	seedTeam("TEAM-GITLAB", "gitlab")

	// F1: ONE project_key, two providers, two independent ownerships.
	seedProject("PROJ-GITHUB", "github", "SHARED-KEY")
	seedProject("PROJ-GITLAB", "gitlab", "SHARED-KEY")
	seedOwnership("github", "TEAM-GITHUB", "PROJ-GITHUB", "SHARED-KEY", ownershipFirstSeen, nil)
	seedOwnership("gitlab", "TEAM-GITLAB", "PROJ-GITLAB", "SHARED-KEY", ownershipFirstSeen, nil)

	// F2a: the LATER assertion closes EARLIER than the older one.
	seedProject("PROJ-CLOSED", "github", "CLOSED-KEY")
	seedOwnership("github", "TEAM-GITHUB", "PROJ-CLOSED", "CLOSED-KEY", ownershipFirstSeen, ownershipStaleFarFutureClose)
	seedOwnership("github", "TEAM-GITHUB", "PROJ-CLOSED", "CLOSED-KEY", ownershipLaterAssertion, ownershipLatestClose)

	// F2b: the LATER assertion is OPEN and must not be skipped for the
	// older closed one.
	seedProject("PROJ-OPEN", "github", "OPEN-KEY")
	seedOwnership("github", "TEAM-GITHUB", "PROJ-OPEN", "OPEN-KEY", ownershipFirstSeen, ownershipSupersededEarlyClose)
	seedOwnership("github", "TEAM-GITHUB", "PROJ-OPEN", "OPEN-KEY", ownershipLaterAssertion, nil)

	seedOwnership("github", "TEAM-GITHUB", "PROJ-AMBIG-A", "AMBIG-KEY", ownershipFirstSeen, nil)

	source, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatalf("new teams/projects source: %v", err)
	}
	return &ownershipFixture{orgID: orgID, source: source, direct: direct, query: query}
}

func (f *ownershipFixture) project(t *testing.T, ctx context.Context) contextfabric.ProjectionBatch {
	t.Helper()
	batch, available, err := f.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: f.orgID, Source: devhealthsource.TeamsProjectsSourceName})
	if err != nil {
		t.Fatalf("project ownership fixture: %v", err)
	}
	if !available {
		t.Fatal("expected an ownership batch to be available")
	}
	return batch
}

func hasRelationship(batch contextfabric.ProjectionBatch, relationshipID string) bool {
	for _, relationship := range batch.Relationships {
		if relationship.RelationshipID == relationshipID {
			return true
		}
	}
	return false
}

// assertUniqueRelationshipIDs restates ContextFabricProjectionBatch.Validate's
// own uniqueness rule at the point of failure. Validate would already have
// rejected the batch, but that surfaces as an opaque build error; this names
// the duplicated edge, which is what an operator debugging a wedged
// organization actually needs.
func assertUniqueRelationshipIDs(t *testing.T, batch contextfabric.ProjectionBatch) {
	t.Helper()
	seen := map[string]int{}
	for _, relationship := range batch.Relationships {
		seen[relationship.RelationshipID]++
	}
	for id, count := range seen {
		if count > 1 {
			t.Errorf("relationship %q emitted %d times -- duplicate relationship IDs fail batch validation and wedge the organization", id, count)
		}
	}
}

// subAmbiguousProjectKeyResolvesAnIDAndOmitsAKey is TWO cases that used to be
// one, and separating them is the CHAOS-4542 premise correction.
//
// The old test asserted that an ownership row naming PROJ-AMBIG-A was omitted
// because two projects share (github, AMBIG-KEY). That was right only while
// project_id was read as a KEY. It is not a key: it is projects.id, which is
// unique, so the row is not ambiguous at all and omitting it discarded an
// ownership the source stated plainly. The ambiguity of some OTHER project's
// key has nothing to do with a match that never consulted a key.
//
// So the id-shaped row is now a POSITIVE assertion. Ambiguity is a property
// of the KEY arm alone, and the key-shaped row below is what carries it:
//
//   - id-shaped  (project_id = projects.id)      -> edge emitted
//   - key-shaped (project_id = an ambiguous key) -> NO edge, and a ledger entry
//
// The second half must be omitted AND recorded. The omission is correct --
// choosing between two equally-matching projects would mint canonical truth
// from a coin flip -- and it is not fatal, because one ambiguous key must not
// take an organization's whole projection down. But an unrecorded omission is
// indistinguishable from an ownership that simply does not exist, and after
// CHAOS-4542 the exclusion happens inside the join where nothing downstream
// can see it. That is why the ledger has its own statement.
func subAmbiguousProjectKeyResolvesAnIDAndOmitsAKey(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	logged := &bytes.Buffer{}
	fixture.source.WithLogger(slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelWarn})))

	// KEY-shaped: project_id carries the ambiguous KEY, so the id arm matches
	// nothing and the key arm excludes it. The baseline fixture already seeded
	// PROJ-AMBIG-A and PROJ-AMBIG-B under (github, AMBIG-KEY).
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITLAB', 'AMBIG-KEY', 'AMBIG-KEY', 'native', ?, NULL, ?)`,
		fixture.orgID, ownershipFirstSeen, ownershipLaterAssertion)

	batch := fixture.project(t, ctx)
	assertUniqueRelationshipIDs(t, batch)

	// The id-shaped row from the baseline fixture: unambiguous, so emitted.
	resolved := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-AMBIG-A", "TEAM-GITHUB", "native")
	if !hasRelationship(batch, resolved) {
		t.Errorf("%q missing -- the ownership row names projects.id, which is unique, so another project sharing its KEY cannot make it ambiguous", resolved)
	}
	// The key-shaped row: neither candidate may be guessed, and the raw key
	// must never be minted as a project id of its own.
	for _, forbidden := range []string{
		devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-AMBIG-A", "TEAM-GITLAB", "native"),
		devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-AMBIG-B", "TEAM-GITLAB", "native"),
		devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "AMBIG-KEY", "TEAM-GITLAB", "native"),
	} {
		if hasRelationship(batch, forbidden) {
			t.Errorf("emitted %q from an ambiguous project_key -- two projects share (github, AMBIG-KEY), so which one the source meant is unknowable and neither may be guessed", forbidden)
		}
	}
	// Omitted is only half; recorded is the other half -- but WHICH half is
	// recorded changed, and honestly.
	//
	// This used to assert `omitted_ambiguous_project_keys`, a per-row claim
	// reconstructed from aggregate SQL. That reconstruction was wrong in four
	// consecutive review rounds in both directions and was removed rather
	// than patched a fifth time (CHAOS-4566 carries it). What remains is a
	// fact a plain query can actually establish: this organization's catalog
	// holds an ambiguous key.
	//
	// So the honest state of this case is: the edge is correctly omitted, the
	// CONDITION that omitted it is visible, and attributing the omission to
	// this specific ownership row is not -- which is exactly what CHAOS-4566
	// exists to restore. Asserting the catalog line rather than deleting the
	// assertion keeps that gap documented by a test instead of by silence.
	if output := logged.String(); !strings.Contains(output, "ambiguous_project_keys_in_catalog=") {
		t.Errorf("nothing reported the ambiguity that omitted this edge -- the exclusion happens inside the join, so with no catalog signal either it is invisible to everyone downstream; got:\n%s", output)
	}

	// One ambiguous key must not suppress the rest.
	for _, id := range []string{
		devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-GITHUB", "TEAM-GITHUB", "native"),
		devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-OPEN", "TEAM-GITHUB", "native"),
	} {
		if !hasRelationship(batch, id) {
			t.Errorf("lost the unambiguous edge %q -- one ambiguous key must not suppress the rest", id)
		}
	}
}

func subAmbiguousRowsDoNotStallPagination(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	fixture.seedAmbiguousBlockThenValidEdge(t, ctx)

	wantEdge := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-BEYOND", "TEAM-GITHUB", "native")
	cursor := ""
	found := false
	for page := 0; page < 40; page++ {
		batch, available, err := fixture.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{
			OrgID: fixture.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if !available {
			break
		}
		if batch.NextCursor == cursor {
			t.Fatalf("page %d: cursor did not advance past a page of omitted rows -- projection would repeat this page forever", page)
		}
		cursor = batch.NextCursor
		if hasRelationship(batch, wantEdge) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("never reached %q -- a block of omitted ambiguous rows stalled the walk before it", wantEdge)
	}
}

// seedAmbiguousBlockThenValidEdge writes more ambiguous ownership rows than
// one page holds, all timestamped after the fixture's other rows, followed by
// a single valid edge later still -- so at least one whole page consists only
// of rows that emit nothing.
func (f *ownershipFixture) seedAmbiguousBlockThenValidEdge(t *testing.T, ctx context.Context) {
	t.Helper()
	// The bulk PROJECT rows are timestamped EARLIER than the ownership rows
	// they make ambiguous. That separation is the whole point: if the project
	// entities shared the ownership rows' timestamp they would emit entity
	// candidates into the same page, the batch would be non-empty, and the
	// cursor would advance for a reason unrelated to the omitted rows --
	// hiding the stall. With entities consumed first, a later page consists
	// of nothing but rows that emit nothing.
	early := ownershipLaterAssertion.Add(1 * time.Hour)
	block := ownershipLaterAssertion.Add(24 * time.Hour)
	beyond := ownershipLaterAssertion.Add(48 * time.Hour)
	// CHAOS-4542: these rows are omitted by identity.Derive (a natural key
	// past MaxNaturalKeyBytes), NOT by ambiguity any more. An ambiguous
	// KEY-shaped row is now excluded inside the join, so it never reaches the
	// scan, spends no page budget, and cannot produce the fully-omitted page
	// this test needs. The keys stay ambiguous so the ledger still has
	// something to report -- but the STALL this test exists for is driven by
	// an omission path that still exists, which is the difference between a
	// test and a decoration.
	mustExec(t, ctx, f.direct, `INSERT INTO projects
SELECT concat('P-AMBIG-A-', repeat('x', 232), toString(number)), ?, 'github', concat('BULK-', toString(number)), 'bulk a', 1, 'started', '', ?
FROM numbers(150)`, f.orgID, early)
	mustExec(t, ctx, f.direct, `INSERT INTO projects
SELECT concat('P-AMBIG-B-', repeat('x', 232), toString(number)), ?, 'github', concat('BULK-', toString(number)), 'bulk b', 1, 'started', '', ?
FROM numbers(150)`, f.orgID, early)
	mustExec(t, ctx, f.direct, `INSERT INTO team_project_ownership
SELECT ?, 'github', 'TEAM-GITHUB', concat('P-AMBIG-A-', repeat('x', 232), toString(number)), concat('BULK-', toString(number)), 'native', ?, NULL, ?
FROM numbers(150)`, f.orgID, block, block)

	// Also early, so the page holding the ambiguous block cannot be rescued
	// by this project's own entity candidate.
	mustExec(t, ctx, f.direct, `INSERT INTO projects VALUES (?, ?, 'github', 'BEYOND-KEY', 'beyond', 1, 'started', '', ?)`,
		"PROJ-BEYOND", f.orgID, early)
	mustExec(t, ctx, f.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITHUB', ?, 'BEYOND-KEY', 'native', ?, NULL, ?)`,
		f.orgID, "PROJ-BEYOND", beyond, beyond)
}

func mustExec(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, statement string, args ...any) {
	t.Helper()
	if err := direct.Exec(ctx, statement, args...); err != nil {
		t.Fatalf("seed: %v\n%s", err, statement)
	}
}

// TestTiedOwnershipAssertionsResolveDeterministically is codex round-2 F2.
//
// argMax keyed on valid_from alone has no ordering between two assertions
// stamped at the SAME instant, so a group holding one open and one closed
// assertion at that instant could project either -- flipping between runs
// with merge order. A flaky pass here IS the defect, so this projects
// repeatedly and asserts every run agrees.
//
// Reaching the tie takes care: (org, provider, project_id, team_id, source,
// valid_from) is team_project_ownership's full ORDER BY, so two rows agreeing
// on all of it collapse under FINAL and no tie survives. The rows must differ
// in a column inside that key but OUTSIDE the producer's GROUP BY, and
// project_id is the only one -- which is realistic precisely because this
// table's project_id is unreliable (the same project_key legitimately appears
// under different project_id values; that is Trap B).
//
// The tiebreak is open-wins, and the justification is semantic rather than
// empirical: live data cannot adjudicate it, because the writer has never
// emitted a closed ownership row at all (0 of 618 carry a valid_to). A
// same-instant assertion of ongoing ownership outranks a same-instant
// closure, so a live owner is never hidden by a simultaneous close.
func subTiedOwnershipAssertionsResolveDeterministically(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	at := ownershipLaterAssertion
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', 'TIE-KEY', 'tie', 1, 'started', '', ?)`,
		"PROJ-TIE", fixture.orgID, at)
	// Same instant, same group after collapse; one closes, one leaves open.
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITHUB', 'ownership-row-closed', 'TIE-KEY', 'native', ?, ?, ?)`,
		fixture.orgID, ownershipFirstSeen, ownershipLatestClose, at)
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITHUB', 'ownership-row-open', 'TIE-KEY', 'native', ?, NULL, ?)`,
		fixture.orgID, ownershipFirstSeen, at)

	tieEdge := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-TIE", "TEAM-GITHUB", "native")
	for run := 0; run < 8; run++ {
		edge := relationshipByID(t, fixture.project(t, ctx), tieEdge)
		if edge.ValidTo != nil {
			t.Fatalf("run %d: ValidTo = %v, want nil -- a same-instant assertion of ongoing ownership outranks a same-instant closure, and the result must not depend on merge order", run, edge.ValidTo)
		}
	}
}

// TestAmbiguityGuardIsScopedToOneOrganization is the self-found gap from the
// round-2 org-scope review, now a test rather than an argument.
//
// The ambiguity guard counts projects per (provider, project_key) with a
// window function whose PARTITION BY does NOT include org_id. It is correct
// only because the org filter sits INSIDE the subquery the window runs over,
// so the window never sees another organization's rows. That correctness
// rests entirely on a subquery boundary, and hoisting the WHERE outward is a
// plausible, well-intentioned refactor that nothing objected to -- a proof
// carrying its own copy.
//
// Two organizations sharing (provider, project_key), each with ONE project:
// neither is ambiguous, and both edges must project. Flatten the filter and
// the window counts across organizations, both keys look ambiguous, and both
// valid edges silently vanish.
func subAmbiguityGuardIsScopedToOneOrganization(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	const otherOrg = "40000000-0000-4000-8000-000000000004"
	at := ownershipLaterAssertion

	// The OTHER organization's project and ownership, sharing this
	// organization's provider and project_key.
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', 'CROSS-ORG-KEY', 'other org project', 1, 'started', '', ?)`,
		"PROJ-OTHER-ORG", otherOrg, at)
	mustExec(t, ctx, fixture.direct, `INSERT INTO teams VALUES (?, ?, '', ?, ?, 'github', ?, ?, 1)`,
		"TEAM-OTHER-ORG", "other org team", at, otherOrg, "TEAM-OTHER-ORG", []string{})
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-OTHER-ORG', ?, 'CROSS-ORG-KEY', 'native', ?, NULL, ?)`,
		otherOrg, "PROJ-OTHER-ORG", ownershipFirstSeen, at)

	// This organization's own project under the SAME (provider, project_key).
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', 'CROSS-ORG-KEY', 'this org project', 1, 'started', '', ?)`,
		"PROJ-THIS-ORG", fixture.orgID, at)
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITHUB', ?, 'CROSS-ORG-KEY', 'native', ?, NULL, ?)`,
		fixture.orgID, "PROJ-THIS-ORG", ownershipFirstSeen, at)

	// CHAOS-4542: the row above is id-shaped, so it resolves without ever
	// consulting a key and would survive a broken ambiguity window. This
	// KEY-shaped row is the one with teeth: 'CROSS-ORG-KEY' names exactly ONE
	// project WITHIN this organization, so the key arm must resolve it. If
	// the window counted the other organization's project too, the count
	// would be two, the key scope row would not be emitted at all, and this
	// edge would vanish with no error anywhere.
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITLAB', 'CROSS-ORG-KEY', 'CROSS-ORG-KEY', 'native', ?, NULL, ?)`,
		fixture.orgID, ownershipFirstSeen, at)

	batch := fixture.project(t, ctx)
	for _, wantEdge := range []string{
		devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-THIS-ORG", "TEAM-GITHUB", "native"),
		devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-THIS-ORG", "TEAM-GITLAB", "native"),
	} {
		if !hasRelationship(batch, wantEdge) {
			t.Errorf("%q was omitted -- the ambiguity window counted another organization's project, so a single unambiguous project looked like two", wantEdge)
		}
	}
	// The other organization's rows must not leak into this projection at all.
	for _, forbidden := range []string{
		devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-OTHER-ORG", "TEAM-OTHER-ORG", "native"),
		devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-OTHER-ORG", "TEAM-GITHUB", "native"),
	} {
		if hasRelationship(batch, forbidden) {
			t.Errorf("projected %q -- another organization's ownership crossed the tenant boundary", forbidden)
		}
	}
}

// memoryCheckpoints is a durable-enough checkpoint store for a multi-tick
// test: it keeps what was saved, so a later RunOnce observes an earlier
// tick's write exactly as a real store would.
type memoryCheckpoints struct {
	checkpoint contextfabric.ProjectionCheckpoint
}

func (m *memoryCheckpoints) LoadProjectionCheckpoint(context.Context, string, string) (contextfabric.ProjectionCheckpoint, error) {
	return m.checkpoint, nil
}

func (m *memoryCheckpoints) CompareAndSwapProjectionCheckpoint(_ context.Context, expected, updated contextfabric.ProjectionCheckpoint) error {
	if m.checkpoint.Cursor != expected.Cursor {
		return contextfabric.ErrProjectionConflict
	}
	m.checkpoint = updated
	return nil
}

type recordingBackend struct {
	applied []contextfabric.ProjectionBatch
}

func (b *recordingBackend) ApplyProjectionBatch(_ context.Context, batch contextfabric.ProjectionBatch) (contextfabric.ProjectionReceipt, error) {
	b.applied = append(b.applied, batch)
	return contextfabric.ProjectionReceipt{BatchID: batch.BatchID, BackendWatermark: "w", AppliedAt: batch.GeneratedAt}, nil
}

func (*recordingBackend) ProjectionWatermark(context.Context, string, string) (contextfabric.ProjectionWatermark, error) {
	return contextfabric.ProjectionWatermark{}, nil
}
func (*recordingBackend) PurgeOrganization(context.Context, string) error { return nil }

// subOmittedRowsBeyondTheSkipBoundStillConverge is codex round-3 F1's
// end-to-end case, and the reason the in-process skip bound was never a fix.
//
// A single NextProjectionBatch call skips at most maxOmittedPageSkips
// fully-omitted pages and then reports available=false. Before the durable
// progress path existed, that answer left the checkpoint untouched, so an
// organization with more consecutive ambiguity-only pages than the bound
// replayed the same prefix on every tick, forever, and the publishable edge
// beyond the block was unreachable. Raising the bound only moves the wall.
//
// This drives the REAL ProjectionWorker across ticks over a block larger than
// the bound, and asserts the durable checkpoint advances between ticks and
// the edge beyond it is published exactly once.
func subOmittedRowsBeyondTheSkipBoundStillConverge(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	fixture.seedOversizedAmbiguousBlock(t, ctx)

	checkpoints := &memoryCheckpoints{checkpoint: contextfabric.ProjectionCheckpoint{OrgID: fixture.orgID, Source: devhealthsource.TeamsProjectsSourceName}}
	backend := &recordingBackend{}
	worker, err := contextfabric.NewProjectionWorker(fixture.source, backend, checkpoints, contextfabric.ProjectionWorkerOptions{})
	if err != nil {
		t.Fatalf("NewProjectionWorker: %v", err)
	}

	// Termination is by CONSTRUCTION, not by observation (codex round-5).
	//
	// Two earlier versions of this loop were vacuous in the same family this
	// batch keeps finding. The first broke on the first sighting of the edge,
	// so a later-tick duplicate could never be counted. The second treated an
	// unchanged checkpoint as caught-up -- but a batch that publishes while
	// leaving the cursor unmoved satisfies that condition, so the exact defect
	// the test exists to catch could end the loop before it was observed.
	//
	// The fixture knows its own size: 5200 ambiguous keys is 10400 joined
	// rows, ~52 pages, and one tick absorbs up to maxOmittedPageSkips (50)
	// pages, so the whole block plus the edge beyond it needs a handful of
	// ticks. Running a fixed count far past that, with no early exit, means
	// the source is exhausted by construction rather than because a signal
	// said so -- and every tick after exhaustion is a cheap no-op.
	const (
		totalTicks = 120
		quietTail  = 10
	)
	wantEdge := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-PAST-BOUND", "TEAM-GITHUB", "native")
	published := 0
	quiet := 0
	var previous cursorPosition
	for tick := 0; tick < totalTicks; tick++ {
		before := checkpoints.checkpoint.Cursor
		if _, err := worker.RunOnce(ctx, fixture.orgID, devhealthsource.TeamsProjectsSourceName); err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
		appliedThisTick := len(backend.applied)
		for _, batch := range backend.applied {
			if hasRelationship(batch, wantEdge) {
				published++
			}
		}
		backend.applied = nil
		after := checkpoints.checkpoint.Cursor

		// First-class invariant, asserted on EVERY tick rather than inferred
		// from how the loop happens to end: publishing without moving the
		// cursor forward would replay the same batch on every later tick.
		if appliedThisTick > 0 && !decodeCursorPosition(t, after).after(decodeCursorPosition(t, before)) {
			t.Fatalf("tick %d: applied %d batch(es) without advancing the cursor past %q -- that batch would republish forever", tick, appliedThisTick, before)
		}
		if after != before {
			current := decodeCursorPosition(t, after)
			if !previous.zero() && !current.after(previous) {
				t.Fatalf("tick %d: cursor went from %+v to %+v -- progress must order strictly forward", tick, previous, current)
			}
			previous = current
			quiet = 0
			continue
		}
		if appliedThisTick == 0 {
			quiet++
		}
	}
	if quiet < quietTail {
		t.Fatalf("only %d consecutive quiet ticks at the end (want >= %d) -- the run never demonstrably reached exhaustion, so the exactly-once claim below is not yet meaningful", quiet, quietTail)
	}
	if published != 1 {
		t.Fatalf("the edge beyond the omitted block was published %d times across the WHOLE run, want exactly 1", published)
	}
}

// cursorPosition mirrors the source's opaque cursor payload so the test can
// assert forward ordering rather than mere difference.
type cursorPosition struct {
	Since time.Time `json:"since"`
	After string    `json:"after"`
}

func (c cursorPosition) zero() bool { return c.Since.IsZero() && c.After == "" }

func (c cursorPosition) after(previous cursorPosition) bool {
	if previous.zero() {
		return !c.zero()
	}
	if c.Since.After(previous.Since) {
		return true
	}
	return c.Since.Equal(previous.Since) && c.After > previous.After
}

func decodeCursorPosition(t *testing.T, cursor string) cursorPosition {
	t.Helper()
	// An empty cursor is the from-scratch position, which sorts before every
	// real one -- not a malformed payload.
	if cursor == "" {
		return cursorPosition{}
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("decode cursor %q: %v", cursor, err)
	}
	var position cursorPosition
	if err := json.Unmarshal(raw, &position); err != nil {
		t.Fatalf("parse cursor %q: %v", cursor, err)
	}
	return position
}

// seedOversizedAmbiguousBlock writes more consecutive ambiguity-only rows
// than maxOmittedPageSkips pages can absorb in one call, then one valid edge
// after them.
func (f *ownershipFixture) seedOversizedAmbiguousBlock(t *testing.T, ctx context.Context) {
	t.Helper()
	early := ownershipLaterAssertion.Add(1 * time.Hour)
	block := ownershipLaterAssertion.Add(24 * time.Hour)
	beyond := ownershipLaterAssertion.Add(48 * time.Hour)
	// 5200 keys x 2 projects each = 10400 joined rows, past the 50-page x
	// 200-row in-process bound (maxOmittedPageSkips 50 x 200), which is why
	// the row count cannot be reduced -- the bound is what this test exists
	// to cross.
	//
	// The padding is 232, the smallest that still overflows
	// MaxNaturalKeyBytes: "project.v2:github:P-BOUND-A-" + pad + index
	// crosses 256 at pad ~225 (measured, not assumed). 256 was arbitrary and
	// made every one of these 10 400 rows a ~275-byte sort key for no
	// benefit. Omitted via an oversized natural key for the
	// same reason as seedAmbiguousBlockThenValidEdge above: after CHAOS-4542
	// ambiguity no longer produces a scan-side omission, so driving this with
	// ambiguous keys alone would leave the worker nothing to skip and this
	// test would converge for the wrong reason.
	const keys = 5200
	for _, half := range []string{"A", "B"} {
		mustExec(t, ctx, f.direct, `INSERT INTO projects
SELECT concat('P-BOUND-`+half+`-', repeat('x', 232), toString(number)), ?, 'github', concat('BOUND-', toString(number)), 'bulk', 1, 'started', '', ?
FROM numbers(?)`, f.orgID, early, uint64(keys))
	}
	mustExec(t, ctx, f.direct, `INSERT INTO team_project_ownership
SELECT ?, 'github', 'TEAM-GITHUB', concat('P-BOUND-A-', repeat('x', 232), toString(number)), concat('BOUND-', toString(number)), 'native', ?, NULL, ?
FROM numbers(?)`, f.orgID, block, block, uint64(keys))

	mustExec(t, ctx, f.direct, `INSERT INTO projects VALUES (?, ?, 'github', 'PAST-BOUND-KEY', 'past bound', 1, 'started', '', ?)`,
		"PROJ-PAST-BOUND", f.orgID, early)
	mustExec(t, ctx, f.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITHUB', ?, 'PAST-BOUND-KEY', 'native', ?, NULL, ?)`,
		f.orgID, "PROJ-PAST-BOUND", beyond, beyond)
}

// subTwoIDSpaceRowsYieldExactlyOneEdge is the CHAOS-4542 wedge case, and the
// reason the GROUP BY had to move to the RESOLVED projects.id rather than
// stay on the source's own columns.
//
// During the CHAOS-4530 transition one project legitimately carries BOTH a
// key-shaped ownership row (project_id holding the project KEY, the GitLab
// shape and today's legacy rows) and a UUID-shaped one (project_id holding
// projects.id, what 4530 writes). Both resolve, through different arms of
// the identity match, to the SAME projects.id -- and therefore to the same
// RelationshipID.
//
// What makes that catastrophic rather than merely untidy:
// ContextFabricProjectionBatch.Validate() rejects a batch with duplicate
// relationship IDs, a rejected batch never advances a checkpoint, and the
// organization's team/project projection then WEDGES PERMANENTLY -- it
// retries the same failing tick forever. This function's own THIRD note
// records that hazard as the reason the source's project_id was originally
// kept out of the GROUP BY.
//
// So: exactly ONE edge, and the batch must validate. Asserting only
// "an edge exists" would pass with two, which is the failure.
func subTwoIDSpaceRowsYieldExactlyOneEdge(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	const projectID = "PROJ-DUAL-SPACE"
	const projectKey = "DUAL-KEY"
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', ?, ?, 1, 'started', '', ?)`,
		projectID, fixture.orgID, projectKey, projectID+" name", ownershipLaterAssertion)
	// Key-shaped: project_id holds the project KEY (the legacy/GitLab shape).
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITHUB', ?, ?, 'native', ?, NULL, ?)`,
		fixture.orgID, projectKey, projectKey, ownershipFirstSeen, ownershipLaterAssertion)
	// UUID-shaped: project_id holds projects.id (what CHAOS-4530 writes).
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITHUB', ?, ?, 'native', ?, NULL, ?)`,
		fixture.orgID, projectID, projectKey, ownershipFirstSeen, ownershipLaterAssertion)

	batch := fixture.project(t, ctx)

	// The batch validating at all is half the assertion: a duplicate
	// RelationshipID is what wedges the projection.
	assertUniqueRelationshipIDs(t, batch)

	wanted := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", projectID, "TEAM-GITHUB", "native")
	matches := 0
	for _, relationship := range batch.Relationships {
		if relationship.RelationshipID == wanted {
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("relationship %q appeared %d times, want exactly 1 -- two ownership rows in different id spaces resolved to one project and were not collapsed; a duplicate RelationshipID rejects the batch, and a rejected batch never advances a checkpoint", wanted, matches)
	}
	// The edge must also be the RESOLVED project, never the raw key the
	// key-shaped row carried.
	if hasRelationship(batch, devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", projectKey, "TEAM-GITHUB", "native")) {
		t.Errorf("emitted an edge keyed on the raw project_key %q rather than the resolved projects.id", projectKey)
	}
}

// subEmptyKeyProjectsEachKeepTheirOwnEdge is the shape every other fixture
// in this file is missing, and the reason a chain of four defects reached
// live data before anyone noticed (CHAOS-4542, P1-1).
//
// Every project seeded above is given a project_key. Real Linear projects
// have NONE -- projects.project_key is nil by design, and CHAOS-4530 nulls
// it on the ownership row too, leaving project_id carrying projects.id. So
// the entire production shape this producer exists to serve appears in no
// test here, and both the ambiguity guard and the identity join were free
// to be wrong about it while thirteen subtests stayed green.
//
// What was wrong: the shared expansion computed key_resolution_count at
// PROJECT grain and carried it on every scope row, so a UUID match -- which
// is unambiguous by construction, projects.id being unique -- inherited
// "how many empty-key projects does this org have". Two is already enough
// to trip a `> 1` guard; the org this was measured against had seventeen.
// Both edges below were omitted as ambiguous, and the omission logged a
// project_key of "" for a match that never consulted a key at all.
//
// TWO projects, not one, is the whole point: with one, an empty-key
// partition of size one passes the guard and the fixture proves nothing.
func subEmptyKeyProjectsEachKeepTheirOwnEdge(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	const teamID = "TEAM-LINEAR"
	projectIDs := []string{
		"6241316a-85be-42ce-b243-8e41f2b18c8d",
		"7c1f0b52-0d33-4a7e-9f21-1b6a5d0e4c77",
	}
	mustExec(t, ctx, fixture.direct, `INSERT INTO teams VALUES (?, ?, '', ?, ?, 'linear', ?, [], 1)`,
		teamID, teamID+" name", ownershipLaterAssertion, fixture.orgID, teamID)
	for _, projectID := range projectIDs {
		// NULL project_key on BOTH rows: the real Linear shape after
		// CHAOS-4530, not an empty string standing in for it.
		mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'linear', NULL, ?, 1, 'started', '', ?)`,
			projectID, fixture.orgID, projectID+" name", ownershipLaterAssertion)
		mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'linear', ?, ?, NULL, 'native', ?, NULL, ?)`,
			fixture.orgID, teamID, projectID, ownershipFirstSeen, ownershipLaterAssertion)
	}

	batch := fixture.project(t, ctx)
	assertUniqueRelationshipIDs(t, batch)

	for _, projectID := range projectIDs {
		wanted := devhealthsource.ProjectTeamRelationshipIDForTest(t, "linear", projectID, teamID, "native")
		if !hasRelationship(batch, wanted) {
			t.Errorf("relationship %q missing -- a UUID match is unambiguous by construction, so no empty-key partition may suppress it; this is what CHAOS-4530 makes the ONLY shape Linear ownership has", wanted)
		}
	}
}

// CHAOS-4565 -- RETRACTION, and the only place it can honestly be proved.
//
// The fake client returns canned rows without executing the statement, so it
// can observe the SCAN's decision to retract (chaos4565_edge_retraction_test.go)
// but nothing about the two halves that actually make retraction reachable:
//
//   - an AMBIGUOUS key produces no result row at all on the parent commit,
//     because ProjectIdentityJoinSQL drops the key scope row upstream. The
//     retraction arm is what makes the row visible, and it is pure SQL.
//   - the CURSOR. Ambiguity arrives because a NEW project starts sharing an
//     existing key -- a write to `projects`, not to team_project_ownership.
//     The parent commit's watermark, max(o.updated_at), never moves for that
//     write, so the group stays behind the checkpoint and no incremental tick
//     ever re-reads it. A retraction emitted by unreachable code is not a fix.
//
// So each case below projects, MUTATES ONLY THE DATA, and projects again FROM
// THE FIRST TICK'S OWN NEXT CURSOR -- never from scratch, never with the
// checkpoint reset. That is the ticket's acceptance verbatim, and taking the
// second tick from a fresh cursor would prove the rebuild path instead, which
// already worked.

// hasTombstone reports whether the batch retracts this relationship id.
func hasTombstone(batch contextfabric.ProjectionBatch, canonicalID string) bool {
	for _, tombstone := range batch.Tombstones {
		if strings.EqualFold(tombstone.Kind, "relationship") && tombstone.CanonicalID == canonicalID {
			return true
		}
	}
	return false
}

// drainUntil pages from `cursor` until `found` is satisfied, and returns the
// cursor standing AFTER the batch that satisfied it.
//
// TERMINATION RULE (this file's header): the loop bound is derived from the
// fixture's size, not from the signal, and the caller asserts on `ok` -- so a
// walk that never finds the edge fails rather than passing quietly.
func drainUntil(t *testing.T, ctx context.Context, fixture *ownershipFixture, cursor string, found func(contextfabric.ProjectionBatch) bool) (string, bool) {
	t.Helper()
	for page := 0; page < 40; page++ {
		batch, available, err := fixture.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{
			OrgID: fixture.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if !available {
			return cursor, false
		}
		if batch.NextCursor == cursor {
			t.Fatalf("page %d: cursor did not advance", page)
		}
		cursor = batch.NextCursor
		assertUniqueRelationshipIDs(t, batch)
		if found(batch) {
			return cursor, true
		}
	}
	return cursor, false
}

// subRetractsAnEdgeWhoseKeyBecomesAmbiguous is CHAOS-4565's first acceptance
// line, and the OLDER of the two suppression paths -- live since v7, and
// invisible only because every intervening source-version bump happened to
// rebuild.
func subRetractsAnEdgeWhoseKeyBecomesAmbiguous(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	at := ownershipLaterAssertion.Add(72 * time.Hour)
	// GitLab-shaped: the key is in project_id AND project_key, and while it
	// names exactly one project both arms resolve it.
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', ?, 'retract me', 1, 'started', '', ?)`,
		"PROJ-RETRACT-KEY", fixture.orgID, "RETRACT-KEY", at)
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITHUB', ?, ?, 'native', ?, NULL, ?)`,
		fixture.orgID, "RETRACT-KEY", "RETRACT-KEY", ownershipFirstSeen, at)
	// The CONTROL rides the same ticks: an ordinary project whose ownership
	// is untouched by any of this. Retracting everything is the cheapest way
	// to pass the assertions below, and this is what refuses it.
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', ?, 'keep me', 1, 'started', '', ?)`,
		"PROJ-KEEP", fixture.orgID, "KEEP-KEY", at)
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITHUB', ?, ?, 'native', ?, NULL, ?)`,
		fixture.orgID, "PROJ-KEEP", "KEEP-KEY", ownershipFirstSeen, at)

	edge := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-RETRACT-KEY", "TEAM-GITHUB", "native")
	control := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-KEEP", "TEAM-GITHUB", "native")

	cursor, ok := drainUntil(t, ctx, fixture, "", func(b contextfabric.ProjectionBatch) bool { return hasRelationship(b, edge) })
	if !ok {
		t.Fatalf("%q was never projected, so there is no edge for the retraction to remove and every assertion below would pass vacuously", edge)
	}

	// THE ONLY MUTATION: a second project starts answering to the same key.
	// team_project_ownership is not touched at all -- that is the point.
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', ?, 'the collision', 1, 'started', '', ?)`,
		"PROJ-RETRACT-OTHER", fixture.orgID, "RETRACT-KEY", at.Add(time.Hour))

	cursor, ok = drainUntil(t, ctx, fixture, cursor, func(b contextfabric.ProjectionBatch) bool { return hasTombstone(b, edge) })
	if !ok {
		t.Fatalf("no incremental tick retracted %q after its key became ambiguous. Either the ambiguous row is still invisible to the producer, or the group never crossed the checkpoint because the watermark ignores the projects side -- both leave the stale edge live until a full rebuild, which is the CHAOS-4565 defect", edge)
	}

	// Nothing may RE-ASSERT it afterwards, and the control must be intact.
	// A retraction followed by the next tick putting the edge back is not a
	// retraction, it is a flap.
	controlSeen := false
	for page := 0; page < 40; page++ {
		batch, available, err := fixture.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{
			OrgID: fixture.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("drain page %d: %v", page, err)
		}
		if !available {
			break
		}
		cursor = batch.NextCursor
		if hasRelationship(batch, edge) {
			t.Fatalf("%q was re-asserted after being retracted; the ambiguity is still there, so nothing may put the edge back", edge)
		}
		if hasRelationship(batch, control) {
			controlSeen = true
		}
	}
	// The control may have been asserted on either side of the retraction, so
	// re-read it from scratch rather than depending on which tick carried it.
	if !controlSeen {
		if _, found := drainUntil(t, ctx, fixture, "", func(b contextfabric.ProjectionBatch) bool { return hasRelationship(b, control) }); !found {
			t.Fatalf("%q is gone: an unrelated, unambiguous ownership lost its edge, so the retraction is not scoped to the row that became unsubstantiable", control)
		}
	}
}

// subRetractsAnEdgeWhoseAmbiguousKeyArrivesViaProjectRef is CHAOS-4566 R4
// P2's shape: the ambiguous key lives in the ownership row's project_id
// (project_ref) column, not its project_key column, which is NULL. Before
// arm D this row matched nothing at all -- not arm A/B (the key's scope row
// does not exist once ambiguous), and not arm C (which requires
// project_key != ”). So the edge went missing with no tombstone, no
// conflict, and no telemetry: silence indistinguishable from "there was
// never an edge here".
func subRetractsAnEdgeWhoseAmbiguousKeyArrivesViaProjectRef(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	at := ownershipLaterAssertion.Add(72 * time.Hour)
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', ?, 'retract me via ref', 1, 'started', '', ?)`,
		"PROJ-RETRACT-REF", fixture.orgID, "RETRACT-REF-KEY", at)
	// project_key NULL on purpose: the row's ONLY tie to the project is
	// project_id, which carries the key-shaped value, exactly the GitLab
	// shape readers.ProjectOwnershipJoinColumn's own doc comment describes.
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITHUB', ?, NULL, 'native', ?, NULL, ?)`,
		fixture.orgID, "RETRACT-REF-KEY", ownershipFirstSeen, at)
	// The CONTROL rides the same ticks: an ordinary, unambiguous project_id
	// match with the same NULL-project_key shape, so a false-positive fix
	// that started matching every empty-project_key row shows up here.
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', ?, 'keep me too', 1, 'started', '', ?)`,
		"PROJ-KEEP-REF", fixture.orgID, "KEEP-REF-KEY", at)
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITHUB', ?, NULL, 'native', ?, NULL, ?)`,
		fixture.orgID, "PROJ-KEEP-REF", ownershipFirstSeen, at)

	edge := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-RETRACT-REF", "TEAM-GITHUB", "native")
	control := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-KEEP-REF", "TEAM-GITHUB", "native")

	cursor, ok := drainUntil(t, ctx, fixture, "", func(b contextfabric.ProjectionBatch) bool { return hasRelationship(b, edge) })
	if !ok {
		t.Fatalf("%q was never projected -- RETRACT-REF-KEY is unambiguous at this point (only PROJ-RETRACT-REF answers to it), so arm A's scope join must resolve it; if this fails, an unrelated arm regressed on the ordinary case, not the ambiguity path this test targets", edge)
	}

	// THE ONLY MUTATION: a second project starts answering to the same key.
	// team_project_ownership is not touched at all.
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', ?, 'the collision', 1, 'started', '', ?)`,
		"PROJ-RETRACT-REF-OTHER", fixture.orgID, "RETRACT-REF-KEY", at.Add(time.Hour))

	cursor, ok = drainUntil(t, ctx, fixture, cursor, func(b contextfabric.ProjectionBatch) bool { return hasTombstone(b, edge) })
	if !ok {
		t.Fatalf("no incremental tick retracted %q after its project_ref-carried key became ambiguous -- arm D (o.project_ref = p.project_key WHERE ... AND o.project_key = '') either regressed or never matched this row's shape", edge)
	}

	controlSeen := false
	for page := 0; page < 40; page++ {
		batch, available, err := fixture.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{
			OrgID: fixture.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("drain page %d: %v", page, err)
		}
		if !available {
			break
		}
		cursor = batch.NextCursor
		if hasRelationship(batch, edge) {
			t.Fatalf("%q was re-asserted after being retracted; the ambiguity is still there, so nothing may put the edge back", edge)
		}
		if hasRelationship(batch, control) {
			controlSeen = true
		}
	}
	if !controlSeen {
		if _, found := drainUntil(t, ctx, fixture, "", func(b contextfabric.ProjectionBatch) bool { return hasRelationship(b, control) }); !found {
			t.Fatalf("%q is gone: an unrelated ownership row sharing the same empty-project_key shape lost its edge, so arm D over-matched beyond the row that actually became ambiguous", control)
		}
	}
}

// subRetractsAnEdgeWhoseIdentityStartsConflicting is the second acceptance
// line. Same shape, different cause: the ownership row is untouched and
// another project's KEY changes underneath it, so the row's project_id and
// project_key now name different projects and neither can be called the
// winner.
func subRetractsAnEdgeWhoseIdentityStartsConflicting(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	at := ownershipLaterAssertion.Add(72 * time.Hour)
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', '', 'the id side', 1, 'started', '', ?)`,
		"PROJ-CONFLICT-A", fixture.orgID, at)
	// project_key names nothing yet, so only the id arm resolves and the edge
	// is clean.
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITHUB', ?, ?, 'native', ?, NULL, ?)`,
		fixture.orgID, "PROJ-CONFLICT-A", "CONFLICT-KEY", ownershipFirstSeen, at)

	edge := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-CONFLICT-A", "TEAM-GITHUB", "native")
	cursor, ok := drainUntil(t, ctx, fixture, "", func(b contextfabric.ProjectionBatch) bool { return hasRelationship(b, edge) })
	if !ok {
		t.Fatalf("%q was never projected; there is no edge for the retraction to remove", edge)
	}

	// THE ONLY MUTATION: a DIFFERENT project starts answering to the key this
	// ownership row carries. The row itself is untouched.
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', ?, 'the key side', 1, 'started', '', ?)`,
		"PROJ-CONFLICT-B", fixture.orgID, "CONFLICT-KEY", at.Add(time.Hour))

	if _, ok = drainUntil(t, ctx, fixture, cursor, func(b contextfabric.ProjectionBatch) bool { return hasTombstone(b, edge) }); !ok {
		t.Fatalf("no incremental tick retracted %q after its project_id and project_key started naming different projects; the edge the producer will no longer assert is still in the graph", edge)
	}
}

// subRetractionIsIdempotentAcrossAReRun pins the property that lets the
// retraction be emitted UNCONDITIONALLY.
//
// This producer is backend-neutral: it cannot ask the graph whether the edge
// is there, so it never knows whether a retraction is "necessary". What makes
// that safe is that re-running the same pass produces the same batch -- same
// batch id, same tombstone -- and applyTombstone's DELETE matches zero rows
// the second time. Without this, unconditional emission would be a source of
// churn rather than of healing.
func subRetractionIsIdempotentAcrossAReRun(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	at := ownershipLaterAssertion.Add(72 * time.Hour)
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', ?, 'one', 1, 'started', '', ?)`,
		"PROJ-IDEMPOTENT-A", fixture.orgID, "IDEMPOTENT-KEY", at)
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', ?, 'two', 1, 'started', '', ?)`,
		"PROJ-IDEMPOTENT-B", fixture.orgID, "IDEMPOTENT-KEY", at)
	// Already ambiguous when this row arrives: the NEVER-PROJECTED ordering.
	// The retraction must still be emitted (the producer cannot know it is
	// unnecessary) and must still be a no-op.
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITHUB', ?, ?, 'native', ?, NULL, ?)`,
		fixture.orgID, "IDEMPOTENT-KEY", "IDEMPOTENT-KEY", ownershipFirstSeen, at)

	edge := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-IDEMPOTENT-A", "TEAM-GITHUB", "native")

	project := func() contextfabric.ProjectionBatch {
		t.Helper()
		cursor := ""
		for page := 0; page < 40; page++ {
			batch, available, err := fixture.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{
				OrgID: fixture.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor,
			})
			if err != nil {
				t.Fatalf("page %d: %v", page, err)
			}
			if !available {
				break
			}
			cursor = batch.NextCursor
			if hasTombstone(batch, edge) {
				return batch
			}
		}
		t.Fatalf("%q was never retracted: an ownership row whose key was ALREADY ambiguous when it arrived must still emit the retraction, because the producer cannot know whether the edge was projected by an earlier deployment", edge)
		return contextfabric.ProjectionBatch{}
	}

	first, second := project(), project()
	if first.BatchID != second.BatchID {
		t.Errorf("re-running the same pass produced a different batch id (%q vs %q); ApplyProjectionBatch's replay idempotency is keyed on it", first.BatchID, second.BatchID)
	}
	if first.Cursor != second.Cursor || first.NextCursor != second.NextCursor {
		t.Errorf("re-running the same pass moved the cursor (%q..%q vs %q..%q)", first.Cursor, first.NextCursor, second.Cursor, second.NextCursor)
	}
	if hasRelationship(first, edge) || hasRelationship(second, edge) {
		t.Errorf("%q was asserted in the same batch that retracts it; tombstones apply after relationships, so the edge would be written and immediately deleted", edge)
	}
}

// subRetractionOnlyFollowsMaxRaisingProjectWrites PINS A BOUND, and it is
// deliberately not written as a success story (CHAOS-4565, codex round 2 P1,
// reproduced by this lane before being accepted).
//
// The retraction watermark is greatest(o.updated_at, max(project_updated_at)
// OVER the ownership row's identity partition), and the keyset filter is
// STRICT on it. A max over a MUTABLE SET only detects changes that RAISE the
// max. So a project inserted into a key partition with an updated_at BELOW the
// partition's existing maximum -- a backfill, a replayed sync, or a partition
// already dominated by a future-dated row -- creates an ambiguity that does
// not bring its group back over the cursor, and the previously projected edge
// survives until some later max-raising write.
//
// WHERE THE FUTURE TIMESTAMP ACTUALLY BITES, because it is not this producer.
// The projection cursor is SHARED by every table in this source, and
// queryProjects (teams_projects.go, `const rowKey = "concat(provider, ':', id)"`,
// ordered on projects.updated_at) is untouched by CHAOS-4565. It is what puts
// the future timestamp into the cursor in the first place -- the decoded
// cursor in the failing repro was
// {"since":"2031-...","after":"github:ZZZ-WATERMARK"}, a projects-ENTITY row
// key. So this class predates the retraction work; what changed is that the
// ownership producer now shares the cursor's fate instead of being immune to
// projects-side changes entirely. It is NOT a regression, and it is NOT a
// promise this producer can currently keep.
//
// The bound is therefore asserted in BOTH directions. Half one proves the
// documented limitation really is the behaviour, so nobody reads the design
// note's claim as unconditional. Half two proves the limitation is exactly
// "max-raising", not "broken": a later write that DOES raise the max retracts
// the edge. Without half two this test would be indistinguishable from the
// mechanism simply not working.
//
// Widening the cursor to catch non-max-raising changes needs a durable record
// of key-partition membership, which is not derivable from current state --
// tracked separately, together with the symmetric membership-LEAVING case.
func subRetractionOnlyFollowsMaxRaisingProjectWrites(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	at := ownershipLaterAssertion.Add(72 * time.Hour)
	future := at.AddDate(5, 0, 0)
	// The future-dated project pins the SOURCE-WIDE cursor five years ahead,
	// through queryProjects, before this producer is even consulted.
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', ?, 'zzz', 1, 'started', '', ?)`,
		"ZZZ-WATERMARK", fixture.orgID, "WM-KEY", future)
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITHUB', ?, ?, 'native', ?, NULL, ?)`,
		fixture.orgID, "WM-KEY", "WM-KEY", ownershipFirstSeen, at)

	edge := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "ZZZ-WATERMARK", "TEAM-GITHUB", "native")
	cursor, ok := drainUntil(t, ctx, fixture, "", func(b contextfabric.ProjectionBatch) bool { return hasRelationship(b, edge) })
	if !ok {
		t.Fatalf("%q was never projected -- with no edge in place both halves below would pass vacuously", edge)
	}

	// HALF ONE: a colliding project whose updated_at does NOT raise the
	// partition max. The ambiguity is real; the retraction does not happen.
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', ?, 'aaa', 1, 'started', '', ?)`,
		"AAA-WATERMARK", fixture.orgID, "WM-KEY", at.Add(time.Hour))
	afterLowWrite, retracted := drainUntil(t, ctx, fixture, cursor, func(b contextfabric.ProjectionBatch) bool { return hasTombstone(b, edge) })
	if retracted {
		t.Fatalf("%q WAS retracted after a projects-side write below the partition max. That is better than documented -- but the design note, this comment and the follow-up ticket all say it cannot happen, and a limitation that has silently been fixed is a lie in the documentation. Re-check the watermark and update all three.", edge)
	}

	// HALF TWO: a write that DOES raise the max. The same ambiguity, still
	// present, must now be retracted -- otherwise half one is measuring a
	// broken mechanism rather than a known bound.
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', ?, 'aaa', 1, 'started', '', ?)`,
		"AAA-WATERMARK", fixture.orgID, "WM-KEY", future.Add(time.Hour))
	if _, ok := drainUntil(t, ctx, fixture, afterLowWrite, func(b contextfabric.ProjectionBatch) bool { return hasTombstone(b, edge) }); !ok {
		t.Fatalf("%q was not retracted even after a projects-side write that RAISES the partition max -- then the bound is not 'max-raising only', the retraction is simply not working for this shape", edge)
	}
}

// subRowKeySQLAgreesWithGoByteForByte is the one test in this file that exists
// because two implementations of one definition cannot be collapsed into one
// (CHAOS-4635).
//
// The keyset predicate compares `toString(<rowKeySQL expression>) > {after}`
// against a string BUILT IN GO by identity.JoinSegments. If those two ever
// disagree by a single byte, pagination silently skips or replays rows -- the
// failure class this producer's own sincePredicate doc comment was written
// about. One side runs in ClickHouse and the other in Go, so there is no
// shared definition to point both at; the only honest substitute is to
// EXECUTE the SQL against a real server and compare.
//
// Eyeballing the two would not do. The escape has an ORDER ('%' before ':')
// and a preimage hazard: a value already containing "%3A" must not decode back
// to ':' and collapse into a different value's key. So the inputs below are
// chosen adversarially rather than illustratively -- each one is a way the
// pair could disagree.
func subRowKeySQLAgreesWithGoByteForByte(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	cases := []struct {
		name              string
		provider          string
		projectID, teamID string
		source            string
	}{
		{"plain values", "github", "PROJ-1", "TEAM-1", "native"},
		{"colons in the project id", "github", "{org}:gitlab:71133891", "TEAM-1", "native"},
		{"colons in the team id", "github", "PROJ-1", "gl:full.chaos", "native"},
		{"colons in both, falling differently", "github", "project:team", "source", "native"},
		{"the same characters, split differently", "github", "project", "team:source", "native"},
		{"a percent sign, which the escape rewrites first", "github", "100%", "TEAM-1", "native"},
		{"a %3A PREIMAGE: must not decode back into a separator", "github", "a%3Ab", "TEAM-1", "native"},
		{"a percent-escape preimage of the escape itself", "github", "a%25%3Ab", "TEAM-1", "native"},
		{"empty middle component", "github", "", "TEAM-1", "native"},
		{"unicode", "github", "проект:一", "команда", "native"},
		{"a trailing colon", "github", "PROJ-1:", "TEAM-1", "native"},
	}
	// One statement, one row per case, so a disagreement names its own input.
	for _, testCase := range cases {
		want := identity.JoinSegments(testCase.provider, testCase.projectID, testCase.teamID, testCase.source)
		statement := "SELECT " + devhealthsource.RowKeySQLForTest("{provider:String}", "{project_id:String}", "{team_id:String}", "{source:String}")
		rows, err := fixture.query.Query(ctx, statement, []contextpacket.ClickHouseBinding{
			{Name: "provider", Value: testCase.provider},
			{Name: "project_id", Value: testCase.projectID},
			{Name: "team_id", Value: testCase.teamID},
			{Name: "source", Value: testCase.source},
		})
		if err != nil {
			t.Fatalf("%s: run the row-key expression on a real server: %v\n%s", testCase.name, err, statement)
		}
		var got string
		if !rows.Next() {
			rows.Close()
			t.Fatalf("%s: the row-key expression returned no row", testCase.name)
		}
		if err := rows.Scan(&got); err != nil {
			rows.Close()
			t.Fatalf("%s: scan: %v", testCase.name, err)
		}
		rows.Close()
		if got != want {
			t.Errorf("%s: SQL and Go disagree on the cursor key.\n  SQL: %q\n  Go:  %q\nPagination compares these with a strict >, so a disagreement silently skips or replays rows.", testCase.name, got, want)
		}
	}

	// NON-VACUITY, and it is not decoration: every assertion above passes if
	// the escape is a no-op on both sides. The two inputs that used to
	// COLLIDE must now produce DIFFERENT keys -- otherwise this test proves
	// the two spellings agree while both remain broken.
	collidingA := identity.JoinSegments("github", "project:team", "source", "native")
	collidingB := identity.JoinSegments("github", "project", "team:source", "native")
	if collidingA == collidingB {
		t.Fatalf("the two historically-colliding inputs still share a cursor key %q -- SQL and Go agreeing on a non-injective key is agreement about the wrong thing", collidingA)
	}
	// And the provider, which the old key omitted entirely.
	if identity.JoinSegments("github", "P", "T", "native") == identity.JoinSegments("gitlab", "P", "T", "native") {
		t.Fatal("two providers still share a cursor key -- the GROUP BY separates them, so the tiebreaker must too")
	}
}

// subTwoGroupsSharingAProjectIDGetDistinctCursorKeys is the cursor-key half of
// CHAOS-4635, and its claim is DELIBERATELY NARROWER than the finding that
// prompted it. Read the refutation below before widening it back.
//
// WHAT IS PROVEN. The parent's tiebreaker was
// concat(project_id, team_id, source_name) and omitted `provider`, which its
// own GROUP BY includes. So one project id under two providers, owned by the
// same team from the same source, is TWO distinct groups with ONE cursor key.
// Cross-provider equal project ids are a documented property of this data
// model, not an edge case. Two groups sharing a tiebreaker have no defined
// order between them, so which one a page ends on is arbitrary and can differ
// between runs and between engines -- that alone is worth removing from a
// keyset cursor.
//
// WHAT IS NOT PROVEN, and was claimed before it was checked. The review
// finding said such a collision SILENTLY DROPS an edge at a page boundary.
// This lane tried twice to reproduce that against a real ClickHouse -- once
// with the bare pair, once with 205 filler groups at an identical watermark to
// force a cut -- and the parent PASSED both times, emitting both edges.
//
// The reason looks structural rather than lucky. fetch() asks for limit+1 raw
// rows and truncates by WHOLE ROW GROUPS keyed on (observedAt, sortKey)
// (truncateToCompleteRows, CHAOS-3753 finding K2). Two rows sharing a
// watermark AND a key are therefore ONE group: they are emitted together or
// deferred together, and the cursor never advances past one while the other is
// unseen. The invariant written for split candidate-lists happens to defend
// key collisions too.
//
// So this test asserts the collision and the arbitrary ordering, NOT a drop.
// Asserting a drop would be asserting something two executed attempts failed
// to show, which is exactly the kind of claim that reads as coverage while
// proving nothing.
func subTwoGroupsSharingAProjectIDGetDistinctCursorKeys(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	at := ownershipLaterAssertion.Add(96 * time.Hour)
	// ONE team for both arms: the parent key omits provider, so the pair must
	// share project id, team and source to collide on it.
	mustExec(t, ctx, fixture.direct, `INSERT INTO teams VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"TEAM-SHARED", "shared", "", at, fixture.orgID, "github", "TEAM-SHARED", []string{}, uint8(1))
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', ?, 'gh', 1, 'started', '', ?)`,
		"SHARED-ID", fixture.orgID, "GH-KEY", at)
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'gitlab', ?, 'gl', 1, 'started', '', ?)`,
		"SHARED-ID", fixture.orgID, "GL-KEY", at)
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-SHARED', ?, ?, 'native', ?, NULL, ?)`,
		fixture.orgID, "SHARED-ID", "GH-KEY", ownershipFirstSeen, at)
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'gitlab', 'TEAM-SHARED', ?, ?, 'native', ?, NULL, ?)`,
		fixture.orgID, "SHARED-ID", "GL-KEY", ownershipFirstSeen, at)

	// The keys must now differ. This is the property the fix delivers, and on
	// the parent these two strings were identical.
	ghKey := identity.JoinSegments("github", "SHARED-ID", "TEAM-SHARED", "native")
	glKey := identity.JoinSegments("gitlab", "SHARED-ID", "TEAM-SHARED", "native")
	if ghKey == glKey {
		t.Fatalf("two distinct ownership groups still share the cursor key %q -- their page order is undefined", ghKey)
	}

	// And both edges are projected, which is the outcome that must survive the
	// key change regardless of the ordering question.
	wantGitHub := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "SHARED-ID", "TEAM-SHARED", "native")
	wantGitLab := devhealthsource.ProjectTeamRelationshipIDForTest(t, "gitlab", "SHARED-ID", "TEAM-SHARED", "native")
	if wantGitHub == wantGitLab {
		t.Fatal("the two edges share a relationship id, so this fixture cannot tell them apart")
	}
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 40; page++ {
		batch, available, err := fixture.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{
			OrgID: fixture.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if !available {
			break
		}
		if batch.NextCursor == cursor {
			t.Fatalf("page %d: cursor did not advance", page)
		}
		cursor = batch.NextCursor
		for _, relationship := range batch.Relationships {
			seen[relationship.RelationshipID] = true
		}
	}
	if !seen[wantGitHub] || !seen[wantGitLab] {
		t.Errorf("an ownership edge is missing: github=%v gitlab=%v", seen[wantGitHub], seen[wantGitLab])
	}
}
