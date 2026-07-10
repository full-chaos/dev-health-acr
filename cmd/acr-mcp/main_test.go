package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validDoctorToken(fill byte) string {
	value := make([]byte, 32)
	for index := range value {
		value[index] = fill
	}
	return "fcacr_" + base64.RawURLEncoding.EncodeToString(value)
}

func TestDoctorReportsCredentialPresenceWithoutValue(t *testing.T) {
	t.Setenv("ACR_API_URL", "https://acr.example.test")
	token := validDoctorToken(7)
	t.Setenv("ACR_API_TOKEN", token)
	result := runDoctor()
	if !result.CredentialSet || !result.CredentialShapeValid || result.CredentialSource != "environment" || result.Status != "ok" {
		t.Fatalf("unexpected doctor result: %#v", result)
	}
	for _, check := range result.Checks {
		if strings.Contains(check.Detail, token) {
			t.Fatal("doctor leaked the credential")
		}
	}
}

func TestDoctorSupportsRestrictedCredentialFile(t *testing.T) {
	t.Setenv("ACR_API_URL", "https://acr.example.test")
	t.Setenv("ACR_API_TOKEN", "")
	path := filepath.Join(t.TempDir(), "acr-token")
	if err := os.WriteFile(path, []byte(validDoctorToken(8)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACR_API_TOKEN_FILE", path)
	result := runDoctor()
	if result.Status != "ok" || result.CredentialSource != "file" || !result.CredentialShapeValid {
		t.Fatalf("unexpected doctor result: %#v", result)
	}
}

func TestDoctorRejectsMalformedCredentialWithoutEchoingIt(t *testing.T) {
	t.Setenv("ACR_API_URL", "https://acr.example.test")
	t.Setenv("ACR_API_TOKEN", "fcacr_not-valid")
	result := runDoctor()
	if result.Status != "invalid_configuration" || result.CredentialShapeValid {
		t.Fatalf("unexpected doctor result: %#v", result)
	}
	for _, check := range result.Checks {
		if strings.Contains(check.Detail, "fcacr_not-valid") {
			t.Fatal("doctor leaked malformed credential")
		}
	}
}
