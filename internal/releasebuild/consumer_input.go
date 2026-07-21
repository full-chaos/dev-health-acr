package releasebuild

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type consumerInput struct {
	manifest  Manifest
	artifacts map[string]Artifact
}

func loadConsumerInput(dir string) (consumerInput, error) {
	if dir == "" {
		return consumerInput{}, fmt.Errorf("release directory is required")
	}
	manifestRaw, err := readRegularFile(filepath.Join(dir, "release-manifest.json"), maxMetadataBytes)
	if err != nil {
		return consumerInput{}, fmt.Errorf("read release manifest: %w", err)
	}
	manifest, err := parseConsumerManifest(manifestRaw)
	if err != nil {
		return consumerInput{}, err
	}
	checksumsRaw, err := readRegularFile(filepath.Join(dir, "SHA256SUMS"), maxMetadataBytes)
	if err != nil {
		return consumerInput{}, fmt.Errorf("read checksums: %w", err)
	}
	checksums, err := parseConsumerChecksums(checksumsRaw)
	if err != nil {
		return consumerInput{}, err
	}
	artifacts := make(map[string]Artifact, len(manifest.Artifacts))
	allowed := map[string]struct{}{"release-manifest.json": {}, "SHA256SUMS": {}}
	for _, artifact := range manifest.Artifacts {
		artifacts[artifact.Name] = artifact
		allowed[artifact.Name] = struct{}{}
		allowed[artifact.Name+".spdx.json"] = struct{}{}
		if checksum, found := checksums[artifact.Name]; !found || checksum != artifact.SHA256 {
			return consumerInput{}, fmt.Errorf("archive checksum declaration is invalid")
		}
	}
	for name := range checksums {
		if _, ok := allowed[name]; !ok {
			return consumerInput{}, fmt.Errorf("checksum contains an unexpected file")
		}
		if _, err := readRegularFile(filepath.Join(dir, name), maxArchiveBytes); err != nil {
			return consumerInput{}, fmt.Errorf("checksum file is missing or unsafe: %w", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return consumerInput{}, fmt.Errorf("read release directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			return consumerInput{}, fmt.Errorf("release directory contains an unsafe entry")
		}
		if _, ok := allowed[entry.Name()]; !ok {
			return consumerInput{}, fmt.Errorf("release directory contains an unexpected file")
		}
		if _, ok := checksums[entry.Name()]; !ok && entry.Name() != "SHA256SUMS" {
			return consumerInput{}, fmt.Errorf("release file is missing a checksum")
		}
	}
	return consumerInput{manifest: manifest, artifacts: artifacts}, nil
}

func parseConsumerManifest(raw []byte) (Manifest, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return Manifest{}, fmt.Errorf("invalid release manifest: %w", err)
	}
	var object map[string]json.RawMessage
	if err := decodeExactJSON(raw, &object); err != nil {
		return Manifest{}, fmt.Errorf("invalid release manifest")
	}
	if err := exactObjectKeys(object, "schema_version", "version", "commit", "date", "artifacts"); err != nil {
		return Manifest{}, fmt.Errorf("invalid release manifest")
	}
	var manifest Manifest
	if err := decodeExactJSON(raw, &manifest); err != nil || validateManifest(manifest) != nil {
		return Manifest{}, fmt.Errorf("invalid release manifest")
	}
	seenNames := make(map[string]struct{}, len(manifest.Artifacts))
	seenDigests := make(map[string]struct{}, len(manifest.Artifacts))
	var rawArtifacts []json.RawMessage
	if err := decodeExactJSON(object["artifacts"], &rawArtifacts); err != nil || len(rawArtifacts) != len(manifest.Artifacts) {
		return Manifest{}, fmt.Errorf("invalid release manifest")
	}
	for index, artifact := range manifest.Artifacts {
		var artifactObject map[string]json.RawMessage
		if err := decodeExactJSON(rawArtifacts[index], &artifactObject); err != nil || exactObjectKeys(artifactObject, "name", "product", "goos", "goarch", "sha256") != nil {
			return Manifest{}, fmt.Errorf("invalid release manifest")
		}
		if _, ok := seenNames[artifact.Name]; ok {
			return Manifest{}, fmt.Errorf("duplicate artifact")
		}
		if _, ok := seenDigests[artifact.SHA256]; ok {
			return Manifest{}, fmt.Errorf("duplicate artifact digest")
		}
		seenNames[artifact.Name], seenDigests[artifact.SHA256] = struct{}{}, struct{}{}
	}
	return manifest, nil
}

func parseConsumerChecksums(raw []byte) (map[string]string, error) {
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return nil, fmt.Errorf("invalid checksums")
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	checksums, names := make(map[string]string, len(lines)), make([]string, 0, len(lines))
	for _, line := range lines {
		if len(line) < 67 || line[64:66] != "  " || strings.Count(line, "  ") != 1 {
			return nil, fmt.Errorf("invalid checksum entry")
		}
		digest, name := line[:64], line[66:]
		if !sha256Hex.MatchString(digest) || !safeConsumerName(name) {
			return nil, fmt.Errorf("invalid checksum entry")
		}
		if _, exists := checksums[name]; exists {
			return nil, fmt.Errorf("duplicate checksum entry")
		}
		checksums[name], names = digest, append(names, name)
	}
	if !sort.StringsAreSorted(names) {
		return nil, fmt.Errorf("checksums are not sorted")
	}
	return checksums, nil
}

func safeConsumerName(name string) bool {
	return safeFileName(name) && strings.ToLower(name) == name && !strings.Contains(name, ":")
}

func readRegularFile(path string, limit int64) ([]byte, error) {
	file, err := openNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || opened.Size() > limit {
		return nil, fmt.Errorf("file is unsafe or exceeds the size limit")
	}
	contents, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > limit {
		return nil, fmt.Errorf("file exceeds the size limit")
	}
	return contents, nil
}

func digestFile(path string, limit int64) (string, error) {
	contents, err := readRegularFile(path, limit)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:]), nil
}

func decodeExactJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing data")
	}
	return nil
}

func exactObjectKeys(object map[string]json.RawMessage, keys ...string) error {
	if len(object) != len(keys) {
		return fmt.Errorf("unexpected fields")
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return fmt.Errorf("missing field")
		}
	}
	return nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	return scanJSONValue(decoder)
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return fmt.Errorf("object key")
				}
				if _, exists := seen[name]; exists {
					return fmt.Errorf("duplicate key")
				}
				seen[name] = struct{}{}
				if err := scanJSONValue(decoder); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := scanJSONValue(decoder); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		}
	}
	return nil
}
