package mcpclientfixtures

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// concreteTestArchive is the placeholder-substituted archive filename the
// adversarial test below executes the real checksum-selection fragment
// against.
const concreteTestArchive = "acr-mcp_1.2.3_darwin_arm64.tar.gz"

// extractChecksumFragment pulls just the archive=/checksum_line=/test/if
// portion out of a bash block -- not the preceding cosign invocation, nor
// the trailing tar/chmod steps -- so the adversarial test below executes
// exactly, and only, the real checksum-selection-and-verification logic
// InstallSidecarSnippet ships, never a second, hand-copied version of it
// that could silently drift from the canonical source.
func extractChecksumFragment(t *testing.T, block string) string {
	t.Helper()
	lines := strings.Split(block, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "archive=") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("expected to find an \"archive=\" line in the bash block")
	}
	for i := start; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "fi" {
			return strings.Join(lines[start:i+1], "\n")
		}
	}
	t.Fatal("expected a closing \"fi\" line after \"archive=\" in the bash block")
	return ""
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestInstallSidecarSnippetChecksumSelectionIsExactAgainstRealSBOMManifest
// is the adversarial regression lock for the exact-field-match fix: it
// executes InstallSidecarSnippet's real, unmodified checksum-selection
// fragment (extracted verbatim, only the archive placeholder substituted)
// against a real, on-disk SHA256SUMS manifest modeled on an actual release
// -- multiple platform archives, each with its own "<archive>.spdx.json"
// SBOM sibling whose filename literally starts with the archive's own
// filename. A prefix/substring selector (the previous `grep -F` design)
// would match both the archive's line and its SBOM sibling's line and
// fail the "exactly one" assertion for every real release; the current
// `awk '$2 == name'` exact-field selector must match only the archive's
// own line and succeed.
func TestInstallSidecarSnippetChecksumSelectionIsExactAgainstRealSBOMManifest(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("awk not available")
	}

	block := findFencedBlockContaining(t, []byte(InstallSidecarSnippet), "bash", "awk -v name=")
	fragment := extractChecksumFragment(t, block)
	fragment = strings.ReplaceAll(fragment, "acr-mcp_<version>_<os>_<arch>.tar.gz", concreteTestArchive)

	dir := t.TempDir()
	archiveContent := []byte("real archive bytes")
	sbomContent := []byte("sbom bytes, deliberately different from the archive")
	sbomName := concreteTestArchive + ".spdx.json"
	writeTestFile(t, dir, concreteTestArchive, archiveContent)
	writeTestFile(t, dir, sbomName, sbomContent)

	manifest := strings.Join([]string{
		fmt.Sprintf("%s  %s", sha256Hex(archiveContent), concreteTestArchive),
		fmt.Sprintf("%s  %s", sha256Hex(sbomContent), sbomName),
		fmt.Sprintf("%s  acr-mcp_1.2.3_linux_amd64.tar.gz", sha256Hex([]byte("a sibling platform's archive"))),
		fmt.Sprintf("%s  acr-mcp_1.2.3_linux_amd64.tar.gz.spdx.json", sha256Hex([]byte("that sibling's own SBOM"))),
		fmt.Sprintf("%s  acr-api_1.2.3_darwin_arm64.tar.gz", sha256Hex([]byte("a different product's archive"))),
	}, "\n") + "\n"
	writeTestFile(t, dir, "SHA256SUMS", []byte(manifest))

	if out, err := runBashFragment(dir, fragment); err != nil {
		t.Fatalf("expected the checksum fragment to succeed for a valid archive despite its SBOM sibling and other manifest entries, got error: %v\noutput:\n%s\nfragment:\n%s", err, out, fragment)
	}

	// Tamper with the archive and confirm the same fragment now fails
	// closed instead of still reporting success.
	writeTestFile(t, dir, concreteTestArchive, []byte("tampered content"))
	if out, err := runBashFragment(dir, fragment); err == nil {
		t.Fatalf("expected the checksum fragment to fail for a tampered archive, got success:\n%s", out)
	}
}

func writeTestFile(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runBashFragment(dir, fragment string) ([]byte, error) {
	cmd := exec.Command("bash", "-c", fragment)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}
