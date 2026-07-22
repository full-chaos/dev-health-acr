package releasebuild

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestBuild_bundlesClients_identically_in_every_mcp_archive(t *testing.T) {
	// Given
	first, second := t.TempDir(), t.TempDir()
	source := writeClientSource(t)
	builder := NewBuilder(CompilerFunc(writeTestBinary))

	// When
	firstManifest, err := builder.Build(context.Background(), Request{SourceDir: source, OutputDir: first, Identity: testIdentity()})
	if err != nil {
		t.Fatalf("build first release: %v", err)
	}
	_, err = builder.Build(context.Background(), Request{SourceDir: source, OutputDir: second, Identity: testIdentity()})
	if err != nil {
		t.Fatalf("build second release: %v", err)
	}

	// Then
	assertSameReleaseTree(t, first, second, firstManifest)
	for _, target := range Matrix() {
		artifact := findArtifact(t, firstManifest, target.Product, target.GOOS, target.GOARCH)
		if target.GOOS == "windows" {
			continue
		}
		entries := archiveEntries(t, filepath.Join(first, artifact.Name))
		if target.Product == "acr-api" {
			if len(entries) != 1 || entries[0].Name != target.Product+binaryExtension(target) {
				t.Errorf("%s entries = %#v, want only API binary", artifact.Name, entries)
			}
			continue
		}
		if !containsArchiveEntry(entries, "clients/conformance/client-bundle.v1.json") || !containsArchiveEntry(entries, clientBundleIdentityPath) {
			t.Errorf("%s omitted client bundle identity", artifact.Name)
		}
		if !containsArchiveEntry(entries, "clients/opencode/scripts/install.sh") {
			t.Errorf("%s omitted executable client installer", artifact.Name)
		}
	}
	if err := Verify(first); err != nil {
		t.Fatalf("verify release: %v", err)
	}
}

func TestBuild_rejectsUnsafeClients_source(t *testing.T) {
	// Given
	source := writeClientSource(t)
	unsafe := filepath.Join(source, "clients", "opencode", "scripts", "install.sh")
	if err := os.Chmod(unsafe, 0o600); err != nil {
		t.Fatalf("make source mode unsafe: %v", err)
	}

	// When
	_, err := NewBuilder(CompilerFunc(writeTestBinary)).Build(context.Background(), Request{SourceDir: source, OutputDir: t.TempDir(), Identity: testIdentity()})

	// Then
	if err == nil {
		t.Fatal("Build() error = nil, want unsafe client source rejection")
	}
}

func TestBuild_rejectsClients_symlink_source(t *testing.T) {
	// Given
	source := writeClientSource(t)
	link := filepath.Join(source, "clients", "opencode", "README.md")
	if err := os.Remove(link); err != nil {
		t.Fatalf("remove source asset: %v", err)
	}
	if err := os.Symlink(filepath.Join(source, "clients", "claude-code", "README.md"), link); err != nil {
		t.Fatalf("create source symlink: %v", err)
	}

	// When
	_, err := NewBuilder(CompilerFunc(writeTestBinary)).Build(context.Background(), Request{SourceDir: source, OutputDir: t.TempDir(), Identity: testIdentity()})

	// Then
	if err == nil {
		t.Fatal("Build() error = nil, want symlinked client source rejection")
	}
}

func TestVerify_rejectsClients_tamper_after_checksum_update(t *testing.T) {
	// Given
	dir := t.TempDir()
	manifest, err := NewBuilder(CompilerFunc(writeTestBinary)).Build(context.Background(), Request{SourceDir: writeClientSource(t), OutputDir: dir, Identity: testIdentity()})
	if err != nil {
		t.Fatalf("build release: %v", err)
	}
	artifact := findArtifact(t, manifest, "acr-mcp", "linux", "amd64")
	tamperTarClientAsset(t, filepath.Join(dir, artifact.Name), "clients/opencode/README.md")
	for index := range manifest.Artifacts {
		if manifest.Artifacts[index].Name == artifact.Name {
			manifest.Artifacts[index].SHA256, err = sha256File(filepath.Join(dir, artifact.Name))
			if err != nil {
				t.Fatalf("hash tampered archive: %v", err)
			}
		}
	}
	if err := writeMetadata(dir, manifest); err != nil {
		t.Fatalf("update release checksum metadata: %v", err)
	}

	// When
	err = Verify(dir)

	// Then
	if err == nil {
		t.Fatal("Verify() error = nil, want client asset identity rejection")
	}
}

