package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// chdir changes the process working directory. Tests using this must not
// run in parallel with each other or with any test that depends on the
// process cwd (none in this package call t.Parallel()).
func chdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
}

// initTempGitRepo creates a minimal Git repository with one commit and an
// "origin" remote in a fresh temp directory, and chdirs the test process
// into it (restored on cleanup) so sidecar.DiscoverWorkspace's cwd
// fallback path resolves it deterministically.
func initTempGitRepo(t *testing.T, remoteSlug string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("commit", "--allow-empty", "-q", "-m", "initial")
	run("remote", "add", "origin", "https://github.com/"+remoteSlug+".git")

	original, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	t.Cleanup(func() { chdir(t, original) })
	return dir
}

func TestResolveScopeExplicitRequestWinsOverDiscovery(t *testing.T) {
	initTempGitRepo(t, "should-not/be-used")

	req := contractsv1.MCPContextForTaskRequest{
		Goal:       "goal",
		Repository: &contractsv1.MCPRepositoryRef{Slug: "explicit/repo"},
		Scope:      &contractsv1.MCPRequestedScope{Branch: "explicit-branch", CommitSHA: "abc1234"},
	}
	repo, scope, err := resolveScope(context.Background(), nil, req)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Slug != "explicit/repo" {
		t.Fatalf("expected explicit repo slug to win, got: %q", repo.Slug)
	}
	if scope.Branch != "explicit-branch" || scope.CommitSHA != "abc1234" {
		t.Fatalf("expected explicit scope to win, got: %#v", scope)
	}
}

func TestResolveScopeFallsBackToCWDDiscovery(t *testing.T) {
	initTempGitRepo(t, "acme/discovered-repo")

	req := contractsv1.MCPContextForTaskRequest{Goal: "goal"}
	repo, scope, err := resolveScope(context.Background(), nil, req)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Slug != "acme/discovered-repo" {
		t.Fatalf("expected repo discovered from cwd, got: %q", repo.Slug)
	}
	if scope.Branch != "main" {
		t.Fatalf("expected branch discovered from cwd, got: %q", scope.Branch)
	}
	if scope.CommitSHA == "" {
		t.Fatal("expected a discovered commit SHA")
	}
}

func TestResolveScopeExplicitFilesSuppressChangedFileDiscovery(t *testing.T) {
	initTempGitRepo(t, "acme/discovered-repo")

	req := contractsv1.MCPContextForTaskRequest{
		Goal:  "goal",
		Scope: &contractsv1.MCPRequestedScope{Files: []string{"explicit/file.go"}},
	}
	_, scope, err := resolveScope(context.Background(), nil, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope.Files) != 1 || scope.Files[0] != "explicit/file.go" {
		t.Fatalf("expected explicit files to be preserved untouched, got: %#v", scope.Files)
	}
}

func TestResolveScopeToleratesNoWorkspaceAvailable(t *testing.T) {
	dir := t.TempDir() // not a Git repository
	original, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	t.Cleanup(func() { chdir(t, original) })

	req := contractsv1.MCPContextForTaskRequest{Goal: "goal-only, no local workspace"}
	repo, scope, err := resolveScope(context.Background(), nil, req)
	if err != nil {
		t.Fatalf("expected no-workspace-available to be tolerated, got: %v", err)
	}
	if repo.Slug != "" || scope.Branch != "" {
		t.Fatalf("expected empty repo/scope with no discoverable workspace, got repo=%q scope=%#v", repo.Slug, scope)
	}
}

// TestResolveScopePropagatesAsOf locks that the optional scope.as_of
// pin (added to MCPRequestedScope alongside include_changed_files) is
// forwarded unchanged onto the hosted RequestedScope: prior to this test
// resolveScope copied every other MCPRequestedScope field but silently
// dropped as_of.
func TestResolveScopePropagatesAsOf(t *testing.T) {
	asOf := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	req := contractsv1.MCPContextForTaskRequest{
		Goal:       "goal",
		Repository: &contractsv1.MCPRepositoryRef{Slug: "explicit/repo"},
		Scope:      &contractsv1.MCPRequestedScope{Branch: "main", CommitSHA: "abc1234", AsOf: &asOf},
	}
	_, scope, err := resolveScope(context.Background(), nil, req)
	if err != nil {
		t.Fatal(err)
	}
	if scope.AsOf == nil || !scope.AsOf.Equal(asOf) {
		t.Fatalf("expected as_of to be propagated, got: %#v", scope.AsOf)
	}
}

