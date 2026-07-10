package v1

import (
	"errors"
	"fmt"
	"strings"
)

func (e AgentEpisodeCreate) Validate() error {
	if e.SchemaVersion != AgentEpisodeCreateSchema {
		return fmt.Errorf("schema_version must be %q", AgentEpisodeCreateSchema)
	}
	if strings.TrimSpace(e.ClientEpisodeID) == "" || strings.TrimSpace(e.IdempotencyKey) == "" {
		return errors.New("client_episode_id and idempotency_key are required")
	}
	if strings.TrimSpace(e.ContextPacketID) == "" {
		return errors.New("context_packet_id is required")
	}
	if strings.TrimSpace(e.Goal) == "" {
		return errors.New("goal is required")
	}
	if strings.TrimSpace(e.Repository.Slug) == "" {
		return errors.New("repository.slug is required")
	}
	if strings.TrimSpace(e.Client.Name) == "" || strings.TrimSpace(e.Client.Version) == "" || strings.TrimSpace(e.Client.SidecarVersion) == "" {
		return errors.New("client.name, client.version, and client.sidecar_version are required")
	}
	if e.EndedAt.Before(e.StartedAt) {
		return errors.New("ended_at must not be before started_at")
	}
	switch e.Outcome {
	case "succeeded", "failed", "abandoned", "superseded", "unknown":
	default:
		return errors.New("invalid outcome")
	}
	switch e.Transcript.Mode {
	case "none", "opaque_ref", "redacted_summary":
	default:
		return errors.New("invalid transcript mode")
	}
	switch e.RetentionClass {
	case "default_90d", "short_30d", "legal_hold", "no_persist":
	default:
		return errors.New("invalid retention_class")
	}
	return nil
}
