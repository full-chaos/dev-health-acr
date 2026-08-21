package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
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
		if _, err := buildPanelist(config, "https://acr.example.com", 0, 0, nil); err != nil {
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
	if _, err := buildPanelist(config, "https://acr.example.com", 0, 0, nil); err == nil {
		t.Error("expected an error when the named environment variable is unset")
	}
}

func TestBuildPanelist_RequiresFileExchangeDir(t *testing.T) {
	const tokenEnv = "ACR_PANEL_HARNESS_TEST_TOKEN"
	t.Setenv(tokenEnv, testValidBearerToken)
	config := panelistConfig{CanonicalModelIdentity: "anthropic/sol-max", BearerTokenEnv: tokenEnv}
	if _, err := buildPanelist(config, "https://acr.example.com", 0, 0, nil); err == nil {
		t.Error("expected an error when file_exchange_dir is empty (the only implemented selector transport)")
	}
}

func TestBuildPanelist_BuildsAWorkingPanelist(t *testing.T) {
	const tokenEnv = "ACR_PANEL_HARNESS_TEST_TOKEN_2"
	t.Setenv(tokenEnv, testValidBearerToken)
	config := panelistConfig{CanonicalModelIdentity: "anthropic/sol-max", BearerTokenEnv: tokenEnv, FileExchangeDir: t.TempDir()}
	panelist, err := buildPanelist(config, "https://acr.example.com", 0, 0, nil)
	if err != nil {
		t.Fatalf("buildPanelist: %v", err)
	}
	if panelist.CanonicalModelIdentity != "anthropic/sol-max" || panelist.Client == nil || panelist.Selector == nil {
		t.Errorf("panelist = %+v, want a fully populated Panelist", panelist)
	}
}

// TestWorkloadCredentialSourceFromEnvironment_DefaultsToNil is CHAOS-4034's
// own proof that the static bearer_token_env path stays this binary's
// default: with neither ACR_SUBJECT_TOKEN_FILE nor ACR_TOKEN_ENDPOINT set,
// no workload credential source is built and no error is returned.
func TestWorkloadCredentialSourceFromEnvironment_DefaultsToNil(t *testing.T) {
	source, err := workloadCredentialSourceFromEnvironment()
	if err != nil {
		t.Fatalf("workloadCredentialSourceFromEnvironment: %v", err)
	}
	if source != nil {
		t.Error("expected a nil CredentialSource when neither env var is set")
	}
}

// TestWorkloadCredentialSourceFromEnvironment_RejectsOnlyOneSet guards the
// half-configured case: an operator who set only one of the pair almost
// certainly meant to enable workload mode, so this fails closed rather
// than silently keeping the static path.
func TestWorkloadCredentialSourceFromEnvironment_RejectsOnlyOneSet(t *testing.T) {
	t.Setenv(sidecar.SubjectTokenFileEnvironment, filepath.Join(t.TempDir(), "token"))
	if _, err := workloadCredentialSourceFromEnvironment(); err == nil {
		t.Error("expected an error when only ACR_SUBJECT_TOKEN_FILE is set")
	}
}

// TestWorkloadCredentialSourceFromEnvironment_BuildsSourceWhenBothSet
// proves the env pair actually wires internal/sidecar.NewWorkloadCredentialSource
// (CHAOS-4013) rather than something ad hoc.
func TestWorkloadCredentialSourceFromEnvironment_BuildsSourceWhenBothSet(t *testing.T) {
	subjectTokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(subjectTokenFile, []byte("k8s-projected-sa-jwt"), 0o600); err != nil {
		t.Fatalf("write subject token file: %v", err)
	}
	t.Setenv(sidecar.SubjectTokenFileEnvironment, subjectTokenFile)
	t.Setenv(sidecar.TokenEndpointEnvironment, "https://acr.example.com/api/v1/oauth/token")
	source, err := workloadCredentialSourceFromEnvironment()
	if err != nil {
		t.Fatalf("workloadCredentialSourceFromEnvironment: %v", err)
	}
	if source == nil {
		t.Error("expected a non-nil CredentialSource when both env vars are set")
	}
}

// TestBuildPanelist_UsesWorkloadCredentialSourceWhenProvided proves
// buildPanelist actually authenticates through a supplied workload
// CredentialSource end to end (against a local httptest token-exchange
// fixture -- a non-live seam, never a real panel/trial run) rather than
// requiring bearer_token_env when one is supplied.
func TestBuildPanelist_UsesWorkloadCredentialSourceWhenProvided(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + testValidBearerToken + `","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	subjectTokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(subjectTokenFile, []byte("k8s-projected-sa-jwt"), 0o600); err != nil {
		t.Fatalf("write subject token file: %v", err)
	}
	tokenEndpoint, err := url.Parse(tokenServer.URL)
	if err != nil {
		t.Fatalf("parse token server URL: %v", err)
	}
	workloadSource, err := sidecar.NewWorkloadCredentialSource(sidecar.WorkloadCredentialSourceOptions{
		TokenEndpoint: tokenEndpoint, SubjectTokenFile: subjectTokenFile,
	})
	if err != nil {
		t.Fatalf("NewWorkloadCredentialSource: %v", err)
	}

	config := panelistConfig{CanonicalModelIdentity: "anthropic/sol-max", FileExchangeDir: t.TempDir()}
	panelist, err := buildPanelist(config, "https://acr.example.com", 0, 0, workloadSource)
	if err != nil {
		t.Fatalf("buildPanelist: %v", err)
	}
	if panelist.Client == nil {
		t.Fatal("expected a Client to be built from the workload credential source")
	}
	if panelist.Client.TokenFingerprint() == "" {
		t.Error("expected a non-empty TokenFingerprint once the workload source resolved a token")
	}
}
