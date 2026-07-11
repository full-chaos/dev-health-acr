package contextpacket

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	maxEvidenceCandidateRepos   = 64
	evidenceRepositoryTagLength = 16
)

var ErrInvalidEvidenceID = errors.New("contextpacket: invalid evidence id")

type EvidenceIDKeyring struct {
	ActiveKID string
	Keys      map[string][]byte
}

type EvidenceIDCodec struct{ keyring EvidenceIDKeyring }

type EvidenceHandle struct {
	KID           string
	QueryID       string
	RepositoryTag []byte
	MAC           []byte
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

func (c *EvidenceIDCodec) Encode(orgID, repoID, queryID, locator string) (string, error) {
	code, ok := evidenceSourceCode(queryID)
	key := c.keyring.Keys[c.keyring.ActiveKID]
	if !ok || len(key) < 32 || orgID == "" || repoID == "" || locator == "" {
		return "", ErrInvalidEvidenceID
	}
	repositoryTag := evidenceMAC(key, "repository", orgID, repoID)[:evidenceRepositoryTagLength]
	payload := base64.RawURLEncoding.EncodeToString(repositoryTag) + "." + base64.RawURLEncoding.EncodeToString(evidenceMAC(key, orgID, repoID, queryID, locator))
	return "ev1_" + c.keyring.ActiveKID + "_" + code + "_" + payload, nil
}

func (c *EvidenceIDCodec) Parse(handle string) (EvidenceHandle, error) {
	parts := strings.SplitN(handle, "_", 4)
	if len(parts) != 4 || parts[0] != "ev1" || parts[1] == "" {
		return EvidenceHandle{}, ErrInvalidEvidenceID
	}
	queryID, ok := evidenceQueryID(parts[2])
	tagText, macText, found := strings.Cut(parts[3], ".")
	repositoryTag, tagErr := base64.RawURLEncoding.DecodeString(tagText)
	mac, macErr := base64.RawURLEncoding.DecodeString(macText)
	if !ok || !found || strings.Contains(macText, ".") || tagErr != nil || macErr != nil || len(c.keyring.Keys[parts[1]]) < 32 || len(repositoryTag) != evidenceRepositoryTagLength || len(mac) != sha256.Size {
		return EvidenceHandle{}, ErrInvalidEvidenceID
	}
	return EvidenceHandle{KID: parts[1], QueryID: queryID, RepositoryTag: repositoryTag, MAC: mac}, nil
}

func (c *EvidenceIDCodec) Matches(handle EvidenceHandle, orgID, repoID, locator string) bool {
	key := c.keyring.Keys[handle.KID]
	return len(key) >= 32 && c.RoutesTo(handle, orgID, repoID) && hmac.Equal(handle.MAC, evidenceMAC(key, orgID, repoID, handle.QueryID, locator))
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
