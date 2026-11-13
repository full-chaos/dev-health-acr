package devhealthsource_test

// The project -> team ownership producer pages the STORED groups (ops
// migration 147): a seek on project_team_ownership_groups_log and a check
// against project_team_ownership_groups_current. These cells run as subtests
// of TestOwnershipProducerAgainstRealClickHouse, on its shared schema.
//
// The stored groups are written by a refresh view every 60 seconds. A fixture
// that changes its source rows and reads at once would read a store the
// refresh has not caught up with, so storedGroupsClient brings the store up to
// date before every read of it: it refreshes until the organization's current
// groups equal the per-organization group statement (the oracle). Late
// refreshes are a separate cell (the late-commit overlap cell).

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const ownershipGroupsRefreshView = "project_team_ownership_groups_refresh"

// createOwnershipGroupsStore creates the two stored-group tables from their
// declaration and the copy and refresh views (migration 147).
func createOwnershipGroupsStore(t *testing.T, ctx context.Context, direct interface {
	Exec(ctx context.Context, query string, args ...any) error
}) {
	t.Helper()
	for _, statement := range append(devhealthschema.DDL(devhealthschema.OwnershipGroupsTables...), devhealthschema.OwnershipGroupsViewsDDL()...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("apply stored ownership group statement: %v\n%s", err, statement)
		}
	}
}

// storedGroupsClient is the producer's query client with the stored groups
// brought up to date before every statement that reads them.
type storedGroupsClient struct {
	inner  contextpacket.ClickHouseQueryClient
	direct clickhousedriver.Conn
	mu     sync.Mutex
	synced map[string]string // organization -> source fingerprint at its last sync
	events []string          // DIAG: sync decisions, newest last
}

// DIAG note records one sync decision with its server-independent wall time.
func (c *storedGroupsClient) note(format string, args ...any) {
	c.events = append(c.events, time.Now().UTC().Format("15:04:05.000000")+" "+fmt.Sprintf(format, args...))
	if len(c.events) > 60 {
		c.events = c.events[len(c.events)-60:]
	}
}

func newStoredGroupsClient(inner contextpacket.ClickHouseQueryClient, direct clickhousedriver.Conn) *storedGroupsClient {
	return &storedGroupsClient{inner: inner, direct: direct, synced: map[string]string{}}
}

func (c *storedGroupsClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if strings.Contains(statement, "project_team_ownership_groups_") {
		for _, b := range bindings {
			if org, ok := b.Value.(string); ok && b.Name == "org_id" && org != "" {
				if err := c.sync(ctx, org); err != nil {
					return nil, err
				}
			}
		}
	}
	return c.inner.Query(ctx, statement, bindings)
}

// sync refreshes until the organization's live stored groups equal the
// oracle. Its own statements run without the caller's settings, so a
// log_comment a cell tags its reads with never lands on them.
func (c *storedGroupsClient) sync(ctx context.Context, orgID string) error {
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{}))
	c.mu.Lock()
	defer c.mu.Unlock()
	// Each row is hashed through its text: cityHash64 of a row with a NULL
	// column is NULL, and sum skips it, so a table with a Nullable column
	// fingerprinted by row count alone, and a background merge that replaced
	// a row kept the count and skipped the sync.
	var fingerprint string
	if err := c.direct.QueryRow(ctx, `SELECT toString(tuple(
  (SELECT tuple(count(), sum(cityHash64(toString(tuple(*))))) FROM projects WHERE org_id = ?),
  (SELECT tuple(count(), sum(cityHash64(toString(tuple(*))))) FROM team_project_ownership WHERE org_id = ?),
  (SELECT tuple(count(), sum(cityHash64(toString(tuple(*))))) FROM teams WHERE org_id = ?)))`, orgID, orgID, orgID).Scan(&fingerprint); err != nil {
		return fmt.Errorf("stored groups fingerprint: %w", err)
	}
	if strings.Contains(fingerprint, "NULL") {
		return fmt.Errorf("stored groups fingerprint of %s has a NULL part, so it cannot see a change: %s", orgID, fingerprint)
	}
	if c.synced[orgID] == fingerprint {
		c.note("skip fp=%s", fingerprint)
		return nil
	}
	c.note("sync fp=%s last=%s", fingerprint, c.synced[orgID])
	deadline := time.Now().Add(3 * time.Minute)
	for {
		diff, err := c.storedDiff(ctx, orgID)
		if err != nil {
			return err
		}
		if diff == "" {
			c.note("match")
			c.synced[orgID] = fingerprint
			return nil
		}
		c.note("diff %s", diff)
		if time.Now().After(deadline) {
			return fmt.Errorf("the stored groups of %s never matched the oracle: %s", orgID, diff)
		}
		if err := refreshStoredGroups(ctx, c.direct); err != nil {
			return err
		}
	}
}

// ownershipRefreshGate orders the refresh view between parallel subtests: a
// sync refreshes under a read lock, and a subtest that must write a log row
// with no refresh in between takes the write lock (landWithoutRefresh).
var ownershipRefreshGate sync.RWMutex

func refreshStoredGroups(ctx context.Context, direct clickhousedriver.Conn) error {
	ownershipRefreshGate.RLock()
	defer ownershipRefreshGate.RUnlock()
	for _, statement := range []string{"SYSTEM REFRESH VIEW " + ownershipGroupsRefreshView, "SYSTEM WAIT VIEW " + ownershipGroupsRefreshView} {
		if err := direct.Exec(ctx, statement); err != nil {
			return fmt.Errorf("%s: %w", statement, err)
		}
	}
	return nil
}

// landWithoutRefresh runs land while no refresh of the stored groups can run:
// it holds the gate against the syncs of parallel subtests and stops the
// view's own 60-second schedule, then starts the schedule again.
func landWithoutRefresh(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, land func()) {
	t.Helper()
	ownershipRefreshGate.Lock()
	defer ownershipRefreshGate.Unlock()
	mustExec(t, ctx, direct, "SYSTEM STOP VIEW "+ownershipGroupsRefreshView)
	defer mustExec(t, ctx, direct, "SYSTEM START VIEW "+ownershipGroupsRefreshView)
	mustExec(t, ctx, direct, "SYSTEM WAIT VIEW "+ownershipGroupsRefreshView)
	land()
}

