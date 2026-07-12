package sidecar

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// maxGitSmallOutputBytes bounds the stdout of Git invocations whose output
// is always small in practice (rev-parse, symbolic-ref, remote listings).
// git status uses its own streaming, record-bounded path in
// workspace_changed_files.go instead, since its output size is
// attacker/working-tree controlled rather than bounded by the command
// itself.
const maxGitSmallOutputBytes = 1 << 20 // 1 MiB

// gitSafeConfigArgs are `-c` overrides applied to every Git invocation this
// package makes (via runGit and gitChangedFiles), disabling repository-
// controlled configuration that could otherwise trigger execution of an
// arbitrary command from within a cloned or otherwise untrusted
// repository's own .git/config or worktree content:
//   - core.fsmonitor may name an arbitrary hook command that `git status`
//     (gitChangedFiles) would execute; "false" disables that hook
//     regardless of what the repository's own config requests.
//   - core.hooksPath is pointed at a nonexistent directory so no Git
//     hook (any repository-supplied script under .git/hooks, or wherever
//     a repository-local core.hooksPath override would otherwise point)
//     can run for any of the read-only operations this package performs,
//     even ones this package's authors did not anticipate triggering a
//     hook in some Git version. /dev/null is not a directory, so Git
//     finds no hook files there and proceeds normally.
//
// Applied to every invocation, not just the ones known today to reach a
// repository-controlled execution path, so a future new call site in
// this package inherits the same hardening automatically.
var gitSafeConfigArgs = []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}

// runGit executes `git -C dir <args...>` with a fixed argv via
// exec.CommandContext. It never invokes a shell, never interpolates a
// command string, and returns ctx.Err() directly (unwrapped) when the
// context was canceled or its deadline exceeded, so callers can distinguish
// caller cancellation from a substantive Git failure.
//
// The git binary itself is resolved via currentExecutableResolver
// (exec_resolver.go), never a bare "git" argv0 left for exec.CommandContext
// to search PATH for on its own: the resolver searches only a fixed system
// directory allowlist, never the process's PATH environment variable, so a
// workspace- or caller-controlled PATH entry (relative or absolute) has no
// effect on which binary runs. The subprocess's environment is
// credentialSafeEnviron() (exec_resolver.go), not this process's own
// (exec.Cmd's default when Env is left nil): every ACR_-prefixed variable,
// including the ACR API bearer credential, is therefore never visible to
// the resolved git binary. gitSafeConfigArgs is inserted right after "-C"
// on every invocation to disable repository-controlled execution features
// (see its doc comment).
//
// Stdout is bounded to maxGitSmallOutputBytes: it is read from a pipe via a
// LimitReader so the process is never allowed to force an unbounded
// in-memory read before that bound is enforced, and the process is killed
// (rather than waited on) if the bound is exceeded, since it may still be
// blocked writing more than was read.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	gitPath, err := currentExecutableResolver("git")
	if err != nil {
		return "", err
	}
	fullArgs := make([]string, 0, len(args)+2+len(gitSafeConfigArgs))
	fullArgs = append(fullArgs, "-C", dir)
	fullArgs = append(fullArgs, gitSafeConfigArgs...)
	fullArgs = append(fullArgs, args...)

	cmd := exec.CommandContext(ctx, gitPath, fullArgs...)
	cmd.Env = credentialSafeEnviron()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	stderr := newBoundedStderrWriter()
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}

	data, readErr := io.ReadAll(io.LimitReader(stdout, maxGitSmallOutputBytes+1))
	oversized := len(data) > maxGitSmallOutputBytes
	if oversized {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()

	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if readErr != nil {
		return "", fmt.Errorf("git %s: read stdout: %w", strings.Join(args, " "), readErr)
	}
	if oversized {
		return "", fmt.Errorf("%w: git %s", ErrGitOutputTooLarge, strings.Join(args, " "))
	}
	if waitErr != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), waitErr, strings.TrimSpace(stderr.String()))
	}
	return string(data), nil
}

// gitShowToplevel resolves the Git working-tree root for dir.
func gitShowToplevel(ctx context.Context, dir string) (string, error) {
	out, err := runGit(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		if isContextErr(err) {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", ErrNotGitRepository, dir)
	}
	return strings.TrimSpace(out), nil
}

// gitCurrentBranch returns the current branch name, or ("", true, nil) when
// HEAD is detached. It uses `symbolic-ref --short -q HEAD`, which succeeds
// (printing the branch name) both on an ordinary branch and on an unborn
// branch (a fresh repository with no commits yet), and fails only when HEAD
// does not resolve to a branch ref, i.e. detached HEAD.
func gitCurrentBranch(ctx context.Context, root string) (branch string, detached bool, err error) {
	out, cmdErr := runGit(ctx, root, "symbolic-ref", "--short", "-q", "HEAD")
	if cmdErr == nil {
		return strings.TrimSpace(out), false, nil
	}
	if isContextErr(cmdErr) {
		return "", false, cmdErr
	}
	return "", true, nil
}

// gitHeadCommit returns the commit SHA that HEAD points to. It returns
// ErrNoCommits when the repository has no commits yet (unborn HEAD).
func gitHeadCommit(ctx context.Context, root string) (string, error) {
	out, err := runGit(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		if isContextErr(err) {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", ErrNoCommits, root)
	}
	return strings.TrimSpace(out), nil
}

// gitRemoteNames lists configured remote names in Git's own order.
func gitRemoteNames(ctx context.Context, root string) ([]string, error) {
	out, err := runGit(ctx, root, "remote")
	if err != nil {
		if isContextErr(err) {
			return nil, err
		}
		return nil, fmt.Errorf("list git remotes: %w", err)
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// gitRemoteURL returns the configured URL for the named remote.
func gitRemoteURL(ctx context.Context, root, name string) (string, error) {
	out, err := runGit(ctx, root, "remote", "get-url", name)
	if err != nil {
		if isContextErr(err) {
			return "", err
		}
		return "", fmt.Errorf("get git remote url for %s: %w", name, err)
	}
	return strings.TrimSpace(out), nil
}
