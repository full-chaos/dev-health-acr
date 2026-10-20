package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

// The client a memory row measures through runs each statement at the
// production thread count, whatever the server's own default (which follows
// the host's cores). The server default is logged so a runner's figure can be
// read beside the hosted one.
func TestMeasuredClientRunsAtTheStatementThreadsOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	raw, _ := sharedClickHouseFixture(t)
	threads := func(client contextpacket.ClickHouseQueryClient) (value string, changed uint8) {
		t.Helper()
		rows, err := client.Query(ctx, "SELECT value, toUInt8(changed) FROM system.settings WHERE name = 'max_threads'", nil)
		if err != nil {
			t.Fatalf("read max_threads: %v", err)
		}
		defer func() { _ = rows.Close() }()
		if !rows.Next() {
			t.Fatal("system.settings has no max_threads row: the measurement did not happen")
		}
		if err := rows.Scan(&value, &changed); err != nil {
			t.Fatalf("scan max_threads: %v", err)
		}
		return value, changed
	}
	serverDefault, _ := threads(raw)
	measured, changed := threads(statementthreads.API(raw))
	t.Logf("STATEMENT-THREADS server default max_threads %s; measured client %s (changed=%d)", serverDefault, measured, changed)
	got, err := statementthreads.ParseThreads(measured)
	if err != nil {
		t.Fatal(err)
	}
	if got != contextpacket.StatementMaxThreads {
		t.Fatalf("the measured client ran at max_threads %s, want %d", measured, contextpacket.StatementMaxThreads)
	}
	// A setting equal to the server default reads back unchanged; only a
	// server whose own default is the statement thread count may show that.
	if defaultThreads, _ := statementthreads.ParseThreads(serverDefault); changed != 1 && defaultThreads != contextpacket.StatementMaxThreads {
		t.Fatalf("the measured client's max_threads %s is not marked as sent (changed=%d) on a server defaulting to %s", measured, changed, serverDefault)
	}
}

// The thread check counts a measured statement that ran at the server default
// on a host whose default is not the statement thread count: query_log's
// Settings map logs no max_threads for it. A statement sent at the statement
// thread count, at another count, or carrying its own SETTINGS max_threads
// logs the value and is not counted, also when the statement is longer than
// query_log keeps and its SETTINGS clause is cut from the logged text.
func TestUnboundThreadsCountsEveryStatementOffTheStatementThreadsOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	raw, direct := sharedClickHouseFixture(t)
	serverDefault, err := statementthreads.ServerDefaultThreads(ctx, direct)
	if err != nil {
		t.Fatal(err)
	}
	unbound := func(client contextpacket.ClickHouseQueryClient, statement, tag string) (uint64, string) {
		t.Helper()
		mark := serverNowMicros(t, ctx, direct)
		rows, err := client.Query(ctx, statement, nil)
		if err != nil {
			t.Fatalf("%s: %v", tag, err)
		}
		for rows.Next() {
		}
		_ = rows.Close()
		if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
			t.Fatal(err)
		}
		var logged, count uint64
		var setting string
		if err := direct.QueryRow(ctx, `SELECT count(), `+statementthreads.UnboundThreadsCount+`, any(Settings['max_threads']) FROM system.query_log WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND position(query, ?) > 0`, mark, tag).Scan(&logged, &count, &setting); err != nil {
			t.Fatalf("%s: read query_log: %v", tag, err)
		}
		if logged != 1 {
			t.Fatalf("%s: %d statements logged, want 1: the measurement did not happen", tag, logged)
		}
		return count, setting
	}
	long := strings.Repeat("1000000, ", 12000) + "1000001"
	at8 := contextpacket.NewMemoryCappedQueryClient(raw, contextpacket.APIStatementMaxMemoryUsage, contextpacket.WithMaxThreads(8))
	for _, c := range []struct {
		name   string
		client contextpacket.ClickHouseQueryClient
		sql    string
		want   uint64
		logged string
	}{
		{"production client", statementthreads.API(raw), "SELECT count() FROM numbers(1000) /* threads-api */", 0, ""},
		{"client at 8 threads", at8, "SELECT count() FROM numbers(1000) /* threads-at8 */", 0, "8"},
		{"statement's own SETTINGS", raw, "SELECT count() FROM numbers(1000) /* threads-inline */ SETTINGS max_threads = 8", 0, "8"},
		{"raw client at the server default", raw, "SELECT count() FROM numbers(1000) /* threads-raw */", map[bool]uint64{true: 0, false: 1}[serverDefault == contextpacket.StatementMaxThreads], ""},
		{"cut statement's own SETTINGS", raw, "SELECT /* threads-cut-inline */ count() FROM numbers(1000) WHERE number NOT IN (" + long + ") SETTINGS max_threads = 8", 0, "8"},
		{"cut statement at the server default", raw, "SELECT /* threads-cut-raw */ count() FROM numbers(1000) WHERE number NOT IN (" + long + ")", map[bool]uint64{true: 0, false: 1}[serverDefault == contextpacket.StatementMaxThreads], ""},
	} {
		tag := c.sql[strings.Index(c.sql, "/* ")+3 : strings.Index(c.sql, " */")]
		got, setting := unbound(c.client, c.sql, tag)
		if got != c.want {
			t.Errorf("%s (server default %d): UnboundThreadsCount = %d, want %d (logged max_threads %q)", c.name, serverDefault, got, c.want, setting)
		}
		if c.logged != "" && setting != c.logged {
			t.Errorf("%s: query_log Settings max_threads = %q, want %q", c.name, setting, c.logged)
		}
		t.Logf("STATEMENT-THREADS %s: logged max_threads %q, unbound %d (server default %d)", c.name, setting, got, serverDefault)
	}
}
