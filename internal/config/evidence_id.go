package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const evidenceIDKeyMinimumBytes = 32

func evidenceIDKeysValue(lookup lookupEnv) (map[string][]byte, error) {
	value, err := SecretValue(lookup, "ACR_EVIDENCE_ID_KEYS")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	keys := make(map[string][]byte)
	for pair := range strings.SplitSeq(value, ",") {
		kid, encoded, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || !validEvidenceKID(kid) || encoded == "" {
			return nil, errors.New("ACR_EVIDENCE_ID_KEYS must be comma-separated KID=base64 values")
		}
		key, err := decodeEvidenceKey(encoded)
		if err != nil || len(key) < evidenceIDKeyMinimumBytes {
			return nil, fmt.Errorf("ACR_EVIDENCE_ID_KEYS key %q must decode to at least %d bytes", kid, evidenceIDKeyMinimumBytes)
		}
		if _, exists := keys[kid]; exists {
			return nil, fmt.Errorf("ACR_EVIDENCE_ID_KEYS repeats KID %q", kid)
		}
		keys[kid] = key
	}
	return keys, nil
}

func decodeEvidenceKey(value string) ([]byte, error) {
	if key, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return key, nil
	}
	return base64.StdEncoding.DecodeString(value)
}

func validEvidenceKID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}
