package releasebuild

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// CHAOS-6777: every package binary of one `go test pkg1 pkg2 ...` call shared
// one testcontainers session (keyed on the parent pid), so one reaper; a
// binary that started while an earlier package's reaper was shutting down
// waited 60 s on it and failed the race shard. Each multi-package go test
// recipe must run its binaries through the per-binary session wrapper.
func TestMultiPackageGoTestRecipesRunEachBinaryInItsOwnTestcontainersSession(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(makefile)
	if !strings.Contains(content, "GOTEST_EXEC := -exec $(CURDIR)/scripts/ci/testcontainers-session-exec.sh\n") {
		t.Fatalf("Makefile must define GOTEST_EXEC as the scripts/ci/testcontainers-session-exec.sh -exec wrapper")
	}
	found := 0
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "\t") || !strings.Contains(line, "$(GOTEST_PKGS)") || !strings.Contains(line, "-count=1") {
			continue
		}
		found++
		if !strings.Contains(line, "$(GOTEST_EXEC)") {
			t.Errorf("go test recipe over $(GOTEST_PKGS) lacks $(GOTEST_EXEC); its binaries would share one testcontainers reaper:\n%s", line)
		}
	}
	// test, test-race, test-shuffle-random, test-coverage.
	if found != 4 {
		t.Fatalf("found %d go test recipes over $(GOTEST_PKGS), want 4; update this pin when a target is added or removed", found)
	}
}

func TestTestcontainersSessionExecGivesEachBinaryItsOwnSession(t *testing.T) {
	wrapper, err := filepath.Abs(filepath.Join("..", "..", "scripts", "ci", "testcontainers-session-exec.sh"))
	if err != nil {
		t.Fatal(err)
	}
	session := func(env ...string) string {
		t.Helper()
		cmd := exec.Command(wrapper, "sh", "-c", `printf %s "$TESTCONTAINERS_SESSION_ID"`)
		cmd.Env = append(withoutSession(os.Environ()), env...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("wrapper: %v", err)
		}
		return string(out)
	}
	first, second := session(), session()
	if len(first) != 32 || len(second) != 32 {
		t.Fatalf("session ids %q / %q, want 32 hex chars each", first, second)
	}
	if first == second {
		t.Fatalf("two binaries got the same session %q; they would share one reaper", first)
	}
	if got := session("TESTCONTAINERS_SESSION_ID=caller"); got != "caller" {
		t.Fatalf("caller-set session replaced with %q", got)
	}
}

func withoutSession(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "TESTCONTAINERS_SESSION_ID=") {
			out = append(out, kv)
		}
	}
	return out
}
