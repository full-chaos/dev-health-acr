package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// `acr-mcp serve --transport=http` runs the hosted transport from the real
// command line with no process credential (the STDIO transport refuses to
// start without one), answers its probe routes, refuses an unauthenticated
// MCP request, and exits 0 on SIGTERM.
func TestServeSubprocessRunsTheHTTPTransportWithoutAProcessCredential(t *testing.T) {
	binPath := filepath.Join(t.TempDir(), "acr-mcp")
	if output, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
		t.Fatalf("compile acr-mcp: %v\n%s", err, output)
	}
	command := exec.Command(binPath, "serve", "--transport=http", "--listen=127.0.0.1:0", "--base-path=/mcp")
	command.Env = compiledEnvironmentWithoutTokenVariables(
		sidecar.APIURLEnvironment+"=https://acr.example.test",
		sidecar.TokenFileEnvironment+"="+t.TempDir()+"/missing-token",
	)
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })

	address := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			var line map[string]any
			if json.Unmarshal(scanner.Bytes(), &line) == nil && line["msg"] == acrmcp.HTTPServingLogMessage {
				address <- line["listen_address"].(string)
			}
		}
	}()
	var base string
	select {
	case a := <-address:
		base = "http://" + a
	case <-time.After(20 * time.Second):
		t.Fatal("the http transport never reported it was serving")
	}
	resp, err := http.Get(base + "/healthz")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz: %v %v", resp, err)
	}
	_ = resp.Body.Close()
	resp, err = http.Post(base+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated MCP request: %v %v", resp, err)
	}
	_ = resp.Body.Close()
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exit after SIGTERM: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("serve did not exit after SIGTERM")
	}
}
