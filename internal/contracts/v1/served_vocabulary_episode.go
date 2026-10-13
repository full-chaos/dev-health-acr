package v1

// Served value sets of the record_episode receipt and the stored episode.
// Producers and validators use only these constants.
const (
	MCPRecordEpisodeStatusRecorded  = "recorded"
	MCPRecordEpisodeStatusNoPersist = "no_persist"

	AgentEpisodeRedactionActive          = "active"
	AgentEpisodeRedactionRedacted        = "redacted"
	AgentEpisodeRedactionPurgedTombstone = "purged_tombstone"

	MCPTranscriptDispositionNotSubmitted = "not_submitted"
	MCPTranscriptDispositionAccepted     = "accepted"
	MCPTranscriptDispositionRedacted     = "redacted"
)

func MCPRecordEpisodeStatusVocabulary() []string {
	return []string{MCPRecordEpisodeStatusRecorded, MCPRecordEpisodeStatusNoPersist}
}

func AgentEpisodeRedactionStateVocabulary() []string {
	return []string{AgentEpisodeRedactionActive, AgentEpisodeRedactionRedacted, AgentEpisodeRedactionPurgedTombstone}
}

func MCPTranscriptDispositionVocabulary() []string {
	return []string{MCPTranscriptDispositionNotSubmitted, MCPTranscriptDispositionAccepted, MCPTranscriptDispositionRedacted}
}
