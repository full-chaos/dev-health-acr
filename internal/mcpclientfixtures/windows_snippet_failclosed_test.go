package mcpclientfixtures

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallSidecarWindowsSnippetHasFailClosedErrorHandling is the
// direct, non-executed regression lock proving the canonical Windows
// snippet fails closed around both PowerShell cmdlets and the one native
// executable in the keyless release-verification path. PowerShell does not
// turn a non-zero cosign.exe exit into a terminating error, so the snippet
// must check $LASTEXITCODE immediately after verification and before any
// checksum selection or archive extraction. The obsolete local-key flow and
// its git show command must not reappear.
func TestInstallSidecarWindowsSnippetHasFailClosedErrorHandling(t *testing.T) {
	snippet := InstallSidecarWindowsSnippet

	if !strings.Contains(snippet, "$ErrorActionPreference = 'Stop'") {
		t.Fatal("expected the Windows snippet to set $ErrorActionPreference = 'Stop' so a failing cmdlet (e.g. Expand-Archive) halts the script")
	}
	if got := strings.Count(snippet, "if ($LASTEXITCODE -ne 0)"); got != 1 {
		t.Fatalf("expected exactly one explicit $LASTEXITCODE check after cosign.exe verify-blob, got %d", got)
	}
	if strings.Contains(snippet, "git show") {
		t.Fatal("keyless release verification must not restore the obsolete git show public-key flow")
	}

	eapIdx := strings.Index(snippet, "$ErrorActionPreference = 'Stop'")
	cosignIdx := strings.Index(snippet, "cosign.exe verify-blob")
	checkIdx := strings.Index(snippet[cosignIdx:], "if ($LASTEXITCODE -ne 0)")
	expandIdx := strings.Index(snippet, "Expand-Archive")

	if eapIdx < 0 || cosignIdx < 0 || checkIdx < 0 || expandIdx < 0 {
		t.Fatalf("expected to find $ErrorActionPreference, cosign.exe verify-blob, its $LASTEXITCODE check, and Expand-Archive, got indices eap=%d cosign=%d check(relative)=%d expand=%d", eapIdx, cosignIdx, checkIdx, expandIdx)
	}
	checkIdx += cosignIdx
	if !(eapIdx < cosignIdx && cosignIdx < checkIdx && checkIdx < expandIdx) {
		t.Fatalf("expected $ErrorActionPreference, then cosign.exe verify-blob, then its $LASTEXITCODE check, before Expand-Archive, got eap=%d cosign=%d check=%d expand=%d", eapIdx, cosignIdx, checkIdx, expandIdx)
	}
}

// buildTestZipArchive creates a real, valid ZIP archive in memory using
// the standard library's archive/zip -- not a placeholder byte string --
// containing one file with known content, so a real, unmocked
// Expand-Archive invoked by the adversarial test below can genuinely
// extract it rather than fail on malformed archive bytes. A prior
// version of this test used a plain "real archive bytes" string as the
// archive payload; that happened to make the fail-closed ("cosign
// fails") case pass, since Expand-Archive is never reached, but it made
// the positive-control ("cosign succeeds") case falsely rely on
// Expand-Archive itself throwing on the malformed input for markerless
// reasoning to still hold -- which only surfaced as a failure once this
// test actually ran against a real pwsh in CI (Ubuntu), not on a
// developer machine without pwsh where the whole test is skipped.
func buildTestZipArchive(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestInstallSidecarWindowsSnippetAdversarial executes the real,
// unmodified InstallSidecarWindowsSnippet PowerShell block (only the
// <version>/<os>/<arch> placeholder substituted) under a real pwsh, with
// a fake cosign.exe stand-in on PATH, proving the
// same fail-closed contract the POSIX adversarial test proves: a failing
// cosign.exe verification must never let Expand-Archive run, and a
// succeeding one must still extract normally -- verified both by an
// injected marker immediately before the real Expand-Archive call and,
// more directly, by the real archived file actually landing in workDir.
// It is skipped -- not weakened -- when pwsh is unavailable; see
// TestInstallSidecarWindowsSnippetHasFailClosedErrorHandling above for
// the structural lock that always runs.
func TestInstallSidecarWindowsSnippetAdversarial(t *testing.T) {
	if _, err := exec.LookPath("pwsh"); err != nil {
		t.Skip("pwsh not available in this environment; relying on TestInstallSidecarWindowsSnippetHasFailClosedErrorHandling's structural lock instead")
	}

	const archive = "acr-mcp_1.2.3_windows_amd64.zip"
	const extractedFileName = "acr-mcp-adversarial-marker.txt"
	const extractedFileContent = "this file must be extracted only after cosign.exe verification succeeds"
	script := findFencedBlockContaining(t, []byte(InstallSidecarWindowsSnippet), "powershell", "cosign.exe verify-blob")
	script = strings.ReplaceAll(script, "acr-mcp_<version>_windows_amd64.zip", archive)
	archiveContent := buildTestZipArchive(t, extractedFileName, extractedFileContent)

	fakeBin := t.TempDir()
	tarMarkerDir := t.TempDir()
	marker := filepath.Join(tarMarkerDir, "expand-archive-was-invoked")

	runAdversarialCase := func(t *testing.T, cosignExitCode int, wantSuccess bool) {
		workDir := t.TempDir()
		writeTestFile(t, workDir, archive, archiveContent)
		writeTestFile(t, workDir, "SHA256SUMS", []byte(sha256Hex(archiveContent)+"  "+archive+"\n"))

		_ = os.Remove(marker)
		writeFakeExecutable(t, fakeBin, "cosign.exe", fmt.Sprintf("exit %d", cosignExitCode))
		// The marker is written immediately before the real Expand-Archive
		// call so its presence proves invocation independently of whether
		// extraction itself later succeeds; archiveContent above is now a
		// real ZIP, so a successful invocation also genuinely extracts
		// extractedFileName, which is asserted below as the stronger,
		// non-marker proof of success.
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
		extractedPath := filepath.Join(workDir, extractedFileName)
		extractedContent, extractedErr := os.ReadFile(extractedPath)
		extractedExists := extractedErr == nil
		if wantSuccess {
			if err != nil {
				t.Fatalf("expected success when cosign.exe succeeds, got error: %v\noutput:\n%s", err, out)
			}
			if !markerExists {
				t.Fatalf("expected Expand-Archive to be invoked when cosign.exe succeeds, but it was not:\n%s", out)
			}
			if !extractedExists {
				t.Fatalf("expected %s to be extracted from the real ZIP archive when cosign.exe succeeds, but it was not:\n%s", extractedFileName, out)
			}
			if string(extractedContent) != extractedFileContent {
				t.Fatalf("expected extracted %s to contain %q, got %q", extractedFileName, extractedFileContent, string(extractedContent))
			}
			return
		}
		if err == nil {
			t.Fatalf("expected the script to fail when cosign.exe fails, got success:\n%s", out)
		}
		if markerExists {
			t.Fatalf("expected Expand-Archive to never be invoked when cosign.exe fails, but it was:\n%s", out)
		}
		if extractedExists {
			t.Fatalf("expected %s to never be extracted when cosign.exe fails, but it was found in workDir", extractedFileName)
		}
	}

	t.Run("cosign fails", func(t *testing.T) { runAdversarialCase(t, 1, false) })
	t.Run("cosign succeeds", func(t *testing.T) { runAdversarialCase(t, 0, true) })
}
