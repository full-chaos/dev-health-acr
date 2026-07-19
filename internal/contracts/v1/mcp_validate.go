package v1

import "fmt"

// mcpRenderedMarkdownMaxLength bounds the markdown rendering wrapped around
// every MCP read-tool response. It is intentionally smaller than
// options.max_output_tokens*4 (16000*4=64000) so the rendering stays a
// concise, bounded summary rather than a full re-serialization of the
// structured contract.
const mcpRenderedMarkdownMaxLength = 24000

func (r MCPContextForTaskRequest) Validate() error {
	if !stringLengthBetween(r.Goal, 1, 4000) {
		return fmt.Errorf("goal violates v1 bounds")
	}
	if r.Repository != nil {
		if err := r.Repository.Validate(); err != nil {
			return fmt.Errorf("repository: %w", err)
		}
	}
	if r.Scope != nil {
		if err := r.Scope.Validate(); err != nil {
			return fmt.Errorf("scope: %w", err)
		}
	}
	if r.Budget != nil {
		if err := r.Budget.Validate(); err != nil {
			return fmt.Errorf("budget: %w", err)
		}
	}
	if len(r.RequestedCategories) > 5 || !uniquePacketCategories(r.RequestedCategories) {
		return fmt.Errorf("requested_categories violates v1 bounds")
	}
	return nil
}

func (r MCPRepositoryRef) Validate() error {
	if !repositorySlugPattern.MatchString(r.Slug) || !stringLengthBetween(r.Slug, 1, 512) {
		return fmt.Errorf("slug violates v1 bounds")
	}
	return nil
}

func (s MCPRequestedScope) Validate() error {
	if !stringLengthBetween(s.Branch, 0, 512) || !stringLengthBetween(s.TaskRef, 0, 1024) {
		return fmt.Errorf("scope string violates v1 bounds")
	}
	if s.CommitSHA != "" && !commitSHAPattern.MatchString(s.CommitSHA) {
		return fmt.Errorf("commit_sha violates v1 bounds")
	}
	if len(s.Files) > 200 || !uniqueStrings(s.Files) {
		return fmt.Errorf("files violates v1 bounds")
	}
	for _, file := range s.Files {
		if !stringLengthBetween(file, 1, 2048) {
			return fmt.Errorf("files violates v1 bounds")
		}
	}
	if s.TimeWindowDays != 0 && (s.TimeWindowDays < 1 || s.TimeWindowDays > 365) {
		return fmt.Errorf("time_window_days violates v1 bounds")
	}
	// as_of and include_changed_files carry no further v1 bound beyond their
	// JSON types (RFC3339 timestamp, boolean): as_of mirrors the HTTP
	// contract's unconstrained scope.as_of, and include_changed_files is a
	// plain tri-state flag with no numeric or string bound to enforce.
	return nil
}

func (b MCPBudget) Validate() error {
	if b.MaxItems != 0 && (b.MaxItems < 1 || b.MaxItems > 50) {
		return fmt.Errorf("max_items violates v1 bounds")
	}
	if b.MaxOutputTokens != 0 && (b.MaxOutputTokens < 500 || b.MaxOutputTokens > 16000) {
		return fmt.Errorf("max_output_tokens violates v1 bounds")
	}
	if b.MaxSerializedBytes != 0 && (b.MaxSerializedBytes < 8192 || b.MaxSerializedBytes > 1048576) {
		return fmt.Errorf("max_serialized_bytes violates v1 bounds")
	}
	return nil
}

func (m MCPRenderedMarkdown) Validate() error {
	if !m.Untrusted {
		return fmt.Errorf("rendered_markdown.untrusted must be true")
	}
	if !stringLengthBetween(m.Markdown, 1, mcpRenderedMarkdownMaxLength) {
		return fmt.Errorf("rendered_markdown.markdown violates v1 bounds")
	}
	return nil
}

