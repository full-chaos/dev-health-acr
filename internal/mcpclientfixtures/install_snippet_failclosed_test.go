package mcpclientfixtures

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeExecutable writes an executable POSIX shell script named name
// into dir, so it can be placed ahead of the real tools on PATH to
// deterministically control an external command's exit code without
// needing a real git repository, a real Cosign key, or a real signature.
func writeFakeExecutable(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

// extractBashBlock returns the full, unmodified bash fenced block from
// InstallSidecarSnippet (the entire install step: git show, cosign
// verify-blob, checksum selection, tar, chmod) with the
// <version>/<os>/<arch> placeholder substituted for a concrete archive
// name, so the adversarial test below executes exactly, and only, the
// real canonical snippet -- never a hand-copied duplicate that could
// silently drift from it.
func extractBashBlock(t *testing.T) string {
	t.Helper()
	block := findFencedBlockContaining(t, []byte(InstallSidecarSnippet), "bash", "cosign verify-blob")
	block = strings.ReplaceAll(block, "acr-mcp_<version>_<os>_<arch>.tar.gz", concreteTestArchive)
	// "<trusted-ref>" is a human-readable doc placeholder only; left
	// literal, bash parses the leading "<" as an input-redirection
	// operator ("< trusted-ref"), not as text, which would fail for an
	// unrelated reason before cosign is ever reached. Substitute a plain
	// token so the fake git/cosign stand-ins are what determine the
	// outcome, not a redirection parse quirk specific to this test.
	block = strings.ReplaceAll(block, "<trusted-ref>", "test-ref")
	return block
}

// runBashBlockWithFakeTools executes fragment with fakeBinDir prepended
// to PATH (so the fake git/cosign/tar stand in for the real tools) in
// workDir, returning combined output and the process error.
func runBashBlockWithFakeTools(workDir, fakeBinDir, fragment string) ([]byte, error) {
	cmd := exec.Command("bash", "-c", fragment)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "PATH="+fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return cmd.CombinedOutput()
}

// TestInstallSidecarSnippetNeverExtractsAfterCosignVerificationFails is
// the adversarial regression lock for the fail-closed fix: with a fake
// `cosign` that always exits nonzero (simulating a failed signature
// verification -- a tampered SHA256SUMS, a wrong key, or an actual
// supply-chain attack) and a fake `git` that always succeeds, the real,
// unmodified InstallSidecarSnippet bash block must both (a) exit nonzero
// itself, and (b) never invoke `tar` -- proving extraction of a
// signature-unverified archive can never happen. `tar` is faked to leave
// a marker file precisely because "did the script eventually error out"
// alone would not catch a `set -e`-less script that ran `tar` anyway and
// only failed later, e.g. on `chmod`.
func TestInstallSidecarSnippetNeverExtractsAfterCosignVerificationFails(t *testing.T) {
	skipIfNotDarwinOrLinux(t)
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	fakeBin := t.TempDir()
	writeFakeExecutable(t, fakeBin, "git", "exit 0")
	writeFakeExecutable(t, fakeBin, "cosign", "exit 1")
	tarMarker := filepath.Join(t.TempDir(), "tar-was-invoked")
	writeFakeExecutable(t, fakeBin, "tar", "touch "+tarMarker+"\nexit 0")

	workDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workDir, "signing"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, workDir, concreteTestArchive, []byte("archive bytes that must never be extracted"))
	writeTestFile(t, workDir, "SHA256SUMS", []byte(sha256Hex([]byte("archive bytes that must never be extracted"))+"  "+concreteTestArchive+"\n"))

	fragment := extractBashBlock(t)
	out, err := runBashBlockWithFakeTools(workDir, fakeBin, fragment)
	if err == nil {
		t.Fatalf("expected the install snippet to exit non-zero when cosign verification fails, got success:\n%s", out)
	}
	if _, statErr := os.Stat(tarMarker); statErr == nil {
		t.Fatalf("expected tar to never be invoked when cosign verification fails, but it was:\n%s", out)
	}
}

// TestInstallSidecarSnippetExtractsWhenCosignVerificationSucceeds is the
// positive control for the adversarial test above: `set -euo pipefail`
// must not break the legitimate happy path. With fake `git` and `cosign`
// that both succeed, the real, unmodified InstallSidecarSnippet bash
// block must invoke `tar` and exit zero.
func TestInstallSidecarSnippetExtractsWhenCosignVerificationSucceeds(t *testing.T) {
	skipIfNotDarwinOrLinux(t)
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	fakeBin := t.TempDir()
	writeFakeExecutable(t, fakeBin, "git", "exit 0")
	writeFakeExecutable(t, fakeBin, "cosign", "exit 0")
	tarMarker := filepath.Join(t.TempDir(), "tar-was-invoked")
	writeFakeExecutable(t, fakeBin, "tar", "touch "+tarMarker+"\nexit 0")
	writeFakeExecutable(t, fakeBin, "chmod", "exit 0")

	workDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workDir, "signing"), 0o700); err != nil {
		t.Fatal(err)
	}
	archiveContent := []byte("real archive bytes that must be extracted")
	writeTestFile(t, workDir, concreteTestArchive, archiveContent)
	writeTestFile(t, workDir, "SHA256SUMS", []byte(sha256Hex(archiveContent)+"  "+concreteTestArchive+"\n"))

	fragment := extractBashBlock(t)
	out, err := runBashBlockWithFakeTools(workDir, fakeBin, fragment)
	if err != nil {
		t.Fatalf("expected the install snippet to succeed when cosign verification succeeds, got error: %v\noutput:\n%s", err, out)
	}
	if _, statErr := os.Stat(tarMarker); statErr != nil {
		t.Fatalf("expected tar to be invoked when cosign verification succeeds, but it was not:\n%s", out)
	}
}

// TestInstallSidecarSnippetHasFailClosedShellOptions is the direct,
// non-executed regression lock proving the canonical snippet's own text
// enables bash's fail-closed options: without `set -e`, a failed `git
// show` or `cosign verify-blob` would not stop the script, and a
// subsequent `command | tool` pipeline failing only in its first stage
// (without `pipefail`) would not be detected either.
func TestInstallSidecarSnippetHasFailClosedShellOptions(t *testing.T) {
	if !strings.Contains(InstallSidecarSnippet, "set -euo pipefail") {
		t.Fatal("expected the POSIX snippet to start with `set -euo pipefail` so a failed git/cosign/checksum step halts before extraction")
	}
	setIdx := strings.Index(InstallSidecarSnippet, "set -euo pipefail")
	cosignIdx := strings.Index(InstallSidecarSnippet, "cosign verify-blob")
	tarIdx := strings.Index(InstallSidecarSnippet, "tar -xzf")
	if setIdx < 0 || cosignIdx < 0 || tarIdx < 0 || !(setIdx < cosignIdx && cosignIdx < tarIdx) {
		t.Fatalf("expected `set -euo pipefail` to appear before cosign verify-blob, which must appear before tar -xzf: set=%d cosign=%d tar=%d", setIdx, cosignIdx, tarIdx)
	}
}

// skipIfNotDarwinOrLinux keeps the adversarial POSIX tests from running
// on a platform where /bin/sh-shebang fake executables and Unix
// permission bits are not meaningful.
func skipIfNotDarwinOrLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("POSIX shell fakes require darwin or linux")
	}
}