// storedDiff is "" when the live stored groups equal the oracle's groups,
// else the first differences.
func (c *storedGroupsClient) storedDiff(ctx context.Context, orgID string) (string, error) {
	read := func(statement string) (map[string]string, error) {
		rows, err := c.direct.Query(clickhouse.Context(ctx, clickhouse.WithParameters(clickhouse.Parameters{"org_id": orgID})), statement)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var key, content string
			if err := rows.Scan(&key, &content); err != nil {
				return nil, err
			}
			out[key] = content
		}
		return out, rows.Err()
	}
	oracle, err := read(devhealthsource.OwnershipGroupsOracleSQLForTest())
	if err != nil {
		return "", fmt.Errorf("oracle groups: %w", err)
	}
	stored, err := read(devhealthsource.OwnershipGroupsCurrentContentSQLForTest())
	if err != nil {
		return "", fmt.Errorf("stored groups: %w", err)
	}
	var diffs []string
	for key, want := range oracle {
		if got, ok := stored[key]; !ok || got != want {
			diffs = append(diffs, fmt.Sprintf("%s stored=%q oracle=%q", key, got, want))
		}
	}
	for key, got := range stored {
		if _, ok := oracle[key]; !ok {
			diffs = append(diffs, fmt.Sprintf("%s stored=%q oracle=absent", key, got))
		}
	}
	sort.Strings(diffs)
	if len(diffs) > 5 {
		diffs = append(diffs[:5], fmt.Sprintf("... %d more", len(diffs)-5))
	}
	return strings.Join(diffs, "; "), nil
}

func (f *ownershipFixture) syncStoredGroups(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := f.client.sync(ctx, f.orgID); err != nil {
		t.Fatal(err)
	}
}

// ownershipState is what a walk leaves for each group: the last edge or
// tombstone a candidate wrote for its key. A candidate that writes nothing
// leaves the key as it was.
type ownershipState map[string]devhealthsource.OwnershipCandidateForTest

func ownershipStateOf(candidates []devhealthsource.OwnershipCandidateForTest) ownershipState {
	out := ownershipState{}
	for _, c := range candidates {
		if c.Relationship == nil && c.Tombstone == nil {
			continue
		}
		c.Position = time.Time{}
		out[c.SortKey] = c
	}
	return out
}

// assertOwnershipMatchesOracle is the answer-equivalence cell: the producer's
// walk over the stored groups writes, for every group, the same edge or
// tombstone (every field; the keyset position is not an answer) as the
// per-organization group statement.
func assertOwnershipMatchesOracle(t *testing.T, ctx context.Context, f *ownershipFixture) devhealthsource.OwnershipCandidateCounts {
	t.Helper()
	f.syncStoredGroups(t, ctx)
	oracle, err := devhealthsource.LegacyOwnershipCandidatesForTest(ctx, f.query, f.orgID)
	if err != nil {
		t.Fatalf("oracle walk: %v", err)
	}
	pageSize := 7
	if len(oracle) > 300 {
		pageSize = 997
	}
	var walked []devhealthsource.OwnershipCandidateForTest
	var since time.Time
	after := ""
	for page := 0; ; page++ {
		if page > 5000 {
			t.Fatalf("the ownership walk did not end in 5000 pages")
		}
		rows, more, err := devhealthsource.OwnershipPageForTest(ctx, f.client, f.orgID, since, after, pageSize)
		if err != nil {
			t.Fatalf("ownership page %d: %v", page, err)
		}
		walked = append(walked, rows...)
		if !more || len(rows) == 0 {
			break
		}
		last := rows[len(rows)-1]
		since, after = last.Position, last.SortKey
	}
	want, got := ownershipStateOf(oracle), ownershipStateOf(walked)
	var diffs []string
	for key, w := range want {
		g, ok := got[key]
		switch {
		case !ok:
			diffs = append(diffs, "missing "+key)
		case !reflect.DeepEqual(g, w):
			diffs = append(diffs, fmt.Sprintf("differs %s: got %+v / %+v, oracle %+v / %+v", key, g.Relationship, g.Tombstone, w.Relationship, w.Tombstone))
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			diffs = append(diffs, "extra "+key)
		}
	}
	sort.Strings(diffs)
	if len(diffs) > 0 {
		if len(diffs) > 10 {
			diffs = append(diffs[:10], fmt.Sprintf("... %d more", len(diffs)-10))
		}
		t.Errorf("the ownership walk differs from the oracle on %d groups:\n%s", len(diffs), strings.Join(diffs, "\n"))
	}
	var counts devhealthsource.OwnershipCandidateCounts
	for _, c := range want {
		if c.Relationship != nil {
			counts.Relationships++
		} else {
			counts.Tombstones++
		}
	}
	return counts
}

// ownershipTicks calls NextProjectionBatch from cursor, applying each batch,
// until the source has nothing more.
func ownershipTicks(t *testing.T, ctx context.Context, f *ownershipFixture, cursor string) (string, []contextfabric.ProjectionBatch) {
	t.Helper()
	var batches []contextfabric.ProjectionBatch
	for tick := 0; tick < 300; tick++ {
		batch, available, err := f.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: f.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor})
		if err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
		if !available {
			return cursor, batches
		}
		assertUniqueRelationshipIDs(t, batch)
		batches = append(batches, batch)
		cursor = batch.NextCursor
	}
	t.Fatalf("the source did not settle within 300 ticks")
	return "", nil
}

// projectTeamEdges is every project -> team OWNED_BY_TEAM edge in a batch.
func projectTeamEdges(batch contextfabric.ProjectionBatch) []contractsv1.ContextFabricRelationshipProjection {
	var out []contractsv1.ContextFabricRelationshipProjection
	for _, r := range batch.Relationships {
		if r.Type == contractsv1.ContextFabricRelationshipOwnedByTeam && r.From.Kind == contractsv1.ContextFabricSubjectProject {
			out = append(out, r)
		}
	}
	return out
}

// ownershipItemIDs is the id of every project -> team edge and of every
// tombstone named in tombstoneIDs, in batch order.
func ownershipItemIDs(batch contextfabric.ProjectionBatch, tombstoneIDs map[string]bool) []string {
	var out []string
	for _, r := range projectTeamEdges(batch) {
		out = append(out, r.RelationshipID)
	}
	for _, tomb := range batch.Tombstones {
		if tombstoneIDs[tomb.CanonicalID] {
			out = append(out, tomb.CanonicalID)
		}
	}
	return out
}

// liveOwnershipIDs is the edge and tombstone ids the oracle writes for the
// organization now.
func liveOwnershipIDs(t *testing.T, ctx context.Context, f *ownershipFixture) (edges, tombstones map[string]bool) {
	t.Helper()
	oracle, err := devhealthsource.LegacyOwnershipCandidatesForTest(ctx, f.query, f.orgID)
	if err != nil {
		t.Fatalf("oracle walk: %v", err)
	}
	edges, tombstones = map[string]bool{}, map[string]bool{}
	for _, c := range ownershipStateOf(oracle) {
		if c.Relationship != nil {
			edges[c.Relationship.RelationshipID] = true
		} else {
			tombstones[c.Tombstone.CanonicalID] = true
		}
	}
	return edges, tombstones
}

