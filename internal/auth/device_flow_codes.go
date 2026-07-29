package auth

import (
	"encoding/base32"
	"encoding/base64"
	"io"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const (
	deviceCodeBytes       = 32
	userCodeRandomBytes   = 5
	maxDeviceCodeAttempts = 8
)

var userCodeEncoding = base32.NewEncoding(contractsv1.DeviceUserCodeAlphabet).WithPadding(base32.NoPadding)

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
	if len(value) != contractsv1.DeviceUserCodeLength {
		return "", false
	}
	for _, character := range value {
		if !strings.ContainsRune(contractsv1.DeviceUserCodeAlphabet, character) {
			return "", false
		}
	}
	return value, true
}
