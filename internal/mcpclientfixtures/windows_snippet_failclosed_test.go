package mcpclientfixtures

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallSidecarWindowsSnippetHasFailClosedErrorHandling is the
// direct, non-executed regression lock proving the canonical Windows
// snippet's own text enables fail-closed error handling for every step
// that can fail: `$ErrorActionPreference = 'Stop'` converts a PowerShell
// cmdlet's (e.g. Expand-Archive's) non-terminating error into a script-
// halting one, but PowerShell does NOT automatically treat a native
// executable's (git.exe, cosign.exe) non-zero exit code as an error at
// all -- $ErrorActionPreference has no effect on external commands -- so
// each of those two native calls must be followed by an explicit
// `$LASTEXITCODE` check that throws before the checksum/extraction steps
// below it ever run.
func TestInstallSidecarWindowsSnippetHasFailClosedErrorHandling(t *testing.T) {
	snippet := InstallSidecarWindowsSnippet

	if !strings.Contains(snippet, "$ErrorActionPreference = 'Stop'") {
		t.Fatal("expected the Windows snippet to set $ErrorActionPreference = 'Stop' so a failing cmdlet (e.g. Expand-Archive) halts the script")
	}
	if got := strings.Count(snippet, "if ($LASTEXITCODE -ne 0)"); got < 2 {
		t.Fatalf("expected at least 2 explicit $LASTEXITCODE checks (one after `git show`, one after `cosign.exe verify-blob`), got %d", got)
	}

	eapIdx := strings.Index(snippet, "$ErrorActionPreference = 'Stop'")
	gitIdx := strings.Index(snippet, "git show")
	cosignIdx := strings.Index(snippet, "cosign.exe verify-blob")
	firstCheckIdx := strings.Index(snippet, "if ($LASTEXITCODE -ne 0)")
	secondCheckIdx := strings.Index(snippet[cosignIdx:], "if ($LASTEXITCODE -ne 0)")
	expandIdx := strings.Index(snippet, "Expand-Archive")

	if eapIdx < 0 || gitIdx < 0 || cosignIdx < 0 || firstCheckIdx < 0 || secondCheckIdx < 0 || expandIdx < 0 {
		t.Fatalf("expected to find $ErrorActionPreference, git show, cosign.exe verify-blob, two $LASTEXITCODE checks, and Expand-Archive, got indices eap=%d git=%d cosign=%d check1=%d check2(relative)=%d expand=%d", eapIdx, gitIdx, cosignIdx, firstCheckIdx, secondCheckIdx, expandIdx)
	}
	secondCheckIdx += cosignIdx // convert back to an absolute index

	if !(eapIdx < gitIdx && gitIdx < firstCheckIdx && firstCheckIdx < cosignIdx) {
		t.Fatalf("expected $ErrorActionPreference, then git show, then its $LASTEXITCODE check, then cosign.exe verify-blob, got eap=%d git=%d check1=%d cosign=%d", eapIdx, gitIdx, firstCheckIdx, cosignIdx)
	}
	if !(cosignIdx < secondCheckIdx && secondCheckIdx < expandIdx) {
		t.Fatalf("expected cosign.exe verify-blob, then its own $LASTEXITCODE check, before Expand-Archive, got cosign=%d check2=%d expand=%d", cosignIdx, secondCheckIdx, expandIdx)
	}
}

// TestInstallSidecarWindowsSnippetAdversarial executes the real,
// unmodified InstallSidecarWindowsSnippet PowerShell block (only the
// <version>/<os>/<arch> and <trusted-ref> placeholders substituted) under
// a real pwsh, with fake git/cosign.exe stand-ins on PATH, proving the
// same fail-closed contract the POSIX adversarial test proves: a failing
// cosign.exe verification must never let Expand-Archive run, and a
// succeeding one must still extract normally. It is skipped -- not
// weakened -- when pwsh is unavailable; see
// TestInstallSidecarWindowsSnippetHasFailClosedErrorHandling above for
// the structural lock that always runs.
func TestInstallSidecarWindowsSnippetAdversarial(t *testing.T) {
	if _, err := exec.LookPath("pwsh"); err != nil {
		t.Skip("pwsh not available in this environment; relying on TestInstallSidecarWindowsSnippetHasFailClosedErrorHandling's structural lock instead")
	}

	const archive = "acr-mcp_1.2.3_windows_amd64.zip"
	script := findFencedBlockContaining(t, []byte(InstallSidecarWindowsSnippet), "powershell", "cosign.exe verify-blob")
	script = strings.ReplaceAll(script, "acr-mcp_<version>_windows_amd64.zip", archive)
	script = strings.ReplaceAll(script, "<trusted-ref>", "test-ref")

	fakeBin := t.TempDir()
	writeFakeExecutable(t, fakeBin, "git", "exit 0")
	tarMarkerDir := t.TempDir()
	marker := filepath.Join(tarMarkerDir, "expand-archive-was-invoked")

	runAdversarialCase := func(t *testing.T, cosignExitCode int, wantSuccess bool) {
		workDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(workDir, "signing"), 0o700); err != nil {
			t.Fatal(err)
		}
		archiveContent := []byte("real archive bytes")
		writeTestFile(t, workDir, archive, archiveContent)
		writeTestFile(t, workDir, "SHA256SUMS", []byte(sha256Hex(archiveContent)+"  "+archive+"\n"))

		_ = os.Remove(marker)
		writeFakeExecutable(t, fakeBin, "cosign.exe", fmt.Sprintf("exit %d", cosignExitCode))
		// A real Expand-Archive would fail on this fake, non-zip archive
		// content anyway; what this test verifies is whether it is ever
		// *invoked*, so unpack the marker file's presence from cosign's
		// own failure by dropping the marker BEFORE running the script and
		// checking it independently of Expand-Archive's own success.
		scriptPath := filepath.Join(workDir, "install.ps1")
		instrumented := strings.Replace(script, "Expand-Archive -Path $archive -DestinationPath .",
			"Set-Content -Path '"+marker+"' -Value 'invoked'\nExpand-Archive -Path $archive -DestinationPath . -Force", 1)
		if err := os.WriteFile(scriptPath, []byte(instrumented), 0o600); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command("pwsh", "-NoProfile", "-File", scriptPath)
		cmd.Dir = workDir
		cmd.Env = append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()

		_, markerErr := os.Stat(marker)
		markerExists := markerErr == nil
		if wantSuccess {
			if err != nil {
				t.Fatalf("expected success when cosign.exe succeeds, got error: %v\noutput:\n%s", err, out)
			}
			if !markerExists {
				t.Fatalf("expected Expand-Archive to be invoked when cosign.exe succeeds, but it was not:\n%s", out)
			}
			return
		}
		if err == nil {
			t.Fatalf("expected the script to fail when cosign.exe fails, got success:\n%s", out)
		}
		if markerExists {
			t.Fatalf("expected Expand-Archive to never be invoked when cosign.exe fails, but it was:\n%s", out)
		}
	}

	t.Run("cosign fails", func(t *testing.T) { runAdversarialCase(t, 1, false) })
	t.Run("cosign succeeds", func(t *testing.T) { runAdversarialCase(t, 0, true) })
}
