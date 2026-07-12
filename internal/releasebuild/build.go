package releasebuild

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var archiveEpoch = time.Unix(0, 0).UTC()

type Builder struct {
	compiler Compiler
}

func NewBuilder(compiler Compiler) Builder {
	return Builder{compiler: compiler}
}

func (b Builder) Build(ctx context.Context, request Request) (Manifest, error) {
	if err := request.Identity.Validate(); err != nil {
		return Manifest{}, err
	}
	if b.compiler == nil {
		return Manifest{}, fmt.Errorf("release compiler is required")
	}
	if err := validateOutputLocation(request.SourceDir, request.OutputDir); err != nil {
		return Manifest{}, err
	}
	if err := prepareOutput(request.OutputDir); err != nil {
		return Manifest{}, err
	}
	staging, err := os.MkdirTemp("", "acr-releasebuild-")
	if err != nil {
		return Manifest{}, fmt.Errorf("create staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	manifest := Manifest{SchemaVersion: manifestSchemaVersion, Version: request.Identity.Version, Commit: request.Identity.Commit, Date: request.Identity.Date}
	for _, target := range Matrix() {
		binaryPath := filepath.Join(staging, target.String()+binaryExtension(target))
		compile := CompileRequest{SourceDir: request.SourceDir, OutputPath: binaryPath, Target: target, Identity: request.Identity, BuildFlags: reproducibleBuildFlags}
		if err := b.compiler.Compile(ctx, compile); err != nil {
			return Manifest{}, fmt.Errorf("compile %s: %w", target.String(), err)
		}
		artifact, err := packageBinary(request.OutputDir, binaryPath, target, request.Identity.Version)
		if err != nil {
			return Manifest{}, err
		}
		manifest.Artifacts = append(manifest.Artifacts, artifact)
	}
	if err := writeMetadata(request.OutputDir, manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateOutputLocation(sourceDir, outputDir string) error {
	if sourceDir == "" || outputDir == "" {
		return nil
	}
	source, err := filepath.Abs(sourceDir)
	if err != nil {
		return fmt.Errorf("resolve source directory: %w", err)
	}
	output, err := filepath.Abs(outputDir)
	if err != nil {
		return fmt.Errorf("resolve release output directory: %w", err)
	}
	relative, err := filepath.Rel(source, output)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil
	}
	if relative != ".tmp" && !strings.HasPrefix(relative, ".tmp"+string(filepath.Separator)) {
		return fmt.Errorf("release output inside the source directory must be under .tmp")
	}
	return nil
}

func ArtifactName(target Target, version string) string {
	extension := ".tar.gz"
	if target.GOOS == "windows" {
		extension = ".zip"
	}
	return fmt.Sprintf("%s_%s_%s_%s%s", target.Product, version, target.GOOS, target.GOARCH, extension)
}

func binaryExtension(target Target) string {
	if target.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func prepareOutput(dir string) error {
	if dir == "" {
		return fmt.Errorf("release output directory is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create release output directory: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read release output directory: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("release output directory must be empty")
	}
	return nil
}

func packageBinary(outputDir, binaryPath string, target Target, version string) (Artifact, error) {
	name := ArtifactName(target, version)
	path := filepath.Join(outputDir, name)
	if target.GOOS == "windows" {
		if err := writeZIP(path, binaryPath, target.Product+".exe"); err != nil {
			return Artifact{}, err
		}
	} else if err := writeTarGZ(path, binaryPath, target.Product); err != nil {
		return Artifact{}, err
	}
	checksum, err := sha256File(path)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{Name: name, Product: target.Product, GOOS: target.GOOS, GOARCH: target.GOARCH, SHA256: checksum}, nil
}

func writeTarGZ(destination, binaryPath, archiveName string) (err error) {
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create archive %s: %w", destination, err)
	}
	defer func() { err = closeWithError(err, output, "archive") }()
	gzipWriter, err := gzip.NewWriterLevel(output, gzip.BestCompression)
	if err != nil {
		return fmt.Errorf("create gzip writer: %w", err)
	}
	gzipWriter.Header.ModTime = archiveEpoch
	gzipWriter.Header.OS = 255
	defer func() { err = closeWithError(err, gzipWriter, "gzip archive") }()
	tarWriter := tar.NewWriter(gzipWriter)
	defer func() { err = closeWithError(err, tarWriter, "tar archive") }()
	return writeArchiveEntry(tarWriter, binaryPath, archiveName)
}

func writeZIP(destination, binaryPath, archiveName string) (err error) {
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create archive %s: %w", destination, err)
	}
	defer func() { err = closeWithError(err, output, "archive") }()
	writer := zip.NewWriter(output)
	defer func() { err = closeWithError(err, writer, "zip archive") }()
	info, err := os.Stat(binaryPath)
	if err != nil {
		return fmt.Errorf("stat binary: %w", err)
	}
	header := &zip.FileHeader{Name: archiveName, Method: zip.Deflate}
	// ZIP's DOS timestamp cannot represent the Unix epoch, so use its earliest value.
	header.Modified = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	header.SetMode(0o755)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create zip entry: %w", err)
	}
	return copyBinary(entry, binaryPath, info.Size())
}

func writeArchiveEntry(writer *tar.Writer, binaryPath, archiveName string) error {
	info, err := os.Stat(binaryPath)
	if err != nil {
		return fmt.Errorf("stat binary: %w", err)
	}
	header := &tar.Header{Name: archiveName, Mode: 0o755, Size: info.Size(), ModTime: archiveEpoch, Format: tar.FormatUSTAR}
	if err := writer.WriteHeader(header); err != nil {
		return fmt.Errorf("write tar header: %w", err)
	}
	return copyBinary(writer, binaryPath, info.Size())
}

func copyBinary(destination io.Writer, binaryPath string, size int64) error {
	input, err := os.Open(binaryPath)
	if err != nil {
		return fmt.Errorf("open binary: %w", err)
	}
	defer input.Close()
	if copied, err := io.Copy(destination, input); err != nil {
		return fmt.Errorf("copy binary: %w", err)
	} else if copied != size {
		return fmt.Errorf("copy binary: copied %d bytes, want %d", copied, size)
	}
	return nil
}

func writeMetadata(outputDir string, manifest Manifest) error {
	sort.Slice(manifest.Artifacts, func(i, j int) bool { return manifest.Artifacts[i].Name < manifest.Artifacts[j].Name })
	if err := writeManifest(outputDir, manifest); err != nil {
		return err
	}
	lines := make([]string, 0, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		lines = append(lines, artifact.SHA256+"  "+artifact.Name)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "SHA256SUMS"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return fmt.Errorf("write checksums: %w", err)
	}
	return nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s for checksum: %w", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("checksum %s: %w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func closeWithError(existing error, closer io.Closer, name string) error {
	if closeErr := closer.Close(); closeErr != nil && existing == nil {
		return fmt.Errorf("close %s: %w", name, closeErr)
	}
	return existing
}
