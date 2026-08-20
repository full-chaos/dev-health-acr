package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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

func TestBuildPanelist_RequiresBearerTokenEnvironmentVariableToBeSet(t *testing.T) {
	config := panelistConfig{CanonicalModelIdentity: "anthropic/sol-max", BearerTokenEnv: "ACR_PANEL_HARNESS_TEST_UNSET_TOKEN", FileExchangeDir: t.TempDir()}
	os.Unsetenv(config.BearerTokenEnv)
	if _, err := buildPanelist(config, "https://acr.example.com", 0, 0); err == nil {
		t.Error("expected an error when the named environment variable is unset")
	}
}

func TestBuildPanelist_RequiresFileExchangeDir(t *testing.T) {
	const tokenEnv = "ACR_PANEL_HARNESS_TEST_TOKEN"
	t.Setenv(tokenEnv, "test-token")
	config := panelistConfig{CanonicalModelIdentity: "anthropic/sol-max", BearerTokenEnv: tokenEnv}
	if _, err := buildPanelist(config, "https://acr.example.com", 0, 0); err == nil {
		t.Error("expected an error when file_exchange_dir is empty (the only implemented selector transport)")
	}
}

func TestBuildPanelist_BuildsAWorkingPanelist(t *testing.T) {
	const tokenEnv = "ACR_PANEL_HARNESS_TEST_TOKEN_2"
	t.Setenv(tokenEnv, "test-token")
	config := panelistConfig{CanonicalModelIdentity: "anthropic/sol-max", BearerTokenEnv: tokenEnv, FileExchangeDir: t.TempDir()}
	panelist, err := buildPanelist(config, "https://acr.example.com", 0, 0)
	if err != nil {
		t.Fatalf("buildPanelist: %v", err)
	}
	if panelist.CanonicalModelIdentity != "anthropic/sol-max" || panelist.Client == nil || panelist.Selector == nil {
		t.Errorf("panelist = %+v, want a fully populated Panelist", panelist)
	}
}
