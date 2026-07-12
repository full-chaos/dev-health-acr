package releasebuild

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBuild_produces_identical_release_trees_when_inputs_match(t *testing.T) {
	// Given
	identity := testIdentity()
	first := t.TempDir()
	second := t.TempDir()
	builder := NewBuilder(CompilerFunc(writeTestBinary))

	// When
	firstManifest, err := builder.Build(context.Background(), Request{OutputDir: first, Identity: identity})
	if err != nil {
		t.Fatalf("build first release: %v", err)
	}
	secondManifest, err := builder.Build(context.Background(), Request{OutputDir: second, Identity: identity})
	if err != nil {
		t.Fatalf("build second release: %v", err)
	}

	// Then
	if !reflect.DeepEqual(firstManifest, secondManifest) {
		t.Fatalf("manifests differ: %#v != %#v", firstManifest, secondManifest)
	}
	assertSameReleaseTree(t, first, second, firstManifest)
	if err := Verify(first); err != nil {
		t.Fatalf("verify first release: %v", err)
	}
}

func TestVerify_rejects_tampered_artifact(t *testing.T) {
	// Given
	dir := t.TempDir()
	builder := NewBuilder(CompilerFunc(writeTestBinary))
	manifest, err := builder.Build(context.Background(), Request{OutputDir: dir, Identity: testIdentity()})
	if err != nil {
		t.Fatalf("build release: %v", err)
	}
	artifact := filepath.Join(dir, manifest.Artifacts[0].Name)
	if err := os.WriteFile(artifact, []byte("tampered"), 0o644); err != nil {
		t.Fatalf("tamper artifact: %v", err)
	}

	// When
	err = Verify(dir)

	// Then
	if err == nil {
		t.Fatal("Verify() error = nil, want tamper rejection")
	}
}

func TestBuild_normalizes_tar_metadata(t *testing.T) {
	// Given
	dir := t.TempDir()
	builder := NewBuilder(CompilerFunc(writeTestBinary))
	manifest, err := builder.Build(context.Background(), Request{OutputDir: dir, Identity: testIdentity()})
	if err != nil {
		t.Fatalf("build release: %v", err)
	}
	artifact := findArtifact(t, manifest, "acr-api", "linux", "amd64")

	// When
	header := tarHeader(t, filepath.Join(dir, artifact.Name))

	// Then
	if got, want := header.Name, "acr-api"; got != want {
		t.Errorf("tar name = %q, want %q", got, want)
	}
	if !header.ModTime.Equal(time.Unix(0, 0).UTC()) {
		t.Errorf("tar modtime = %s, want epoch", header.ModTime)
	}
	if header.Mode != 0o755 || header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" {
		t.Errorf("tar metadata = %#v, want normalized mode/ownership", header)
	}
	zipArtifact := findArtifact(t, manifest, "acr-api", "windows", "amd64")
	zipHeader := zipHeader(t, filepath.Join(dir, zipArtifact.Name))
	if zipHeader.Name != "acr-api.exe" || zipHeader.Mode().Perm() != 0o755 || !zipHeader.Modified.Equal(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("zip metadata = %#v, want normalized entry", zipHeader.FileHeader)
	}
}

func TestBuild_declares_full_supported_matrix_and_injected_identity(t *testing.T) {
	// Given
	dir := t.TempDir()
	compiler := &recordingCompiler{}
	identity := testIdentity()
	builder := NewBuilder(compiler)

	// When
	manifest, err := builder.Build(context.Background(), Request{OutputDir: dir, Identity: identity})
	if err != nil {
		t.Fatalf("build release: %v", err)
	}

	// Then
	if got, want := len(manifest.Artifacts), 10; got != want {
		t.Fatalf("artifact count = %d, want %d", got, want)
	}
	for _, target := range Matrix() {
		name := ArtifactName(target, identity.Version)
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("artifact %s: %v", name, err)
		}
	}
	if got, want := len(compiler.requests), len(Matrix()); got != want {
		t.Fatalf("compiler calls = %d, want %d", got, want)
	}
	for _, request := range compiler.requests {
		if request.Identity != identity {
			t.Errorf("compiler identity = %#v, want %#v", request.Identity, identity)
		}
		if request.CGOEnabled || request.BuildFlags != "-trimpath -buildvcs=false -mod=readonly" || !strings.Contains(request.LinkerFlags(), "-buildid= -X ") {
			t.Errorf("compiler request = %#v, want reproducible build settings", request)
		}
	}
}

func testIdentity() Identity {
	return Identity{Version: "1.2.3", Commit: "0123456789abcdef0123456789abcdef01234567", Date: "2026-07-12T13:14:15Z"}
}

func writeTestBinary(_ context.Context, request CompileRequest) error {
	return os.WriteFile(request.OutputPath, []byte(request.Identity.Version+"\n"+request.Identity.Commit+"\n"+request.Identity.Date+"\n"+request.Target.String()), 0o755)
}

type recordingCompiler struct {
	requests []CompileRequest
}

func (c *recordingCompiler) Compile(ctx context.Context, request CompileRequest) error {
	c.requests = append(c.requests, request)
	return writeTestBinary(ctx, request)
}

func assertSameReleaseTree(t *testing.T, first, second string, manifest Manifest) {
	t.Helper()
	names := []string{"SHA256SUMS", "release-manifest.json"}
	for _, artifact := range manifest.Artifacts {
		names = append(names, artifact.Name)
	}
	for _, name := range names {
		left, err := os.ReadFile(filepath.Join(first, name))
		if err != nil {
			t.Fatalf("read first %s: %v", name, err)
		}
		right, err := os.ReadFile(filepath.Join(second, name))
		if err != nil {
			t.Fatalf("read second %s: %v", name, err)
		}
		if !bytes.Equal(left, right) {
			t.Errorf("%s differs across identical builds", name)
		}
	}
}

func findArtifact(t *testing.T, manifest Manifest, product, goos, goarch string) Artifact {
	t.Helper()
	for _, artifact := range manifest.Artifacts {
		if artifact.Product == product && artifact.GOOS == goos && artifact.GOARCH == goarch {
			return artifact
		}
	}
	t.Fatalf("missing %s %s/%s artifact", product, goos, goarch)
	return Artifact{}
}

func tarHeader(t *testing.T, path string) *tar.Header {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("open gzip: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	header, err := tar.NewReader(reader).Next()
	if err != nil {
		t.Fatalf("read tar header: %v", err)
	}
	return header
}

func zipHeader(t *testing.T, path string) *zip.File {
	t.Helper()
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	t.Cleanup(func() { _ = archive.Close() })
	if len(archive.File) != 1 {
		t.Fatalf("zip entry count = %d, want 1", len(archive.File))
	}
	return archive.File[0]
}
