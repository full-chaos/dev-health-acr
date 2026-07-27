package sidecar

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunKeyringMutationSuppliesSecretOnlyThroughStdin(t *testing.T) {
	// Given
	requireSh(t)
	directory := t.TempDir()
	stdinPath := filepath.Join(directory, "stdin")
	argumentsPath := filepath.Join(directory, "arguments")
	token := validTestToken(8)
	script := `cat > "$1"; printf '%s\n' "$@" > "$2"`

	// When
	err := runKeyringMutation(context.Background(), strings.NewReader(token), false, "sh", "-c", script, "sh", stdinPath, argumentsPath, "store", "service", defaultKeyringService)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(stdin) != token {
		t.Fatal("keyring mutation did not receive the credential through stdin")
	}
	arguments, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(arguments), token) {
		t.Fatal("keyring mutation placed the credential in argv")
	}
}
