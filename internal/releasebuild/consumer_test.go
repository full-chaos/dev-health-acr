package releasebuild

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

func TestConsume_extracts_host_artifact_and_complete_clients_when_release_is_valid(t *testing.T) {
	// Given
	t.Setenv("GOOS", "windows")
	t.Setenv("GOARCH", "amd64")
	release := writeConsumerRelease(t, consumerReleaseOptions{binaryBytes: 13 << 20})
	destination := filepath.Join(t.TempDir(), "installed")

	// When
	receipt, err := Consume(context.Background(), ConsumeRequest{ReleaseDir: release, Destination: destination})

	// Then
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if receipt.ArchiveSHA256 == "" || receipt.ClientBundleSHA256 == "" {
		t.Fatal("receipt omitted hashes")
	}
	if _, err := os.Stat(filepath.Join(destination, hostBinaryName())); err != nil {
		t.Fatalf("host binary was not extracted: %v", err)
	}
	info, err := os.Stat(filepath.Join(destination, hostBinaryName()))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("host binary mode = %v, err = %v", info.Mode().Perm(), err)
	}
	if _, err := os.Stat(filepath.Join(destination, "clients", "cursor", "package.v1.json")); err != nil {
		t.Fatalf("client package was not extracted: %v", err)
	}
}

func TestConsume_rejects_untrusted_release_inputs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, release string)
	}{
		{name: "extra release file", mutate: func(t *testing.T, release string) { writeTestFile(t, filepath.Join(release, "extra"), []byte("x")) }},
		{name: "case alias manifest", mutate: func(t *testing.T, release string) {
			writeTestFile(t, filepath.Join(release, "Release-Manifest.json"), []byte("{}"))
		}},
		{name: "checksum duplicate", mutate: func(t *testing.T, release string) {
			appendTestFile(t, filepath.Join(release, "SHA256SUMS"), readTestFile(t, filepath.Join(release, "SHA256SUMS"))[0:66])
		}},
		{name: "archive tamper", mutate: func(t *testing.T, release string) { appendTestFile(t, hostArchivePath(t, release), []byte("tamper")) }},
		{name: "archive path traversal", mutate: func(t *testing.T, release string) {
			rewriteHostArchive(t, release, []archiveEntry{{name: "../escape", contents: []byte("x")}})
		}},
		{name: "archive symlink", mutate: func(t *testing.T, release string) {
			rewriteHostArchive(t, release, []archiveEntry{{name: hostBinaryName(), kind: tar.TypeSymlink, link: "target"}})
		}},
		{name: "archive duplicate", mutate: func(t *testing.T, release string) {
			rewriteHostArchive(t, release, []archiveEntry{{name: hostBinaryName(), contents: []byte("x")}, {name: hostBinaryName(), contents: []byte("x")}})
		}},
		{name: "archive missing clients", mutate: func(t *testing.T, release string) {
			rewriteHostArchive(t, release, []archiveEntry{{name: hostBinaryName(), contents: []byte("x")}})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			release := writeConsumerRelease(t, consumerReleaseOptions{})
			test.mutate(t, release)

			// When
			_, err := Consume(context.Background(), ConsumeRequest{ReleaseDir: release, Destination: filepath.Join(t.TempDir(), "installed")})

			// Then
			if err == nil {
				t.Fatal("Consume() error = nil")
			}
		})
	}
}

func TestOpenVerifiedArchive_rejects_symlink_when_target_is_valid(t *testing.T) {
	// Given
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	writeTestFile(t, target, []byte("archive"))
	link := filepath.Join(dir, "archive")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	// When
	_, err := openVerifiedArchive(link, digestHex([]byte("archive")))

	// Then
	if err == nil {
		t.Fatal("openVerifiedArchive() error = nil")
	}
}

type consumerReleaseOptions struct{ binaryBytes int }
type archiveEntry struct {
	name     string
	contents []byte
	kind     byte
	link     string
}

