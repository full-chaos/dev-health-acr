package releasebuild

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/mcpclientfixtures"
)

const (
	clientBundleIdentityPath   = "clients/conformance/bundle-identity.v1.json"
	clientBundleIdentitySchema = "client_bundle_identity.v1"
)

type clientAsset struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   int64  `json:"mode"`
	Source string `json:"-"`
}

type clientBundleIdentity struct {
	SchemaVersion         string        `json:"schema_version"`
	BundleVersion         string        `json:"bundle_version"`
	MinimumSidecarVersion string        `json:"minimum_sidecar_version"`
	Release               Identity      `json:"release"`
	Assets                []clientAsset `json:"assets"`
}

type clientAssets struct {
	assets   []clientAsset
	identity []byte
}

type archiveMember struct {
	Name       string
	SourcePath string
	Contents   []byte
	Mode       int64
}

func loadClientAssets(sourceDir string, release Identity) (clientAssets, error) {
	if sourceDir == "" {
		return clientAssets{}, fmt.Errorf("release source directory is required")
	}
	root, err := filepath.Abs(sourceDir)
	if err != nil {
		return clientAssets{}, fmt.Errorf("resolve release source directory: %w", err)
	}
	bundlePath := filepath.Join(root, "clients", "conformance", "client-bundle.v1.json")
	bundle, err := mcpclientfixtures.LoadClientBundle(bundlePath)
	if err != nil {
		return clientAssets{}, fmt.Errorf("validate source client bundle: %w", err)
	}
	if err := mcpclientfixtures.ValidateClientPackageRoots(root, bundle); err != nil {
		return clientAssets{}, fmt.Errorf("validate source client packages: %w", err)
	}
	assets, err := collectClientAssets(root, bundle)
	if err != nil {
		return clientAssets{}, err
	}
	identity, err := json.Marshal(clientBundleIdentity{
		SchemaVersion:         clientBundleIdentitySchema,
		BundleVersion:         bundle.BundleVersion,
		MinimumSidecarVersion: bundle.MinimumSidecarVersion,
		Release:               release,
		Assets:                assets,
	})
	if err != nil {
		return clientAssets{}, fmt.Errorf("marshal client bundle identity: %w", err)
	}
	return clientAssets{assets: assets, identity: identity}, nil
}

func collectClientAssets(root string, bundle mcpclientfixtures.ClientBundle) ([]clientAsset, error) {
	paths := []string{filepath.Join(root, "clients", "conformance", "client-bundle.v1.json")}
	clients := append([]string(nil), bundle.SupportedClients...)
	sort.Strings(clients)
	for _, client := range clients {
		dir := filepath.Join(root, "clients", client)
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			paths = append(paths, path)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk source client package: %w", err)
		}
	}
	sort.Strings(paths)
	assets := make([]clientAsset, 0, len(paths))
	for _, source := range paths {
		asset, err := sourceClientAsset(root, source)
		if err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	return assets, nil
}

func sourceClientAsset(root, source string) (clientAsset, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return clientAsset{}, fmt.Errorf("inspect source client asset: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return clientAsset{}, fmt.Errorf("source client asset is not a regular file")
	}
	path, err := filepath.Rel(root, source)
	if err != nil || !safeArchivePath(filepath.ToSlash(path)) {
		return clientAsset{}, fmt.Errorf("source client asset path is unsafe")
	}
	mode, err := safeClientMode(info.Mode())
	if err != nil {
		return clientAsset{}, err
	}
	digest, err := sha256File(source)
	if err != nil {
		return clientAsset{}, fmt.Errorf("hash source client asset: %w", err)
	}
	return clientAsset{Path: filepath.ToSlash(path), SHA256: digest, Mode: mode, Source: source}, nil
}

func safeClientMode(mode os.FileMode) (int64, error) {
	switch mode.Perm() {
	case 0o644, 0o755:
		return int64(mode.Perm()), nil
	default:
		return 0, fmt.Errorf("source client asset has unsafe mode %04o", mode.Perm())
	}
}

func (assets clientAssets) members() []archiveMember {
	members := make([]archiveMember, 0, len(assets.assets)+1)
	for _, asset := range assets.assets {
		members = append(members, archiveMember{Name: asset.Path, SourcePath: asset.Source, Mode: asset.Mode})
	}
	members = append(members, archiveMember{Name: clientBundleIdentityPath, Contents: assets.identity, Mode: 0o644})
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
	return members
}

func (member archiveMember) Size() (int64, error) {
	if member.SourcePath == "" {
		return int64(len(member.Contents)), nil
	}
	info, err := os.Lstat(member.SourcePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return 0, fmt.Errorf("archive source member is unsafe")
	}
	return info.Size(), nil
}

func (member archiveMember) CopyTo(destination io.Writer, size int64) error {
	var source io.Reader = bytes.NewReader(member.Contents)
	var file *os.File
	if member.SourcePath != "" {
		opened, err := os.Open(member.SourcePath)
		if err != nil {
			return fmt.Errorf("open archive source member: %w", err)
		}
		file = opened
		defer file.Close()
		source = file
	}
	copied, err := io.Copy(destination, source)
	if err != nil {
		return fmt.Errorf("copy archive source member: %w", err)
	}
	if copied != size {
		return fmt.Errorf("copy archive source member: copied %d bytes, want %d", copied, size)
	}
	return nil
}
