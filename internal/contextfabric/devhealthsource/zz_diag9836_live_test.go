package devhealthsource_test

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// TEMPORARY diagnostic (not for merge): per-run memory of the paged statement
// with the thread count, under the projector settings and under max_threads=1.
func TestDiag9836PagedStatementMemoryPerRun(t *testing.T) {
	var paged devhealthsource.ProjectTeamsFragment
	for _, frag := range devhealthsource.ProjectTeamsFragmentsForTest(true) {
		if frag.Name == "paged_statement" {
			paged = frag
		}
	}
	t.Logf("go NumCPU=%d", runtime.NumCPU())
	for i, versions := range []int{1000, 2000} {
		m := freshDatabaseFixture(t, fmt.Sprintf("95460000-0000-4000-8000-0000000009%02d", i), "merged", versions)
		capped := contextpacket.NewMemoryCappedQueryClient(m.query, prodMaxMemoryUsage)
		for _, extra := range []string{"", ", max_threads = 1"} {
			var b strings.Builder
			for run := 0; run < 12; run++ {
				tag := fmt.Sprintf("diag9836-%d-%d-%d-%d", versions, len(extra), run, time.Now().UnixNano())
				rows, err := capped.Query(m.f.ctx, paged.SQL+"\nSETTINGS "+fmt.Sprintf("max_bytes_to_read = %d", prodMaxBytesToRead)+extra+", log_comment = '"+tag+"'", devhealthsource.ProjectTeamsPagedBindingsForTest(m.f.h.orgID, 500))
				if err != nil {
					t.Fatalf("query: %v", err)
				}
				for rows.Next() {
				}
				_ = rows.Close()
				mustExec(t, m.f.ctx, m.f.h.direct, "SYSTEM FLUSH LOGS")
				r, err := m.f.h.direct.Query(m.f.ctx, `SELECT memory_usage, peak_threads_usage, length(thread_ids), query_duration_ms, read_rows, toUInt64(ProfileEvents['SelectedMarks']), Settings['max_threads'] FROM system.query_log WHERE type = 'QueryFinish' AND log_comment = ? ORDER BY event_time DESC LIMIT 1`, tag)
				if err != nil {
					t.Fatalf("query_log: %v", err)
				}
				var mem, peakThreads, readRows, marks, dur uint64
				var threads uint64
				var maxThreads string
				if r.Next() {
					if err := r.Scan(&mem, &peakThreads, &threads, &dur, &readRows, &marks, &maxThreads); err != nil {
						t.Fatalf("scan: %v", err)
					}
				}
				_ = r.Close()
				fmt.Fprintf(&b, "  run=%2d mem=%7.2f MiB peak_threads=%d threads=%d dur_ms=%d read_rows=%d marks=%d max_threads=%q\n", run, float64(mem)/(1<<20), peakThreads, threads, dur, readRows, marks, maxThreads)
			}
			t.Logf("diag9836 versions=%d settings=%q\n%s", versions, extra, b.String())
		}
	}
	t.Error("diag9836: intentional failure so the per-run log is printed")
}
