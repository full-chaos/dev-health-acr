package releasebuild

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestDevhealthfactsRaceSplit_fourJobsAreWired pins that the four
// devhealthfacts race jobs each run through the budget wrapper with their own
// race-split.sh mode, and that the aggregate verify job waits for all of them.
// The one-job-per-test partition itself is proved by race-split.sh
// race-partition inside the jobs.
func TestDevhealthfactsRaceSplit_fourJobsAreWired(t *testing.T) {
	ci, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(ci)
	jobs := []struct{ job, name, mode string }{
		{"race-devhealthfacts", "race (devhealthfacts, isolated scope)", "race-rest"},
		{"race-devhealthfacts-heavy", "race (devhealthfacts heavy, isolated scope)", "race-heavy"},
		{"race-devhealthfacts-mid", "race (devhealthfacts mid, isolated scope)", "race-mid"},
		{"race-devhealthfacts-fourth", "race (devhealthfacts fourth, isolated scope)", "race-fourth"},
	}
	jobHeader := regexp.MustCompile(`(?m)^  [A-Za-z0-9_-]+:\s*$`)
	for _, j := range jobs {
		start := strings.Index(src, "\n  "+j.job+":\n")
		if start < 0 {
			t.Fatalf("ci.yml must define job %s", j.job)
		}
		block := src[start+1:]
		if next := jobHeader.FindStringIndex(block[3:]); next != nil {
			block = block[:next[0]+3]
		}
		for _, want := range []string{
			"name: " + j.name,
			"needs: mirror-preflight",
			"race-split.sh devhealthfacts " + j.mode + ")",
			"GOTEST_TIMEOUT=20m scripts/ci/run-isolated-budget.sh",
			"race-split.sh devhealthfacts verify",
		} {
			if !strings.Contains(block, want) {
				t.Fatalf("job %s must contain %q", j.job, want)
			}
		}
		if !regexp.MustCompile(`(?m)^      - ` + regexp.QuoteMeta(j.job) + `\s*$`).MatchString(src) {
			t.Fatalf("aggregate verify job must list %s in its needs", j.job)
		}
	}
	if n := strings.Count(src, "race-split.sh devhealthfacts race-partition"); n != 1 {
		t.Fatalf("race-partition must run in exactly one job, found %d", n)
	}
}
