package mcp

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// initTempGitRepoNoRemote is initTempGitRepo without a configured remote,
// for tests that need a discoverable Git workspace with no repository
// identity at all (info.Remote == nil).
func initTempGitRepoNoRemote(t *testing.T) string {
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

	original, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	t.Cleanup(func() { chdir(t, original) })
	return dir
}

// TestResolveScopePartialExplicitFillsOnlyMissingFieldsWhenRepoMatches
// locks the enrichment happy path: when the caller's explicit repository
// slug matches the locally discovered workspace's identity (here, only
// up to case and incidental whitespace -- "ACME/Discovered-Repo" versus
// the fixture's "acme/discovered-repo" remote), the still-missing branch
// is filled from discovery exactly as before this fix.
func TestResolveScopePartialExplicitFillsOnlyMissingFieldsWhenRepoMatches(t *testing.T) {
	initTempGitRepo(t, "acme/discovered-repo")

	req := contractsv1.MCPContextForTaskRequest{
		Goal:       "goal",
		Repository: &contractsv1.MCPRepositoryRef{Slug: "ACME/Discovered-Repo"},
	}
	repo, scope, err := resolveScope(context.Background(), nil, req)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Slug != "ACME/Discovered-Repo" {
		t.Fatalf("expected explicit repo slug to be preserved verbatim, got: %q", repo.Slug)
	}
	if scope.Branch != "main" {
		t.Fatalf("expected branch to be filled from discovery for a matching repo, got: %q", scope.Branch)
	}
}

// TestResolveScopeExplicitRepositoryMismatchDoesNotEnrichBranchOrCommit is
// the regression lock for the cross-repo enrichment vulnerability: when
// the caller's explicit repository does not match the locally discovered
// workspace's identity, none of that workspace's branch, commit, or
// changed-file state may be attached to the caller's repository. No
// changed-file discovery is requested here, so this must fail silently
// (nil error), exactly as if no workspace had been discoverable at all --
// not surface the mismatch as a hard error.
func TestResolveScopeExplicitRepositoryMismatchDoesNotEnrichBranchOrCommit(t *testing.T) {
	initTempGitRepo(t, "acme/discovered-repo")

	req := contractsv1.MCPContextForTaskRequest{
		Goal:       "goal",
		Repository: &contractsv1.MCPRepositoryRef{Slug: "explicit/repo"},
	}
	repo, scope, err := resolveScope(context.Background(), nil, req)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Slug != "explicit/repo" {
		t.Fatalf("expected explicit repo slug to be preserved, got: %q", repo.Slug)
	}
	if scope.Branch != "" || scope.CommitSHA != "" {
		t.Fatalf("expected no branch/commit enrichment from a mismatched repository, got: %#v", scope)
	}
}

// TestResolveScopeExplicitRepositoryMismatchWithChangedFilesRequestedReturnsSanitizedError
// covers the other half of the same vulnerability: when the caller both
// names a repository that does not match local discovery AND explicitly
// requests changed-file discovery, resolveScope must not silently return
// an empty (and therefore misleading -- indistinguishable from "no local
// changes") file list. It must fail with ErrRepositoryScopeMismatch,
// whose message is asserted to carry neither the explicit nor the
// discovered slug.
func TestResolveScopeExplicitRepositoryMismatchWithChangedFilesRequestedReturnsSanitizedError(t *testing.T) {
	initTempGitRepo(t, "acme/discovered-repo")

	req := contractsv1.MCPContextForTaskRequest{
		Goal:       "goal",
		Repository: &contractsv1.MCPRepositoryRef{Slug: "explicit/repo"},
		Scope:      &contractsv1.MCPRequestedScope{IncludeChangedFiles: boolPtr(true)},
	}
	_, _, err := resolveScope(context.Background(), nil, req)
	if !errors.Is(err, ErrRepositoryScopeMismatch) {
		t.Fatalf("expected ErrRepositoryScopeMismatch, got: %v", err)
	}
	if strings.Contains(err.Error(), "explicit/repo") || strings.Contains(err.Error(), "acme/discovered-repo") {
		t.Fatalf("error leaked a repository slug: %v", err)
	}
}

// TestResolveScopeExplicitRepositoryWithNoDiscoveredRemoteDoesNotEnrich
// covers the "absent identity" half of the fix: a caller-named repository
// combined with a discoverable local Git workspace that has no configured
// remote at all (info.Remote == nil) must not be enriched from that
// workspace either -- there is no identity to confirm a match against, so
// none is assumed.
func TestResolveScopeExplicitRepositoryWithNoDiscoveredRemoteDoesNotEnrich(t *testing.T) {
	initTempGitRepoNoRemote(t)

	req := contractsv1.MCPContextForTaskRequest{
		Goal:       "goal",
		Repository: &contractsv1.MCPRepositoryRef{Slug: "explicit/repo"},
	}
	repo, scope, err := resolveScope(context.Background(), nil, req)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Slug != "explicit/repo" {
		t.Fatalf("expected explicit repo slug to be preserved, got: %q", repo.Slug)
	}
	if scope.Branch != "" || scope.CommitSHA != "" {
		t.Fatalf("expected no branch/commit enrichment from an identity-less workspace, got: %#v", scope)
	}
}
