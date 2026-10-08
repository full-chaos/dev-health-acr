//go:build darwin || linux

package mcpclientfixtures

import (
	"bytes"
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// holdHostCredentialLock takes the exclusive flock the real acr-mcp credential
// lifecycle uses, as a second process of the same user would while it logs in.
func holdHostCredentialLock(t *testing.T) func() {
	t.Helper()
	path := filepath.Join("/var/tmp", fmt.Sprintf("acr-credential-lifecycle-%d.lock", os.Geteuid()))
	deadline := time.Now().Add(10 * time.Second)
	for {
		fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			t.Fatalf("open the host credential lock: %v", err)
		}
		if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return func() { _ = syscall.Close(fd) }
		}
		_ = syscall.Close(fd)
		if time.Now().After(deadline) {
			t.Fatal("the host credential lock stayed busy for 10s; a sibling test holds it")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func smokeServerAndCA(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(capabilitiesFixtureJSON(t))
	}))
	t.Cleanup(server.Close)
	caPath := filepath.Join(t.TempDir(), "smoke-ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return server, caPath
}

// Two acr-mcp processes of one user collide at boot when a credential
// operation holds the host lock. An environment token is a pure read, so the
// server must come up regardless.
func TestRealBinaryServesWithEnvironmentTokenWhileCredentialLockIsHeld(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-spawning smoke test in -short mode")
	}
	binPath := buildVersionedACRMCPBinaryWithTags(t, "")
	server, caPath := smokeServerAndCA(t)
	release := holdHostCredentialLock(t)
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, binPath, "serve")
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(),
		"ACR_API_URL="+server.URL,
		"ACR_API_CA_BUNDLE="+caPath,
		"ACR_API_TOKEN="+fixtureToken(),
		"ACR_SIDECAR_VERSION=1.0.0",
	)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "mcpclientfixtures-lock", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect while the credential lock is held: %v\nchild stderr: %s", err, stderr.String())
	}
	defer session.Close()
	if tools, err := session.ListTools(ctx, nil); err != nil || len(tools.Tools) != 2 {
		t.Fatalf("tools/list = %v, %v", tools, err)
	}
}

// A file credential has to wait for a running mutation. When the mutation
// outlasts the bound, boot fails with the specific cause, never "internal".
func TestRealBinaryNamesCredentialOperationWhenFileCredentialWaitExpires(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-spawning smoke test in -short mode")
	}
	binPath := buildVersionedACRMCPBinaryWithTags(t, "")
	server, caPath := smokeServerAndCA(t)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(fixtureToken()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	release := holdHostCredentialLock(t)
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "serve")
	cmd.Env = append(os.Environ(),
		"ACR_API_URL="+server.URL,
		"ACR_API_CA_BUNDLE="+caPath,
		"ACR_API_TOKEN=",
		"ACR_API_TOKEN_FILE="+tokenFile,
		"ACR_API_TOKEN_KEYRING_DISABLED=true",
		"ACR_SIDECAR_VERSION=1.0.0",
	)
	out, err := cmd.CombinedOutput()
	exitErr, isExit := err.(*exec.ExitError)
	if !isExit || exitErr.ExitCode() != 1 {
		t.Fatalf("serve = %v, want exit status 1\n%s", err, out)
	}
	if !strings.Contains(string(out), "another acr-mcp credential operation is in progress") {
		t.Fatalf("stderr = %q, want the credential-operation cause", out)
	}
	if strings.Contains(string(out), "internal:") {
		t.Fatalf("stderr = %q, must not report an internal failure", out)
	}
}

// A child pointed at its own lock path boots with a file credential while the
// real host lock is held, because it never contends on the host file.
func TestRealBinaryFileCredentialBootsViaOverrideLockPathWhileHostLockIsHeld(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-spawning smoke test in -short mode")
	}
	binPath := buildVersionedACRMCPBinaryWithTags(t, "")
	server, caPath := smokeServerAndCA(t)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(fixtureToken()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockDir := t.TempDir()
	if err := os.Chmod(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	release := holdHostCredentialLock(t)
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, binPath, "serve")
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(),
		"ACR_API_URL="+server.URL,
		"ACR_API_CA_BUNDLE="+caPath,
		"ACR_API_TOKEN=",
		"ACR_API_TOKEN_FILE="+tokenFile,
		"ACR_API_TOKEN_KEYRING_DISABLED=true",
		"ACR_SIDECAR_VERSION=1.0.0",
		sidecar.CredentialLifecycleLockPathEnvironment+"="+filepath.Join(lockDir, "child.lock"),
	)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "mcpclientfixtures-lock", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect via the override lock path: %v\nchild stderr: %s", err, stderr.String())
	}
	defer session.Close()
}
