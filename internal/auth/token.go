package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

const tokenSecretBytes = 32

func GenerateToken() (string, error) {
	secret := make([]byte, tokenSecretBytes)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return TokenPrefix + base64.RawURLEncoding.EncodeToString(secret), nil
}

func GenerateCredentialID() (string, error) {
	random := make([]byte, 10)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate credential id: %w", err)
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(random)
	return "cred_" + strings.ToLower(encoded), nil
}

func HashToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func DisplayPrefix(token string) string {
	const displayLength = len(TokenPrefix) + 10
	if len(token) <= displayLength {
		return token
	}
	return token[:displayLength]
}

// tokenEncodedLen is the unpadded base64url length of tokenSecretBytes bytes.
const tokenEncodedLen = (tokenSecretBytes*8 + 5) / 6

// IsTokenShapeValid checks the exact alphabet and length of the secret before
// decoding it: the base64 decoder skips CR and LF, so a decode alone cannot
// refuse them.
func IsTokenShapeValid(token string) bool {
	if !strings.HasPrefix(token, TokenPrefix) {
		return false
	}
	secret := strings.TrimPrefix(token, TokenPrefix)
	if len(secret) != tokenEncodedLen || !isRawURLAlphabet(secret) {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(secret)
	return err == nil && len(decoded) == tokenSecretBytes
}

func isRawURLAlphabet(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
