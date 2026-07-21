package releasebuild

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/full-chaos/dev-health-acr/internal/mcpclientfixtures"
)

const (
	maxArchiveBytes   int64 = 64 << 20
	maxExtractedBytes int64 = 64 << 20
	maxMemberBytes    int64 = 32 << 20
	maxArchiveMembers       = 128
	maxMetadataBytes  int64 = 1 << 20
)

type ConsumeRequest struct {
	ReleaseDir  string
	Destination string
}

type Receipt struct {
	ArchiveSHA256      string `json:"archive_sha256"`
	ClientBundleSHA256 string `json:"client_bundle_sha256"`
	Product            string `json:"product"`
	GOOS               string `json:"goos"`
	GOARCH             string `json:"goarch"`
}

func Consume(ctx context.Context, request ConsumeRequest) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, fmt.Errorf("consume release: %w", err)
	}
	input, err := loadConsumerInput(request.ReleaseDir)
	if err != nil {
		return Receipt{}, err
	}
	target := Target{Product: "acr-mcp", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	name := ArtifactName(target, input.manifest.Version)
	artifact, ok := input.artifacts[name]
	if !ok {
		return Receipt{}, fmt.Errorf("host artifact is unavailable")
	}
	archive, err := openVerifiedArchive(filepath.Join(request.ReleaseDir, name), artifact.SHA256)
	if err != nil {
		return Receipt{}, err
	}
	defer archive.Close()
	if err := validateDestination(request.Destination); err != nil {
		return Receipt{}, err
	}
	staging, err := os.MkdirTemp(filepath.Dir(request.Destination), ".releasebuild-")
	if err != nil {
		return Receipt{}, fmt.Errorf("create extraction staging: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := extractArchive(archive, staging, target.GOOS == "windows"); err != nil {
		return Receipt{}, err
	}
	bundlePath := filepath.Join(staging, "clients", "conformance", "client-bundle.v1.json")
	bundle, err := mcpclientfixtures.LoadClientBundle(bundlePath)
	if err != nil {
		return Receipt{}, fmt.Errorf("validate client bundle: %w", err)
	}
	if err := mcpclientfixtures.ValidateClientPackageRoots(staging, bundle); err != nil {
		return Receipt{}, fmt.Errorf("validate client packages: %w", err)
	}
	bundleDigest, err := digestFile(bundlePath, maxMetadataBytes)
	if err != nil {
		return Receipt{}, err
	}
	if err := os.Rename(staging, request.Destination); err != nil {
		return Receipt{}, fmt.Errorf("commit extraction: %w", err)
	}
	archiveDigest := sha256.Sum256(archive.hash)
	return Receipt{
		ArchiveSHA256:      hex.EncodeToString(archiveDigest[:]),
		ClientBundleSHA256: bundleDigest,
		Product:            target.Product,
		GOOS:               target.GOOS,
		GOARCH:             target.GOARCH,
	}, nil
}

func validateDestination(destination string) error {
	if destination == "" {
		return fmt.Errorf("destination is required")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("create destination parent: %w", err)
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("destination already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect destination: %w", err)
	}
	return nil
}
