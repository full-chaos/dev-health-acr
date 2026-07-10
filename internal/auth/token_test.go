package auth

import (
	"strings"
	"testing"
)

func TestGenerateTokenShapeAndHash(t *testing.T) {
	token, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if !IsTokenShapeValid(token) {
		t.Fatalf("invalid token shape: %q", token)
	}
	if !strings.HasPrefix(DisplayPrefix(token), TokenPrefix) {
		t.Fatalf("display prefix lost marker: %q", DisplayPrefix(token))
	}
	if DisplayPrefix(token) == token {
		t.Fatal("display prefix must not expose the complete token")
	}
	if HashToken(token) == token || len(HashToken(token)) != 64 {
		t.Fatal("token hash must be a SHA-256 hex digest")
	}

	other, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if token == other {
		t.Fatal("generated tokens must be unique")
	}
}

func TestIsTokenShapeValidRejectsLicenseAndMalformedValues(t *testing.T) {
	for _, value := range []string{"", "license.jwt.value", "fcpush_abc", "fcacr_short", "Bearer fcacr_value"} {
		if IsTokenShapeValid(value) {
			t.Fatalf("accepted malformed token %q", value)
		}
	}
}
