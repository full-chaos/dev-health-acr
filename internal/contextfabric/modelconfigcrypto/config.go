package modelconfigcrypto

import (
	"encoding/base64"
	"fmt"
	"strings"

	acrconfig "github.com/full-chaos/dev-health-acr/internal/config"
)

// Environment variable names. EnvKeys follows the KID=base64,KID=base64
// convention internal/config/evidence_id.go already uses for
// ACR_EVIDENCE_ID_KEYS; EnvActiveKID follows ACR_EVIDENCE_ID_ACTIVE_KID.
// Both use the KEY_FILE-capable acrconfig.SecretValue convention, so a
// deployment may supply either directly or via a mounted secret file.
const (
	EnvKeys      = "ACR_CONTEXT_FABRIC_CREDENTIAL_ENCRYPTION_KEYS"
	EnvActiveKID = "ACR_CONTEXT_FABRIC_CREDENTIAL_ENCRYPTION_ACTIVE_KID"
)

// Configured reports whether the environment names any encryption key
// material at all. Composition uses this the same way
// modelprovider.Configured is used: it is the gate between "an operator has
// not opted into per-organization BYO LLM configuration yet" (leave the
// per-org config store's write path disabled) and "a key is expected, so
// any failure to build a valid Cipher must fail composition closed".
func Configured(lookup func(string) (string, bool)) bool {
	for _, key := range []string{EnvKeys, EnvKeys + "_FILE", EnvActiveKID, EnvActiveKID + "_FILE"} {
		if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// NewFromEnv builds a Cipher from the process environment. Callers own
// deciding when to call it -- see Configured.
func NewFromEnv(lookup func(string) (string, bool)) (*Cipher, error) {
	rawKeys, err := acrconfig.SecretValue(lookup, EnvKeys)
	if err != nil {
		return nil, fmt.Errorf("context fabric credential encryption keys: %w", err)
	}
	activeKID, err := acrconfig.SecretValue(lookup, EnvActiveKID)
	if err != nil {
		return nil, fmt.Errorf("context fabric credential encryption active key id: %w", err)
	}
	keys, err := parseKeys(rawKeys)
	if err != nil {
		return nil, err
	}
	return New(keys, strings.TrimSpace(activeKID))
}

func parseKeys(value string) (map[string][]byte, error) {
	keys := make(map[string][]byte)
	if strings.TrimSpace(value) == "" {
		return keys, nil
	}
	for pair := range strings.SplitSeq(value, ",") {
		kid, encoded, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || kid == "" || encoded == "" {
			return nil, fmt.Errorf("%s must be comma-separated KID=base64 values", EnvKeys)
		}
		key, err := decodeKey(encoded)
		if err != nil || len(key) != KeyLength {
			return nil, fmt.Errorf("%s key %q must decode to exactly %d bytes", EnvKeys, kid, KeyLength)
		}
		if _, exists := keys[kid]; exists {
			return nil, fmt.Errorf("%s repeats key id %q", EnvKeys, kid)
		}
		keys[kid] = key
	}
	return keys, nil
}

func decodeKey(value string) ([]byte, error) {
	if key, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return key, nil
	}
	return base64.StdEncoding.DecodeString(value)
}
