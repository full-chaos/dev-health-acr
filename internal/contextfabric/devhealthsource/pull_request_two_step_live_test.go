package devhealthsource_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

const twoStepOrganization = "86830000-0000-4000-8000-0000000003c1"

// newPullRequestStore starts a per-test database with the production schema
// and one organization's pull requests: rows of 20 repositories interleaved
// in ingest order, each with a body of bodyBytes. limit is the reader's
// max_bytes_to_read; nil leaves the client default.
func newPullRequestStore(t *testing.T, ctx context.Context, rows, bodyBytes int, limit *uint64) (*runtimeclickhouse.Client, clickhousedriver.Conn) {
	t.Helper()
	addr, admin := sharedDevHealthClickHouse(t)
	database := fmt.Sprintf("t%d", perTestDatabaseSeq.Add(1))
	if err := admin.Exec(ctx, "CREATE DATABASE "+database); err != nil {
		t.Fatalf("create per-test database %s: %v", database, err)
	}
	t.Cleanup(func() { _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+database+" SYNC") })
	direct, err := clickhousedriver.Open(&clickhousedriver.Options{
		Addr: []string{addr}, Auth: clickhousedriver.Auth{Database: database, Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open native ClickHouse connection: %v", err)
	}
	t.Cleanup(func() { _ = direct.Close() })
	reader, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/" + database, DialTimeout: 10 * time.Second, MaxBytesToRead: limit,
	})
	if err != nil {
		t.Fatalf("open the query client: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("apply rendered schema statement: %v\n%s", err, statement)
		}
	}
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	repoIDs := make([]string, 20)
	for i := range repoIDs {
		repoIDs[i] = fmt.Sprintf("86830000-0000-4000-8000-%012d", 800+i)
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, created_at, last_synced) VALUES (?, ?, ?, 'github', ?, ?)`,
			repoIDs[i], twoStepOrganization, fmt.Sprintf("acme/s%02d", i), base, base); err != nil {
			t.Fatalf("seed repo %d: %v", i, err)
		}
	}
	repoArray := "[toUUID('" + strings.Join(repoIDs, "'), toUUID('") + "')]"
	if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, body, created_at, last_synced, head_branch)
SELECT arrayElement(%s, toUInt32(number %% 20) + 1), ?, toUInt32(intDiv(number, 20) + 1), concat('PR ', toString(number)), 'open', repeat('b', %d),
       toDateTime64(?, 3, 'UTC'), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number), 'main'
FROM numbers(%d)`, repoArray, bodyBytes, rows), twoStepOrganization, base, base); err != nil {
		t.Fatalf("seed pull requests: %v", err)
	}
	return reader, direct
}

// resyncBetweenTheReads writes a new version of the first row a wide read
// names, after the page read chose that row's version and before the wide
// read reads it: the race the two reads open.
type resyncBetweenTheReads struct {
	contextpacket.ClickHouseQueryClient
	t        *testing.T
	direct   clickhousedriver.Conn
	once     sync.Once
	resynced string
}

