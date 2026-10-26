package emitscan_test

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
)

func TestTheThreeRealTreeCallersShareOneScanAndOneExportLoad(t *testing.T) {
	first, firstSites := scanWithin(t)
	runsAfterFirst, listsAfterFirst := scanRuns.Load(), emitscan.ExportListExecs()
	for i := 0; i < 2; i++ {
		again, _ := scanWithin(t)
		if len(again) != len(first) {
			t.Fatalf("a repeated scan returned %d findings, the first %d", len(again), len(first))
		}
	}
	if got := scanRuns.Load(); got != 1 || runsAfterFirst != 1 {
		t.Errorf("real-tree scans run in this process: %d (after the first call %d), want exactly 1", got, runsAfterFirst)
	}
	if got := emitscan.ExportListExecs(); got != listsAfterFirst {
		t.Errorf("go list -export -deps commands started by the repeated calls: %d, want 0", got-listsAfterFirst)
	}
	if len(firstSites) > 0 && len(firstSites[0].Keys) > 0 {
		firstSites[0].Keys[0] = "mutated"
		_, again := scanWithin(t)
		if again[0].Keys[0] == "mutated" {
			t.Error("a caller changed the result another caller reads")
		}
	}
}
