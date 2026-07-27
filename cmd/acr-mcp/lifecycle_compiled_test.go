package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

func TestCompiledLoginThenBareDoctorDiscoversPersistedCredential(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("default file persistence without a keyring is a macOS platform contract")
	}

	// Given
	token := validDoctorToken(91)
	server, fixture := newLifecycleServerWithState(t, token, []string{"success"}, nil)
	binPath := compileLifecycleBinary(t)
	home := t.TempDir()
	browserMarker := compiledLifecycleEventMarker(t, "browser-events")
	keyringMarker := compiledLifecycleEventMarker(t, "keyring-events")
	env := compiledEnvironmentWithoutTokenVariables(
		"HOME="+home,
		sidecar.APIURLEnvironment+"="+server.URL,
		sidecar.AllowInsecureLoopbackEnvironment+"=true",
		"ACR_TEST_BROWSER_EVENT_MARKER="+browserMarker,
		"ACR_TEST_KEYRING_EVENT_MARKER="+keyringMarker,
	)
	assertNoTokenVariablesInChildEnvironment(t, env)

	// When
	login := exec.Command(binPath, "login", "--no-browser")
	login.Env = env
	loginOutput, loginErr := login.CombinedOutput()
	doctor := exec.Command(binPath, "doctor")
	doctor.Env = env
	doctorOutput, doctorErr := doctor.CombinedOutput()

	// Then
	if loginErr != nil {
		t.Fatalf("compiled login failed: %v\n%s", loginErr, loginOutput)
	}
	if doctorErr != nil {
		t.Fatalf("bare compiled doctor failed: %v\n%s", doctorErr, doctorOutput)
	}
	if strings.Contains(string(loginOutput), token) || strings.Contains(string(doctorOutput), token) {
		t.Fatal("compiled lifecycle output leaked the credential")
	}
	if !strings.Contains(string(doctorOutput), `"credential_source":"file"`) || !strings.Contains(string(doctorOutput), `"reachable":true`) {
		t.Fatalf("bare doctor did not discover the login credential:\n%s", doctorOutput)
	}
	if _, _, _, capabilities := fixture.counts(); capabilities != 1 {
		t.Fatalf("doctor capability requests = %d, want exactly one bearer-authenticated request", capabilities)
	}
	if !strings.Contains(string(loginOutput), "Open "+deviceVerificationURI+" and enter code") {
		t.Fatalf("compiled login did not print the verification address:\n%s", loginOutput)
	}
	assertCompiledLifecycleEvents(t, keyringMarker, []string{"keyring.lookup", "keyring.write", "keyring.lookup", "keyring.lookup", "keyring.lookup"}, token)
	assertCompiledLifecycleEvents(t, browserMarker, nil, token)
	tokenPath := filepath.Join(home, ".acr", "token")
	assertCredentialFileModes(t, tokenPath)
}

func TestCompiledLogoutRemovesDefaultFileCredential(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("compiled lifecycle fixture is POSIX-only")
	}
	token := validDoctorToken(73)
	revocations := 0
	server := newCredentialLifecycleServer(t, token, validDoctorToken(74), &revocations, false)
	defer server.Close()
	binPath := compileLifecycleBinary(t)
	home := t.TempDir()
	browserMarker := compiledLifecycleEventMarker(t, "browser-events")
	keyringMarker := compiledLifecycleEventMarker(t, "keyring-events")
	tokenPath := filepath.Join(home, ".acr", "token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := compiledEnvironmentWithoutTokenVariables("HOME="+home, sidecar.APIURLEnvironment+"="+server.URL, sidecar.AllowInsecureLoopbackEnvironment+"=true", "ACR_TEST_BROWSER_EVENT_MARKER="+browserMarker, "ACR_TEST_KEYRING_EVENT_MARKER="+keyringMarker)
	assertNoTokenVariablesInChildEnvironment(t, env)
	logout := exec.Command(binPath, "logout")
	logout.Env = env
	if output, err := logout.CombinedOutput(); err != nil {
		t.Fatalf("compiled logout failed: %v\n%s", err, output)
	}
	if revocations != 1 {
		t.Fatalf("remote revocations = %d, want 1", revocations)
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatalf("credential file remains after compiled logout: %v", err)
	}
	assertCompiledLifecycleEvents(t, browserMarker, nil, token)
	assertCompiledLifecycleEvents(t, keyringMarker, []string{"keyring.lookup"}, token)
}

