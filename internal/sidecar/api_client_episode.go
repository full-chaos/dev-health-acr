package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// RecordEpisodeResult distinguishes a persisted hosted episode from a
// successful no_persist outcome, which deliberately has no episode payload.
type RecordEpisodeResult struct {
	Episode   *contractsv1.AgentEpisode
	NoPersist bool
}

// RecordEpisode records an agent episode through the fixed hosted endpoint.
func (c *Client) RecordEpisode(ctx context.Context, episode contractsv1.AgentEpisodeCreate) (RecordEpisodeResult, error) {
	if !c.cfg.EnableWriteback {
		return RecordEpisodeResult{}, ErrWritebackDisabled
	}
	if episode.Transcript.Mode != "none" && !c.cfg.EnableTranscriptCapture {
		return RecordEpisodeResult{}, ErrTranscriptCaptureDisabled
	}
	episode.SchemaVersion = contractsv1.AgentEpisodeCreateSchema
	episode.Client = contractsv1.EpisodeClient{
		Name:           c.cfg.ClientName,
		Version:        c.cfg.ClientVersion,
		SidecarVersion: c.cfg.SidecarVersion,
		AgentName:      episode.Client.AgentName,
		Model:          episode.Client.Model,
	}
	if err := episode.Validate(); err != nil {
		return RecordEpisodeResult{}, fmt.Errorf("invalid agent episode request: %w", err)
	}
	encoded, err := json.Marshal(episode)
	if err != nil {
		return RecordEpisodeResult{}, fmt.Errorf("encode agent episode request: %w", err)
	}

	var recorded contractsv1.AgentEpisode
	status, err := c.callWithHeaders(ctx, http.MethodPost, episodesPath, encoded, &recorded, http.Header{
		"Idempotency-Key": []string{episode.IdempotencyKey},
	})
	if err != nil {
		return RecordEpisodeResult{}, err
	}
	if status == http.StatusNoContent {
		if episode.RetentionClass != "no_persist" {
			return RecordEpisodeResult{}, fmt.Errorf("%w: received no-content response for retention class %q", ErrInvalidResponse, episode.RetentionClass)
		}
		return RecordEpisodeResult{NoPersist: true}, nil
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return RecordEpisodeResult{}, fmt.Errorf("%w: unexpected success status %d", ErrInvalidResponse, status)
	}
	if err := validateAgentEpisode(recorded); err != nil {
		return RecordEpisodeResult{}, err
	}
	if recorded.ClientEpisodeID != episode.ClientEpisodeID || recorded.IdempotencyKey != episode.IdempotencyKey {
		return RecordEpisodeResult{}, fmt.Errorf("%w: agent episode response does not match request attribution", ErrInvalidResponse)
	}
	return RecordEpisodeResult{Episode: &recorded}, nil
}
