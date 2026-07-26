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
	if runtime.GOOS == "windows" {
		t.Skip("browser opener tripwire is a POSIX executable fixture")
	}

	// Given
	token := validDoctorToken(91)
	server := newLifecycleServer(t, token, []string{"success"})
	binPath := filepath.Join(t.TempDir(), "acr-mcp")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile acr-mcp: %v\n%s", err, output)
	}
	home := t.TempDir()
	browserBin := t.TempDir()
	browserLog := filepath.Join(t.TempDir(), "browser-opener.log")
	for _, name := range []string{"open", "xdg-open"} {
		path := filepath.Join(browserBin, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' \"$0\" > \"$ACR_TEST_BROWSER_OPEN_LOG\"\n"), 0o700); err != nil {
			t.Fatalf("write %s browser tripwire: %v", name, err)
		}
	}
	env := append(os.Environ(), "HOME="+home, sidecar.APIURLEnvironment+"="+server.URL, sidecar.AllowInsecureLoopbackEnvironment+"=true", sidecar.TokenEnvironment+"=", sidecar.TokenKeyringDisabledEnvironment+"=true", sidecar.TokenFileEnvironment+"="+filepath.Join(home, ".acr", "token"), "ACR_TEST_BROWSER_OPEN_LOG="+browserLog, "PATH="+browserBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	// When
	login := exec.Command(binPath, "login")
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
	browserInvocation, err := os.ReadFile(browserLog)
	if err != nil {
		t.Fatalf("browser opener tripwire was not invoked: %v", err)
	}
	if opener := filepath.Base(strings.TrimSpace(string(browserInvocation))); opener != "open" && opener != "xdg-open" {
		t.Fatalf("unexpected browser opener tripwire invocation: %q", opener)
	}
}
