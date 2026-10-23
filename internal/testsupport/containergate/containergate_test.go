package containergate

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestDecideCoversEveryCell(t *testing.T) {
	cases := []struct {
		value, actions string
		want           outcome
	}{
		{"1", "", run}, {"1", "true", run},
		{"0", "", skip}, {"0", "true", skip},
		{"", "", skip}, {"", "false", skip}, {"", "true", fail},
		{"yes", "", fail}, {"true", "true", fail}, {"2", "", fail}, {" 1", "", fail},
	}
	for _, tc := range cases {
		if got := decide(tc.value, tc.actions); got != tc.want {
			t.Errorf("decide(%q, %q) = %d, want %d", tc.value, tc.actions, got, tc.want)
		}
	}
}

const probeMarkerEnv = "ACR_CONTAINERGATE_PROBE_MARKER"

func TestProbeChild(t *testing.T) {
	marker := os.Getenv(probeMarkerEnv)
	if marker == "" {
		t.Skip("child of TestRequireInAChildProcess")
	}
	Require(t)
	if err := os.WriteFile(marker, []byte("ran"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRequireInAChildProcess(t *testing.T) {
	cases := []struct {
		name       string
		env        []string
		wantMarker bool
		wantFail   bool
		wantSkip   bool
	}{
		{"unset", nil, false, false, true},
		{"zero", []string{Env + "=0"}, false, false, true},
		{"one", []string{Env + "=1"}, true, false, false},
		{"zero on github actions", []string{Env + "=0", "GITHUB_ACTIONS=true"}, false, false, true},
		{"unset on github actions", []string{"GITHUB_ACTIONS=true"}, false, true, false},
		{"junk", []string{Env + "=yes"}, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			marker := t.TempDir() + "/marker"
			cmd := exec.Command(os.Args[0], "-test.run=^TestProbeChild$", "-test.v", "-test.count=1")
			cmd.Env = append(cleanEnv(), probeMarkerEnv+"="+marker)
			cmd.Env = append(cmd.Env, tc.env...)
			out, err := cmd.CombinedOutput()
			if failed := err != nil; failed != tc.wantFail {
				t.Fatalf("child failed=%v, want %v:\n%s", failed, tc.wantFail, out)
			}
			if skipped := strings.Contains(string(out), "--- SKIP: TestProbeChild"); skipped != tc.wantSkip {
				t.Fatalf("child skipped=%v, want %v:\n%s", skipped, tc.wantSkip, out)
			}
			_, statErr := os.Stat(marker)
			if ran := statErr == nil; ran != tc.wantMarker {
				t.Fatalf("child ran past Require=%v, want %v:\n%s", ran, tc.wantMarker, out)
			}
		})
	}
}

func cleanEnv() []string {
	var kept []string
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, Env+"=") || strings.HasPrefix(entry, "GITHUB_ACTIONS=") || strings.HasPrefix(entry, probeMarkerEnv+"=") {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}
