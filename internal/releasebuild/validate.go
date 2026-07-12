package releasebuild

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/version"
)

var (
	fullCommitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Hex     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func (i Identity) Validate() error {
	if !version.IsCanonical(i.Version) {
		return fmt.Errorf("version must be canonical semantic version without v prefix")
	}
	if !fullCommitSHA.MatchString(i.Commit) {
		return fmt.Errorf("commit must be a lowercase full 40-character SHA")
	}
	parsed, err := time.Parse("2006-01-02T15:04:05Z", i.Date)
	if err != nil || parsed.Location() != time.UTC || parsed.Format("2006-01-02T15:04:05Z") != i.Date {
		return fmt.Errorf("date must be a UTC RFC3339 timestamp with whole-second precision")
	}
	return nil
}

func Verify(dir string) error {
	manifest, err := readManifest(dir)
	if err != nil {
		return err
	}
	if err := validateManifest(manifest); err != nil {
		return err
	}
	checksums, err := readChecksums(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		return err
	}
	if len(checksums) != len(manifest.Artifacts) {
		return fmt.Errorf("checksum count = %d, want %d", len(checksums), len(manifest.Artifacts))
	}
	for _, artifact := range manifest.Artifacts {
		checksum, found := checksums[artifact.Name]
		if !found || checksum != artifact.SHA256 {
			return fmt.Errorf("checksum declaration mismatch for %s", artifact.Name)
		}
		actual, err := sha256File(filepath.Join(dir, artifact.Name))
		if err != nil {
			return err
		}
		if actual != artifact.SHA256 {
			return fmt.Errorf("checksum mismatch for %s", artifact.Name)
		}
	}
	return nil
}

func readManifest(dir string) (Manifest, error) {
	if dir == "" {
		return Manifest{}, fmt.Errorf("release directory is required")
	}
	contents, err := os.ReadFile(filepath.Join(dir, "release-manifest.json"))
	if err != nil {
		return Manifest{}, fmt.Errorf("read release manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode release manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Manifest{}, fmt.Errorf("release manifest contains trailing data")
	}
	return manifest, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != manifestSchemaVersion {
		return fmt.Errorf("unsupported release manifest schema %q", manifest.SchemaVersion)
	}
	if err := (Identity{Version: manifest.Version, Commit: manifest.Commit, Date: manifest.Date}).Validate(); err != nil {
		return fmt.Errorf("invalid release manifest identity: %w", err)
	}
	expected := make(map[string]Target, len(Matrix()))
	for _, target := range Matrix() {
		expected[ArtifactName(target, manifest.Version)] = target
	}
	if len(manifest.Artifacts) != len(expected) {
		return fmt.Errorf("manifest artifact count = %d, want %d", len(manifest.Artifacts), len(expected))
	}
	for _, artifact := range manifest.Artifacts {
		if !safeFileName(artifact.Name) {
			return fmt.Errorf("invalid artifact name %q", artifact.Name)
		}
		target, found := expected[artifact.Name]
		if !found {
			return fmt.Errorf("unexpected or duplicate artifact %q", artifact.Name)
		}
		if artifact.Product != target.Product || artifact.GOOS != target.GOOS || artifact.GOARCH != target.GOARCH || !sha256Hex.MatchString(artifact.SHA256) {
			return fmt.Errorf("invalid artifact metadata for %s", artifact.Name)
		}
		delete(expected, artifact.Name)
	}
	if len(expected) != 0 {
		return fmt.Errorf("manifest is missing required artifacts")
	}
	return nil
}

func readChecksums(path string) (map[string]string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read checksums: %w", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(contents), "\n"), "\n")
	checksums := make(map[string]string, len(lines))
	names := make([]string, 0, len(lines))
	for _, line := range lines {
		if len(line) < 67 || line[64:66] != "  " {
			return nil, fmt.Errorf("invalid checksum entry %q", line)
		}
		checksum, name := line[:64], line[66:]
		if !sha256Hex.MatchString(checksum) || !safeFileName(name) {
			return nil, fmt.Errorf("invalid checksum entry %q", line)
		}
		if _, exists := checksums[name]; exists {
			return nil, fmt.Errorf("duplicate checksum entry %q", name)
		}
		checksums[name] = checksum
		names = append(names, name)
	}
	if !sort.StringsAreSorted(names) {
		return nil, fmt.Errorf("checksums are not sorted")
	}
	return checksums, nil
}

func safeFileName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.Contains(name, string(filepath.Separator)) && !strings.Contains(name, "\\")
}

func writeManifest(dir string, manifest Manifest) error {
	contents, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal release manifest: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, "release-manifest.json"), append(contents, '\n'), 0o644)
}
