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
	binPath := filepath.Join(t.TempDir(), "acr-mcp")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile acr-mcp: %v\n%s", err, output)
	}
	home := t.TempDir()
	browserBin, browserLog := compiledBrowserFixture(t)
	keyringBin, keyringLog := compiledKeyringFixture(t)
	env := compiledEnvironmentWithoutTokenVariables(
		"HOME="+home,
		sidecar.APIURLEnvironment+"="+server.URL,
		sidecar.AllowInsecureLoopbackEnvironment+"=true",
		"ACR_TEST_BROWSER_OPEN_LOG="+browserLog,
		"ACR_TEST_KEYRING_OPEN_LOG="+keyringLog,
		"PATH="+keyringBin+string(os.PathListSeparator)+browserBin,
	)

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
	assertNoBrowserLaunch(t, browserLog)
	assertNoKeyringLaunch(t, keyringLog)
	tokenPath := filepath.Join(home, ".acr", "token")
	assertCredentialFileModes(t, tokenPath)
}

func TestCompiledLogoutRemovesDisabledKeyringFileCredential(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("compiled lifecycle fixture is POSIX-only")
	}
	token := validDoctorToken(73)
	revocations := 0
	server := newCredentialLifecycleServer(t, token, validDoctorToken(74), &revocations, false)
	defer server.Close()
	binPath := compileLifecycleBinary(t)
	home := t.TempDir()
	browserBin, browserLog := compiledBrowserFixture(t)
	tokenPath := filepath.Join(home, ".acr", "token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "HOME="+home, sidecar.APIURLEnvironment+"="+server.URL, sidecar.AllowInsecureLoopbackEnvironment+"=true", sidecar.TokenEnvironment+"=", sidecar.TokenKeyringDisabledEnvironment+"=true", sidecar.TokenKeyringServiceEnvironment+"=acr-sidecar-test", sidecar.TokenKeyringAccountEnvironment+"=agent-a", sidecar.TokenFileEnvironment+"="+tokenPath, "ACR_TEST_BROWSER_OPEN_LOG="+browserLog, "PATH="+browserBin+string(os.PathListSeparator)+os.Getenv("PATH"))
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
	if _, err := os.Stat(browserLog); !os.IsNotExist(err) {
		t.Fatalf("logout unexpectedly invoked browser fixture: %v", err)
	}
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
	browserBin, browserLog := compiledBrowserFixture(t)
	tokenPath := filepath.Join(home, ".acr", "token")
	env := append(os.Environ(), "HOME="+home, sidecar.APIURLEnvironment+"="+server.URL, sidecar.AllowInsecureLoopbackEnvironment+"=true", sidecar.TokenEnvironment+"=", sidecar.TokenKeyringDisabledEnvironment+"=true", sidecar.TokenFileEnvironment+"="+tokenPath, "ACR_TEST_BROWSER_OPEN_LOG="+browserLog, "PATH="+browserBin+string(os.PathListSeparator)+os.Getenv("PATH"))
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
	assertNoBrowserLaunch(t, browserLog)
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatalf("credential persisted after terminal invalid-grant failure: %v", err)
	}
}

func compileLifecycleBinary(t *testing.T) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "acr-mcp")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile acr-mcp: %v\n%s", err, output)
	}
	return binPath
}

// compiledBrowserFixture plants PATH-resolvable "open"/"xdg-open" tripwires.
// The hardened opener resolves its binary from a fixed system directory
// allowlist and never from PATH, so an entry recorded here means production
// consulted PATH; an empty log means it did not. The compiled login fixtures
// also pass --no-browser, so no opener is resolved or executed at all and no
// host browser can be started by these tests.
func compiledBrowserFixture(t *testing.T) (string, string) {
	t.Helper()
	browserBin := t.TempDir()
	browserLog := filepath.Join(t.TempDir(), "browser-opener.log")
	for _, name := range []string{"open", "xdg-open"} {
		path := filepath.Join(browserBin, name)
		contents := "#!/bin/sh\nprintf '%s' \"$0\" >> \"$ACR_TEST_BROWSER_OPEN_LOG\"\nfor arg do printf ' %s' \"$arg\" >> \"$ACR_TEST_BROWSER_OPEN_LOG\"; done\nprintf '\\n' >> \"$ACR_TEST_BROWSER_OPEN_LOG\"\n"
		if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
			t.Fatalf("write %s browser tripwire: %v", name, err)
		}
	}
	return browserBin, browserLog
}

// assertNoBrowserLaunch proves production did not consult PATH. The
// --no-browser guarantee is exercised in-process by lifecycle_browser_test.go.
func assertNoBrowserLaunch(t *testing.T, logPath string) {
	t.Helper()
	contents, err := os.ReadFile(logPath)
	if err == nil {
		t.Fatalf("browser opener ran from PATH: %q", contents)
	}
	if !os.IsNotExist(err) {
		t.Fatalf("read browser opener tripwire log: %v", err)
	}
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

func compiledKeyringFixture(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "keyring.log")
	for _, name := range []string{"security", "secret-tool"} {
		path := filepath.Join(directory, name)
		contents := "#!/bin/sh\nprintf '%s\\n' \"$0\" >> \"$ACR_TEST_KEYRING_OPEN_LOG\"\nexit 97\n"
		if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
			t.Fatalf("write %s keyring tripwire: %v", name, err)
		}
	}
	return directory, logPath
}

func assertNoKeyringLaunch(t *testing.T, logPath string) {
	t.Helper()
	contents, err := os.ReadFile(logPath)
	if err == nil {
		t.Fatalf("compiled process reached a keyring executable: %q", contents)
	}
	if !os.IsNotExist(err) {
		t.Fatalf("read keyring tripwire log: %v", err)
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
