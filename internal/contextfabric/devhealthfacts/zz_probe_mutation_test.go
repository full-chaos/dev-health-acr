package devhealthfacts

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

const probeLiveTests = "^(TestLiveARepositoryServesTheIssuesLinkedToItsPullRequests|TestLiveARepositoryAppliesTheStatusAndPeriodFilters|TestLiveARestrictedCallerSeesOnlyTheLinkedIssuesItMayRead|TestLiveAnUnknownRepositoryIsNotAnEmptyOne)$"

func TestProbeRepositoryWalkMutationsAreKilled(t *testing.T) {
	if os.Getenv("ACR_PROBE_MUTATION") != "" {
		t.Skip("inside a probe run")
	}
	run := func(id string) (string, error) {
		cmd := exec.Command("go", "test", "-count=1", "-timeout", "10m", "-run", probeLiveTests, ".")
		cmd.Env = append(os.Environ(), "ACR_PROBE_MUTATION="+id)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run(""); err != nil {
		t.Fatalf("baseline live tests failed: %v\n%s", err, out)
	}
	for _, id := range []string{"S1_pr_type", "S2_pr_repository", "S3_issue_type", "S4a_key_first", "S4b_key_second", "S5_reverse_branch", "S6_anchor_exists", "S7_pull_count_type", "S8_status", "S9_time", "S10_authorization"} {
		out, err := run(id)
		var fails []string
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "--- FAIL") || strings.Contains(line, "panic: probe") {
				fails = append(fails, strings.TrimSpace(line))
			}
		}
		if err == nil {
			t.Errorf("PROBE %s SURVIVED", id)
			continue
		}
		t.Logf("PROBE %s KILLED: %s", id, strings.Join(fails, " | "))
	}
}
