package auth

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestIssuedCredential_redactsPlaintextTokenInStringGoStringAndSlog(t *testing.T) {
	issued := IssuedCredential{Token: "fcacr_abcdefghijklmnopqrstuvwxyz0123456789"}
	if got := issued.String(); got != issuedCredentialRedacted || strings.Contains(got, issued.Token) {
		t.Fatalf("String() = %q", got)
	}
	if got := fmt.Sprintf("%#v", issued); got != issuedCredentialRedacted || strings.Contains(got, issued.Token) {
		t.Fatalf("GoString() = %q", got)
	}
	if got := issued.LogValue(); got.Kind() != slog.KindString || got.String() != issuedCredentialRedacted || strings.Contains(got.String(), issued.Token) {
		t.Fatalf("LogValue() = %#v", got)
	}
}

func TestIssuedCredential_JSONSerialization_redactsPlaintextToken(t *testing.T) {
	// Given
	issued := IssuedCredential{Token: "fcacr_abcdefghijklmnopqrstuvwxyz0123456789"}

	// When
	encoded, err := json.Marshal(issued)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), issued.Token) {
		t.Fatalf("JSON serialization exposed token: %s", encoded)
	}
}