func seedOwnedProjectBlock(t *testing.T, ctx context.Context, f *ownershipFixture, prefix string, n int) {
	t.Helper()
	mustExec(t, ctx, f.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at)
SELECT concat(?, leftPad(toString(number), 4, '0')), ?, 'github', concat(?, '-KEY-', toString(number)), 'bulk', 1, 'started', '', ? FROM numbers(?)`,
		prefix, f.orgID, prefix, ownershipLaterAssertion, uint64(n))
	mustExec(t, ctx, f.direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at)
SELECT ?, 'github', 'TEAM-GITHUB', concat(?, leftPad(toString(number), 4, '0')), concat(?, '-KEY-', toString(number)), 'native', ?, NULL, ? FROM numbers(?)`,
		f.orgID, prefix, prefix, ownershipFirstSeen, ownershipLaterAssertion, uint64(n))
}

// ownershipPageReadBound is six index granules: the page statement reads the
// log seek twice (the page itself and the row-key set of the current lookup,
// ownershipGroupsPageStatement), each at most two granules for a contiguous
// keyset range, and the current granules that hold the page's row keys, at
// most two. A page whose range crosses a granule boundary reads all six. The
// granularity is read from both tables, not assumed; the bound is stated for
// 8192 only.
func ownershipPageReadBound(t *testing.T, ctx context.Context, direct clickhousedriver.Conn) uint64 {
	t.Helper()
	rows, err := direct.Query(ctx, `SELECT name, toUInt64OrZero(extract(engine_full, 'index_granularity = ([0-9]+)')) FROM system.tables WHERE database = currentDatabase() AND name IN (?, ?)`,
		"project_team_ownership_groups_log", "project_team_ownership_groups_current")
	if err != nil {
		t.Fatalf("read the stored group tables' index_granularity: %v", err)
	}
	defer rows.Close()
	seen := map[string]uint64{}
	for rows.Next() {
		var name string
		var granularity uint64
		if err := rows.Scan(&name, &granularity); err != nil {
			t.Fatal(err)
		}
		seen[name] = granularity
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"project_team_ownership_groups_log", "project_team_ownership_groups_current"} {
		if seen[table] != 8192 {
			t.Fatalf("%s index_granularity = %d (read from system.tables), the page bound 6 x granularity is stated for 8192 only: restate the bound", table, seen[table])
		}
	}
	return 6 * seen["project_team_ownership_groups_log"]
}

// C1: a page's cost is bounded and does not grow with the organization. The
// read rows of each page statement are read back from system.query_log by a
// per-page log_comment, on an organization of 5,200 groups and one of 10,400.
//
// The bound is per data part: a page reads its granules in every active part
// that holds the organization's rows in its range, and the first page (no
// keyset yet) touches every such part. A refresh can write its rows as several
// parts, and parallel subtests keep appending, so under load the first page
// can read an extra granule per unmerged part. Both tables are merged before the
// measure, as background merges do in time, and each page logs the parts and
// marks it selected.
func subOwnershipPageCostIsBoundedAndFlat(t *testing.T, ctx context.Context, f *ownershipFixture) {
	bound := ownershipPageReadBound(t, ctx, f.direct)
	big := newOwnershipFixture(t, ctx, f.query, f.direct, "30000000-0000-4000-8000-0000000063ff")
	seedBoundBlock(t, ctx, f, 2600)
	seedBoundBlock(t, ctx, big, 5200)
	run := fmt.Sprintf("%d", time.Now().UnixNano())
	measure := func(fx *ownershipFixture, label string) []uint64 {
		fx.syncStoredGroups(t, ctx)
		for _, table := range []string{"project_team_ownership_groups_log", "project_team_ownership_groups_current"} {
			mustExec(t, ctx, f.direct, "OPTIMIZE TABLE "+table+" FINAL")
		}
		var tags []string
		var since time.Time
		after := ""
		for page := 0; page < 6; page++ {
			tag := fmt.Sprintf("own-c1-%s-%s-%d", label, run, page)
			tagged := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"log_comment": tag}))
			rows, more, err := devhealthsource.OwnershipPageForTest(tagged, fx.client, fx.orgID, since, after, 200)
			if err != nil {
				t.Fatalf("%s page %d: %v", label, page, err)
			}
			tags = append(tags, tag)
			if !more || len(rows) == 0 {
				break
			}
			last := rows[len(rows)-1]
			since, after = last.Position, last.SortKey
		}
		if len(tags) < 3 {
			t.Fatalf("%s: %d pages, want >= 3", label, len(tags))
		}
		mustExec(t, ctx, f.direct, "SYSTEM FLUSH LOGS")
		out := make([]uint64, 0, len(tags))
		for _, tag := range tags {
			var read, statements, parts, marks uint64
			if err := f.direct.QueryRow(ctx, `SELECT sum(read_rows), count(), sum(ProfileEvents['SelectedParts']), sum(ProfileEvents['SelectedMarks']) FROM system.query_log WHERE type = 'QueryFinish' AND log_comment = ?`, tag).Scan(&read, &statements, &parts, &marks); err != nil {
				t.Fatal(err)
			}
			if statements == 0 {
				t.Fatalf("%s: no finished statement carries log_comment %s", label, tag)
			}
			t.Logf("C1 %s %s read_rows=%d statements=%d selected_parts=%d selected_marks=%d", label, tag, read, statements, parts, marks)
			if read > bound {
				t.Errorf("%s: a page read %d rows, bound %d", label, read, bound)
			}
			out = append(out, read)
		}
		return out
	}
	median := func(v []uint64) uint64 {
		s := append([]uint64(nil), v...)
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		return s[len(s)/2]
	}
	small, large := median(measure(f, "5200")), median(measure(big, "10400"))
	t.Logf("C1 median read_rows per page: 5200 groups %d, 10400 groups %d", small, large)
	if small == 0 || float64(large) > 1.1*float64(small) {
		t.Errorf("page cost grows with the organization: median %d rows at 10400 groups, %d at 5200 (bound 1.1x)", large, small)
	}
}

