package contextpacket

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"time"
)

func evidenceAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(evidenceMAC(key, "locator-encryption"))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func evidenceAAD(version, kid, code, tag, nonce string) []byte {
	return []byte(version + "_" + kid + "_" + code + "_" + tag + "." + nonce)
}

func parseEvidenceHandleV1(parts []string, queryID string) (EvidenceHandle, error) {
	tagText, macText, found := strings.Cut(parts[3], ".")
	repositoryTag, tagErr := base64.RawURLEncoding.DecodeString(tagText)
	mac, macErr := base64.RawURLEncoding.DecodeString(macText)
	if !found || strings.Contains(macText, ".") || tagErr != nil || macErr != nil || len(repositoryTag) != evidenceRepositoryTagLength || len(mac) != sha256.Size {
		return EvidenceHandle{}, ErrInvalidEvidenceID
	}
	return EvidenceHandle{Version: evidenceIDVersionV1, KID: parts[1], QueryID: queryID, RepositoryTag: repositoryTag, MAC: mac}, nil
}

func parseEvidenceHandleV2(parts []string, queryID string, key []byte) (EvidenceHandle, error) {
	payload := strings.Split(parts[3], ".")
	if len(payload) != 3 {
		return EvidenceHandle{}, ErrInvalidEvidenceID
	}
	repositoryTag, tagErr := base64.RawURLEncoding.DecodeString(payload[0])
	nonce, nonceErr := base64.RawURLEncoding.DecodeString(payload[1])
	sealed, sealedErr := base64.RawURLEncoding.DecodeString(payload[2])
	aead, aeadErr := evidenceAEAD(key)
	if tagErr != nil || nonceErr != nil || sealedErr != nil || aeadErr != nil || len(repositoryTag) != evidenceRepositoryTagLength || len(nonce) != aead.NonceSize() || len(sealed) != evidenceIDPayloadLength+aead.Overhead() {
		return EvidenceHandle{}, ErrInvalidEvidenceID
	}
	plaintext, err := aead.Open(nil, nonce, sealed, evidenceAAD(parts[0], parts[1], parts[2], payload[0], payload[1]))
	if err != nil || len(plaintext) != evidenceIDPayloadLength || plaintext[0] > 7 {
		return EvidenceHandle{}, ErrInvalidEvidenceID
	}
	handle := EvidenceHandle{Version: evidenceIDVersionV2, KID: parts[1], QueryID: queryID, RepositoryTag: repositoryTag, LookupDigest: plaintext[1:33], RepositoryWide: plaintext[0]&1 != 0}
	if plaintext[0]&2 != 0 {
		handle.BranchDigest = plaintext[33:65]
	} else if !allZero(plaintext[33:65]) {
		return EvidenceHandle{}, ErrInvalidEvidenceID
	}
	if plaintext[0]&4 != 0 {
		value := time.UnixMilli(int64(binary.BigEndian.Uint64(plaintext[65:]))).UTC()
		handle.AsOf = &value
	} else if !allZero(plaintext[65:]) {
		return EvidenceHandle{}, ErrInvalidEvidenceID
	}
	return handle, nil
}

func allZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}
