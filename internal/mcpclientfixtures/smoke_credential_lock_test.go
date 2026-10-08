//go:build darwin || linux

package mcpclientfixtures

import (
	"bytes"
	"context"
	"encoding/pem"
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

// lockDirAndPath returns a private directory and a lock file path in it, the
// path a child is told to use through the lock path override. Tests never
// touch the real host lock file, which other packages' tests use.
func lockDirAndPath(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, name)
}

// holdCredentialLock takes the exclusive flock a credential operation of
// another acr-mcp process of the same user would hold while it logs in.
func holdCredentialLock(t *testing.T, path string) func() {
	t.Helper()
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		t.Fatalf("open the credential lock: %v", err)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = syscall.Close(fd)
		t.Fatalf("take the credential lock: %v", err)
	}
	return func() { _ = syscall.Close(fd) }
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
	lockPath := lockDirAndPath(t, "held.lock")
	release := holdCredentialLock(t, lockPath)
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
		sidecar.CredentialLifecycleLockPathEnvironment+"="+lockPath,
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
	lockPath := lockDirAndPath(t, "held.lock")
	release := holdCredentialLock(t, lockPath)
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
		sidecar.CredentialLifecycleLockPathEnvironment+"="+lockPath,
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

// A child pointed at its own lock path boots with a file credential while
// another lock file is held, because it contends only on the path it was given.
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
	heldPath := lockDirAndPath(t, "held.lock")
	release := holdCredentialLock(t, heldPath)
	defer release()
	childPath := lockDirAndPath(t, "child.lock")

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
		sidecar.CredentialLifecycleLockPathEnvironment+"="+childPath,
	)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "mcpclientfixtures-lock", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect via the override lock path: %v\nchild stderr: %s", err, stderr.String())
	}
	defer session.Close()
}