// seedBoundBlock is seedOversizedAmbiguousBlock's block at a chosen size:
// keys ambiguous keys x 2 projects, one ownership row per key, 2 x keys groups.
func seedBoundBlock(t *testing.T, ctx context.Context, f *ownershipFixture, keys int) {
	t.Helper()
	early := ownershipLaterAssertion.Add(1 * time.Hour)
	block := ownershipLaterAssertion.Add(24 * time.Hour)
	for _, half := range []string{"A", "B"} {
		mustExec(t, ctx, f.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at)
SELECT concat('P-BOUND-`+half+`-', repeat('x', 232), toString(number)), ?, 'github', concat('BOUND-', toString(number)), 'bulk', 1, 'started', '', ?
FROM numbers(?)`, f.orgID, early, uint64(keys))
	}
	mustExec(t, ctx, f.direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at)
SELECT ?, 'github', 'TEAM-GITHUB', concat('P-BOUND-A-', repeat('x', 232), toString(number)), concat('BOUND-', toString(number)), 'native', ?, NULL, ?
FROM numbers(?)`, f.orgID, block, block, uint64(keys))
}

// C2 on a randomized fixture: several rounds of new projects (keys shared,
// keys equal to ids, ids with ':' and '%'), ownership rows (id-shaped,
// key-shaped, unresolved, conflicting, open and closed), team activity flips
// and deleted rows; after each round the walk equals the oracle.
func subOwnershipStoredReaderEqualsTheOracleOnARandomizedFixture(t *testing.T, ctx context.Context, f *ownershipFixture) {
	seed := time.Now().UnixNano()
	t.Logf("seed %d", seed)
	r := rand.New(rand.NewSource(seed))
	providers := []string{"github", "gitlab"}
	type team struct{ id, provider string }
	teams := []team{{"T-R0", "github"}, {"T-R1", "github"}, {"T-R2", "gitlab"}, {"T-R3", "gitlab"}}
	stamp := ownershipLaterAssertion
	tick := func() time.Time { stamp = stamp.Add(time.Second); return stamp }
	setTeam := func(tm team, active uint8) {
		mustExec(t, ctx, f.direct, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, native_team_key, project_keys, is_active) VALUES (?, ?, '', ?, ?, ?, ?, [], ?)`,
			tm.id, tm.id, tick(), f.orgID, tm.provider, tm.id, active)
	}
	for _, tm := range teams {
		setTeam(tm, 1)
	}
	type project struct{ id, provider, key string }
	var projects []project
	var ownershipRefs []string
	var total devhealthsource.OwnershipCandidateCounts
	for round := 0; round < 4; round++ {
		for i := 0; i < 12; i++ {
			p := project{provider: providers[r.Intn(2)]}
			switch r.Intn(3) {
			case 0:
				p.id = fmt.Sprintf("RP-%d-%d", round, i)
			case 1:
				p.id = fmt.Sprintf("%s:gitlab:%d", f.orgID, round*100+i)
			default:
				p.id = fmt.Sprintf("RP%%3A%d:%d", round, i)
			}
			switch r.Intn(4) {
			case 0:
				p.key = ""
			case 1:
				p.key = p.id
			default:
				p.key = fmt.Sprintf("RK-%d", r.Intn(6))
			}
			var key any = p.key
			if p.key == "" && r.Intn(2) == 0 {
				key = nil
			}
			mustExec(t, ctx, f.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, ?, ?, ?, 1, 'started', '', ?)`,
				p.id, f.orgID, p.provider, key, p.id, tick())
			projects = append(projects, p)
		}
		for i := 0; i < 16; i++ {
			tm := teams[r.Intn(len(teams))]
			provider := providers[r.Intn(2)]
			pick := projects[r.Intn(len(projects))]
			other := projects[r.Intn(len(projects))]
			ref, key := pick.id, ""
			switch r.Intn(5) {
			case 0:
				ref, key = pick.key, pick.key
			case 1:
				key = other.key
			case 2:
				ref = fmt.Sprintf("NOPE-%d", r.Intn(4))
			case 3:
				key = pick.key
			}
			var keyValue any = key
			if key == "" {
				keyValue = nil
			}
			source := []string{"native", "manual"}[r.Intn(2)]
			validFrom := ownershipFirstSeen.Add(time.Duration(r.Intn(100)) * 24 * time.Hour)
			var validTo any
			if r.Intn(3) == 0 {
				validTo = validFrom.Add(time.Duration(1+r.Intn(30)) * 24 * time.Hour)
			}
			mustExec(t, ctx, f.direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				f.orgID, provider, tm.id, ref, keyValue, source, validFrom, validTo, tick())
			ownershipRefs = append(ownershipRefs, ref)
		}
		if r.Intn(2) == 0 {
			setTeam(teams[r.Intn(len(teams))], uint8(r.Intn(2)))
		}
		if round > 0 && r.Intn(2) == 0 {
			mustExec(t, ctx, f.direct, `ALTER TABLE team_project_ownership DELETE WHERE org_id = ? AND project_id = ? SETTINGS mutations_sync = 2`,
				f.orgID, ownershipRefs[r.Intn(len(ownershipRefs))])
		}
		counts := assertOwnershipMatchesOracle(t, ctx, f)
		t.Logf("round %d: %d edges, %d tombstones", round, counts.Relationships, counts.Tombstones)
		total.Relationships += counts.Relationships
		total.Tombstones += counts.Tombstones
	}
	if total.Relationships == 0 || total.Tombstones == 0 {
		t.Errorf("the randomized fixture compared %d edges and %d tombstones; it must compare both", total.Relationships, total.Tombstones)
	}
}

