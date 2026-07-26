package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

func TestCompiledLoginThenBareDoctorDiscoversPersistedCredential(t *testing.T) {
	// Given
	token := validDoctorToken(91)
	server := newLifecycleServer(t, token, []string{"success"})
	binPath := filepath.Join(t.TempDir(), "acr-mcp")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile acr-mcp: %v\n%s", err, output)
	}
	home := t.TempDir()
	env := append(os.Environ(), "HOME="+home, sidecar.APIURLEnvironment+"="+server.URL, sidecar.AllowInsecureLoopbackEnvironment+"=true", sidecar.TokenEnvironment+"=", sidecar.TokenKeyringDisabledEnvironment+"=true", sidecar.TokenFileEnvironment+"="+filepath.Join(home, ".acr", "token"))

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
}
