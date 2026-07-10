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

func IsTokenShapeValid(token string) bool {
	if !strings.HasPrefix(token, TokenPrefix) {
		return false
	}
	secret := strings.TrimPrefix(token, TokenPrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(secret)
	return err == nil && len(decoded) == tokenSecretBytes
}
