//go:build !race

package emitscan_test

import (
	"runtime"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
)

// helperNanosPerNode is the helper CPU per syntax node of the largest real-tree package on a quiet host: 975 ms / 266896 nodes
// = 3653 ns, measured 2026-10-25 and rounded up. CPU time of the same work grows several times on a loaded host, so the
// headroom is asserted on the node count and the time stays in the log.
const helperNanosPerNode = 3700

// The helper's limits are plain-build numbers; the race detector only slows the real-tree scan this check repeats, so it runs
// in the plain unit shards (every PR that touches emitscan runs them) and not in the race job.
func TestKeyScopeHelperLimitsKeepHeadroomOverTheRealTree(t *testing.T) {
	if _, err := emitscan.Scan(benignRepoRoot(t), []string{"internal", "cmd"}); err != nil {
		t.Fatal(err)
	}
	cpu, rssKB, helpers := emitscan.ChildStats()
	limitCPU, limitRSS, _ := emitscan.ChildLimits()
	t.Logf("real tree: %d helpers, largest cpu %s (log only), largest package %d nodes, largest resident set %d MB; limits: cpu %ds, resident set %d MB", helpers, time.Duration(cpu), emitscan.ChildMaxNodes(), rssKB>>10, limitCPU, limitRSS>>20)
	if helpers == 0 {
		t.Fatal("no helper ran: the headroom check reads nothing")
	}
	nodes := emitscan.ChildMaxNodes()
	if ceiling := int64(limitCPU) * int64(time.Second) / 3 / helperNanosPerNode; nodes > ceiling {
		t.Errorf("the cpu limit %ds is under 3x the largest package: %d syntax nodes, at most %d fit", limitCPU, nodes, ceiling)
	}
	// CHAOS-12747: the resident set is read from /proc/<pid>/status, which darwin lacks, so there the reading is 0 and the
	// headroom check measured nothing. It is its own subtest: skipped, with the reason, on darwin; on Linux a run that read no
	// resident set fails, since a measurement that did not happen is not a pass.
	t.Run("resident set headroom", func(t *testing.T) {
		if runtime.GOOS == "darwin" {
			t.Skip("darwin: the helper's resident set is read from /proc/<pid>/status, which darwin lacks, so its headroom is not measured; Linux CI measures it (CHAOS-12747)")
		}
		if rssKB <= 0 {
			t.Fatalf("no helper's resident set was read (%d KB over %d helpers): the resident-set headroom check measured nothing", rssKB, helpers)
		}
		if rssKB<<10*3 > limitRSS {
			t.Errorf("the resident-set limit %d MB is under 3x the largest helper (%d MB)", limitRSS>>20, rssKB>>10)
		}
	})
}
