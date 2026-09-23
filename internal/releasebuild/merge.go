package releasebuild

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const fragmentFile = "release-fragment.json"

// ParseTargets expands "goos/goarch" pairs (comma separated) into the matrix
// targets for both products. Every pair must exist in Matrix().
func ParseTargets(spec string) ([]Target, error) {
	var pairs []string
	for _, part := range strings.Split(spec, ",") {
		if part = strings.TrimSpace(part); part != "" {
			pairs = append(pairs, part)
		}
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("at least one goos/goarch platform is required")
	}
	var selected []Target
	for _, pair := range pairs {
		found := false
		for _, target := range Matrix() {
			if target.GOOS+"/"+target.GOARCH == pair {
				selected = append(selected, target)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("platform %q is not in the release matrix", pair)
		}
	}
	return selected, nil
}

func selectTargets(only []Target) ([]Target, error) {
	var selected []Target
	for _, want := range only {
		found := false
		for _, target := range Matrix() {
			if target == want {
				selected = append(selected, target)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("target %s is not in the release matrix", want.String())
		}
	}
	return selected, nil
}

func writeFragment(dir string, manifest Manifest) error {
	contents, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal release fragment: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, fragmentFile), append(contents, '\n'), 0o644)
}

func readFragment(dir string) (Manifest, error) {
	contents, err := os.ReadFile(filepath.Join(dir, fragmentFile))
	if err != nil {
		return Manifest{}, fmt.Errorf("read release fragment: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode release fragment in %s: %w", dir, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Manifest{}, fmt.Errorf("release fragment in %s contains trailing data", dir)
	}
	return manifest, nil
}

// Merge combines per-platform fragments into the same release tree (archives,
// release-manifest.json, SHA256SUMS) a single full Build would produce. It
// fails unless the fragments share one identity, cover the whole Matrix()
// exactly once, and the merged tree passes Verify -- which re-hashes every
// archive against the checksum each leg recorded.
func Merge(fragmentDirs []string, outputDir string) (Manifest, error) {
	if len(fragmentDirs) == 0 {
		return Manifest{}, fmt.Errorf("at least one fragment directory is required")
	}
	if err := prepareOutput(outputDir); err != nil {
		return Manifest{}, err
	}
	var merged Manifest
	for index, dir := range fragmentDirs {
		fragment, err := readFragment(dir)
		if err != nil {
			return Manifest{}, err
		}
		if fragment.SchemaVersion != manifestSchemaVersion {
			return Manifest{}, fmt.Errorf("fragment %s has unsupported schema %q", dir, fragment.SchemaVersion)
		}
		if index == 0 {
			merged = Manifest{SchemaVersion: fragment.SchemaVersion, Version: fragment.Version, Commit: fragment.Commit, Date: fragment.Date}
		} else if fragment.Version != merged.Version || fragment.Commit != merged.Commit || fragment.Date != merged.Date {
			return Manifest{}, fmt.Errorf("fragment %s identity differs from the first fragment", dir)
		}
		for _, artifact := range fragment.Artifacts {
			if !safeFileName(artifact.Name) {
				return Manifest{}, fmt.Errorf("invalid artifact name %q", artifact.Name)
			}
			if err := copyFile(filepath.Join(dir, artifact.Name), filepath.Join(outputDir, artifact.Name)); err != nil {
				return Manifest{}, err
			}
			merged.Artifacts = append(merged.Artifacts, artifact)
		}
	}
	if err := validateManifest(merged); err != nil {
		return Manifest{}, fmt.Errorf("merged fragments: %w", err)
	}
	if err := writeMetadata(outputDir, merged); err != nil {
		return Manifest{}, err
	}
	if err := Verify(outputDir); err != nil {
		return Manifest{}, err
	}
	return merged, nil
}

func copyFile(source, destination string) (err error) {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open fragment artifact: %w", err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create merged artifact: %w", err)
	}
	defer func() { err = closeWithError(err, output, "merged artifact") }()
	if _, err := io.Copy(output, input); err != nil {
		return fmt.Errorf("copy %s: %w", source, err)
	}
	return nil
}