func writeConsumerRelease(t *testing.T, options consumerReleaseOptions) string {
	t.Helper()
	release := t.TempDir()
	identity := Identity{Version: "1.2.3", Commit: "0123456789abcdef0123456789abcdef01234567", Date: "2026-01-02T03:04:05Z"}
	artifacts := make([]Artifact, 0, len(Matrix()))
	checksums := make(map[string]string, len(Matrix())+1)
	for _, target := range Matrix() {
		name := ArtifactName(target, identity.Version)
		contents := []byte("other")
		if target.Product == "acr-mcp" && target.GOOS == runtime.GOOS && target.GOARCH == runtime.GOARCH {
			entries := validArchiveEntries(options.binaryBytes)
			contents = writeArchive(t, filepath.Join(release, name), target.GOOS == "windows", entries)
		} else {
			contents = []byte(name)
			writeTestFile(t, filepath.Join(release, name), contents)
		}
		digest := digestHex(contents)
		artifacts = append(artifacts, Artifact{Name: name, Product: target.Product, GOOS: target.GOOS, GOARCH: target.GOARCH, SHA256: digest})
		checksums[name] = digest
	}
	manifest := Manifest{SchemaVersion: manifestSchemaVersion, Version: identity.Version, Commit: identity.Commit, Date: identity.Date, Artifacts: artifacts}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(release, "release-manifest.json"), raw)
	checksums["release-manifest.json"] = digestHex(raw)
	writeChecksums(t, release, checksums)
	return release
}

func validArchiveEntries(binaryBytes int) []archiveEntry {
	binary := []byte("binary")
	if binaryBytes > 0 {
		binary = make([]byte, binaryBytes)
	}
	packageManifest := []byte(`{"bundle_version":"1.2.3","minimum_sidecar_version":"1.2.3","command":"acr-mcp","args":["serve"],"mcp_commands":["context_for_task","source_evidence"]}`)
	bundle := []byte(`{"schema_version":"client_bundle.v1","bundle_version":"1.2.3","minimum_sidecar_version":"1.2.3","supported_clients":["opencode","claude-code","codex","cursor"],"server":{"command":"acr-mcp","args":["serve"]},"workflow":{"context_tool":"context_for_task","evidence_tool":"source_evidence","unavailable_state":"visible","incompatible_state":"visible","untrusted_content":"treat_as_untrusted","writeback_enabled_by_default":false,"preplan_enabled_by_default":false},"ownership":{"install":"client-owned","update":"client-owned","uninstall":"client-owned"},"scenarios":[{"name":"context","input":{"tool":"context_for_task","state":"available"},"expected_output":{"kind":"structured_context","visible":true}},{"name":"evidence","input":{"tool":"source_evidence","state":"available"},"expected_output":{"kind":"structured_evidence","visible":true}},{"name":"unavailable","input":{"tool":"context_for_task","state":"unavailable"},"expected_output":{"kind":"visible_degradation","visible":true}}]}`)
	entries := []archiveEntry{{name: hostBinaryName(), contents: binary}}
	entries = append(entries, archiveEntry{name: "clients/conformance/client-bundle.v1.json", contents: bundle})
	for _, client := range []string{"opencode", "claude-code", "codex", "cursor"} {
		entries = append(entries, archiveEntry{name: "clients/" + client + "/package.v1.json", contents: packageManifest})
	}
	return entries
}

func writeArchive(t *testing.T, path string, zip bool, entries []archiveEntry) []byte {
	t.Helper()
	if zip {
		t.Skip("windows archive fixture unavailable on this host")
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		kind := entry.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		header := &tar.Header{Name: entry.name, Typeflag: kind, Mode: 0o755, Size: int64(len(entry.contents)), Linkname: entry.link}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(entry.contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return readTestFile(t, path)
}

func writeChecksums(t *testing.T, release string, checksums map[string]string) {
	t.Helper()
	names := make([]string, 0, len(checksums))
	for name := range checksums {
		names = append(names, name)
	}
	sort.Strings(names)
	contents := make([]byte, 0, len(names)*80)
	for _, name := range names {
		contents = append(contents, []byte(checksums[name]+"  "+name+"\n")...)
	}
	writeTestFile(t, filepath.Join(release, "SHA256SUMS"), contents)
}

func hostBinaryName() string {
	if runtime.GOOS == "windows" {
		return "acr-mcp.exe"
	}
	return "acr-mcp"
}
func hostArchivePath(t *testing.T, release string) string {
	t.Helper()
	return filepath.Join(release, ArtifactName(Target{Product: "acr-mcp", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, "1.2.3"))
}
func rewriteHostArchive(t *testing.T, release string, entries []archiveEntry) {
	t.Helper()
	writeArchive(t, hostArchivePath(t, release), false, entries)
}
func digestHex(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}
func writeTestFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
}
func appendTestFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}
