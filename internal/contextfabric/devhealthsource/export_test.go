package devhealthsource

import (
	"context"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// EntityTableNamesForTest exposes entityTables' table names (tables.go) to
// devhealthsource_test -- CHAOS-3789 codex round-1 F2: the schema-parity
// test derives its table inventory from this instead of a hand-duplicated
// list, so a producer added to entityTables without a matching parity-test
// seed row and expectation fails loudly instead of going silently
// unasserted.
func EntityTableNamesForTest() []string {
	names := make([]string, len(entityTables))
	for i, table := range entityTables {
		names[i] = table.name
	}
	return names
}

// TeamsProjectsTableNamesForTest exposes teamsProjectsTables' table names
// (teams_projects.go) for the same CHAOS-3789 F2 reason
// EntityTableNamesForTest exists: the schema-parity sweep derives its table
// inventory from the producer list itself, so a producer added without a
// matching seed row and expectation fails loudly instead of going silently
// unasserted.
func TeamsProjectsTableNamesForTest() []string {
	tables := teamsProjectsTables(nil, nil, nil, nil)
	names := make([]string, len(tables))
	for i, table := range tables {
		names[i] = table.name
	}
	return names
}

// NoTeamOwnershipSentinelForTest exposes noTeamOwnershipSentinel
// (teams_projects.go, CHAOS-4390) to devhealthsource_test, so a test can
// assert against the SAME literal production actually emits rather than a
// second hand-copied string that could silently drift from it.
func NoTeamOwnershipSentinelForTest() string {
	return noTeamOwnershipSentinel
}

// ProjectAuthorizationScopeForTest exposes queryProjects' reserved-namespace
// decision directly (CHAOS-3802 codex round-1 F4), so a test can prove the
// PRODUCER refuses a colliding project id without routing through
// ContextFabricEntityProjection.Validate(). Testing it end-to-end cannot
// distinguish the two: the contract rejects the same row either way, which is
// exactly how an earlier producer-side guard here went unverifiable and was
// removed. Both layers are wanted -- the producer fails fast and
// attributably, the contract is the unforgettable backstop -- so the producer
// half needs its own reachable seam.
func ProjectAuthorizationScopeForTest(projectID string) error {
	_, err := projectAuthorizationScope(projectID)
	return err
}

// EdgeValidityForTest exposes edgeValidity (validity.go) directly to
// devhealthsource_test -- CHAOS-3825. The end-to-end tests prove the
// degenerate-window collapse through two of the four call sites, but the
// invariant edgeValidity actually owns ("never return a valid_to before
// the valid_from") is a property of the FUNCTION, not of any one caller,
// and the remaining callers reach it with combinations no fixture
// exercises (nil starts, nil ends, touching bounds). Asserting it through
// a seam keeps the guard covered when a call site is added or a query is
// rewritten.
func EdgeValidityForTest(fromValidFrom, fromValidTo, toValidFrom, toValidTo *time.Time) (*time.Time, *time.Time) {
	return edgeValidity(fromValidFrom, fromValidTo, toValidFrom, toValidTo)
}

// ProjectTeamRelationshipIDForTest exposes the OWNED_BY_TEAM project<->team
// edge id to devhealthsource_test, derived exactly the way the producer
// derives it (CHAOS-4635).
//
// It takes the RAW source values a fixture actually seeds -- provider,
// projects.id, teams.id, the attribution source -- and runs them through the
// same identity.Derive + projectTeamRelationshipID pair queryProjectTeams
// uses, so a test expectation cannot become a second spelling of the
// encoding. That mattered immediately: the ids these tests used to hard-code
// were a raw colon join, and after the digest change every literal would have
// had to be re-copied by hand from a failing test's output -- which is how a
// fixture stops describing the producer and starts describing whatever it
// last printed.
//
// It is also why the conversion could not be mechanical. A literal like
// `relationship:project_team:github:70d529e0-...:gitlab:71133891:gl:full.chaos:native`
// cannot be split back into its components without already knowing where the
// boundaries are -- the exact ambiguity CHAOS-4635 exists to remove. Every
// call site was therefore rewritten from the fixture's OWN seeded values.
//
// Fails loudly rather than returning a wrong expectation: a project id this
// producer cannot represent yields no edge at all, so a test asserting on one
// is asserting about something that never existed.
func ProjectTeamRelationshipIDForTest(t interface{ Fatalf(string, ...any) }, provider, projectID, teamID, source string) string {
	projectCanonicalID, omitted, err := identity.Derive(identity.KindProject, []string{provider, projectID}, nil)
	if err != nil {
		t.Fatalf("derive project canonical id for (%q, %q): %v", provider, projectID, err)
		return ""
	}
	if omitted {
		t.Fatalf("project (%q, %q) is not representable, so the producer emits no edge for it -- this expectation is about an edge that cannot exist", provider, projectID)
		return ""
	}
	return projectTeamRelationshipID(projectCanonicalID, teamID, source)
}

// RepositoryTeamRelationshipIDForTest exposes the OWNED_BY_TEAM
// repository<->team edge id (CHAOS-6561) from the RAW values a fixture seeds
// -- repos.id, teams.id, team_repo_ownership.provider and .source -- through
// the producer's own derivation, for the reason
// ProjectTeamRelationshipIDForTest exists.
func RepositoryTeamRelationshipIDForTest(repoID, teamID, provider, source string) string {
	return repositoryTeamRelationshipID(repositoryCanonicalID(repoID), teamID, provider, source)
}

// RowKeySQLForTest exposes rowKeySQL so the SQL/Go byte-agreement test builds
// the SAME expression the producers page on, rather than a second copy of it
// that could agree with Go while production disagrees.
func RowKeySQLForTest(columns ...string) string { return rowKeySQL(columns...) }

// WorkItemTeamRelationshipIDForTest and ProjectMembershipRelationshipIDForTest
// are the seams for the other two edge families (CHAOS-4635), for the same
// reason ProjectTeamRelationshipIDForTest exists: an expectation must run the
// producer's own derivation, never a second copy of it.
//
// Both take the RAW values a fixture seeds and derive the endpoint canonical
// ids exactly as the producers do, so a fixture change and an expectation
// cannot drift apart.
func WorkItemTeamRelationshipIDForTest(t interface{ Fatalf(string, ...any) }, repoID, workItemID, teamID string) string {
	workItemCanonicalID, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, workItemID}, nil)
	if err != nil {
		t.Fatalf("derive work item (%q, %q): %v", repoID, workItemID, err)
		return ""
	}
	if omitted {
		t.Fatalf("work item (%q, %q) is not representable, so no edge exists to assert on", repoID, workItemID)
		return ""
	}
	return workItemTeamRelationshipID(workItemCanonicalID, teamID)
}