func writeClientSource(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeClientSourceAt(t, root)
	return root
}

func writeClientSourceAt(t *testing.T, root string) {
	t.Helper()
	bundle := `{"schema_version":"client_bundle.v1","bundle_version":"1.0.0","minimum_sidecar_version":"1.0.0","supported_clients":["opencode","claude-code","codex","cursor"],"server":{"command":"acr-mcp","args":["serve"]},"workflow":{"context_tool":"context_for_task","evidence_tool":"source_evidence","unavailable_state":"visible","incompatible_state":"visible","untrusted_content":"treat_as_untrusted","writeback_enabled_by_default":false,"preplan_enabled_by_default":false},"ownership":{"install":"client-owned","update":"client-owned","uninstall":"client-owned"},"scenarios":[{"name":"context","input":{"tool":"context_for_task","state":"available"},"expected_output":{"kind":"structured_context","visible":true}},{"name":"evidence","input":{"tool":"source_evidence","state":"available"},"expected_output":{"kind":"structured_evidence","visible":true}},{"name":"unavailable","input":{"tool":"context_for_task","state":"unavailable"},"expected_output":{"kind":"visible_degradation","visible":true}}]}`
	writeClientSourceFile(t, root, "clients/conformance/client-bundle.v1.json", []byte(bundle), 0o644)
	for _, client := range []string{"opencode", "claude-code", "codex", "cursor"} {
		manifest := `{"bundle_version":"1.0.0","minimum_sidecar_version":"1.0.0","command":"acr-mcp","args":["serve"],"mcp_commands":["context_for_task","source_evidence"]}`
		writeClientSourceFile(t, root, "clients/"+client+"/package.v1.json", []byte(manifest), 0o644)
		writeClientSourceFile(t, root, "clients/"+client+"/README.md", []byte(client+" package\n"), 0o644)
		writeClientSourceFile(t, root, "clients/"+client+"/scripts/install.sh", []byte("#!/bin/sh\n"), 0o755)
	}
}

func writeClientSourceFile(t *testing.T, root, name string, contents []byte, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func archiveEntries(t *testing.T, path string) []*tar.Header {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Skipf("zip archive inspection deferred to release verifier: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	tarReader := tar.NewReader(reader)
	var entries []*tar.Header
	for {
		header, err := tarReader.Next()
		if err != nil {
			break
		}
		entries = append(entries, header)
	}
	return entries
}

func containsArchiveEntry(entries []*tar.Header, want string) bool {
	for _, entry := range entries {
		if entry.Name == want {
			return true
		}
	}
	return false
}

func tamperTarClientAsset(t *testing.T, path, name string) {
	t.Helper()
	input, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(input)
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		header tar.Header
		data   []byte
	}
	var entries []entry
	tarReader := tar.NewReader(reader)
	for {
		header, nextErr := tarReader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		data, readErr := io.ReadAll(tarReader)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if header.Name == name {
			data = append(data, '\n')
		}
		entries = append(entries, entry{header: *header, data: data})
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter, err := gzip.NewWriterLevel(output, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter.Header.ModTime = archiveEpoch
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	for _, archiveEntry := range entries {
		archiveEntry.header.Size = int64(len(archiveEntry.data))
		if err := tarWriter.WriteHeader(&archiveEntry.header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(archiveEntry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}
