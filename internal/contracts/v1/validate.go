package v1

import (
	"errors"
	"fmt"
	"strings"
)

func (r ContextPacketRequest) Validate() error {
	if r.SchemaVersion != ContextPacketRequestSchema {
		return fmt.Errorf("schema_version must be %q", ContextPacketRequestSchema)
	}
	if strings.TrimSpace(r.RequestID) == "" {
		return errors.New("request_id is required")
	}
	if strings.TrimSpace(r.Goal) == "" {
		return errors.New("goal is required")
	}
	if strings.TrimSpace(r.Repository.Slug) == "" {
		return errors.New("repository.slug is required")
	}
	if r.Options.MaxItems < 1 || r.Options.MaxItems > 50 {
		return errors.New("options.max_items must be between 1 and 50")
	}
	if r.Options.MaxOutputTokens < 500 || r.Options.MaxOutputTokens > 16000 {
		return errors.New("options.max_output_tokens must be between 500 and 16000")
	}
	if r.Options.MaxSerializedBytes < 8192 || r.Options.MaxSerializedBytes > 1048576 {
		return errors.New("options.max_serialized_bytes must be between 8192 and 1048576")
	}
	if strings.TrimSpace(r.Client.Name) == "" || strings.TrimSpace(r.Client.Version) == "" {
		return errors.New("client.name and client.version are required")
	}
	return nil
}

func (i ContextPacketItem) Validate() error {
	if i.SchemaVersion != ContextPacketItemSchema {
		return fmt.Errorf("schema_version must be %q", ContextPacketItemSchema)
	}
	if strings.TrimSpace(i.PacketItemID) == "" {
		return errors.New("packet_item_id is required")
	}
	if strings.TrimSpace(i.Title) == "" || strings.TrimSpace(i.Summary) == "" {
		return errors.New("title and summary are required")
	}
	if strings.TrimSpace(i.WhyIncluded) == "" || strings.TrimSpace(i.RuleID) == "" {
		return errors.New("why_included and rule_id are required")
	}
	if i.Confidence < 0 || i.Confidence > 1 {
		return errors.New("confidence must be between 0 and 1")
	}
	if i.Rank < 0 {
		return errors.New("rank must be non-negative")
	}
	switch i.Category {
	case CategoryState, CategoryPressure, CategoryCause, CategoryEvidence, CategoryAction:
	default:
		return errors.New("invalid category")
	}
	switch i.ClaimKind {
	case ClaimObserved, ClaimInferred, ClaimRecommendation:
	default:
		return errors.New("invalid claim_kind")
	}
	switch i.Severity {
	case SeverityInfo, SeverityWarning, SeverityHigh, SeverityCritical:
	default:
		return errors.New("invalid severity")
	}
	if i.ClaimKind == ClaimObserved && len(i.EvidenceRefIDs) == 0 {
		return errors.New("observed claims require at least one evidence_ref_id")
	}
	return nil
}

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