func TestCompiledLoginBoundsInvalidGrantAuthorizationRestarts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("browser opener tripwire is a POSIX executable fixture")
	}
	token := validDoctorToken(75)
	var trace deviceAuthorizationTrace
	server := newLifecycleRetryServer(t, token, []string{"invalid_grant", "invalid_grant"}, &trace)
	defer server.Close()
	binPath := compileLifecycleBinary(t)
	home := t.TempDir()
	browserMarker := compiledLifecycleEventMarker(t, "browser-events")
	keyringMarker := compiledLifecycleEventMarker(t, "keyring-events")
	tokenPath := filepath.Join(home, ".acr", "token")
	env := compiledEnvironmentWithoutTokenVariables("HOME="+home, sidecar.APIURLEnvironment+"="+server.URL, sidecar.AllowInsecureLoopbackEnvironment+"=true", "ACR_TEST_BROWSER_EVENT_MARKER="+browserMarker, "ACR_TEST_KEYRING_EVENT_MARKER="+keyringMarker)
	assertNoTokenVariablesInChildEnvironment(t, env)
	login := exec.Command(binPath, "login", "--no-browser")
	login.Env = env
	output, err := login.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "device authorization was invalidated twice") {
		t.Fatalf("compiled invalid-grant login err=%v output=%s", err, output)
	}
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != lifecycleExitFailure {
		t.Fatalf("compiled invalid-grant exit = %v, want %d", err, lifecycleExitFailure)
	}
	authorizations, issued, _ := trace.snapshot()
	if authorizations != 2 {
		t.Fatalf("authorizations = %d, want exactly two", authorizations)
	}
	if len(issued) != 2 || issued[0] == issued[1] {
		t.Fatalf("issued device codes = %v, want two distinct codes", issued)
	}
	if printed := strings.Count(string(output), "Open "+deviceVerificationURI+" and enter code"); printed != 2 {
		t.Fatalf("verification address printed %d times, want once per authorization:\n%s", printed, output)
	}
	assertCompiledLifecycleEvents(t, browserMarker, nil, token)
	assertCompiledLifecycleEvents(t, keyringMarker, []string{"keyring.lookup"}, token)
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatalf("credential persisted after terminal invalid-grant failure: %v", err)
	}
}

func compileLifecycleBinary(t *testing.T) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "acr-mcp")
	build := exec.Command("go", "build", "-tags", "acr_compiled_lifecycle_fixture", "-o", binPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile acr-mcp: %v\n%s", err, output)
	}
	return binPath
}

func compiledEnvironmentWithoutTokenVariables(additions ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(additions))
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if found && !strings.HasPrefix(name, "ACR_API_TOKEN") {
			env = append(env, entry)
		}
	}
	return append(env, additions...)
}

func assertNoTokenVariablesInChildEnvironment(t *testing.T, env []string) {
	t.Helper()
	for _, entry := range env {
		name, _, found := strings.Cut(entry, "=")
		if found && strings.HasPrefix(name, "ACR_API_TOKEN") {
			t.Fatalf("child environment contains credential variable %s", name)
		}
	}
}

func compiledLifecycleEventMarker(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}

func assertCompiledLifecycleEvents(t *testing.T, marker string, want []string, token string) {
	t.Helper()
	contents, err := os.ReadFile(marker)
	if os.IsNotExist(err) {
		if len(want) == 0 {
			return
		}
		t.Fatalf("compiled lifecycle marker %q was not written", marker)
	}
	if err != nil {
		t.Fatalf("read compiled lifecycle marker: %v", err)
	}
	if strings.Contains(string(contents), token) {
		t.Fatal("compiled lifecycle marker leaked the credential")
	}
	got := strings.Fields(string(contents))
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("compiled lifecycle events = %v, want %v", got, want)
	}
}

func assertCredentialFileModes(t *testing.T, path string) {
	t.Helper()
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat persisted credential: %v", err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("credential file mode = %#o, want 0600", fileInfo.Mode().Perm())
	}
	parentInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat credential parent: %v", err)
	}
	if parentInfo.Mode().Perm() != 0o700 {
		t.Fatalf("credential parent mode = %#o, want 0700", parentInfo.Mode().Perm())
	}
}
