package auth

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/full-chaos/dev-health-go/authverify"
)

// keys delegates JWKS loading and validation to authverify's standalone
// Ed25519JWKSVerifier, translating its ErrInvalidJWKS back to ACR's own
// ErrInvalidWebAssertion at the boundary so this package's error contract
// (and its tests) are unchanged. A file-read failure passes through
// unwrapped, exactly as before -- every caller of keys() already treats
// any non-nil error as ErrInvalidWebAssertion (see NewWebAssertionVerifier
// and Verify).
func (v *WebAssertionVerifier) keys() (map[string]ed25519.PublicKey, error) {
	keys, err := v.jwks.Keys()
	if err != nil {
		if errors.Is(err, authverify.ErrInvalidJWKS) {
			return nil, ErrInvalidWebAssertion
		}
		return nil, err
	}
	return keys, nil
}

func decodeWebAssertion[T any](segment string) (T, error) {
	var value T
	encoded, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return value, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil || decoder.More() {
		return value, ErrInvalidWebAssertion
	}
	return value, nil
}

func singleHeader(r *http.Request, name string) (string, bool) {
	values := r.Header.Values(name)
	if len(values) != 1 || strings.TrimSpace(values[0]) != values[0] || values[0] == "" {
		return "", false
	}
	return values[0], true
}

func validWebAssertionID(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 256
}
