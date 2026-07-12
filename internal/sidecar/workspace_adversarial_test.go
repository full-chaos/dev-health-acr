package sidecar

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDiscoverWorkspace_CanceledContext exercises long_commands/cancellation:
// an already-canceled context must abort promptly with context.Canceled and
// must not be misreported as ErrNotGitRepository or any other typed
// discovery error.
func TestDiscoverWorkspace_CanceledContext(t *testing.T) {
	repo := newTestRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := DiscoverWorkspace(ctx, DiscoverOptions{ExplicitRepoPath: repo})
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if errors.Is(err, ErrNotGitRepository) {
		t.Fatalf("cancellation must not be misreported as ErrNotGitRepository: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("canceled context should abort promptly, took %s", elapsed)
	}
}

func TestDiscoverWorkspace_DeadlineExceededContext(t *testing.T) {
	repo := newTestRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	time.Sleep(time.Millisecond) // ensure the deadline has definitely passed

	_, err := DiscoverWorkspace(ctx, DiscoverOptions{ExplicitRepoPath: repo})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
}

// TestDiscoverWorkspace_RepeatedInterruptions exercises repeated
// cancellation followed by successful calls to confirm no shared/leaked
// state corrupts subsequent discovery.
func TestDiscoverWorkspace_RepeatedInterruptions(t *testing.T) {
	repo := newTestRepo(t)
	for i := range 5 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := DiscoverWorkspace(ctx, DiscoverOptions{ExplicitRepoPath: repo}); !errors.Is(err, context.Canceled) {
			t.Fatalf("iteration %d: expected context.Canceled, got %v", i, err)
		}
	}
	info, err := DiscoverWorkspace(context.Background(), DiscoverOptions{ExplicitRepoPath: repo})
	if err != nil {
		t.Fatalf("DiscoverWorkspace after repeated cancellations: %v", err)
	}
	if info.GitRoot == "" {
		t.Fatal("discovery must still succeed cleanly after prior cancellations")
	}
}

// TestDiscoverWorkspace_StaleStateReflectsNewCommits exercises stale_state:
// discovery must not cache; a second call after a new commit reflects it.
func TestDiscoverWorkspace_StaleStateReflectsNewCommits(t *testing.T) {
	repo := newTestRepo(t)
	opts := DiscoverOptions{ExplicitRepoPath: repo}

	first, err := DiscoverWorkspace(context.Background(), opts)
	if err != nil {
		t.Fatalf("DiscoverWorkspace: %v", err)
	}
	second := commitFile(t, repo, "again.txt", "again\n")
	updated, err := DiscoverWorkspace(context.Background(), opts)
	if err != nil {
		t.Fatalf("DiscoverWorkspace: %v", err)
	}
	if updated.CommitSHA != second {
		t.Fatalf("CommitSHA = %q, want fresh commit %q", updated.CommitSHA, second)
	}
	if updated.CommitSHA == first.CommitSHA {
		t.Fatal("expected the commit SHA to change after a new commit")
	}
}

// TestDiscoverWorkspace_NeverReturnsSuccessWithEmptyGitRoot guards against
// misleading_success_output: whenever err is nil, GitRoot and CommitSHA must
// be populated.
func TestDiscoverWorkspace_NeverReturnsSuccessWithEmptyGitRoot(t *testing.T) {
	repo := newTestRepo(t)
	info, err := DiscoverWorkspace(context.Background(), DiscoverOptions{ExplicitRepoPath: repo})
	if err != nil {
		t.Fatalf("DiscoverWorkspace: %v", err)
	}
	if info.GitRoot == "" {
		t.Fatal("success result must have a non-empty GitRoot")
	}
	if info.CommitSHA == "" {
		t.Fatal("success result must have a non-empty CommitSHA")
	}
}

func TestDiscoverWorkspace_AmbiguousErrorNeverReturnsPartialSuccess(t *testing.T) {
	repoA := newTestRepo(t)
	repoB := newTestRepo(t)
	info, err := DiscoverWorkspace(context.Background(), DiscoverOptions{
		MCPFileRoots: []string{repoA, repoB},
	})
	if err == nil {
		t.Fatal("expected an error for ambiguous roots")
	}
	if info.GitRoot != "" || info.CommitSHA != "" {
		t.Fatalf("ambiguous discovery must not leak a partial success value, got %#v", info)
	}
}

// TestDiscoverWorkspace_UnsupportedRemoteNeverLeaksCredentials exercises
// the credential-leak adversarial case end-to-end: a rejected remote URL
// carrying an embedded canary credential must never have that credential
// echoed back in DiscoverWorkspace's returned error.
func TestDiscoverWorkspace_UnsupportedRemoteNeverLeaksCredentials(t *testing.T) {
	repo := newTestRepo(t)
	const canary = "S3cr3t-Canary-Token-Do-Not-Leak"
	runGitCmdT(t, repo, "remote", "add", "origin", "https://svc-account:"+canary+"@github.com/owner/repo.git")

	_, err := DiscoverWorkspace(context.Background(), DiscoverOptions{ExplicitRepoPath: repo})
	if !errors.Is(err, ErrUnsupportedRemote) {
		t.Fatalf("expected ErrUnsupportedRemote, got %v", err)
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("DiscoverWorkspace error leaked the credential canary: %v", err)
	}
}

// TestDiscoverWorkspace_SchemeLessRemoteNeverLeaksCredentials exercises the
// same end-to-end credential-leak invariant against a scheme-less SCP-like
// remote ("user@host:path", no "git@" prefix, no "://"). This shape is
// url.Parse-hostile — net/url returns an outright parse error on it ("first
// path segment in URL cannot contain colon") — so it exercises a distinct
// code path from the https:// canary case above, where url.Parse succeeds
// and populates u.User directly.
func TestDiscoverWorkspace_SchemeLessRemoteNeverLeaksCredentials(t *testing.T) {
	repo := newTestRepo(t)
	const canary = "S3cr3t-Canary-Token-Do-Not-Leak"
	runGitCmdT(t, repo, "remote", "add", "origin", canary+"@github.com:owner/repo.git")

	_, err := DiscoverWorkspace(context.Background(), DiscoverOptions{ExplicitRepoPath: repo})
	if !errors.Is(err, ErrUnsupportedRemote) {
		t.Fatalf("expected ErrUnsupportedRemote, got %v", err)
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("DiscoverWorkspace error leaked the credential canary: %v", err)
	}
}

// TestDiscoverWorkspace_RejectsTooManyMCPRootsEndToEnd exercises the
// many_roots adversarial case through the public entry point: more than
// MaxMCPFileRoots supplied roots fails closed rather than validating an
// unbounded number of candidates.
func TestDiscoverWorkspace_RejectsTooManyMCPRootsEndToEnd(t *testing.T) {
	roots := make([]string, MaxMCPFileRoots+1)
	for i := range roots {
		roots[i] = "/nonexistent-" + strconv.Itoa(i)
	}
	_, err := DiscoverWorkspace(context.Background(), DiscoverOptions{MCPFileRoots: roots})
	if !errors.Is(err, ErrTooManyWorkspaceRoots) {
		t.Fatalf("expected ErrTooManyWorkspaceRoots, got %v", err)
	}
}
