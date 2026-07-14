package main

import (
	"os/exec"
	"strings"
	"testing"
)

// initFixtureRepo creates a real, minimal Git repository at dir with one
// commit, so tests can assert the workspace command's exact reported
// commit SHA against real Git state.
func initFixtureRepo(t *testing.T) (dir, commit string) {
	t.Helper()
	dir = physicalTempDir(t)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--quiet")
	run("-c", "user.email=workspace-test@example.invalid", "-c", "user.name=workspace test",
		"commit", "--quiet", "--allow-empty", "-m", "workspace command fixture")
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v\n%s", err, out)
	}
	return dir, strings.TrimSpace(string(out))
}

func TestParseWorkspaceArgsRequiresExplicitPath(t *testing.T) {
	if _, err := parseWorkspaceArgs(nil); err == nil {
		t.Fatal("expected an error when --path is not provided")
	}
}

func TestParseWorkspaceArgsRejectsDuplicatePath(t *testing.T) {
	if _, err := parseWorkspaceArgs([]string{"--path", "/a", "--path", "/b"}); err == nil {
		t.Fatal("expected an error for a duplicate --path")
	}
}

func TestRunWorkspaceCommandReportsRealGitState(t *testing.T) {
	dir, commit := initFixtureRepo(t)
	code, output := captureStdout(t, func() int { return runWorkspaceCommand([]string{"--path", dir}) })
	if code != 0 {
		t.Fatalf("runWorkspaceCommand code = %d, output = %s", code, output)
	}
	if !strings.Contains(output, `"status":"ok"`) || !strings.Contains(output, `"commit_sha":"`+commit+`"`) {
		t.Fatalf("workspace output missing expected fields:\n%s", output)
	}
}

func TestRunWorkspaceCommandRejectsNonGitPath(t *testing.T) {
	dir := physicalTempDir(t)
	code, output := captureStdout(t, func() int { return runWorkspaceCommand([]string{"--path", dir}) })
	if code != 1 {
		t.Fatalf("runWorkspaceCommand code = %d, want 1\noutput=%s", code, output)
	}
	if !strings.Contains(output, `"status":"error"`) {
		t.Fatalf("expected an error status:\n%s", output)
	}
}

func TestWorkspaceSubprocessRejectsInvalidFormsWithSanitizedUsage(t *testing.T) {
	code, output := runACRMCPCLI(t, "workspace")
	if code != 2 {
		t.Fatalf("workspace without --path exited %d, want 2\n%s", code, output)
	}
	if !strings.Contains(output, "Usage: acr-mcp workspace") {
		t.Fatalf("workspace usage missing from output:\n%s", output)
	}
}

func TestWorkspaceSubprocessReportsRealGitState(t *testing.T) {
	dir, commit := initFixtureRepo(t)
	code, output := runACRMCPCLI(t, "workspace", "--path", dir)
	if code != 0 {
		t.Fatalf("workspace subprocess failed: code=%d output=%s", code, output)
	}
	if !strings.Contains(output, `"commit_sha":"`+commit+`"`) {
		t.Fatalf("workspace subprocess output missing commit sha:\n%s", output)
	}
}

func TestWorkspaceHelpPrintsUsageAndExitsZero(t *testing.T) {
	code, output := runACRMCPCLI(t, "workspace", "--help")
	if code != 0 {
		t.Fatalf("workspace --help exited %d, want 0\n%s", code, output)
	}
	if !strings.Contains(output, "Usage: acr-mcp workspace") {
		t.Fatalf("usage missing from output:\n%s", output)
	}
}
