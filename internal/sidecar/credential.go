package sidecar

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
)

const (
	TokenEnvironment     = "ACR_API_TOKEN"
	TokenFileEnvironment = "ACR_API_TOKEN_FILE"
)

type CredentialResult struct {
	Token  string
	Source string
}

// LoadCredential prefers the process environment for agent-client
// compatibility, then supports a permission-restricted token file. Future OS
// keychain adapters can implement the same result contract without changing
// the MCP client.
func LoadCredential() (CredentialResult, error) {
	if token := strings.TrimSpace(os.Getenv(TokenEnvironment)); token != "" {
		return CredentialResult{Token: token, Source: "environment"}, nil
	}
	path := strings.TrimSpace(os.Getenv(TokenFileEnvironment))
	if path == "" {
		return CredentialResult{}, errors.New("ACR API credential is not configured")
	}
	info, err := os.Stat(path)
	if err != nil {
		return CredentialResult{}, fmt.Errorf("stat ACR credential file: %w", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return CredentialResult{}, fmt.Errorf("ACR credential file permissions must not grant group or world access: %s", info.Mode().Perm())
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return CredentialResult{}, fmt.Errorf("read ACR credential file: %w", err)
	}
	token := strings.TrimSpace(string(contents))
	if token == "" {
		return CredentialResult{}, errors.New("ACR credential file is empty")
	}
	return CredentialResult{Token: token, Source: "file"}, nil
}
