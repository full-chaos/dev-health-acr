package contextpacket

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	maxEvidenceCandidateRepos   = 64
	evidenceRepositoryTagLength = 16
	evidenceIDVersionV1         = "ev1"
	evidenceIDVersionV2         = "ev2"
)

var ErrInvalidEvidenceID = errors.New("contextpacket: invalid evidence id")

type EvidenceIDKeyring struct {
	ActiveKID string
	Keys      map[string][]byte
}

type EvidenceIDCodec struct{ keyring EvidenceIDKeyring }

type EvidenceHandle struct {
	Version        string
	KID            string
	QueryID        string
	RepositoryTag  []byte
	MAC            []byte
	LocatorDigest  []byte
	RepositoryWide bool
}

func NewEvidenceIDCodec(keyring EvidenceIDKeyring) (*EvidenceIDCodec, error) {
	if !validEvidenceKID(keyring.ActiveKID) || len(keyring.Keys[keyring.ActiveKID]) < 32 {
		return nil, ErrInvalidEvidenceID
	}
	keys := make(map[string][]byte, len(keyring.Keys))
	for kid, key := range keyring.Keys {
		if !validEvidenceKID(kid) || len(key) < 32 {
			return nil, ErrInvalidEvidenceID
		}
		keys[kid] = append([]byte(nil), key...)
	}
	return &EvidenceIDCodec{keyring: EvidenceIDKeyring{ActiveKID: keyring.ActiveKID, Keys: keys}}, nil
}

func (c *EvidenceIDCodec) Encode(orgID, repoID, queryID, locator string, repositoryWide bool) (string, error) {
	code, ok := evidenceSourceCode(queryID)
	key := c.keyring.Keys[c.keyring.ActiveKID]
	if !ok || len(key) < 32 || orgID == "" || repoID == "" || locator == "" {
		return "", ErrInvalidEvidenceID
	}
	repositoryTag := evidenceMAC(key, "repository", orgID, repoID)[:evidenceRepositoryTagLength]
	digest := sha256.Sum256([]byte(locator))
	plaintext := make([]byte, sha256.Size+1)
	if repositoryWide {
		plaintext[0] = 1
	}
	copy(plaintext[1:], digest[:])
	nonce := evidenceMAC(key, "locator-nonce", orgID, repoID, queryID, string(plaintext))[:12]
	aead, err := evidenceAEAD(key)
	if err != nil {
		return "", ErrInvalidEvidenceID
	}
	tagText := base64.RawURLEncoding.EncodeToString(repositoryTag)
	nonceText := base64.RawURLEncoding.EncodeToString(nonce)
	aad := evidenceAAD(evidenceIDVersionV2, c.keyring.ActiveKID, code, tagText, nonceText)
	sealed := aead.Seal(nil, nonce, plaintext, aad)
	payload := tagText + "." + nonceText + "." + base64.RawURLEncoding.EncodeToString(sealed)
	return evidenceIDVersionV2 + "_" + c.keyring.ActiveKID + "_" + code + "_" + payload, nil
}

func (c *EvidenceIDCodec) Parse(handle string) (EvidenceHandle, error) {
	parts := strings.SplitN(handle, "_", 4)
	if len(parts) != 4 || parts[1] == "" {
		return EvidenceHandle{}, ErrInvalidEvidenceID
	}
	queryID, ok := evidenceQueryID(parts[2])
	key := c.keyring.Keys[parts[1]]
	if !ok || len(key) < 32 {
		return EvidenceHandle{}, ErrInvalidEvidenceID
	}
	switch parts[0] {
	case evidenceIDVersionV1:
		return parseEvidenceHandleV1(parts, queryID)
	case evidenceIDVersionV2:
		return parseEvidenceHandleV2(parts, queryID, key)
	default:
		return EvidenceHandle{}, ErrInvalidEvidenceID
	}
}

func (c *EvidenceIDCodec) Matches(handle EvidenceHandle, orgID, repoID, locator string) bool {
	key := c.keyring.Keys[handle.KID]
	if len(key) < 32 || !c.RoutesTo(handle, orgID, repoID) {
		return false
	}
	if handle.Version == evidenceIDVersionV2 {
		digest := sha256.Sum256([]byte(locator))
		return len(handle.LocatorDigest) == sha256.Size && hmac.Equal(handle.LocatorDigest, digest[:])
	}
	return handle.Version == evidenceIDVersionV1 && hmac.Equal(handle.MAC, evidenceMAC(key, orgID, repoID, handle.QueryID, locator))
}

func (h EvidenceHandle) LocatorHash() string {
	if h.Version != evidenceIDVersionV2 || len(h.LocatorDigest) != sha256.Size {
		return ""
	}
	return hex.EncodeToString(h.LocatorDigest)
}

func (c *EvidenceIDCodec) RoutesTo(handle EvidenceHandle, orgID, repoID string) bool {
	key := c.keyring.Keys[handle.KID]
	if len(key) < 32 || len(handle.RepositoryTag) != evidenceRepositoryTagLength {
		return false
	}
	expected := evidenceMAC(key, "repository", orgID, repoID)[:evidenceRepositoryTagLength]
	return hmac.Equal(handle.RepositoryTag, expected)
}

func evidenceMAC(key []byte, values ...string) []byte {
	mac := hmac.New(sha256.New, key)
	for _, value := range values {
		_, _ = fmt.Fprintf(mac, "%d:%s", len(value), value)
	}
	return mac.Sum(nil)
}

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
	if tagErr != nil || nonceErr != nil || sealedErr != nil || aeadErr != nil || len(repositoryTag) != evidenceRepositoryTagLength || len(nonce) != aead.NonceSize() || len(sealed) != sha256.Size+1+aead.Overhead() {
		return EvidenceHandle{}, ErrInvalidEvidenceID
	}
	plaintext, err := aead.Open(nil, nonce, sealed, evidenceAAD(parts[0], parts[1], parts[2], payload[0], payload[1]))
	if err != nil || len(plaintext) != sha256.Size+1 || plaintext[0] > 1 {
		return EvidenceHandle{}, ErrInvalidEvidenceID
	}
	return EvidenceHandle{Version: evidenceIDVersionV2, KID: parts[1], QueryID: queryID, RepositoryTag: repositoryTag, LocatorDigest: plaintext[1:], RepositoryWide: plaintext[0] == 1}, nil
}

func evidenceSourceCode(queryID string) (string, bool) {
	code, ok := evidenceSourceCodes[queryID]
	return code, ok
}

func evidenceQueryID(code string) (string, bool) {
	queryID, ok := evidenceQueryCodes[code]
	return queryID, ok
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

var evidenceSourceCodes = map[string]string{
	"repository_freshness.v1": "repo", "work_items.v1": "work", "work_item_dependencies.v1": "deps", "git_commits.v1": "commit", "git_commit_files.v1": "cfile", "pull_requests.v1": "pr", "pull_request_reviews.v1": "review", "ci_pipeline_runs.v1": "ci", "work_graph.v1": "graph", "ai_workflow_runs.v1": "airun", "ai_workflow_artifacts.v1": "aiart", "ai_review_outcomes.v1": "aiout", "deployments.v1": "deploy", "incidents.v1": "incident", "deployment_incident_provenance.v1": "depinc", "file_hotspots.v1": "hotspot", "file_complexity.v1": "complex",
}

var evidenceQueryCodes = func() map[string]string {
	values := make(map[string]string, len(evidenceSourceCodes))
	for queryID, code := range evidenceSourceCodes {
		values[code] = queryID
	}
	return values
}()
