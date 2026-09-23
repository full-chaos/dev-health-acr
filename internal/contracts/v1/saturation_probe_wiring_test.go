package v1

import (
	"os"
	"strings"
	"testing"
)

// TestContractTestTargetRunsTheSaturationProbe pins the CHAOS-6360 wiring: the
// saturation probe skips unless ACR_RUN_SATURATION_PROBE=1, so the Makefile's
// contract-test target (run by ci.yml and `make verify`) must set it for that
// exact test, under a timeout above go test's 10m default. Without this the
// skip would read as coverage while the probe never ran anywhere.
func TestContractTestTargetRunsTheSaturationProbe(t *testing.T) {
	raw, err := os.ReadFile("../../../Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "TestMaximalIsSaturated") && strings.HasPrefix(strings.TrimSpace(line), saturationProbeEnv+"=1 go test") {
			found = true
			if !strings.Contains(line, "-timeout 20m") {
				t.Errorf("contract-test saturation line lacks -timeout 20m: %s", line)
			}
		}
	}
	if !found {
		t.Fatalf("no Makefile line runs TestMaximalIsSaturated with %s=1", saturationProbeEnv)
	}
}
