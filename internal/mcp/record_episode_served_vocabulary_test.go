package mcp

import (
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

func TestRecordEpisodeReceiptCarriesAnAddedHostedRedactionState(t *testing.T) {
	input := contractsv1.MCPRecordEpisodeRequest{
		ClientEpisodeID: "client-episode-1",
		IdempotencyKey:  "idempotency-key-1",
		Scope:           &contractsv1.EpisodeScope{},
		Transcript:      contractsv1.TranscriptRef{Mode: "none"},
	}
	episode := &contractsv1.AgentEpisode{
		EpisodeID:      "episode-12345",
		CreatedAt:      time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC),
		RedactionState: "value_added_after_the_client_cached_the_schema",
	}
	response, err := recordEpisodeResponse(input, sidecar.RecordEpisodeResult{Episode: episode})
	if err != nil {
		t.Fatalf("a receipt for a hosted episode with an added redaction_state was refused: %v", err)
	}
	if response.TranscriptDisposition != "not_submitted" || response.RedactionState != episode.RedactionState {
		t.Fatalf("receipt = %+v", response)
	}
	if err := response.Validate(); err == nil {
		t.Fatal("the strict Validate must still refuse an unserved redaction_state")
	}
}
