package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// testValidBearerToken is a shape-valid (auth.IsTokenShapeValid), but
// otherwise fake, fcacr_ credential -- NewClient now rejects any value
// without the real ACR credential shape (codex round 1, HIGH), so a plain
// placeholder like "test-token" no longer builds a Panelist successfully.
const testValidBearerToken = "fcacr_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestRun_RequiresAllFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"missing everything", nil},
		{"missing panelists", []string{"-api-base-url=https://acr.example.com", "-org-id=org-1", "-question=q", "-output=/tmp/out.json"}},
		{"missing output", []string{"-api-base-url=https://acr.example.com", "-org-id=org-1", "-question=q", "-panelists=/tmp/panelists.json"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := run(tc.args, os.Stdout, os.Stderr); err == nil {
				t.Error("expected an error for missing required flags")
			}
		})
	}
}

func TestLoadPanelistConfigs_RejectsEmptyArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panelists.json")
	if err := os.WriteFile(path, []byte(`[]`), 0o644); err != nil {
		t.Fatalf("write panelists file: %v", err)
	}
	if _, err := loadPanelistConfigs(path); err == nil {
		t.Error("expected an error for an empty panelists array")
	}
}

func TestLoadPanelistConfigs_ParsesValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panelists.json")
	configs := []panelistConfig{
		{CanonicalModelIdentity: "anthropic/sol-max", BearerTokenEnv: "ACR_PANEL_TOKEN_SOL", FileExchangeDir: "/tmp/panel-sol"},
	}
	encoded, err := json.Marshal(configs)
	if err != nil {
		t.Fatalf("marshal panelist configs: %v", err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatalf("write panelists file: %v", err)
	}
	loaded, err := loadPanelistConfigs(path)
	if err != nil {
		t.Fatalf("loadPanelistConfigs: %v", err)
	}
	if len(loaded) != 1 || loaded[0].CanonicalModelIdentity != "anthropic/sol-max" {
		t.Errorf("loaded = %+v, want one entry for anthropic/sol-max", loaded)
	}
}

// TestLoadPanelistConfigs_RejectsSharedFileExchangeDir is a regression test
// for codex round-1 finding HIGH-7: two panelists pointed at the same
// file_exchange_dir would race on identical sequence-numbered filenames.
func TestLoadPanelistConfigs_RejectsSharedFileExchangeDir(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "shared-exchange")
	path := filepath.Join(dir, "panelists.json")
	configs := []panelistConfig{
		{CanonicalModelIdentity: "anthropic/sol-max", BearerTokenEnv: "ACR_PANEL_TOKEN_SOL", FileExchangeDir: shared},
		{CanonicalModelIdentity: "anthropic/luna", BearerTokenEnv: "ACR_PANEL_TOKEN_LUNA", FileExchangeDir: shared},
	}
	encoded, err := json.Marshal(configs)
	if err != nil {
		t.Fatalf("marshal panelist configs: %v", err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatalf("write panelists file: %v", err)
	}
	if _, err := loadPanelistConfigs(path); err == nil {
		t.Error("expected an error when two panelists share the same file_exchange_dir")
	}
}

// TestDetectAliasedFileExchangeDirs_RejectsSymlinkAlias is a regression test
// for codex round-2 finding HIGH-1: loadPanelistConfigs's own distinct-
// directory check compares filepath.Abs results, which does not resolve
// symlinks, so two differently-spelled paths naming the SAME directory (one
// via a symlink) would sail through it undetected. detectAliasedFileExchangeDirs
// is the authoritative check that runs after every panelist's requests/
// subdirectory actually exists, using os.SameFile (device+inode identity).
func TestDetectAliasedFileExchangeDirs_RejectsSymlinkAlias(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real-exchange")
	alias := filepath.Join(dir, "alias-exchange")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	const tokenEnvA, tokenEnvB = "ACR_PANEL_HARNESS_TEST_TOKEN_ALIAS_A", "ACR_PANEL_HARNESS_TEST_TOKEN_ALIAS_B"
	t.Setenv(tokenEnvA, testValidBearerToken)
	t.Setenv(tokenEnvB, testValidBearerToken)
	configs := []panelistConfig{
		{CanonicalModelIdentity: "anthropic/sol-max", BearerTokenEnv: tokenEnvA, FileExchangeDir: real},
		{CanonicalModelIdentity: "anthropic/luna", BearerTokenEnv: tokenEnvB, FileExchangeDir: alias},
	}
	for _, config := range configs {
		if _, err := buildPanelist(config, "https://acr.example.com", 0, 0); err != nil {
			t.Fatalf("buildPanelist(%s): %v", config.CanonicalModelIdentity, err)
		}
	}

	if err := detectAliasedFileExchangeDirs(configs); err == nil {
		t.Error("expected an error when two panelists' file_exchange_dir values are a symlink alias of the same directory")
	}
}

func TestBuildPanelist_RequiresBearerTokenEnvironmentVariableToBeSet(t *testing.T) {
	config := panelistConfig{CanonicalModelIdentity: "anthropic/sol-max", BearerTokenEnv: "ACR_PANEL_HARNESS_TEST_UNSET_TOKEN", FileExchangeDir: t.TempDir()}
	os.Unsetenv(config.BearerTokenEnv)
	if _, err := buildPanelist(config, "https://acr.example.com", 0, 0); err == nil {
		t.Error("expected an error when the named environment variable is unset")
	}
}

func TestBuildPanelist_RequiresFileExchangeDir(t *testing.T) {
	const tokenEnv = "ACR_PANEL_HARNESS_TEST_TOKEN"
	t.Setenv(tokenEnv, testValidBearerToken)
	config := panelistConfig{CanonicalModelIdentity: "anthropic/sol-max", BearerTokenEnv: tokenEnv}
	if _, err := buildPanelist(config, "https://acr.example.com", 0, 0); err == nil {
		t.Error("expected an error when file_exchange_dir is empty (the only implemented selector transport)")
	}
}

func TestBuildPanelist_BuildsAWorkingPanelist(t *testing.T) {
	const tokenEnv = "ACR_PANEL_HARNESS_TEST_TOKEN_2"
	t.Setenv(tokenEnv, testValidBearerToken)
	config := panelistConfig{CanonicalModelIdentity: "anthropic/sol-max", BearerTokenEnv: tokenEnv, FileExchangeDir: t.TempDir()}
	panelist, err := buildPanelist(config, "https://acr.example.com", 0, 0)
	if err != nil {
		t.Fatalf("buildPanelist: %v", err)
	}
	if panelist.CanonicalModelIdentity != "anthropic/sol-max" || panelist.Client == nil || panelist.Selector == nil {
		t.Errorf("panelist = %+v, want a fully populated Panelist", panelist)
	}
}
