//go:build linux

package sidecar

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultKeyringWriterPassesCredentialOnlyThroughSecretToolStdin(t *testing.T) {
	// Given
	directory := t.TempDir()
	script := filepath.Join(directory, "secret-tool")
	arguments := filepath.Join(directory, "arguments")
	stdin := filepath.Join(directory, "stdin")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$KEYRING_WRITE_ARGUMENTS\"\ncat > \"$KEYRING_WRITE_STDIN\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	injectExecutableResolver(t, "secret-tool", script)
	t.Setenv("KEYRING_WRITE_ARGUMENTS", arguments)
	t.Setenv("KEYRING_WRITE_STDIN", stdin)
	token := validTestToken(9)

	// When
	err := defaultKeyringWriter(context.Background(), defaultKeyringService, "https://api.dev-health.example.com", token)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	argumentBytes, err := os.ReadFile(arguments)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(argumentBytes), token) {
		t.Fatal("keyring writer placed the credential in argv")
	}
	stdinBytes, err := os.ReadFile(stdin)
	if err != nil {
		t.Fatal(err)
	}
	if string(stdinBytes) != token {
		t.Fatal("keyring writer did not provide the credential through stdin")
	}
}