// writeUntrackedFile adds an untracked file to dir so DiscoverWorkspace's
// changed-file listing has something bounded and deterministic to find.
func writeUntrackedFile(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestResolveScopeOmittedIncludeChangedFilesSkipsDiscovery locks the
// tri-state default: a request that never mentions
// scope.include_changed_files must never auto-include locally changed
// files, even though a workspace with changes is discoverable.
func TestResolveScopeOmittedIncludeChangedFilesSkipsDiscovery(t *testing.T) {
	dir := initTempGitRepo(t, "acme/discovered-repo")
	writeUntrackedFile(t, dir)

	req := contractsv1.MCPContextForTaskRequest{Goal: "goal"}
	_, scope, err := resolveScope(context.Background(), nil, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope.Files) != 0 {
		t.Fatalf("expected no auto-included changed files when include_changed_files is omitted, got: %#v", scope.Files)
	}
}

// TestResolveScopeExplicitFalseIncludeChangedFilesSkipsDiscovery locks
// that an explicit false behaves identically to omission: never
// auto-include changed files.
func TestResolveScopeExplicitFalseIncludeChangedFilesSkipsDiscovery(t *testing.T) {
	dir := initTempGitRepo(t, "acme/discovered-repo")
	writeUntrackedFile(t, dir)

	req := contractsv1.MCPContextForTaskRequest{
		Goal:  "goal",
		Scope: &contractsv1.MCPRequestedScope{IncludeChangedFiles: boolPtr(false)},
	}
	_, scope, err := resolveScope(context.Background(), nil, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope.Files) != 0 {
		t.Fatalf("expected no auto-included changed files for explicit false, got: %#v", scope.Files)
	}
}

// TestResolveScopeExplicitTrueIncludeChangedFilesDiscoversFiles locks
// the only case that actually triggers changed-file discovery: an
// explicit true with no client-supplied scope.files, even when the
// repository/branch/commit are all already known (so the general
// explicit-repo+branch+commit short-circuit must not skip the
// changed-file discovery this request opted into), and even though the
// explicit repository slug and the discovered one differ only by case
// (a match, not a mismatch, per normalizeSlugForComparison).
func TestResolveScopeExplicitTrueIncludeChangedFilesDiscoversFiles(t *testing.T) {
	dir := initTempGitRepo(t, "acme/discovered-repo")
	writeUntrackedFile(t, dir)

	req := contractsv1.MCPContextForTaskRequest{
		Goal:       "goal",
		Repository: &contractsv1.MCPRepositoryRef{Slug: "Acme/Discovered-Repo"},
		Scope:      &contractsv1.MCPRequestedScope{Branch: "main", CommitSHA: "abc1234", IncludeChangedFiles: boolPtr(true)},
	}
	_, scope, err := resolveScope(context.Background(), nil, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope.Files) != 1 || scope.Files[0] != "untracked.txt" {
		t.Fatalf("expected discovered changed files, got: %#v", scope.Files)
	}
}

// TestResolveScopeExplicitTrueIncludeChangedFilesYieldsToExplicitFiles
// locks precedence: an explicit scope.files always wins over
// include_changed_files=true, per the same explicit-wins contract as
// every other scope field.
func TestResolveScopeExplicitTrueIncludeChangedFilesYieldsToExplicitFiles(t *testing.T) {
	dir := initTempGitRepo(t, "acme/discovered-repo")
	writeUntrackedFile(t, dir)

	req := contractsv1.MCPContextForTaskRequest{
		Goal:  "goal",
		Scope: &contractsv1.MCPRequestedScope{Files: []string{"explicit/file.go"}, IncludeChangedFiles: boolPtr(true)},
	}
	_, scope, err := resolveScope(context.Background(), nil, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope.Files) != 1 || scope.Files[0] != "explicit/file.go" {
		t.Fatalf("expected explicit files to win over include_changed_files, got: %#v", scope.Files)
	}
}
