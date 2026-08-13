// Package modelconfigcrypto encrypts and decrypts the credential field of a
// per-organization BYO LLM configuration (CHAOS-3775, AC-3775-4) before it
// ever reaches a database row, and decrypts it only at the point a
// contextfabric.ModelRuntime is constructed for that organization
// (internal/contextfabric/modelprovider.New's Config.APIKey).
//
// This package is deliberately the ONLY place in the repository that holds
// an org's BYO LLM credential in plaintext outside of the single in-memory
// value passed to modelprovider.New. It never logs, and never returns,
// plaintext except through Decrypt's direct return value.
//
// AEAD choice: AES-256-GCM from the Go standard library (crypto/aes,
// crypto/cipher) -- no third-party crypto dependency, no custom cipher
// construction. Key management follows the exact KID-rotation shape
// internal/config/evidence_id.go already uses for
// ACR_EVIDENCE_ID_KEYS/ACR_EVIDENCE_ID_ACTIVE_KID: a deployment names a set
// of 32-byte keys under short key ids, one KID is marked active for new
// writes, and decrypt uses ONLY the KID a ciphertext was stored under --
// never a try-every-configured-key loop (team-lead review requirement 3: an
// unknown/retired KID must fail loudly with ErrUnknownKID, not silently
// hunt for a key that still works). This lets an operator rotate the master
// key by adding a new KID, repointing ACTIVE_KID at it, and re-saving each
// org's configuration over time -- old ciphertext keeps decrypting under
// its original KID until it is rewritten.
//
// AAD binding (team-lead review requirement 2): every Encrypt/Decrypt call
// takes an explicit Additional Authenticated Data value that the caller
// binds to the row's own identity -- see CredentialAAD. GCM authenticates
// AAD without encrypting it, so a ciphertext physically copied from one
// organization's row into another's (a corrupted migration, a bad manual
// UPDATE, a copy-paste in an admin tool) fails authentication at Decrypt
// time instead of silently opening under the wrong identity.
package modelconfigcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// KeyLength is the required decoded length of every configured encryption
// key: 32 bytes, selecting AES-256.
const KeyLength = 32

var (
	// ErrNoActiveKey means the cipher has no key configured to encrypt
	// under. Composition must fail closed on this, not silently skip
	// encryption or store plaintext.
	ErrNoActiveKey = errors.New("modelconfigcrypto: no active encryption key configured")
	// ErrUnknownKID means a stored ciphertext names a key id this
	// deployment does not currently have configured -- e.g. a key was
	// retired before every row encrypted under it was rewritten. This is
	// an operational data-availability error, not a corruption: the
	// ciphertext itself may be intact.
	ErrUnknownKID = errors.New("modelconfigcrypto: ciphertext key id is not configured")
	// ErrDecryptFailed means GCM authentication failed: the ciphertext,
	// nonce, or KID association does not match what Encrypt produced. This
	// covers both genuine corruption and tampering; the two are
	// indistinguishable and are handled identically.
	ErrDecryptFailed = errors.New("modelconfigcrypto: credential ciphertext failed to authenticate")
)

// Cipher encrypts and decrypts org model config credentials under a
// deployment-configured set of AES-256-GCM keys, keyed by KID.
type Cipher struct {
	keys      map[string][]byte
	activeKID string
}

// New builds a Cipher from keys (kid -> 32-byte AES-256 key) and the KID new
// encryptions use. It fails closed rather than accepting a partially usable
// configuration: every key must decode to exactly KeyLength bytes, and
// activeKID must name a key that is actually present in keys.
func New(keys map[string][]byte, activeKID string) (*Cipher, error) {
	if len(keys) == 0 {
		return nil, ErrNoActiveKey
	}
	for kid, key := range keys {
		if len(key) != KeyLength {
			return nil, fmt.Errorf("modelconfigcrypto: key %q must be %d bytes", kid, KeyLength)
		}
	}
	if activeKID == "" {
		return nil, ErrNoActiveKey
	}
	if _, ok := keys[activeKID]; !ok {
		return nil, fmt.Errorf("modelconfigcrypto: active key id %q is not among the configured keys", activeKID)
	}
	copied := make(map[string][]byte, len(keys))
	for kid, key := range keys {
		copied[kid] = append([]byte(nil), key...)
	}
	return &Cipher{keys: copied, activeKID: activeKID}, nil
}

// Encrypt seals plaintext under the active key, authenticating aad without
// encrypting it (see CredentialAAD). It returns the ciphertext (nonce
// prepended) and the KID it was sealed under -- the caller persists both
// AND the same aad-deriving identity (e.g. the org_id column already on the
// row); Decrypt needs the KID to select the right key and the identical aad
// to authenticate, since a deployment may have several keys configured
// during a rotation window and every row already carries its own identity.
func (c *Cipher) Encrypt(plaintext string, aad []byte) (ciphertext []byte, kid string, err error) {
	if c == nil {
		return nil, "", ErrNoActiveKey
	}
	gcm, err := c.aead(c.activeKID)
	if err != nil {
		return nil, "", err
	}
	// Fresh, crypto/rand-sourced nonce on every call -- never derived from
	// a counter or any other deterministic input (team-lead review
	// requirement 1). GCM's security guarantee depends on a nonce never
	// repeating under the same key; a random 96-bit nonce makes collision
	// negligible for any realistic number of credentials this deployment
	// will ever encrypt.
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", fmt.Errorf("modelconfigcrypto: generate nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), aad)
	return sealed, c.activeKID, nil
}

// Decrypt opens ciphertext (as produced by Encrypt, nonce prepended) using
// the key named by kid, authenticating against the same aad Encrypt was
// called with. It never falls back to a different key: an org's stored kid
// is the only key its ciphertext may ever be opened under (team-lead review
// requirement 3). A ciphertext authenticated under a DIFFERENT aad --
// e.g. one physically copied from another organization's row -- fails here
// with ErrDecryptFailed, even if kid and the raw bytes are otherwise
// well-formed (team-lead review requirement 2).
func (c *Cipher) Decrypt(ciphertext []byte, kid string, aad []byte) (string, error) {
	if c == nil {
		return "", ErrNoActiveKey
	}
	gcm, err := c.aead(kid)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", ErrDecryptFailed
	}
	nonce, sealed := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, sealed, aad)
	if err != nil {
		return "", ErrDecryptFailed
	}
	return string(plaintext), nil
}

// CredentialAAD derives the Additional Authenticated Data value for a
// per-organization BYO LLM credential (team-lead review requirement 2): a
// fixed purpose tag plus the owning organization's id, so a ciphertext can
// only ever authenticate against the exact organization it was sealed for.
// Both Encrypt and Decrypt must be called with the identical value for the
// same row -- the caller (internal/contextfabric/pgmodelconfig) derives it
// from the row's own org_id on both the write and the read path, never from
// caller input that could diverge from the row.
func CredentialAAD(orgID string) []byte {
	return []byte("acr:org-model-credential:" + orgID)
}

func (c *Cipher) aead(kid string) (cipher.AEAD, error) {
	key, ok := c.keys[kid]
	if !ok {
		return nil, ErrUnknownKID
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("modelconfigcrypto: construct cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("modelconfigcrypto: construct AEAD: %w", err)
	}
	return gcm, nil
}
