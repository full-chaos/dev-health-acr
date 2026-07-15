package auth

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

type jwksDocument struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	KeyType string `json:"kty"`
	Curve   string `json:"crv"`
	KeyID   string `json:"kid"`
	Use     string `json:"use"`
	Alg     string `json:"alg"`
	X       string `json:"x"`
}

func (v *WebAssertionVerifier) keys() (map[string]ed25519.PublicKey, error) {
	encoded, err := os.ReadFile(v.jwksPath)
	if err != nil {
		return nil, err
	}
	var document jwksDocument
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || len(document.Keys) == 0 {
		return nil, ErrInvalidWebAssertion
	}
	keys := make(map[string]ed25519.PublicKey, len(document.Keys))
	for _, candidate := range document.Keys {
		key, err := base64.RawURLEncoding.DecodeString(candidate.X)
		if err != nil || candidate.KeyType != "OKP" || candidate.Curve != "Ed25519" || candidate.Alg != "EdDSA" ||
			(candidate.Use != "" && candidate.Use != "sig") || !validWebAssertionID(candidate.KeyID) || len(key) != ed25519.PublicKeySize {
			return nil, ErrInvalidWebAssertion
		}
		if _, duplicate := keys[candidate.KeyID]; duplicate {
			return nil, ErrInvalidWebAssertion
		}
		keys[candidate.KeyID] = ed25519.PublicKey(key)
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
