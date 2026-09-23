package v1

import (
	"os"
	"strings"
	"testing"
)

// TestContractTestTargetRunsTheHeavyFixtureTests pins the CHAOS-6360 wiring: the
// heavy fixture tests skip unless ACR_RUN_SATURATION_PROBE=1, so the Makefile's
// contract-test target (run by ci.yml and `make verify`) must set it for that
// exact tests, under a timeout above go test's 10m default. Without this the
// skip would read as coverage while the probe never ran anywhere.
func TestContractTestTargetRunsTheHeavyFixtureTests(t *testing.T) {
	raw, err := os.ReadFile("../../../Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	found := false
	target := ""
	for _, line := range strings.Split(string(raw), "\n") {
		// A recipe line is tab-indented; anything else at column 0 that has a
		// colon opens a new target, so the command must sit under contract-test.
		if line != "" && line[0] != '\t' && line[0] != '#' && strings.Contains(line, ":") {
			target = strings.TrimSpace(line[:strings.Index(line, ":")])
		}
		if target != "contract-test" {
			continue
		}
		if strings.Contains(line, "TestMaximalIsSaturated") && strings.Contains(line, "TestEveryBoundIsBreachable") && strings.HasPrefix(strings.TrimSpace(line), saturationProbeEnv+"=1 go test") {
			found = true
			if !strings.Contains(line, "-timeout 20m") {
				t.Errorf("contract-test saturation line lacks -timeout 20m: %s", line)
			}
		}
	}
	if !found {
		t.Fatalf("no Makefile line runs TestMaximalIsSaturated and TestEveryBoundIsBreachable with %s=1", saturationProbeEnv)
	}
}
