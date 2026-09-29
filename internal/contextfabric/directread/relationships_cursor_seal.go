package directread

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// CursorKeyring is the key material read_relationships seals its cursors
// with (CHAOS-7074 r1 P1). It is the hosted evidence identifier keyring
// (config EvidenceIDActiveKID / EvidenceIDKeys), which production already
// requires; no new secret. The cursor key is never the evidence key itself:
// it is HMAC-SHA256(evidence key, CursorKeyLabel), so the two uses cannot
// collide.
//
// Rotation: a cursor carries the kid of the key that sealed it. New cursors
// use ActiveKID. A cursor sealed under another kid opens while that kid is
// still in Keys, and is refused (invalid_cursor) once it was removed; a
// client then starts the walk again. Cursors live 15 minutes, so a key may
// leave the keyring 15 minutes after it stopped being active.
type CursorKeyring struct {
	ActiveKID string
	Keys      map[string][]byte
}

// CursorKeyLabel domain-separates the cursor key from every other use of the
// evidence keyring.
const CursorKeyLabel = "acr-data.v1/read_relationships/cursor"

// minCursorKeyBytes is the smallest evidence key accepted (the hosted
// keyring's own minimum is 32 bytes).
const minCursorKeyBytes = 32

// ErrCursorKeyring: the keyring cannot seal cursors. The reader is then not
// composed and the route fails closed.
var ErrCursorKeyring = errors.New("read_relationships cursor keyring is not usable")

// cursorSealer encrypts and authenticates cursors (AES-256-GCM). The
// position a cursor holds is the last EXAMINED edge, which may be one the
// caller may not see; sealing is what keeps that id from the caller, and the
// authentication is what makes the position, the bindings and the issue time
// impossible to edit.
type cursorSealer struct {
	active string
	aeads  map[string]cipher.AEAD
}

func newCursorSealer(keyring CursorKeyring) (*cursorSealer, error) {
	active := strings.TrimSpace(keyring.ActiveKID)
	if active == "" || !validCursorKID(active) {
		return nil, fmt.Errorf("%w: no valid active kid", ErrCursorKeyring)
	}
	sealer := &cursorSealer{active: active, aeads: map[string]cipher.AEAD{}}
	for kid, key := range keyring.Keys {
		if !validCursorKID(kid) || len(key) < minCursorKeyBytes {
			continue
		}
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(CursorKeyLabel))
		block, err := aes.NewCipher(mac.Sum(nil))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCursorKeyring, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCursorKeyring, err)
		}
		sealer.aeads[kid] = aead
	}
	if sealer.aeads[active] == nil {
		return nil, fmt.Errorf("%w: the active kid has no key of at least %d bytes", ErrCursorKeyring, minCursorKeyBytes)
	}
	return sealer, nil
}

func validCursorKID(kid string) bool {
	if kid == "" || len(kid) > 64 {
		return false
	}
	for _, r := range kid {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func cursorAAD(kid string) []byte { return []byte(CursorKeyLabel + "\x00" + kid) }

// seal returns "<kid>.<base64url(nonce || ciphertext)>".
func (s *cursorSealer) seal(plaintext []byte) (string, error) {
	aead := s.aeads[s.active]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("read_relationships cursor nonce: %w", err)
	}
	sealed := aead.Seal(nonce, nonce, plaintext, cursorAAD(s.active))
	return s.active + "." + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// open verifies and decrypts a token. Any failure (malformed, unknown or
// retired kid, edited byte) is one refusal: invalid.
func (s *cursorSealer) open(token string) ([]byte, error) {
	kid, payload, found := strings.Cut(strings.TrimSpace(token), ".")
	if !found {
		return nil, &cursorError{CursorInvalid}
	}
	aead := s.aeads[kid]
	if aead == nil {
		return nil, &cursorError{CursorInvalid}
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || len(raw) < aead.NonceSize()+aead.Overhead() {
		return nil, &cursorError{CursorInvalid}
	}
	plaintext, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], cursorAAD(kid))
	if err != nil {
		return nil, &cursorError{CursorInvalid}
	}
	return plaintext, nil
}
