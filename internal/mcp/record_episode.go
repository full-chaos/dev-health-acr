package mcp

import (
	"context"
	"slices"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func recordEpisodeEnabled(boot *Bootstrap) bool {
	return boot != nil && boot.Config.EnableWriteback && boot.Capabilities.Entitlements.AgentContextRuntime && boot.Capabilities.Permissions.EpisodeWrite && slices.Contains(boot.Capabilities.EnabledTools, toolRecordEpisode)
}

func handleRecordEpisode(ctx context.Context, boot *Bootstrap, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	if !recordEpisodeEnabled(boot) {
		return toolErrorResult(&classifiedError{category: "entitlement", message: "record_episode is not enabled for this sidecar and credential"}), nil
	}

	input, err := decodeRecordEpisodeRequest(rawArgs(req))
	if err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "record_episode arguments are not valid JSON for the declared schema"}), nil
	}
	if input.Transcript.Mode != "none" && !boot.Config.EnableTranscriptCapture {
		return toolErrorResult(&classifiedError{category: "validation", message: "record_episode transcript capture is not enabled for this sidecar"}), nil
	}

	result, err := boot.Client.RecordEpisode(ctx, agentEpisodeCreate(input))
	if err != nil {
		return toolErrorResult(err), nil
	}
	response, err := recordEpisodeResponse(input, result)
	if err != nil {
		return toolErrorResult(&classifiedError{category: "internal", message: "the recorded episode response failed contract validation"}), nil
	}
	return buildToolResult(response, "Episode writeback is append-only evidence, not durable memory or promoted truth.")
}

func agentEpisodeCreate(input contractsv1.MCPRecordEpisodeRequest) contractsv1.AgentEpisodeCreate {
	return contractsv1.AgentEpisodeCreate{
		SchemaVersion:   contractsv1.AgentEpisodeCreateSchema,
		ClientEpisodeID: input.ClientEpisodeID,
		IdempotencyKey:  input.IdempotencyKey,
		ContextPacketID: input.ContextPacketID,
		Goal:            input.Goal,
		TaskRef:         input.TaskRef,
		Repository:      contractsv1.RepositoryRef{Slug: input.Repository.Slug},
		Scope:           *input.Scope,
		Client:          contractsv1.EpisodeClient{AgentName: input.AgentName, Model: input.Model},
		StartedAt:       input.StartedAt,
		EndedAt:         input.EndedAt,
		Outcome:         input.Outcome,
		Summary:         input.Summary,
		Artifacts:       input.Artifacts,
		Transcript:      input.Transcript,
		RetentionClass:  input.RetentionClass,
	}
}

func recordEpisodeResponse(input contractsv1.MCPRecordEpisodeRequest, result sidecar.RecordEpisodeResult) (contractsv1.MCPRecordEpisodeResponse, error) {
	response := contractsv1.MCPRecordEpisodeResponse{
		SchemaVersion:   contractsv1.MCPRecordEpisodeResponseSchema,
		ClientEpisodeID: input.ClientEpisodeID,
		IdempotencyKey:  input.IdempotencyKey,
		Scope:           *input.Scope,
	}
	if result.NoPersist {
		response.Status = "no_persist"
		response.TranscriptDisposition = transcriptDisposition(input, nil)
		return response, response.Validate()
	}
	if result.Episode == nil {
		return contractsv1.MCPRecordEpisodeResponse{}, sidecar.ErrInvalidResponse
	}
	response.Status = "recorded"
	response.EpisodeID = result.Episode.EpisodeID
	response.CreatedAt = &result.Episode.CreatedAt
	response.RedactionState = result.Episode.RedactionState
	response.Scope = result.Episode.Scope
	response.TranscriptDisposition = transcriptDisposition(input, result.Episode)
	duplicate := result.Episode.Duplicate
	response.Duplicate = &duplicate
	return response, response.Validate()
}

func transcriptDisposition(input contractsv1.MCPRecordEpisodeRequest, episode *contractsv1.AgentEpisode) string {
	if input.Transcript.Mode == "none" {
		return "not_submitted"
	}
	if episode != nil && (episode.RedactionState != "active" || episode.Transcript.Mode == "redacted_summary") {
		return "redacted"
	}
	return "accepted"
}