// C3: a group changed twice between two reads yields its latest state only;
// a vanished group yields nothing; a group that comes back is emitted again;
// a from-scratch walk leaves the same edges as the incremental one.
func subOwnershipGroupChangesYieldTheLatestState(t *testing.T, ctx context.Context, f *ownershipFixture) {
	if _, err := f.source.WithWindowPassInterval(1000 * time.Hour); err != nil {
		t.Fatal(err)
	}
	seedOwned := func(projectID, key string, validTo any, updated time.Time) {
		mustExec(t, ctx, f.direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?, 'github', 'TEAM-GITHUB', ?, ?, 'native', ?, ?, ?)`,
			f.orgID, projectID, key, ownershipFirstSeen, validTo, updated)
	}
	for _, p := range []string{"PROJ-C3-G", "PROJ-C3-H"} {
		mustExec(t, ctx, f.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, 'github', ?, ?, 1, 'started', '', ?)`,
			p, f.orgID, p+"-KEY", p, ownershipLaterAssertion)
		seedOwned(p, p+"-KEY", nil, ownershipLaterAssertion)
	}
	gID := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-C3-G", "TEAM-GITHUB", "native")
	hID := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-C3-H", "TEAM-GITHUB", "native")
	graph := map[string]contractsv1.ContextFabricRelationshipProjection{}
	apply := func(batches []contextfabric.ProjectionBatch) {
		for _, b := range batches {
			for _, r := range projectTeamEdges(b) {
				graph[r.RelationshipID] = r
			}
			for _, tomb := range b.Tombstones {
				delete(graph, tomb.CanonicalID)
			}
		}
	}
	emitted := func(batches []contextfabric.ProjectionBatch, id string) []contractsv1.ContextFabricRelationshipProjection {
		var out []contractsv1.ContextFabricRelationshipProjection
		for _, b := range batches {
			for _, r := range projectTeamEdges(b) {
				if r.RelationshipID == id {
					out = append(out, r)
				}
			}
		}
		return out
	}
	f.syncStoredGroups(t, ctx)
	cursor, first := ownershipTicks(t, ctx, f, "")
	apply(first)
	if _, ok := graph[gID]; !ok {
		t.Fatalf("the first walk did not emit %s", gID)
	}

	// Two changes of G, each stored before the next read.
	closeOne, closeTwo := ownershipLatestClose, ownershipStaleFarFutureClose
	seedOwned("PROJ-C3-G", "PROJ-C3-G-KEY", closeOne, ownershipLaterAssertion.Add(time.Hour))
	f.syncStoredGroups(t, ctx)
	seedOwned("PROJ-C3-G", "PROJ-C3-G-KEY", closeTwo, ownershipLaterAssertion.Add(2*time.Hour))
	f.syncStoredGroups(t, ctx)
	cursor, changed := ownershipTicks(t, ctx, f, cursor)
	apply(changed)
	gEmitted := emitted(changed, gID)
	if len(gEmitted) == 0 {
		t.Fatalf("a group changed twice was not emitted")
	}
	for _, r := range gEmitted {
		if r.ValidTo == nil || !r.ValidTo.Equal(closeTwo) {
			t.Errorf("a group changed twice was emitted with valid_to %v, want only its latest %v", r.ValidTo, closeTwo)
		}
	}

	// H vanishes: nothing is emitted for it.
	mustExec(t, ctx, f.direct, `ALTER TABLE team_project_ownership DELETE WHERE org_id = ? AND project_id = 'PROJ-C3-H' SETTINGS mutations_sync = 2`, f.orgID)
	f.syncStoredGroups(t, ctx)
	cursor, vanished := ownershipTicks(t, ctx, f, cursor)
	apply(vanished)
	for _, b := range vanished {
		if hasRelationship(b, hID) {
			t.Errorf("a vanished group was emitted as an edge")
		}
		for _, tomb := range b.Tombstones {
			if tomb.CanonicalID == hID {
				t.Errorf("a vanished group was emitted as a tombstone")
			}
		}
	}

	// H comes back: it is emitted again.
	seedOwned("PROJ-C3-H", "PROJ-C3-H-KEY", nil, ownershipLaterAssertion.Add(3*time.Hour))
	f.syncStoredGroups(t, ctx)
	_, back := ownershipTicks(t, ctx, f, cursor)
	apply(back)
	if len(emitted(back, hID)) == 0 {
		t.Errorf("a group that came back was not emitted again")
	}

	scratchSource, err := devhealthsource.NewTeamsProjectsSource(f.client, true)
	if err != nil {
		t.Fatal(err)
	}
	scratch := &ownershipFixture{orgID: f.orgID, source: scratchSource, direct: f.direct, query: f.query, client: f.client}
	_, fresh := ownershipTicks(t, ctx, scratch, "")
	rebuilt := graph
	graph = map[string]contractsv1.ContextFabricRelationshipProjection{}
	apply(fresh)
	if !reflect.DeepEqual(graph, rebuilt) {
		t.Errorf("a from-scratch walk left %d project -> team edges, the incremental walk %d, and they differ", len(graph), len(rebuilt))
	}
}

// C4: a refresh stamps its rows when it starts and they become visible when
// it commits, so a row can land below a cursor that has already passed its
// stamp. The fixture lands one: the group's source rows carry stamps far
// behind the cursor (no paged or overlap read of the source tables reaches
// them) and its stored row is stamped 290 s below the cursor, inside the
// overlap. The next window pass must emit its edge.
func subOwnershipLateCommittedGroupIsReadThroughTheOverlap(t *testing.T, ctx context.Context, f *ownershipFixture) {
	clock := time.Now().UTC()
	f.source.SetClockForTest(func() time.Time { return clock })
	f.syncStoredGroups(t, ctx)
	c0, _ := ownershipTicks(t, ctx, f, "")
	frontier := decodeCursorPosition(t, c0).Since
	if frontier.IsZero() {
		t.Fatalf("the first walk left no frontier")
	}
	late := frontier.Add(-290 * time.Second)
	old := frontier.Add(-2 * time.Hour)
	refreshSelect := strings.SplitN(devhealthschema.OwnershipGroupsRefreshViewDDL, "\nAS ", 2)[1]
	const columns = "org_id, row_key, project_id, team_id, source_name, provider, first_valid_from, latest_is_open, latest_valid_to, observed_at, edge_suppressed, conflict_identities, conflicting_identity_present, content_hash"
	projectID, teamID := "PROJ-C4-LATE", "TEAM-C4"
	rowKey := identity.JoinSegments("gitea-c4", projectID, teamID, "native")
	landWithoutRefresh(t, ctx, f.direct, func() {
		mustExec(t, ctx, f.direct, `INSERT INTO teams (id, name, description, updated_at, last_synced, org_id, provider, native_team_key, project_keys, is_active) VALUES (?, ?, '', ?, ?, ?, 'gitea-c4', ?, [], 1)`,
			teamID, teamID, old, old, f.orgID, teamID)
		mustExec(t, ctx, f.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at, last_synced) VALUES (?, ?, 'gitea-c4', ?, ?, 1, 'started', '', ?, ?)`,
			projectID, f.orgID, projectID+"-KEY", projectID, old, old)
		mustExec(t, ctx, f.direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at, last_synced) VALUES (?, 'gitea-c4', ?, ?, ?, 'native', ?, NULL, ?, ?)`,
			f.orgID, teamID, projectID, projectID+"-KEY", ownershipFirstSeen, old, old)
		mustExec(t, ctx, f.direct, `INSERT INTO project_team_ownership_groups_log (`+columns+`, stored_at, is_deleted)
SELECT `+columns+`, fromUnixTimestamp64Micro(toInt64(?), 'UTC'), is_deleted FROM (`+refreshSelect+`) WHERE org_id = ? AND row_key = ?`, late.UnixMicro(), f.orgID, rowKey)
	})
	var logRows uint64
	var storedUS int64
	if err := f.direct.QueryRow(ctx, `SELECT (SELECT count() FROM project_team_ownership_groups_log WHERE org_id = ? AND row_key = ?),
  (SELECT toUnixTimestamp64Micro(stored_at) FROM project_team_ownership_groups_current FINAL WHERE org_id = ? AND row_key = ?)`, f.orgID, rowKey, f.orgID, rowKey).Scan(&logRows, &storedUS); err != nil {
		t.Fatal(err)
	}
	if logRows != 1 || storedUS != late.UnixMicro() {
		t.Fatalf("the late group was not landed alone: log rows %d (want 1), stored_at %d (want %d)", logRows, storedUS, late.UnixMicro())
	}
	edgeID := devhealthsource.ProjectTeamRelationshipIDForTest(t, "gitea-c4", projectID, teamID, "native")
	clock = clock.Add(devhealthsource.DefaultWindowPassInterval + time.Minute)
	_, after := ownershipTicks(t, ctx, f, c0)
	for _, b := range after {
		if hasRelationship(b, edgeID) {
			return
		}
	}
	t.Errorf("a group stored 290 s below the cursor was never read (%d batches after the cursor passed it)", len(after))
}

