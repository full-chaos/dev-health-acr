package modelconfigcrypto

import (
	"encoding/base64"
	"strings"
	"testing"
)

func testKey(t *testing.T, seed byte) []byte {
	t.Helper()
	key := make([]byte, KeyLength)
	for i := range key {
		key[i] = seed
	}
	return key
}

var aadOrgA = CredentialAAD("org-a")
var aadOrgB = CredentialAAD("org-b")

// TestEncryptDecrypt_roundTrips locks the basic contract: what Encrypt
// seals, Decrypt under the same KID and the same AAD opens back to the
// original plaintext.
func TestEncryptDecrypt_roundTrips(t *testing.T) {
	cipher, err := New(map[string][]byte{"k1": testKey(t, 0x01)}, "k1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ciphertext, kid, err := cipher.Encrypt("sk-live-secret", aadOrgA)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if kid != "k1" {
		t.Fatalf("kid = %q, want k1", kid)
	}
	if strings.Contains(string(ciphertext), "sk-live-secret") {
		t.Fatalf("ciphertext leaks plaintext")
	}
	plaintext, err := cipher.Decrypt(ciphertext, kid, aadOrgA)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if plaintext != "sk-live-secret" {
		t.Fatalf("plaintext = %q, want sk-live-secret", plaintext)
	}
}