func (r MCPContextForTaskResponse) Validate() error {
	if r.SchemaVersion != MCPContextForTaskResponseSchema {
		return fmt.Errorf("schema_version must be %q", MCPContextForTaskResponseSchema)
	}
	if err := r.Structured.Validate(); err != nil {
		return fmt.Errorf("structured: %w", err)
	}
	if err := r.RenderedMarkdown.Validate(); err != nil {
		return fmt.Errorf("rendered_markdown: %w", err)
	}
	if r.LocalContext != nil {
		if err := r.LocalContext.Validate(); err != nil {
			return fmt.Errorf("local_context: %w", err)
		}
		if r.FederatedBudget == nil {
			return fmt.Errorf("federated_budget is required when local_context is present")
		}
		if r.FederatedBudget.LocalItemsUsed != len(r.LocalContext.Items) {
			return fmt.Errorf("federated_budget.local_items_used must match local_context.items")
		}
		localBytes, err := r.LocalContext.packetContentSerializedBytes()
		if err != nil {
			return fmt.Errorf("local_context packet content: %w", err)
		}
		if r.FederatedBudget.LocalSerializedBytes != localBytes {
			return fmt.Errorf("federated_budget.local_serialized_bytes must match local_context packet content")
		}
	}
	if r.FederatedBudget != nil {
		if err := r.FederatedBudget.Validate(r.Structured.Budget); err != nil {
			return fmt.Errorf("federated_budget: %w", err)
		}
	}
	return nil
}

func (r MCPSourceEvidenceRequest) Validate() error {
	if !stringLengthBetween(r.EvidenceRefID, 1, 256) {
		return fmt.Errorf("evidence_ref_id violates v1 bounds")
	}
	return nil
}

func (r MCPSourceEvidenceResponse) Validate() error {
	if r.SchemaVersion != MCPSourceEvidenceResponseSchema {
		return fmt.Errorf("schema_version must be %q", MCPSourceEvidenceResponseSchema)
	}
	if err := r.Structured.Validate(); err != nil {
		return fmt.Errorf("structured: %w", err)
	}
	if err := r.RenderedMarkdown.Validate(); err != nil {
		return fmt.Errorf("rendered_markdown: %w", err)
	}
	return nil
}

func (r MCPRecordEpisodeRequest) Validate() error {
	if r.Repository == nil || r.Scope == nil {
		return fmt.Errorf("repository and scope are required")
	}
	// Reuse the canonical hosted-create validator with fixed placeholders for
	// sidecar-owned identity. The MCP boundary accepts only agent_name/model;
	// sidecar.Client.RecordEpisode replaces the placeholders before transport.
	return AgentEpisodeCreate{
		SchemaVersion:   AgentEpisodeCreateSchema,
		ClientEpisodeID: r.ClientEpisodeID,
		IdempotencyKey:  r.IdempotencyKey,
		ContextPacketID: r.ContextPacketID,
		Goal:            r.Goal,
		TaskRef:         r.TaskRef,
		Repository:      RepositoryRef{Slug: r.Repository.Slug},
		Scope:           *r.Scope,
		Client: EpisodeClient{
			Name:           "mcp",
			Version:        "mcp",
			SidecarVersion: "mcp",
			AgentName:      r.AgentName,
			Model:          r.Model,
		},
		StartedAt:      r.StartedAt,
		EndedAt:        r.EndedAt,
		Outcome:        r.Outcome,
		Summary:        r.Summary,
		Artifacts:      r.Artifacts,
		Transcript:     r.Transcript,
		RetentionClass: r.RetentionClass,
	}.Validate()
}

func (r MCPRecordEpisodeResponse) Validate() error {
	if r.SchemaVersion != MCPRecordEpisodeResponseSchema {
		return fmt.Errorf("schema_version must be %q", MCPRecordEpisodeResponseSchema)
	}
	if !stringLengthBetween(r.ClientEpisodeID, 1, 256) || !stringLengthBetween(r.IdempotencyKey, 8, 256) {
		return fmt.Errorf("episode receipt identifiers violate v1 bounds")
	}
	if !stringLengthBetween(r.Scope.Branch, 0, 512) || !stringLengthBetween(r.Scope.CommitSHA, 0, 64) {
		return fmt.Errorf("receipt scope violates v1 bounds")
	}
	switch r.TranscriptDisposition {
	case "not_submitted", "accepted", "redacted":
	default:
		return fmt.Errorf("invalid transcript disposition")
	}
	switch r.Status {
	case "recorded":
		if !stringLengthBetween(r.EpisodeID, 8, 256) || r.CreatedAt == nil || r.CreatedAt.IsZero() || r.Duplicate == nil || !validAgentEpisodeRedactionState(r.RedactionState) {
			return fmt.Errorf("recorded receipt violates v1 bounds")
		}
	case "no_persist":
		if r.EpisodeID != "" || r.CreatedAt != nil || r.RedactionState != "" || r.Duplicate != nil {
			return fmt.Errorf("no_persist receipt must not include a persisted episode")
		}
	default:
		return fmt.Errorf("invalid episode receipt status")
	}
	return nil
}
