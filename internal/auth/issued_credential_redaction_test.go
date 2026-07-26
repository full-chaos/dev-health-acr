package auth

import (
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
