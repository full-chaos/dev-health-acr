package panelharness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile serializes manifest as indented JSON and publishes it to path
// via the same temp-file-then-rename atomicity every other durable
// artifact in this codebase uses (mirrors fileexchange.go's own request
// publish, migrations/postgres's own applyMigration transactionality in
// spirit): a reader can never observe a partially-written manifest file,
// and a process crash mid-write leaves no file at path at all rather than
// a truncated one. Manifests are immutable by convention -- this is the
// ONLY function in this package that ever creates or replaces a manifest
// file; nothing here opens one for in-place editing.
func (manifest PanelRunManifest) WriteFile(path string) error {
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("panelharness: marshal panel run manifest: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("panelharness: create manifest directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".panel_run_manifest.*.tmp")
	if err != nil {
		return fmt.Errorf("panelharness: create temp manifest file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("panelharness: write temp manifest file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("panelharness: close temp manifest file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("panelharness: publish manifest file: %w", err)
	}
	return nil
}