func (r *resyncBetweenTheReads) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if strings.Contains(statement, "AS wide_repo_id") {
		r.once.Do(func() {
			bound := map[string]any{}
			for _, b := range bindings {
				bound[b.Name] = b.Value
			}
			repo, number := bound["r0"].(string), bound["n0"].(uint32)
			if err := r.direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, body, created_at, last_synced, head_branch)
SELECT repo_id, org_id, number, title, 'merged', body, created_at, now64(3), head_branch
FROM git_pull_requests FINAL WHERE org_id = ? AND repo_id = toUUID(?) AND number = ?`, twoStepOrganization, repo, number); err != nil {
				r.t.Fatalf("resync a row between the reads: %v", err)
			}
			r.resynced = fmt.Sprintf("pull_request:%s:%d", repo, number)
		})
	}
	return r.ClickHouseQueryClient.Query(ctx, statement, bindings)
}

// TestLivePullRequestResyncedBetweenTheTwoReadsIsProjectedOnceWithItsNewestVersion:
// a row synced again between the page read (which chose its old version) and
// the wide read (which names that version) is not in that page, and the
// cursor does not pass it: its new version has a later ingest time than
// every row the page read, so a later page admits it. Draining the source
// projects every pull request exactly once, the re-synced one with its
// newest version.
//
// Needs Docker (ClickHouse container). Written to be run by CI or by the lane
// owner; not run in the authoring sandbox.
func TestLivePullRequestResyncedBetweenTheTwoReadsIsProjectedOnceWithItsNewestVersion(t *testing.T) {
	ctx := context.Background()
	const rows = 600
	reader, direct := newPullRequestStore(t, ctx, rows, 200, nil)
	racing := &resyncBetweenTheReads{ClickHouseQueryClient: reader, t: t, direct: direct}
	source, err := devhealthsource.NewClickHouseProjectionSource(racing)
	if err != nil {
		t.Fatal(err)
	}
	emitted := map[string]int{}
	state := map[string]string{}
	cursor := ""
	for page := 0; page < 200; page++ {
		batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: twoStepOrganization, Source: devhealthsource.SourceName, Cursor: cursor})
		if err != nil {
			t.Fatalf("batch %d: %v", page, err)
		}
		if !available {
			break
		}
		for _, entity := range batch.Entities {
			if entity.Subject.Kind != contextfabric.SubjectPullRequest {
				continue
			}
			emitted[entity.Subject.CanonicalID]++
			if s := entity.Properties["state"].String; s != nil {
				state[entity.Subject.CanonicalID] = *s
			}
		}
		cursor = batch.NextCursor
	}
	if racing.resynced == "" {
		t.Fatal("no wide read ran: the race was not exercised")
	}
	if len(emitted) != rows {
		t.Fatalf("%d pull requests projected, want all %d", len(emitted), rows)
	}
	for id, n := range emitted {
		if n != 1 {
			t.Errorf("%s projected %d times, want once", id, n)
		}
	}
	if got := state[racing.resynced]; got != "merged" {
		t.Fatalf("the row re-synced between the reads ends with state %q, want its newest version (merged)", got)
	}
}

// countingClient counts the statements a reader sends.
type countingClient struct {
	contextpacket.ClickHouseQueryClient
	statements int
}

func (c *countingClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.statements++
	return c.ClickHouseQueryClient.Query(ctx, statement, bindings)
}

// TestLivePullRequestReadCostAgainstMainsShape drains every pull request page
// of a store main's single statement can read (under the client's default
// limit) through main's shape and through the two-step read, and reports the
// statements each sends and the wall time each takes. Both must read every
// row. The two-step read sends one page read and ceil(page/n) wide reads per
// page, n derived from the byte limit (5 under the 64 MiB default).
//
// Needs Docker (ClickHouse container). Written to be run by CI or by the lane
// owner; not run in the authoring sandbox.
func TestLivePullRequestReadCostAgainstMainsShape(t *testing.T) {
	ctx := context.Background()
	const rows = 3000
	reader, _ := newPullRequestStore(t, ctx, rows, 1000, nil)
	for _, shape := range []struct {
		name   string
		legacy bool
	}{{"main's single statement", true}, {"two-step read", false}} {
		counter := &countingClient{ClickHouseQueryClient: reader}
		started := time.Now()
		got, err := devhealthsource.DrainPullRequestPagesForTest(ctx, counter, twoStepOrganization, shape.legacy, 200)
		elapsed := time.Since(started)
		if err != nil {
			t.Fatalf("%s: %v", shape.name, err)
		}
		if got != rows {
			t.Errorf("%s read %d rows, want %d", shape.name, got, rows)
		}
		t.Logf("%s: %d rows, %d statements, %s", shape.name, got, counter.statements, elapsed.Round(time.Millisecond))
	}
}
