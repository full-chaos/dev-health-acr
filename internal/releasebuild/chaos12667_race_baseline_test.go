package releasebuild

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestRaceBaselineTokenReachesOnlyTheFetchStep pins CHAOS-12667 (Codex #1967
// r1): the eight devhealthfacts and devhealthsource race jobs read main's last
// race-json artifact with a token that has actions read. The token must reach
// the race-baseline step alone, never the step that runs repository scripts,
// make and the tests, and that step must run gh and nothing of the repository.
func TestRaceBaselineTokenReachesOnlyTheFetchStep(t *testing.T) {
	ci, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(ci)
	jobHeader := regexp.MustCompile(`(?m)^  [A-Za-z0-9_-]+:\s*$`)
	stepStart := regexp.MustCompile(`(?m)^      - `)
	for _, job := range []string{
		"race-devhealthfacts", "race-devhealthfacts-heavy", "race-devhealthfacts-mid", "race-devhealthfacts-fourth",
		"race-devhealthsource", "race-devhealthsource-heavy", "race-devhealthsource-mid", "race-devhealthsource-fourth",
	} {
		start := strings.Index(src, "\n  "+job+":\n")
		if start < 0 {
			t.Fatalf("ci.yml must define job %s", job)
		}
		block := src[start+1:]
		if next := jobHeader.FindStringIndex(block[3:]); next != nil {
			block = block[:next[0]+3]
		}
		if !strings.Contains(block, "      actions: read\n") {
			t.Fatalf("job %s must grant actions: read for the baseline download", job)
		}
		// Split the job into steps; exactly one step may name the token, and
		// it must be the race-baseline step for this job's own artifact.
		idx := stepStart.FindAllStringIndex(block, -1)
		withToken := 0
		for i, at := range idx {
			end := len(block)
			if i+1 < len(idx) {
				end = idx[i+1][0]
			}
			step := block[at[0]:end]
			if !strings.Contains(step, "github.token") && !strings.Contains(step, "GH_TOKEN") {
				continue
			}
			withToken++
			if !strings.Contains(step, "uses: ./.github/actions/race-baseline") || !strings.Contains(step, "artifact: race-json-"+job+"\n") {
				t.Fatalf("job %s passes a token to a step that is not its race-baseline step:\n%s", job, step)
			}
		}
		if withToken != 1 {
			t.Fatalf("job %s: %d steps name the token, want exactly the race-baseline step", job, withToken)
		}
		if !strings.Contains(block, "RACE_BASELINE_JSON: .tmp/race-baseline/race.json") {
			t.Fatalf("job %s must hand the fetched baseline to the guard as RACE_BASELINE_JSON", job)
		}
	}
	action, err := os.ReadFile(filepath.Join("..", "..", ".github", "actions", "race-baseline", "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"scripts/", "make ", "go ", "./", "GH_TOKEN: "} {
		if strings.Contains(string(action), banned) {
			t.Fatalf("the race-baseline action must run gh only, with the token out of its environment; it names %q", banned)
		}
	}
	// Codex #1967 r2: the token leaves the environment before any command
	// runs, and each gh call gets it alone.
	text := string(action)
	unset := strings.Index(text, "unset RACE_BASELINE_TOKEN")
	if unset < 0 || unset > strings.Index(text, "rm -rf") {
		t.Fatal("the race-baseline action must unset RACE_BASELINE_TOKEN before its first command")
	}
	if calls, scoped := strings.Count(text, " gh run "), strings.Count(text, `GH_TOKEN="$token" gh run `); calls != scoped || calls != 2 {
		t.Fatalf("every gh call must carry the token alone: %d gh calls, %d with GH_TOKEN=\"$token\"", calls, scoped)
	}
}
