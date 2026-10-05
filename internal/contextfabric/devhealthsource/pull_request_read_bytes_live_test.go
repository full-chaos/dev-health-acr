package devhealthsource_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// pullRequestReadByteLimit is the max_bytes_to_read the reader runs under in
// this test: below the bytes of every pull request body of the seeded
// organization, above what one page's keys and their granules need. Prod runs
// the projector at 64 MiB (ACR_CLICKHOUSE_MAX_BYTES_TO_READ, a deploy value);
// the seed is scaled to this lower limit, not to prod's.
const pullRequestReadByteLimit uint64 = 16 << 20

// TestLivePullRequestPageReadStaysUnderTheByteLimit drives the pull request
// producer's real statement through the real query client with a
// max_bytes_to_read limit, on a store whose pull request bodies together
// exceed it: the catch-up page after a rebuild (a cursor at zero, every row
// past it) and a steady tick (a cursor near the end). Each page must be read,
// not refused with ClickHouse 307: the page is chosen on narrow columns and
// the wide ones are read for its keys only. The prod fault after the v8
// rebuild was the catch-up shape: every tick failed with 307.
//
// Needs Docker (ClickHouse container). Written to be run by CI or by the lane
// owner; not run in the authoring sandbox.
func TestLivePullRequestPageReadStaysUnderTheByteLimit(t *testing.T) {
	ctx := context.Background()
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
	limit := pullRequestReadByteLimit
	reader, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/" + database, DialTimeout: 10 * time.Second, MaxBytesToRead: &limit,
	})
	if err != nil {
		t.Fatalf("open the limited query client: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("apply rendered schema statement: %v\n%s", err, statement)
		}
	}

	const orgID = "86830000-0000-4000-8000-000000000307"
	const repoID = "86830000-0000-4000-8000-0000000003a0"
	const rows = 20000
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repoID, orgID, "acme/bodies", "github", base); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	// One pull request per number, last_synced rising with the number, each
	// with a 1000-byte body: about 20 MB of bodies, above the limit.
	if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, body, created_at, last_synced)
SELECT toUUID(?), ?, toUInt32(number + 1), concat('PR ', toString(number + 1)), 'open', repeat('b', 1000), toDateTime64(?, 3, 'UTC'), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number)
FROM numbers(?)`, repoID, orgID, base, base, rows); err != nil {
		t.Fatalf("seed pull requests: %v", err)
	}
	if err := direct.Exec(ctx, "OPTIMIZE TABLE git_pull_requests FINAL"); err != nil {
		t.Fatalf("merge the seeded parts: %v", err)
	}

	// The limit bites at this size: reading every body is refused. Without
	// this, a limit the store does not apply would make the test pass for the
	// wrong reason.
	if err := readEveryBody(ctx, reader, orgID); !isTooManyBytes(err) {
		t.Fatalf("reading every body under the %d-byte limit returned %v, want ClickHouse 307: the limit does not bite at this size", limit, err)
	}

	for name, c := range map[string]struct {
		since time.Time
		after string
	}{
		"catch-up after a rebuild": {},
		"steady tick":              {since: base.Add(time.Duration(rows-150) * time.Second), after: ""},
	} {
		got, truncated, err := devhealthsource.ReadPullRequestPageForTest(ctx, reader, orgID, c.since, c.after, 200)
		if err != nil {
			t.Errorf("%s: page read failed: %v", name, err)
			continue
		}
		if got == 0 {
			t.Errorf("%s: page read returned no row", name)
		}
		t.Logf("%s: %d rows, truncated %t", name, got, truncated)
	}
}

// readEveryBody reads the body of every pull request of the organization
// through the limited reader.
func readEveryBody(ctx context.Context, reader contextpacket.ClickHouseQueryClient, orgID string) error {
	return drainQuery(ctx, reader, "SELECT sum(length(body)) FROM git_pull_requests WHERE org_id = {org_id:String}", orgID)
}

// drainQuery runs one statement bound to an organization through the limited
// reader and drains its rows, so a limit raised mid-read surfaces too.
func drainQuery(ctx context.Context, reader contextpacket.ClickHouseQueryClient, statement, orgID string) error {
	rows, err := reader.Query(ctx, statement, []contextpacket.ClickHouseBinding{{Name: "org_id", Value: orgID}})
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	return rows.Err()
}

// isTooManyBytes reports ClickHouse 307 (TOO_MANY_BYTES), the max_bytes_to_read refusal.
func isTooManyBytes(err error) bool {
	var exception *clickhousedriver.Exception
	return errors.As(err, &exception) && exception.Code == 307
}
