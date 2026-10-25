//go:build !race

package emitscan_test

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
)

// The helper's limits are plain-build numbers; the race detector only slows the real-tree scan this check repeats, so it runs
// in the plain unit shards (every PR that touches emitscan runs them) and not in the race job.
func TestKeyScopeHelperLimitsKeepHeadroomOverTheRealTree(t *testing.T) {
	if _, err := emitscan.Scan(benignRepoRoot(t), []string{"internal", "cmd"}); err != nil {
		t.Fatal(err)
	}
	cpu, rssKB, helpers := emitscan.ChildStats()
	limitCPU, limitRSS, _ := emitscan.ChildLimits()
	t.Logf("real tree: %d helpers, largest cpu %s, largest resident set %d MB; limits: cpu %ds, resident set %d MB", helpers, time.Duration(cpu), rssKB>>10, limitCPU, limitRSS>>20)
	if helpers == 0 {
		t.Fatal("no helper ran: the headroom check reads nothing")
	}
	if time.Duration(cpu)*3 > time.Duration(limitCPU)*time.Second {
		t.Errorf("the cpu limit %ds is under 3x the largest helper (%s)", limitCPU, time.Duration(cpu))
	}
	if rssKB<<10*3 > limitRSS {
		t.Errorf("the resident-set limit %d MB is under 3x the largest helper (%d MB)", limitRSS>>20, rssKB>>10)
	}
}
