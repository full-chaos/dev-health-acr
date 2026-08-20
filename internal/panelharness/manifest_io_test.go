package panelharness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPanelRunManifest_WriteFileRoundTrips(t *testing.T) {
	manifest := PanelRunManifest{
		SchemaVersion: ManifestSchemaVersion, PanelRunID: "panel_run_test0001", OrgID: "org-test",
		QuestionHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd", AlgorithmVersion: AlgorithmVersion,
		StartedAt: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC), CompletedAt: time.Date(2026, 8, 20, 12, 1, 0, 0, time.UTC),
		Members: []PanelMemberRun{BuildMemberRun("expected_kind", 1, []PanelistSelection{selection("sol", "pull_request")})},
	}

	path := filepath.Join(t.TempDir(), "nested", "panel_run_test0001.json")
	if err := manifest.WriteFile(path); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var roundTripped PanelRunManifest
	if err := json.Unmarshal(raw, &roundTripped); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if roundTripped.PanelRunID != manifest.PanelRunID {
		t.Errorf("PanelRunID = %q, want %q", roundTripped.PanelRunID, manifest.PanelRunID)
	}
	if len(roundTripped.Members) != 1 || roundTripped.Members[0].Consensus.MajorityValue != "pull_request" {
		t.Errorf("Members = %+v, want one member with majority pull_request", roundTripped.Members)
	}
}

func TestPanelRunManifest_WriteFileNeverLeavesTempFileBehindOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	manifest := PanelRunManifest{SchemaVersion: ManifestSchemaVersion, PanelRunID: "panel_run_test0002"}
	if err := manifest.WriteFile(path); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "manifest.json" {
		t.Errorf("directory entries = %v, want exactly [manifest.json] (no leftover temp file)", entries)
	}
}
