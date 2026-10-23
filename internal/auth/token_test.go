package auth

import (
	"bytes"
	"encoding/base64"
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

func shapeTestToken() (token string, body string) {
	raw := bytes.Repeat([]byte{0x5a}, tokenSecretBytes)
	body = base64.RawURLEncoding.EncodeToString(raw)
	return TokenPrefix + body, body
}

func TestIsTokenShapeValidRefusesLineBreaksAndPadding(t *testing.T) {
	valid, body := shapeTestToken()
	if len(body) != 43 {
		t.Fatalf("encoded secret length = %d, want 43", len(body))
	}
	std := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, tokenSecretBytes))
	last := strings.IndexByte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_", body[42])
	nonCanonical := body[:42] + string("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"[last|1])
	rows := []struct {
		name  string
		value string
		want  bool
	}{
		{"control", valid, true},
		{"trailing_lf", valid + "\n", false},
		{"trailing_crlf", valid + "\r\n", false},
		{"trailing_cr", valid + "\r", false},
		{"embedded_lf", valid[:20] + "\n" + valid[20:], false},
		{"embedded_cr", valid[:20] + "\r" + valid[20:], false},
		{"embedded_crlf", valid[:20] + "\r\n" + valid[20:], false},
		{"leading_space", " " + valid, false},
		{"trailing_space", valid + " ", false},
		{"embedded_tab", valid[:20] + "\t" + valid[20:], false},
		{"short_42", valid[:len(valid)-1], false},
		{"long_44", valid + "A", false},
		{"std_alphabet", TokenPrefix + std[:43], false},
		{"padding", valid + "=", false},
		{"non_canonical_last_char", TokenPrefix + nonCanonical, false},
	}
	for _, row := range rows {
		if got := IsTokenShapeValid(row.value); got != row.want {
			t.Errorf("%s: IsTokenShapeValid = %v, want %v", row.name, got, row.want)
		}
	}
}

func TestIsTokenShapeValidAcceptsEveryCanonicalFinalCharacter(t *testing.T) {
	for hi := 0; hi < 256; hi++ {
		for lo := 0; lo < 256; lo++ {
			raw := bytes.Repeat([]byte{0x11}, tokenSecretBytes)
			raw[tokenSecretBytes-2], raw[tokenSecretBytes-1] = byte(hi), byte(lo)
			token := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
			if !IsTokenShapeValid(token) {
				t.Fatalf("canonical encoding of final bytes %d,%d refused", hi, lo)
			}
		}
	}
	for i := 0; i < 2048; i++ {
		token, err := GenerateToken()
		if err != nil {
			t.Fatal(err)
		}
		if !IsTokenShapeValid(token) {
			t.Fatal("an issued token was refused")
		}
	}
}