// ProjectMembershipRelationshipIDForTest takes the SUBJECT canonical id
// directly rather than re-deriving it: the pull-request arm mints its own
// legacy `pull_request:<repo>:<number>` id (see querySubjectProjectMemberships'
// doc comment on why identity.Derive is deliberately not used there), so a
// single derive-by-kind helper here would quietly disagree with the producer
// for half its call sites.
func ProjectMembershipRelationshipIDForTest(t interface{ Fatalf(string, ...any) }, subjectCanonicalID, provider, projectID, intervalSuffix string) string {
	projectCanonicalID, omitted, err := identity.Derive(identity.KindProject, []string{provider, projectID}, nil)
	if err != nil {
		t.Fatalf("derive project (%q, %q): %v", provider, projectID, err)
		return ""
	}
	if omitted {
		t.Fatalf("project (%q, %q) is not representable, so no edge exists to assert on", provider, projectID)
		return ""
	}
	return projectMembershipRelationshipID(subjectCanonicalID, projectCanonicalID, intervalSuffix)
}

// WorkItemSubjectCanonicalIDForTest and PullRequestSubjectCanonicalIDForTest
// mirror the two subject-id shapes querySubjectProjectMemberships mints, so a
// test naming a membership edge builds its FROM endpoint the same way.
func WorkItemSubjectCanonicalIDForTest(t interface{ Fatalf(string, ...any) }, repoID, workItemID string) string {
	id, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, workItemID}, nil)
	if err != nil || omitted {
		t.Fatalf("derive work item (%q, %q): omitted=%v err=%v", repoID, workItemID, omitted, err)
		return ""
	}
	return id
}

func PullRequestSubjectCanonicalIDForTest(repoID string, number int) string {
	return fmt.Sprintf("pull_request:%s:%d", repoID, number)
}

