package hosted_test

// CHAOS-4157 fix-forward, base_sha half (2026-08-23): every trial report's
// BaseSHA field used to be a wrapper-script-exported `git rev-parse
// origin/main`, read AT LAUNCH TIME -- a genuine provenance defect caught
// live during a CHAOS-4100 clean re-measure: origin/main moved (3
// unrelated PRs landed) while the run was in flight, so the artifact's own
// BaseSHA named a commit that never actually produced it. Every BaseSHA
// call site (chaos3742_two_turn_confirmation_test.go,
// chaos3884_replay_harness_test.go, chaos3900_w0_window_shadow_test.go,
// chaos3899_d2b_cardinality_test.go) now stamps from the SAME
// requireGitSourceIdentity().commit value SourceCommit already used --
// this test pins that value to the worktree's actual checked-out commit
// (`git rev-parse HEAD`, computed independently here) so a future edit
// cannot silently reintroduce a second, divergent source of truth.

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestRequireGitSourceIdentityCommitMatchesHEAD(t *testing.T) {
	t.Parallel()
	source := requireGitSourceIdentity(t)

	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("independently compute git rev-parse HEAD: %v", err)
	}
	wantHEAD := strings.TrimSpace(string(out))

	if source.commit != wantHEAD {
		t.Fatalf("requireGitSourceIdentity(t).commit = %q, want the worktree's actual HEAD %q -- every report's BaseSHA is stamped from this exact value", source.commit, wantHEAD)
	}
}

// TestUntrackedFilePathsExpandsDirectories pins the codex sol/high
// review's P2 fix directly: a `git status --porcelain`-shaped untracked
// DIRECTORY entry (trailing "/") must expand to its own file list, never
// be handed to os.ReadFile as-is (which requireGitSourceIdentity used to
// do, Fatalf-ing on the first untracked scratch directory it found -- this
// harness's own `.trial-exchange-*` dirs are exactly that shape) and never
// silently drop that directory's content from the provenance digest.
func TestUntrackedFilePathsExpandsDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scratch", "nested"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for rel, content := range map[string]string{
		"top.txt":              "top",
		"scratch/a.txt":        "a",
		"scratch/nested/b.txt": "b",
	} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	got := untrackedFilePaths(t, root, []string{"top.txt", "scratch/"})
	want := []string{"scratch/a.txt", "scratch/nested/b.txt", "top.txt"}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("untrackedFilePaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("untrackedFilePaths = %v, want %v", got, want)
		}
	}
}