// TestDecrypt_rejectsTampering (AC-3775-4 adjacent: a credential must never
// silently decrypt to the wrong value) locks that GCM authentication fails
// closed on a flipped byte, rather than returning corrupted plaintext.
func TestDecrypt_rejectsTampering(t *testing.T) {
	cipher, err := New(map[string][]byte{"k1": testKey(t, 0x02)}, "k1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ciphertext, kid, err := cipher.Encrypt("sk-live-secret", aadOrgA)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	tampered := append([]byte(nil), ciphertext...)
	tampered[len(tampered)-1] ^= 0xFF
	if _, err := cipher.Decrypt(tampered, kid, aadOrgA); err == nil {
		t.Fatal("Decrypt accepted tampered ciphertext")
	}
}

// TestDecrypt_unknownKID locks ErrUnknownKID for a ciphertext whose KID this
// deployment no longer has configured -- distinct from tampering, and the
// case a retired rotation key produces. Locks team-lead review requirement
// 3: Decrypt takes the stored KID directly and never tries another
// configured key as a fallback.
func TestDecrypt_unknownKID(t *testing.T) {
	cipher, err := New(map[string][]byte{"k1": testKey(t, 0x03)}, "k1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ciphertext, _, err := cipher.Encrypt("sk-live-secret", aadOrgA)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	_, err = cipher.Decrypt(ciphertext, "unknown-kid", aadOrgA)
	if err == nil {
		t.Fatal("Decrypt accepted an unknown key id")
	}
	if err != ErrUnknownKID {
		t.Fatalf("err = %v, want the named ErrUnknownKID sentinel", err)
	}
}

// TestDecrypt_rejectsCiphertextSwappedBetweenOrganizations is team-lead
// review requirement 2's exact probe: a ciphertext sealed under one
// organization's AAD must fail authentication when opened under a
// different organization's AAD, even with the correct KID and otherwise
// byte-identical ciphertext -- the scenario a bad migration, a hand-edited
// UPDATE, or a copy-paste in an admin tool would produce by physically
// moving one org's encrypted credential into another org's row.
func TestDecrypt_rejectsCiphertextSwappedBetweenOrganizations(t *testing.T) {
	cipher, err := New(map[string][]byte{"k1": testKey(t, 0x09)}, "k1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ciphertextA, kid, err := cipher.Encrypt("sk-org-a-secret", aadOrgA)
	if err != nil {
		t.Fatalf("Encrypt org-a: %v", err)
	}

	// Attempt to open org-a's ciphertext as if it were org-b's row.
	_, err = cipher.Decrypt(ciphertextA, kid, aadOrgB)
	if err == nil {
		t.Fatal("Decrypt opened one organization's ciphertext under a different organization's AAD")
	}
	if err != ErrDecryptFailed {
		t.Fatalf("err = %v, want ErrDecryptFailed", err)
	}

	// The identical ciphertext still opens correctly under its own AAD --
	// proves the failure above is specifically the AAD mismatch, not some
	// other corruption.
	plaintext, err := cipher.Decrypt(ciphertextA, kid, aadOrgA)
	if err != nil {
		t.Fatalf("Decrypt under the correct AAD: %v", err)
	}
	if plaintext != "sk-org-a-secret" {
		t.Fatalf("plaintext = %q, want sk-org-a-secret", plaintext)
	}
}

// TestCredentialAAD_isDistinctPerOrganization is a sanity check on the AAD
// derivation itself: two different organization ids must never collide
// into the same AAD value (which would silently defeat requirement 2).
func TestCredentialAAD_isDistinctPerOrganization(t *testing.T) {
	if string(CredentialAAD("org-a")) == string(CredentialAAD("org-b")) {
		t.Fatal("CredentialAAD produced the same value for two different organization ids")
	}
	if string(CredentialAAD("org-a")) != string(CredentialAAD("org-a")) {
		t.Fatal("CredentialAAD is not deterministic for the same organization id")
	}
}

// TestNew_rejectsShortKey locks that a key not decoding to exactly
// KeyLength bytes fails composition, rather than silently truncating or
// padding into a weaker key.
func TestNew_rejectsShortKey(t *testing.T) {
	if _, err := New(map[string][]byte{"k1": []byte("too-short")}, "k1"); err == nil {
		t.Fatal("New accepted a short key")
	}
}

// TestNew_rejectsActiveKIDNotConfigured locks that naming an active KID
// absent from the key set fails composition, rather than silently falling
// back to whichever key happens to be present.
func TestNew_rejectsActiveKIDNotConfigured(t *testing.T) {
	if _, err := New(map[string][]byte{"k1": testKey(t, 0x04)}, "k2"); err == nil {
		t.Fatal("New accepted an active key id that isn't configured")
	}
}

// TestNew_rejectsEmpty locks that a Cipher with no keys at all fails
// closed, mirroring the "opted in but mis-specified" composition failures
// modelprovider.Config.validate() uses for the deployment-default surface.
func TestNew_rejectsEmpty(t *testing.T) {
	if _, err := New(nil, ""); err == nil {
		t.Fatal("New accepted an empty key set")
	}
}

// TestRotation_oldCiphertextStillDecryptsAfterActiveKIDChanges locks the
// whole point of KID-keyed rotation: adding a new key and repointing
// ACTIVE_KID at it must not strand ciphertext already sealed under the old
// key.
func TestRotation_oldCiphertextStillDecryptsAfterActiveKIDChanges(t *testing.T) {
	before, err := New(map[string][]byte{"k1": testKey(t, 0x05)}, "k1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ciphertext, kid, err := before.Encrypt("sk-live-secret", aadOrgA)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	after, err := New(map[string][]byte{"k1": testKey(t, 0x05), "k2": testKey(t, 0x06)}, "k2")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	plaintext, err := after.Decrypt(ciphertext, kid, aadOrgA)
	if err != nil {
		t.Fatalf("Decrypt after rotation: %v", err)
	}
	if plaintext != "sk-live-secret" {
		t.Fatalf("plaintext = %q, want sk-live-secret", plaintext)
	}
}

func TestNewFromEnv_parsesKIDRotationSyntax(t *testing.T) {
	key1 := base64.StdEncoding.EncodeToString(testKey(t, 0x07))
	key2 := base64.StdEncoding.EncodeToString(testKey(t, 0x08))
	lookup := mapLookup(map[string]string{
		EnvKeys:      "k1=" + key1 + ",k2=" + key2,
		EnvActiveKID: "k2",
	})
	cipher, err := NewFromEnv(lookup)
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	ciphertext, kid, err := cipher.Encrypt("sk-live-secret", aadOrgA)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if kid != "k2" {
		t.Fatalf("kid = %q, want k2", kid)
	}
	if _, err := cipher.Decrypt(ciphertext, kid, aadOrgA); err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
}

// TestNewFromEnv_namesTheVariableOnAShortKey locks team-lead review
// requirement 4: a misconfigured key length fails at config parse with the
// offending environment variable named in the error, the same AC-3770-2
// style newContextFabricModelRuntime already uses for the deployment
// surface.
func TestNewFromEnv_namesTheVariableOnAShortKey(t *testing.T) {
	lookup := mapLookup(map[string]string{
		EnvKeys:      "k1=" + base64.StdEncoding.EncodeToString([]byte("too-short")),
		EnvActiveKID: "k1",
	})
	_, err := NewFromEnv(lookup)
	if err == nil {
		t.Fatal("NewFromEnv accepted a key that does not decode to KeyLength bytes")
	}
	if !strings.Contains(err.Error(), EnvKeys) {
		t.Fatalf("err = %v, want it to name %s", err, EnvKeys)
	}
}

func TestConfigured_reportsFalseWhenUnset(t *testing.T) {
	if Configured(mapLookup(nil)) {
		t.Fatal("Configured reported true for an empty environment")
	}
}

func TestConfigured_reportsTrueWhenKeysSet(t *testing.T) {
	if !Configured(mapLookup(map[string]string{EnvKeys: "k1=AAAA"})) {
		t.Fatal("Configured reported false when EnvKeys is set")
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