// EntityTableSubjectKindsForTest exposes, per table name, the entity subject
// kinds each producer registry DECLARES (tables.go / teams_projects.go), so a
// test can compare the declaration against what the producers actually emit.
//
// Exported for that comparison alone. ProjectedSubjectKinds() returns the
// union and is what consumers read; a consumer cannot tell which producer
// contributed a kind, and the pin has to, or a declaration moved from one
// table to another would leave the union unchanged and the pin green.
func EntityTableSubjectKindsForTest() map[string][]contractsv1.ContextFabricSubjectKind {
	declared := map[string][]contractsv1.ContextFabricSubjectKind{}
	for _, table := range entityTables {
		declared[table.name] = append([]contractsv1.ContextFabricSubjectKind(nil), table.subjectKinds...)
	}
	for _, table := range teamsProjectsTables(nil, nil, nil, nil) {
		declared[table.name] = append([]contractsv1.ContextFabricSubjectKind(nil), table.subjectKinds...)
	}
	return declared
}

// SetOrgSourceClockForTest pins ClickHouseOrgSource's activity-window clock
// (CHAOS-6182), so an eligibility test states its fixtures relative to a
// FIXED now instead of wall time. Without it, a boundary cell ("exactly one
// window old is still eligible") is decided by however many microseconds
// elapsed between building the fixture and running the query -- a test that
// passes because it is fast, not because the comparison is right.
func SetOrgSourceClockForTest(source *ClickHouseOrgSource, now func() time.Time) {
	source.now = now
}

// RepositoryTeamsStatementForTest renders queryRepositoryTeams' statement
// from scratch (CHAOS-7119 group-key parity test).
func RepositoryTeamsStatementForTest() string { return repositoryTeamsStatement(cursorState{}) }

// RepositoryTeamsGroupColumnsForTest is the edge's GROUP BY / row-key column
// list (CHAOS-7119).
func RepositoryTeamsGroupColumnsForTest() []string {
	return append([]string(nil), repositoryTeamsGroupColumns...)
}

// OverBoundTeamOwnershipSentinelForTest exposes overBoundTeamOwnershipSentinel
// (CHAOS-7139) so tests assert the literal production emits.
func OverBoundTeamOwnershipSentinelForTest() string {
	return overBoundTeamOwnershipSentinel
}

// OwnedRepositoriesJoinSQLForTest exposes the team authorization list's join
// statement (CHAOS-7130 grouping-key parity test).
func OwnedRepositoriesJoinSQLForTest() string { return ownedRepositoriesJoinSQL }

// RepositoryTeamsGroupedSQLForTest exposes the shared derivation (CHAOS-7130).
func RepositoryTeamsGroupedSQLForTest() string { return repositoryTeamsGroupedSQL() }

// SetClockForTest pins the source's clock, which dates overlap-window passes
// (overlap.go), so a window edge can be asserted to the millisecond.
func (s *ClickHouseProjectionSource) SetClockForTest(now func() time.Time) { s.now = now }

// SetWindowPagesPerCallForTest lowers how many window pages one call walks,
// so a pass that spans several calls needs a window of a few pages instead
// of more than overlapWindowPagesPerCall (1,000+ rows). The walk logic is the
// same; only the per-call bound changes.
func (s *ClickHouseProjectionSource) SetWindowPagesPerCallForTest(n int) { s.windowPages = n }

// IngestCursorForTest encodes a cursor in the current (ingest) position space,
// unlike a space-less cursor, which the source reads as a reset and re-reads
// from the start.
func IngestCursorForTest(since time.Time, after string) string {
	encoded, err := encodeCursor(cursorState{Since: since, After: after})
	if err != nil {
		panic(err)
	}
	return encoded
}

// IssuePullRequestLinkRowForTest is one scanned row of queryIssuePullRequestLinks
// as the keyset sees it: its position, its row key, and either the projected
// edge (RelationshipID, Tier, Rank) or the skip reason it was counted under.
type IssuePullRequestLinkRowForTest struct {
	Position       time.Time
	SortKey        string
	RelationshipID string
	From, To       string
	Tier           string
	Rank           int64
	IgnoredReason  string
}

// IssuePullRequestLinkPageForTest runs queryIssuePullRequestLinks for ONE page
// from the cursor (since, after) with the given page limit, and returns the
// page's rows in the order the producer returned them, and whether the
// producer reports more rows.
func IssuePullRequestLinkPageForTest(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, since time.Time, after string, limit int) ([]IssuePullRequestLinkRowForTest, bool, error) {
	candidates, more, err := queryIssuePullRequestLinks(ctx, client, orgID, cursorState{Since: since, After: after}, limit)
	if err != nil {
		return nil, false, err
	}
	rows := make([]IssuePullRequestLinkRowForTest, 0, len(candidates))
	for _, c := range candidates {
		row := IssuePullRequestLinkRowForTest{Position: c.position(), SortKey: c.sortKey, IgnoredReason: c.ignoredType}
		if r := c.relationship; r != nil {
			row.RelationshipID, row.From, row.To = r.RelationshipID, r.From.CanonicalID, r.To.CanonicalID
			if v := r.Properties[IssuePullRequestLinkTierProperty].String; v != nil {
				row.Tier = *v
			}
			if v := r.Properties[IssuePullRequestLinkRankProperty].Integer; v != nil {
				row.Rank = *v
			}
		}
		rows = append(rows, row)
	}
	return rows, more, nil
}

