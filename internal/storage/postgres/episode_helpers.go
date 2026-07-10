package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type episodePayload struct {
	Digest    string                          `json:"idempotency_digest"`
	Episode   *contractsv1.AgentEpisodeCreate `json:"episode,omitempty"`
	Tombstone bool                            `json:"tombstone,omitempty"`
}

func episodeDigest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func authorizedRepositoryStorageID(principal storage.Principal, slug string) (string, error) {
	if strings.TrimSpace(principal.OrgID) == "" || !episodeRepositoryAllowed(principal.RepositoryScopes, slug) {
		return "", storage.ErrNotFound
	}
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(principal.OrgID)) + "\x00" + strings.ToLower(strings.TrimSpace(slug))))
	value := digest[:16]
	value[6] = value[6]&0x0f | 0x50
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func episodePayloadDigest(payload []byte) string {
	var stored episodePayload
	if err := json.Unmarshal(payload, &stored); err != nil {
		return ""
	}
	return stored.Digest
}

func storedEpisodeCreate(payload []byte) (contractsv1.AgentEpisodeCreate, error) {
	var stored episodePayload
	if err := json.Unmarshal(payload, &stored); err != nil {
		return contractsv1.AgentEpisodeCreate{}, err
	}
	if stored.Tombstone {
		return contractsv1.AgentEpisodeCreate{}, storage.ErrNotFound
	}
	if stored.Episode != nil {
		return *stored.Episode, nil
	}
	var legacy contractsv1.AgentEpisodeCreate
	if err := json.Unmarshal(payload, &legacy); err != nil {
		return contractsv1.AgentEpisodeCreate{}, err
	}
	if legacy.SchemaVersion == "" {
		return contractsv1.AgentEpisodeCreate{}, errors.New("episode payload has no create record")
	}
	return legacy, nil
}

func redactedCreate(value contractsv1.AgentEpisodeCreate) contractsv1.AgentEpisodeCreate {
	value.Goal, value.Summary, value.TaskRef = redactedEpisodeText, redactedEpisodeText, ""
	value.Artifacts = contractsv1.EpisodeArtifacts{FilesTouched: []string{}, ArtifactURIs: []string{}, TestsRun: []string{}}
	value.Transcript = contractsv1.TranscriptRef{Mode: "none"}
	return value
}

func episodeRepositoryAllowed(scopes []string, slug string) bool {
	normalized := strings.ToLower(slug)
	owner, _, _ := strings.Cut(normalized, "/")
	for _, scope := range scopes {
		scope = strings.ToLower(strings.TrimSpace(scope))
		if scope == "*" || scope == normalized || scope == owner+"/*" {
			return true
		}
	}
	return false
}