// R1: the ownership reconcile pass re-derives every LIVE stored group once
// per period, with no change in the log: an edge the backend lost is written
// again, a deleted group is not, and the cursor does not move.
func subOwnershipReconcileReemitsEveryLiveGroupWithNoLogChange(t *testing.T, ctx context.Context, f *ownershipFixture) {
	clock := time.Now().UTC().Truncate(time.Second)
	f.source.SetClockForTest(func() time.Time { return clock })
	if _, err := f.source.WithWindowPassInterval(1000 * time.Hour); err != nil {
		t.Fatal(err)
	}
	mustExec(t, ctx, f.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES ('PROJ-R1-D', ?, 'github', 'R1-D-KEY', 'deleted', 1, 'started', '', ?)`,
		f.orgID, ownershipLaterAssertion)
	mustExec(t, ctx, f.direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?, 'github', 'TEAM-GITHUB', 'PROJ-R1-D', 'R1-D-KEY', 'native', ?, NULL, ?)`,
		f.orgID, ownershipFirstSeen, ownershipLaterAssertion)
	deletedID := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-R1-D", "TEAM-GITHUB", "native")
	lostID := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-OPEN", "TEAM-GITHUB", "native")
	f.syncStoredGroups(t, ctx)
	c0, _ := ownershipTicks(t, ctx, f, "")
	mustExec(t, ctx, f.direct, `ALTER TABLE team_project_ownership DELETE WHERE org_id = ? AND project_id = 'PROJ-R1-D' SETTINGS mutations_sync = 2`, f.orgID)
	f.syncStoredGroups(t, ctx)
	c1, _ := ownershipTicks(t, ctx, f, c0)
	wantEdges, tombstoneIDs := liveOwnershipIDs(t, ctx, f)
	if !wantEdges[lostID] || wantEdges[deletedID] {
		t.Fatalf("fixture: live edges %v must hold %s and not %s", wantEdges, lostID, deletedID)
	}
	tombstoneIDs[deletedID] = true

	clock = clock.Add(devhealthsource.TeamOwnershipReconcilePeriodForTest)
	_, reconciled := ownershipTicks(t, ctx, f, c1)
	seen := map[string]int{}
	for _, b := range reconciled {
		ids := ownershipItemIDs(b, tombstoneIDs)
		if len(ids) > 0 && keysetPosition(t, b.NextCursor) != keysetPosition(t, c1) {
			t.Errorf("a reconcile batch moved the cursor: %s, want %s", keysetPosition(t, b.NextCursor), keysetPosition(t, c1))
		}
		for _, r := range projectTeamEdges(b) {
			seen[r.RelationshipID]++
		}
		for _, tomb := range b.Tombstones {
			if tomb.CanonicalID == deletedID {
				t.Errorf("the reconcile pass wrote a tombstone for the deleted group")
			}
		}
	}
	if seen[deletedID] > 0 {
		t.Errorf("the reconcile pass re-emitted the deleted group")
	}
	if seen[lostID] == 0 {
		t.Errorf("the edge the backend lost (%s) was not re-emitted by the reconcile pass", lostID)
	}
	reemitted := 0
	for id := range wantEdges {
		switch seen[id] {
		case 0:
		case 1:
			reemitted++
		default:
			t.Errorf("live group %s re-emitted %d times in one pass", id, seen[id])
		}
	}
	if reemitted != len(wantEdges) {
		t.Errorf("the reconcile pass re-emitted %d of %d live groups", reemitted, len(wantEdges))
	}
}

// R2: the ownership reconcile pass walks every page of the stored groups,
// keeps the cursor position, and serves a page whose batch was not applied
// again, unchanged.
func subOwnershipReconcileWalksEveryPageAndRetriesAnUnappliedPage(t *testing.T, ctx context.Context, f *ownershipFixture) {
	clock := time.Now().UTC().Truncate(time.Second)
	f.source.SetClockForTest(func() time.Time { return clock })
	if _, err := f.source.WithWindowPassInterval(1000 * time.Hour); err != nil {
		t.Fatal(err)
	}
	seedOwnedProjectBlock(t, ctx, f, "PROJ-R2-", 250)
	f.syncStoredGroups(t, ctx)
	c0, _ := ownershipTicks(t, ctx, f, "")
	wantEdges, tombstoneIDs := liveOwnershipIDs(t, ctx, f)
	want := len(wantEdges) + len(tombstoneIDs)
	if want <= 200 {
		t.Fatalf("fixture: %d live groups, want more than one reconcile page (200)", want)
	}
	clock = clock.Add(devhealthsource.TeamOwnershipReconcilePeriodForTest)
	next := func(cursor string) (contextfabric.ProjectionBatch, bool) {
		t.Helper()
		batch, ok, err := f.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: f.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		return batch, ok
	}
	cursor := c0
	var one contextfabric.ProjectionBatch
	before := ""
	for tick := 0; tick < 10 && one.BatchID == ""; tick++ {
		batch, ok := next(cursor)
		if !ok {
			break
		}
		if len(ownershipItemIDs(batch, tombstoneIDs)) > 0 {
			one, before = batch, cursor
			break
		}
		cursor = batch.NextCursor
	}
	if one.BatchID == "" {
		t.Fatalf("no ownership reconcile page within 10 ticks after the period elapsed")
	}
	oneIDs := ownershipItemIDs(one, tombstoneIDs)
	if len(oneIDs) != 200 {
		t.Errorf("first ownership reconcile page holds %d groups, want 200", len(oneIDs))
	}
	if keysetPosition(t, one.NextCursor) != keysetPosition(t, c0) || one.NextCursor == before {
		t.Fatalf("an ownership reconcile batch must keep the cursor position and carry its own ack")
	}
	if wrong, ok := next(chaos7246WithAck(t, before, "00ff00ff00ff00ff")); !ok || !reflect.DeepEqual(ownershipItemIDs(wrong, tombstoneIDs), oneIDs) {
		t.Fatalf("a foreign ack advanced the ownership reconcile pass")
	}
	again, ok := next(before)
	if !ok || again.BatchID != one.BatchID || !reflect.DeepEqual(ownershipItemIDs(again, tombstoneIDs), oneIDs) {
		t.Fatalf("an unapplied ownership reconcile page was not served again unchanged")
	}
	two, ok := next(again.NextCursor)
	twoIDs := ownershipItemIDs(two, tombstoneIDs)
	if !ok || len(twoIDs) != want-200 {
		t.Fatalf("second ownership reconcile page: available=%v groups=%d, want %d", ok, len(twoIDs), want-200)
	}
	seen := map[string]bool{}
	for _, id := range append(oneIDs, twoIDs...) {
		if seen[id] {
			t.Fatalf("group %s served twice in one pass", id)
		}
		seen[id] = true
	}
	for id := range wantEdges {
		if !seen[id] {
			t.Errorf("live group %s missing from the pass", id)
		}
	}
	cursor = two.NextCursor
	for call := 0; call < 3; call++ {
		batch, ok := next(cursor)
		if !ok {
			break
		}
		if ids := ownershipItemIDs(batch, tombstoneIDs); len(ids) > 0 {
			t.Fatalf("call %d: a finished ownership reconcile pass served %d more groups", call, len(ids))
		}
		cursor = batch.NextCursor
	}
}

