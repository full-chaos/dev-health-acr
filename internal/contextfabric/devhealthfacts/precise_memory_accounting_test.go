package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/testcontainers/testcontainers-go"
)

// Precise memory accounting for this package's test ClickHouse servers.
//
// ClickHouse adds each thread's allocations to a query's memory tracker in
// steps of max_untracked_memory (4 MiB by default). So
// system.query_log.memory_usage, the peak every memory guard in this package
// reads, is off by up to one step per thread, and by a different amount on
// each run. A guard that keeps the smallest of several runs then picks the most
// under-reported one. Hosted probe runs 41196980097 and 41206439488 measured
// projectLinkedRepositories' min-of-three spread at up to 4,272,370 bytes on
// the default, and at most 8,632 bytes with this profile. The ScopeExpander
// failure on main run 41143459210 read 4,939,038 bytes at the smaller size,
// where precise accounting reads 7,715,073 bytes.
//
// Both servers (the shared container and the dedicated one) run every query
// with max_untracked_memory = 1, so the tracker sees each allocation.
// Production keeps ClickHouse's default. Precise accounting reads higher peaks
// than the default, so the ceilings in this package are conservative for
// production.

// preciseMemoryAccountingProfile is the users.d file both servers load.
const preciseMemoryAccountingProfile = `<clickhouse><profiles><default><max_untracked_memory>1</max_untracked_memory></default></profiles></clickhouse>`

// preciseMemoryAccountingFiles is the container file that installs the
// profile.
func preciseMemoryAccountingFiles() []testcontainers.ContainerFile {
	return []testcontainers.ContainerFile{{
		Reader:            strings.NewReader(preciseMemoryAccountingProfile),
		ContainerFilePath: "/etc/clickhouse-server/users.d/zz-precise-memory-accounting.xml",
		FileMode:          0o644,
	}}
}

// verifyPreciseMemoryAccounting returns an error unless the server really runs
// with max_untracked_memory = 1. A server that lost the profile would measure
// in 4 MiB steps again, and every memory guard would compare noise without
// failing, so the caller fails the test.
func verifyPreciseMemoryAccounting(ctx context.Context, direct clickhousedriver.Conn) error {
	var effective string
	if err := direct.QueryRow(ctx, `SELECT toString(getSetting('max_untracked_memory'))`).Scan(&effective); err != nil {
		return fmt.Errorf("read max_untracked_memory: %w", err)
	}
	if effective != "1" {
		return fmt.Errorf("the test ClickHouse server runs max_untracked_memory = %s, want 1 (CHAOS-12168): query_log memory peaks would be off by up to one 4 MiB step per thread", effective)
	}
	return nil
}
