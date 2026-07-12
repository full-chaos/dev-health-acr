package mcpclientfixtures

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/releasebuild"
)

// TestInstallSidecarSnippetVerifiesOnlyTheDownloadedArchive is the direct
// regression lock for the targeted-verification fix: a consumer downloads
// only one archive out of everything SHA256SUMS lists, so the POSIX
// snippet must never batch-check the whole file directly (which would
// report every other, un-downloaded artifact as "FAILED open or read" and
// exit non-zero even though the one archive that matters verified fine).
// It must instead select and verify only the single matching line, using
// an exact-field match (`awk '$2 == name'`) rather than a substring/
// prefix match, since every archive has an
// "<archive-name>.spdx.json" SBOM sibling in the same manifest whose
// filename literally starts with the archive's own filename -- see
// TestInstallSidecarSnippetChecksumSelectionIsExactAgainstRealSBOMManifest
// for the adversarial, executed proof against a real multi-archive-plus-
// SBOM manifest.
func TestInstallSidecarSnippetVerifiesOnlyTheDownloadedArchive(t *testing.T) {
	if strings.Contains(InstallSidecarSnippet, "--check SHA256SUMS") {
		t.Fatal("expected the checksum step to verify only the downloaded archive's matching SHA256SUMS line, not batch-check the whole file directly")
	}
	if !strings.Contains(InstallSidecarSnippet, "awk -v name=\"$archive\" '$2 == name'") {
		t.Fatal("expected the checksum step to select the SHA256SUMS line with an exact-field awk match ('$2 == name'), not a substring/prefix grep that would also match the archive's own .spdx.json SBOM sibling")
	}
	if !strings.Contains(InstallSidecarSnippet, "= 1") {
		t.Fatal("expected an explicit exactly-one-line assertion before trusting the selected checksum line")
	}
	if !strings.Contains(InstallSidecarSnippet, `archive="acr-mcp_<version>_<os>_<arch>.tar.gz"`) {
		t.Fatal("expected an explicit archive= variable naming the single downloaded artifact, reused for both the checksum check and extraction")
	}
	if !strings.Contains(InstallSidecarSnippet, `tar -xzf "$archive"`) {
		t.Fatal("expected extraction to reuse the same $archive variable rather than repeating the filename literally")
	}
}

// TestInstallSidecarSnippetReleaseArchiveExtensionMatchesReleaseBuild is a
// regression lock against a broad, extension-blind find/replace silently
// reintroducing a mismatch between the doc snippet and what releasebuild
// actually produces: it derives the real POSIX archive extension from
// internal/releasebuild.ArtifactName -- the same function
// cmd/releasebuild uses to name every published artifact -- rather than
// hard-coding ".tar.gz" a second time, so this test fails if either side
// (the release builder or this canonical snippet) ever changes its
// archive format without the other following. Windows is asserted
// separately as its own known-fixed ".zip", since ArtifactName only
// special-cases GOOS=="windows"; every other GOOS shares one non-Windows
// extension.
func TestInstallSidecarSnippetReleaseArchiveExtensionMatchesReleaseBuild(t *testing.T) {
	posixName := releasebuild.ArtifactName(releasebuild.Target{Product: "acr-mcp", GOOS: "linux", GOARCH: "amd64"}, "1.2.3")
	posixExt := posixName[strings.Index(posixName, "_amd64")+len("_amd64"):]
	if posixExt != ".tar.gz" {
		t.Fatalf("expected releasebuild's own non-Windows archive extension to be \".tar.gz\", got %q -- update this test and InstallSidecarSnippet together if the release format intentionally changed", posixExt)
	}
	wantArchiveDecl := `archive="acr-mcp_<version>_<os>_<arch>` + posixExt + `"`
	if !strings.Contains(InstallSidecarSnippet, wantArchiveDecl) {
		t.Fatalf("expected InstallSidecarSnippet's archive declaration to use releasebuild's real non-Windows extension %q, want to contain %q", posixExt, wantArchiveDecl)
	}
	wantExtract := `tar -xzf "$archive"`
	if posixExt != ".tar.gz" {
		t.Fatalf("this test's extraction-command assertion is hard-coded for .tar.gz and must be updated if releasebuild's extension changes")
	}
	if !strings.Contains(InstallSidecarSnippet, wantExtract) {
		t.Fatalf("expected InstallSidecarSnippet to extract with gzip-decompressing tar (%q) to match the .tar.gz archive releasebuild produces", wantExtract)
	}
	windowsName := releasebuild.ArtifactName(releasebuild.Target{Product: "acr-mcp", GOOS: "windows", GOARCH: "amd64"}, "1.2.3")
	if !strings.HasSuffix(windowsName, ".zip") {
		t.Fatalf("expected releasebuild's Windows archive extension to remain .zip, got %q", windowsName)
	}
	if !strings.Contains(InstallSidecarWindowsSnippet, `$archive = "acr-mcp_<version>_windows_amd64.zip"`) {
		t.Fatal("expected InstallSidecarWindowsSnippet's archive assignment to keep the .zip extension releasebuild produces for Windows")
	}
}

// TestInstallSidecarWindowsSnippetVerifiesOnlyTheDownloadedArchive is the
// Windows equivalent of the lock above: the filter must target the
// archive's own filename with an end-anchored match (`EndsWith`, never a
// plain substring match that could over-match a filename containing
// another as a prefix), and must assert exactly one such line exists
// before trusting it.
func TestInstallSidecarWindowsSnippetVerifiesOnlyTheDownloadedArchive(t *testing.T) {
	if !strings.Contains(InstallSidecarWindowsSnippet, `Where-Object { $_.EndsWith("  $archive") }`) {
		t.Fatal("expected an end-anchored EndsWith filter locating only the single SHA256SUMS line matching $archive")
	}
	if !strings.Contains(InstallSidecarWindowsSnippet, ".Count -ne 1") {
		t.Fatal("expected an explicit exactly-one-line assertion before trusting the filtered checksum line")
	}
}