// C6: the source-row citation of a project-team edge carries the STORED
// group's observed_at, the edge's own ObservedAt. A newer project elsewhere in
// the provider does not move it: the per-organization group statement put the
// provider-wide max(provider_updated_at) into observed_at, the stored group
// does not (an accepted answer change of the citation).
func subOwnershipSourceRowCitesTheStoredGroupObservedAt(t *testing.T, ctx context.Context, f *ownershipFixture) {
	newer := ownershipLaterAssertion.Add(30 * 24 * time.Hour)
	mustExec(t, ctx, f.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES ('PROJ-C6-NEWER', ?, 'github', 'C6-NEWER-KEY', 'newer', 1, 'started', '', ?)`,
		f.orgID, newer)
	f.syncStoredGroups(t, ctx)
	edgeID := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-OPEN", "TEAM-GITHUB", "native")
	var edge *contractsv1.ContextFabricRelationshipProjection
	_, batches := ownershipTicks(t, ctx, f, "")
	for _, b := range batches {
		for _, r := range projectTeamEdges(b) {
			if r.RelationshipID == edgeID {
				r := r
				edge = &r
			}
		}
	}
	if edge == nil || len(edge.EvidenceRefIDs) != 1 {
		t.Fatalf("fixture: edge %s with one evidence ref was not projected (%+v)", edgeID, edge)
	}
	var stored time.Time
	if err := f.direct.QueryRow(ctx, `SELECT observed_at FROM project_team_ownership_groups_current FINAL WHERE org_id = ? AND project_id = 'PROJ-OPEN' AND team_id = 'TEAM-GITHUB' AND source_name = 'native' AND provider = 'github' AND is_deleted = 0`, f.orgID).Scan(&stored); err != nil {
		t.Fatalf("read the stored group's observed_at: %v", err)
	}
	if !stored.Before(newer) {
		t.Fatalf("fixture: the stored observed_at %s must be older than the newer project %s", stored, newer)
	}
	rows, err := contextpacket.NewCatalogClickHouseRows(f.client).WithOrganizationRowQuery(devhealthsource.ProjectTeamSourceRowQuery())
	if err != nil {
		t.Fatal(err)
	}
	refs, err := rows.ResolveOrganizationRow(ctx, f.orgID, contextpacket.SourceRowRead{QueryID: contextpacket.ProjectTeamSourceRowQueryID, Locator: edge.EvidenceRefIDs[0]})
	if err != nil {
		t.Fatalf("resolve the edge's source row: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("the edge's source row resolved to %d rows, want 1", len(refs))
	}
	cited := refs[0].Reference.Evidence.ObservedAt
	if !cited.Equal(stored.UTC()) {
		t.Errorf("the source-row citation's observed_at is %s, want the stored group's %s (the provider-wide newest project is %s)", cited, stored.UTC(), newer)
	}
	if !edge.ObservedAt.Equal(stored.UTC()) {
		t.Errorf("the projected edge's ObservedAt is %s, want the stored group's %s", edge.ObservedAt, stored.UTC())
	}
}

// C7: a group changed twice after the cursor has two log rows; only the
// newer is its current row. The page writes the group once, from that row:
// the older row is a progress row, so the walk neither writes the group twice
// nor drops a duplicate of it from a batch.
func subOwnershipSupersededLogRowWritesNothing(t *testing.T, ctx context.Context, f *ownershipFixture) {
	if _, err := f.source.WithWindowPassInterval(1000 * time.Hour); err != nil {
		t.Fatal(err)
	}
	seedOwned := func(validTo any, updated time.Time) {
		mustExec(t, ctx, f.direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?, 'github', 'TEAM-GITHUB', 'PROJ-C7', 'PROJ-C7-KEY', 'native', ?, ?, ?)`,
			f.orgID, ownershipFirstSeen, validTo, updated)
	}
	mustExec(t, ctx, f.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES ('PROJ-C7', ?, 'github', 'PROJ-C7-KEY', 'PROJ-C7', 1, 'started', '', ?)`,
		f.orgID, ownershipLaterAssertion)
	seedOwned(nil, ownershipLaterAssertion)
	f.syncStoredGroups(t, ctx)
	c0, _ := ownershipTicks(t, ctx, f, "")

	seedOwned(ownershipLatestClose, ownershipLaterAssertion.Add(time.Hour))
	f.syncStoredGroups(t, ctx)
	seedOwned(ownershipStaleFarFutureClose, ownershipLaterAssertion.Add(2*time.Hour))
	f.syncStoredGroups(t, ctx)
	rowKey := identity.JoinSegments("github", "PROJ-C7", "TEAM-GITHUB", "native")
	var after uint64
	if err := f.direct.QueryRow(ctx, `SELECT count() FROM project_team_ownership_groups_log WHERE org_id = ? AND row_key = ? AND stored_at > fromUnixTimestamp64Micro(toInt64(?), 'UTC')`,
		f.orgID, rowKey, decodeCursorPosition(t, c0).Since.UnixMicro()).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after < 2 {
		t.Fatalf("premise: the group must have two log rows after the cursor, has %d", after)
	}

	var logs bytes.Buffer
	f.source.WithLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	_, changed := ownershipTicks(t, ctx, f, c0)
	id := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-C7", "TEAM-GITHUB", "native")
	var writes []contractsv1.ContextFabricRelationshipProjection
	for _, b := range changed {
		for _, r := range projectTeamEdges(b) {
			if r.RelationshipID == id {
				writes = append(writes, r)
			}
		}
	}
	if len(writes) != 1 {
		t.Errorf("a group with two log rows after the cursor was written %d times, want once (its current row only)", len(writes))
	}
	for _, r := range writes {
		if r.ValidTo == nil || !r.ValidTo.Equal(ownershipStaleFarFutureClose) {
			t.Errorf("the group was written with valid_to %v, want its latest %v", r.ValidTo, ownershipStaleFarFutureClose)
		}
	}
	if strings.Contains(logs.String(), "duplicate_within_batch") {
		t.Errorf("a batch dropped a duplicate item: the superseded log row was read as live\n%s", logs.String())
	}
}

// C8: a log row whose group has no current row (its copy is not visible yet,
// or was removed) is a progress row: the walk moves past it. More such rows
// than a page holds must not stop the walk before a later group.
func subOwnershipLogRowsWithoutACurrentRowAreProgressRows(t *testing.T, ctx context.Context, f *ownershipFixture) {
	if _, err := f.source.WithWindowPassInterval(1000 * time.Hour); err != nil {
		t.Fatal(err)
	}
	f.syncStoredGroups(t, ctx)
	c0, _ := ownershipTicks(t, ctx, f, "")
	frontier := decodeCursorPosition(t, c0).Since
	const orphans = 401 // two full pages and one row
	mustExec(t, ctx, f.direct, `INSERT INTO project_team_ownership_groups_log (org_id, row_key, project_id, team_id, source_name, provider, first_valid_from, latest_is_open, latest_valid_to, observed_at, edge_suppressed, conflict_identities, conflicting_identity_present, content_hash, stored_at, is_deleted)
SELECT ?, concat('github:PROJ-C8-ORPHAN-', leftPad(toString(number), 4, '0'), ':TEAM-GITHUB:native'), concat('PROJ-C8-ORPHAN-', leftPad(toString(number), 4, '0')), 'TEAM-GITHUB', 'native', 'github', ?, 1, ?, ?, 0, [], 0, number, fromUnixTimestamp64Micro(toInt64(?) + toInt64(number), 'UTC'), 0 FROM numbers(?)`,
		f.orgID, ownershipFirstSeen, ownershipFirstSeen, ownershipLaterAssertion, frontier.UnixMicro()+1, uint64(orphans))
	// The copy view wrote each orphan's current row; remove them. A refresh of
	// another subtest that ran in between saw them live with no source group
	// and stored a deletion row for each (its copy lands in current again), so
	// delete until no refresh is running and none is left.
	for attempt := 0; ; attempt++ {
		mustExec(t, ctx, f.direct, "SYSTEM WAIT VIEW "+ownershipGroupsRefreshView)
		mustExec(t, ctx, f.direct, `ALTER TABLE project_team_ownership_groups_current DELETE WHERE org_id = ? AND startsWith(row_key, 'github:PROJ-C8-ORPHAN-') SETTINGS mutations_sync = 2`, f.orgID)
		mustExec(t, ctx, f.direct, "SYSTEM WAIT VIEW "+ownershipGroupsRefreshView)
		var left uint64
		if err := f.direct.QueryRow(ctx, `SELECT count() FROM project_team_ownership_groups_current WHERE org_id = ? AND startsWith(row_key, 'github:PROJ-C8-ORPHAN-')`, f.orgID).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left == 0 {
			break
		}
		if attempt == 4 {
			t.Fatalf("%d orphan current rows are still there after 5 deletes", left)
		}
	}

	mustExec(t, ctx, f.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES ('PROJ-C8-LATER', ?, 'github', 'PROJ-C8-LATER-KEY', 'PROJ-C8-LATER', 1, 'started', '', ?)`,
		f.orgID, ownershipLaterAssertion)
	mustExec(t, ctx, f.direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?, 'github', 'TEAM-GITHUB', 'PROJ-C8-LATER', 'PROJ-C8-LATER-KEY', 'native', ?, NULL, ?)`,
		f.orgID, ownershipFirstSeen, ownershipLaterAssertion)
	f.syncStoredGroups(t, ctx)
	laterKey := identity.JoinSegments("github", "PROJ-C8-LATER", "TEAM-GITHUB", "native")
	var logged, current uint64
	var lastOrphan, later int64
	if err := f.direct.QueryRow(ctx, `SELECT
  (SELECT count() FROM project_team_ownership_groups_log WHERE org_id = ? AND startsWith(row_key, 'github:PROJ-C8-ORPHAN-')),
  (SELECT count() FROM project_team_ownership_groups_current FINAL WHERE org_id = ? AND startsWith(row_key, 'github:PROJ-C8-ORPHAN-')),
  (SELECT toUnixTimestamp64Micro(max(stored_at)) FROM project_team_ownership_groups_log WHERE org_id = ? AND startsWith(row_key, 'github:PROJ-C8-ORPHAN-')),
  (SELECT toUnixTimestamp64Micro(max(stored_at)) FROM project_team_ownership_groups_log WHERE org_id = ? AND row_key = ?)`,
		f.orgID, f.orgID, f.orgID, f.orgID, laterKey).Scan(&logged, &current, &lastOrphan, &later); err != nil {
		t.Fatal(err)
	}
	if logged < orphans || current != 0 || later <= lastOrphan {
		t.Fatalf("premise: %d orphan log rows (want >= %d), %d with a current row (want 0), later group stored at %d after the last orphan %d", logged, orphans, current, later, lastOrphan)
	}

	cursor, batches := ownershipTicks(t, ctx, f, c0)
	id := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "PROJ-C8-LATER", "TEAM-GITHUB", "native")
	found := false
	for _, b := range batches {
		found = found || hasRelationship(b, id)
		for _, r := range projectTeamEdges(b) {
			if strings.Contains(r.From.CanonicalID, "PROJ-C8-ORPHAN-") {
				t.Errorf("a log row without a current row was written: %s", r.RelationshipID)
			}
		}
	}
	if !found {
		t.Errorf("the group stored after %d log rows without a current row was never written: the walk stopped at them", orphans)
	}
	if at := decodeCursorPosition(t, cursor).Since; at.UnixMicro() < later {
		t.Errorf("the walk's cursor %s stands before the later group (%d µs)", at, later)
	}
}
