package v1

import (
	"fmt"
	"testing"
	"time"
)

func TestEpisodeServedVocabularyRefusesAnOffSetValueBuiltAtRunTime(t *testing.T) {
	receipt := func(status, redaction, disposition string) MCPRecordEpisodeResponse {
		created := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
		duplicate := false
		response := MCPRecordEpisodeResponse{
			SchemaVersion: MCPRecordEpisodeResponseSchema, ClientEpisodeID: "client-1", IdempotencyKey: "idempotency-1",
			Status: status, TranscriptDisposition: disposition,
		}
		if status == MCPRecordEpisodeStatusRecorded {
			response.EpisodeID, response.CreatedAt, response.Duplicate, response.RedactionState = "episode-12345", &created, &duplicate, redaction
		}
		return response
	}
	off := func(set []string) string { return fmt.Sprintf("%s_%d", set[0], len(set)) }
	for _, status := range MCPRecordEpisodeStatusVocabulary() {
		for _, disposition := range MCPTranscriptDispositionVocabulary() {
			if err := receipt(status, AgentEpisodeRedactionActive, disposition).Validate(); err != nil {
				t.Errorf("%s/%s refused: %v", status, disposition, err)
			}
		}
	}
	if receipt(off(MCPRecordEpisodeStatusVocabulary()), "", MCPTranscriptDispositionAccepted).Validate() == nil {
		t.Error("an off-set status was served")
	}
	if receipt(MCPRecordEpisodeStatusRecorded, AgentEpisodeRedactionActive, off(MCPTranscriptDispositionVocabulary())).ValidateRead() == nil {
		t.Error("an off-set transcript_disposition was served")
	}
	if receipt(MCPRecordEpisodeStatusRecorded, off(AgentEpisodeRedactionStateVocabulary()), MCPTranscriptDispositionAccepted).Validate() == nil {
		t.Error("the strict producer check admits an off-set redaction_state")
	}
	for _, state := range AgentEpisodeRedactionStateVocabulary() {
		if !validAgentEpisodeRedactionState(state) {
			t.Errorf("%s refused", state)
		}
	}
}
