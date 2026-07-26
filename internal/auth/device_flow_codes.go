package auth

import (
	"encoding/base32"
	"encoding/base64"
	"io"
	"strings"
)

const (
	deviceCodeBytes       = 32
	userCodeRandomBytes   = 5
	userCodeLength        = 8
	maxDeviceCodeAttempts = 8
	userCodeAlphabet      = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
)

var userCodeEncoding = base32.NewEncoding(userCodeAlphabet).WithPadding(base32.NoPadding)

func generateDeviceCodes(random io.Reader) (string, string, error) {
	deviceBytes := make([]byte, deviceCodeBytes)
	if _, err := io.ReadFull(random, deviceBytes); err != nil {
		return "", "", err
	}
	userBytes := make([]byte, userCodeRandomBytes)
	if _, err := io.ReadFull(random, userBytes); err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(deviceBytes), userCodeEncoding.EncodeToString(userBytes), nil
}

func normalizeDeviceCode(value string) (string, bool) {
	value = strings.TrimSpace(value)
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != deviceCodeBytes || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return "", false
	}
	return value, true
}

func normalizeUserCode(value string) (string, bool) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if len(value) != userCodeLength {
		return "", false
	}
	for _, character := range value {
		if !strings.ContainsRune(userCodeAlphabet, character) {
			return "", false
		}
	}
	return value, true
}