// ReadPullRequestPageForTest runs the pull request producer's own read
// (queryPullRequests) for one page from a cursor position, so a live test can
// drive the exact statement the projector sends at a chosen cursor: the
// catch-up after a rebuild (a zero position) or a steady tick (a recent one).
func ReadPullRequestPageForTest(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, since time.Time, after string, limit int) (rows int, truncated bool, err error) {
	candidates, truncated, err := queryPullRequests(ctx, client, orgID, cursorState{Since: since, After: after, Space: cursorSpaceIngest}, limit)
	if err != nil {
		return 0, false, err
	}
	for _, c := range candidates {
		if c.entity != nil {
			rows++
		}
	}
	return rows, truncated, nil
}

// legacyPullRequestStatement is queryPullRequests' statement as it stood on
// main before CHAOS-8683's keys-first read (main c994ba42), kept here only so
// a live test can show the seed it runs reproduces the prod refusal.
func legacyPullRequestStatement(cursor cursorState) string {
	const rowKey = "concat(toString(p.repo_id), ':', toString(p.number))"
	return `SELECT toString(p.repo_id), r.repo, p.number, ifNull(p.title, ''), ifNull(p.state, ''), p.last_synced,
       p.created_at, ` + nullableTimestamp("coalesce(p.merged_at, p.closed_at)") + `,
       ifNull(p.head_branch, ''), ifNull(p.body, '')
FROM git_pull_requests AS p FINAL INNER JOIN repos AS r FINAL ON r.id = p.repo_id AND r.org_id = p.org_id
WHERE p.org_id = {org_id:String}` + sincePredicate(cursor, "p.last_synced", rowKey) + orderBy("p.last_synced", rowKey)
}

// LegacyPullRequestReadForTest runs that statement for one page from a zero
// cursor (the catch-up after a rebuild) and returns its error.
func LegacyPullRequestReadForTest(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, limit int) error {
	cursor := cursorState{Space: cursorSpaceIngest}
	rows, err := client.Query(ctx, legacyPullRequestStatement(cursor), rowLimitBindings(orgID, cursor, limit))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	return rows.Err()
}

// SetPullRequestGranuleBytesForTest sets the granule size the pull request
// wide read sizes its statements from, for a live test whose store cuts
// granules smaller than ClickHouse's default; restored when the test ends.
func SetPullRequestGranuleBytesForTest(t interface{ Cleanup(func()) }, granule uint64) {
	previous := pullRequestGranuleBytes
	pullRequestGranuleBytes = granule
	t.Cleanup(func() { pullRequestGranuleBytes = previous })
}

// DrainPullRequestPagesForTest reads every pull request page of an
// organization from a zero cursor, through main's single statement (legacy)
// or the two-step read, and returns the rows read.
func DrainPullRequestPagesForTest(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, legacy bool, limit int) (int, error) {
	cursor := cursorState{Space: cursorSpaceIngest}
	rows := 0
	for page := 0; page < 10000; page++ {
		var candidates []candidate
		var truncated bool
		var err error
		if legacy {
			candidates, truncated, err = fetch(ctx, client, legacyPullRequestStatement(cursor), rowLimitBindings(orgID, cursor, limit), limit, scanPullRequestRow)
		} else {
			candidates, truncated, err = queryPullRequests(ctx, client, orgID, cursor, limit)
		}
		if err != nil {
			return rows, err
		}
		var last *candidate
		for i := range candidates {
			if candidates[i].entity != nil {
				rows++
				last = &candidates[i]
			}
		}
		if !truncated || last == nil {
			return rows, nil
		}
		cursor = cursorState{Since: last.position(), After: last.sortKey, Space: cursorSpaceIngest}
	}
	return rows, fmt.Errorf("pull request pages did not end")
}

// CursorFactPositionMovedForTest reports whether a cursor carries a fact
// position (the walk has moved it from zero).
func CursorFactPositionMovedForTest(t interface{ Fatalf(string, ...any) }, cursor string) bool {
	state, err := decodeCursor(cursor)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	return state.Dim == nil && !state.Since.IsZero()
}
