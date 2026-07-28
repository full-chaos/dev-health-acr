package contextpacket

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const (
	maxEvidenceCandidateRepos   = 64
	evidenceRepositoryTagLength = 16
	evidenceIDVersionV1         = "ev1"
	evidenceIDVersionV2         = "ev2"
	evidenceIDPayloadLength     = 73
)

var ErrInvalidEvidenceID = errors.New("contextpacket: invalid evidence id")

type EvidenceIDKeyring struct {
	ActiveKID string
	Keys      map[string][]byte
}

type EvidenceIDCodec struct{ keyring EvidenceIDKeyring }

type EvidenceIDContext struct {
	Branch         string
	AsOf           *time.Time
	RepositoryWide bool
}

type EvidenceHandle struct {
	Version        string
	KID            string
	QueryID        string
	RepositoryTag  []byte
	MAC            []byte
	LookupDigest   []byte
	BranchDigest   []byte
	AsOf           *time.Time
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

func (c *EvidenceIDCodec) EncodeEvidence(orgID, repoID string, evidence contractsv1.EvidenceRef, handleContext EvidenceIDContext) (string, error) {
	code, ok := evidenceSourceCode(evidence.SourceVersion)
	key := c.keyring.Keys[c.keyring.ActiveKID]
	if !ok || len(key) < 32 || orgID == "" || repoID == "" || evidence.EvidenceRefID == "" {
		return "", ErrInvalidEvidenceID
	}
	repositoryTag := evidenceMAC(key, "repository", orgID, repoID)[:evidenceRepositoryTagLength]
	plaintext := make([]byte, evidenceIDPayloadLength)
	if handleContext.RepositoryWide {
		plaintext[0] = 1
	}
	copy(plaintext[1:33], evidenceLookupDigest(evidence))
	if query := catalogSourceQuery(evidence.SourceVersion); query != nil && query.Scope == EvidenceScopeBranch && strings.TrimSpace(handleContext.Branch) != "" {
		branch := strings.TrimSpace(handleContext.Branch)
		plaintext[0] |= 2
		digest := sha256.Sum256([]byte(branch))
		copy(plaintext[33:65], digest[:])
	}
	if handleContext.AsOf != nil {
		plaintext[0] |= 4
		binary.BigEndian.PutUint64(plaintext[65:], uint64(handleContext.AsOf.UTC().UnixMilli()))
	}
	nonce := evidenceMAC(key, "locator-nonce", evidenceIDVersionV2, c.keyring.ActiveKID, code, orgID, repoID, evidence.SourceVersion, string(plaintext))[:12]
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

func (c *EvidenceIDCodec) Matches(handle EvidenceHandle, orgID, repoID string, evidence contractsv1.EvidenceRef) bool {
	key := c.keyring.Keys[handle.KID]
	if len(key) < 32 || !c.RoutesTo(handle, orgID, repoID) {
		return false
	}
	if handle.Version == evidenceIDVersionV2 {
		return len(handle.LookupDigest) == sha256.Size && hmac.Equal(handle.LookupDigest, evidenceLookupDigest(evidence))
	}
	return handle.Version == evidenceIDVersionV1 && hmac.Equal(handle.MAC, evidenceMAC(key, orgID, repoID, handle.QueryID, evidence.EvidenceRefID))
}

func (h EvidenceHandle) LookupHash() string {
	if h.Version != evidenceIDVersionV2 || len(h.LookupDigest) != sha256.Size {
		return ""
	}
	return hex.EncodeToString(h.LookupDigest)
}

func (h EvidenceHandle) BranchHash() string {
	if h.Version != evidenceIDVersionV2 || len(h.BranchDigest) != sha256.Size {
		return ""
	}
	return hex.EncodeToString(h.BranchDigest)
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
