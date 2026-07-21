package releasebuild

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/mcpclientfixtures"
)

type archiveLayout map[string]int64

func verifyArchiveLayout(path string, target Target, release Identity) error {
	staging, err := os.MkdirTemp("", "acr-releaseverify-")
	if err != nil {
		return fmt.Errorf("create archive verification staging: %w", err)
	}
	defer os.RemoveAll(staging)
	layout, err := readArchiveLayout(path, target.GOOS == "windows", staging)
	if err != nil {
		return err
	}
	binary := target.Product + binaryExtension(target)
	if target.Product == "acr-api" {
		if len(layout) != 1 || layout[binary] != 0o755 {
			return fmt.Errorf("acr-api archive layout is invalid")
		}
		return nil
	}
	return verifyClientArchive(staging, layout, binary, release)
}

func readArchiveLayout(path string, zipArchive bool, staging string) (archiveLayout, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open archive for layout validation: %w", err)
	}
	defer file.Close()
	layout := archiveLayout{}
	if zipArchive {
		info, err := file.Stat()
		if err != nil {
			return nil, fmt.Errorf("stat archive for layout validation: %w", err)
		}
		reader, err := zip.NewReader(file, info.Size())
		if err != nil {
			return nil, fmt.Errorf("open zip archive for layout validation: %w", err)
		}
		for _, member := range reader.File {
			if member.FileInfo().Mode()&os.ModeType != 0 {
				return nil, fmt.Errorf("archive contains an unsupported entry type")
			}
			stream, err := member.Open()
			if err != nil {
				return nil, fmt.Errorf("open zip archive member: %w", err)
			}
			err = layout.add(staging, member.Name, member.FileInfo().Mode(), stream)
			closeErr := stream.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, fmt.Errorf("close zip archive member: %w", closeErr)
			}
		}
		return layout, nil
	}
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("open tar archive for layout validation: %w", err)
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return layout, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read tar archive member: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("archive contains an unsupported entry type")
		}
		if err := layout.add(staging, header.Name, os.FileMode(header.Mode), reader); err != nil {
			return nil, err
		}
	}
}

func (layout archiveLayout) add(staging, name string, mode os.FileMode, contents io.Reader) error {
	if !safeArchivePath(name) {
		return fmt.Errorf("archive contains an unsafe client path")
	}
	safeMode, err := safeClientMode(mode)
	if err != nil {
		return fmt.Errorf("archive member has unsafe mode: %w", err)
	}
	if _, exists := layout[name]; exists {
		return fmt.Errorf("archive contains duplicate paths")
	}
	layout[name] = safeMode
	if !strings.HasPrefix(name, "clients/") {
		_, err := io.Copy(io.Discard, contents)
		return err
	}
	path := filepath.Join(staging, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create client verification path: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(safeMode))
	if err != nil {
		return fmt.Errorf("create client verification asset: %w", err)
	}
	_, copyErr := io.Copy(file, contents)
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("copy client verification asset: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close client verification asset: %w", closeErr)
	}
	return nil
}

func verifyClientArchive(staging string, layout archiveLayout, binary string, release Identity) error {
	identityPath := filepath.Join(staging, filepath.FromSlash(clientBundleIdentityPath))
	identity, err := loadClientBundleIdentity(identityPath)
	if err != nil {
		return err
	}
	bundle, err := mcpclientfixtures.LoadClientBundle(filepath.Join(staging, "clients", "conformance", "client-bundle.v1.json"))
	if err != nil {
		return fmt.Errorf("validate archived client bundle: %w", err)
	}
	if err := mcpclientfixtures.ValidateClientPackageRoots(staging, bundle); err != nil {
		return fmt.Errorf("validate archived client packages: %w", err)
	}
	if identity.BundleVersion != bundle.BundleVersion || identity.MinimumSidecarVersion != bundle.MinimumSidecarVersion || identity.Release != release {
		return fmt.Errorf("client bundle identity does not match release")
	}
	expected := map[string]int64{binary: 0o755, clientBundleIdentityPath: 0o644}
	for _, asset := range identity.Assets {
		if !safeArchivePath(asset.Path) || asset.Path == clientBundleIdentityPath || !strings.HasPrefix(asset.Path, "clients/") || !sha256Hex.MatchString(asset.SHA256) {
			return fmt.Errorf("client bundle identity contains an unsafe asset")
		}
		if _, exists := expected[asset.Path]; exists {
			return fmt.Errorf("client bundle identity contains duplicate assets")
		}
		if _, err := safeClientMode(os.FileMode(asset.Mode)); err != nil {
			return fmt.Errorf("client bundle identity contains an unsafe mode")
		}
		expected[asset.Path] = asset.Mode
		digest, err := digestFile(filepath.Join(staging, filepath.FromSlash(asset.Path)), maxMetadataBytes)
		if err != nil || digest != asset.SHA256 {
			return fmt.Errorf("client bundle asset does not match identity")
		}
	}
	if len(layout) != len(expected) {
		return fmt.Errorf("acr-mcp archive client layout is invalid")
	}
	for name, mode := range expected {
		if layout[name] != mode {
			return fmt.Errorf("acr-mcp archive client layout is invalid")
		}
	}
	return nil
}

func loadClientBundleIdentity(path string) (clientBundleIdentity, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return clientBundleIdentity{}, fmt.Errorf("read client bundle identity: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var identity clientBundleIdentity
	if err := decoder.Decode(&identity); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return clientBundleIdentity{}, fmt.Errorf("decode client bundle identity")
	}
	if identity.SchemaVersion != clientBundleIdentitySchema || identity.BundleVersion == "" || identity.MinimumSidecarVersion == "" || identity.Release.Validate() != nil || !sort.SliceIsSorted(identity.Assets, func(i, j int) bool { return identity.Assets[i].Path < identity.Assets[j].Path }) {
		return clientBundleIdentity{}, fmt.Errorf("invalid client bundle identity")
	}
	return identity, nil
}
