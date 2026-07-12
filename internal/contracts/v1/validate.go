package v1

import (
	"errors"
	"fmt"
)

func (e AgentEpisodeCreate) Validate() error {
	if e.SchemaVersion != AgentEpisodeCreateSchema {
		return fmt.Errorf("schema_version must be %q", AgentEpisodeCreateSchema)
	}
	return validateAgentEpisodeCreateFields(e)
}

// Validate enforces agent_episode.v1.schema.json exactly. AgentEpisode
// embeds AgentEpisodeCreate, so the wire object has exactly one physical
// schema_version field -- AgentEpisodeCreate.SchemaVersion, promoted by the
// embed -- and it must equal agent_episode.v1, never the embedded create
// contract's own agent_episode_create.v1 constant. The three
// server-assigned fields (episode_id, created_at, redaction_state) are
// checked directly, and every other embedded AgentEpisodeCreate invariant
// is enforced by validateAgentEpisodeCreateFields, which deliberately never
// inspects or reassigns SchemaVersion: the single physical field is
// checked here, once, against the response constant.
//
// This closes the gap the former per-caller sidecar workaround masked: that
// code copied AgentEpisode.AgentEpisodeCreate into a local variable, forced
// its SchemaVersion to agent_episode_create.v1, and only then called
// AgentEpisodeCreate.Validate. Forcing the field before validating meant a
// response that leaked the wrong schema_version could never be told apart
// from a genuinely valid embed, and the copy (while not mutating the
// caller) was pure overhead for a check that never actually ran. Validate
// reads the receiver's embedded value directly and never copies or
// rewrites it, so calling it is side-effect-free for the caller.
func (a AgentEpisode) Validate() error {
	if a.SchemaVersion != AgentEpisodeSchema {
		return fmt.Errorf("schema_version must be %q", AgentEpisodeSchema)
	}
	if !stringLengthBetween(a.EpisodeID, 8, 256) {
		return errors.New("episode_id violates v1 bounds")
	}
	if a.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	if !validAgentEpisodeRedactionState(a.RedactionState) {
		return errors.New("invalid redaction_state")
	}
	if err := validateAgentEpisodeCreateFields(a.AgentEpisodeCreate); err != nil {
		return fmt.Errorf("agent episode: %w", err)
	}
	return nil
}

// validateAgentEpisodeCreateFields enforces every agent_episode_create.v1
// invariant except schema_version, which the two callers own independently:
// AgentEpisodeCreate.Validate checks it against agent_episode_create.v1,
// and AgentEpisode.Validate checks the same promoted physical field against
// agent_episode.v1 instead. Sharing this helper -- rather than each caller
// re-deriving its own copy of these bounds, enums, and required-field
// checks -- is the single canonical source of truth both the create-request
// path and the response path validate against.
func validateAgentEpisodeCreateFields(e AgentEpisodeCreate) error {
	if !stringLengthBetween(e.ClientEpisodeID, 1, 256) {
		return errors.New("client_episode_id violates v1 bounds")
	}
	if !stringLengthBetween(e.IdempotencyKey, 8, 256) {
		return errors.New("idempotency_key violates v1 bounds")
	}
	if !stringLengthBetween(e.ContextPacketID, 8, 256) {
		return errors.New("context_packet_id violates v1 bounds")
	}
	if !stringLengthBetween(e.Goal, 1, 4000) {
		return errors.New("goal violates v1 bounds")
	}
	if !stringLengthBetween(e.TaskRef, 0, 1024) {
		return errors.New("task_ref violates v1 bounds")
	}
	if err := e.Repository.Validate(); err != nil {
		return fmt.Errorf("repository: %w", err)
	}
	if !stringLengthBetween(e.Scope.Branch, 0, 512) || !stringLengthBetween(e.Scope.CommitSHA, 0, 64) {
		return errors.New("scope violates v1 bounds")
	}
	if !stringLengthBetween(e.Client.Name, 1, 200) || !stringLengthBetween(e.Client.Version, 1, 200) || !stringLengthBetween(e.Client.SidecarVersion, 1, 200) {
		return errors.New("client.name, client.version, and client.sidecar_version are required")
	}
	if !stringLengthBetween(e.Client.AgentName, 0, 500) || !stringLengthBetween(e.Client.Model, 0, 500) {
		return errors.New("client metadata violates v1 bounds")
	}
	if e.StartedAt.IsZero() {
		return errors.New("started_at is required")
	}
	if e.EndedAt.IsZero() {
		return errors.New("ended_at is required")
	}
	if e.EndedAt.Before(e.StartedAt) {
		return errors.New("ended_at must not be before started_at")
	}
	if !validAgentEpisodeOutcome(e.Outcome) {
		return errors.New("invalid outcome")
	}
	if !stringLengthBetween(e.Summary, 1, 8000) {
		return errors.New("summary violates v1 bounds")
	}
	if err := validateEpisodeArtifacts(e.Artifacts); err != nil {
		return err
	}
	if err := validateEpisodeTranscript(e.Transcript); err != nil {
		return err
	}
	if !validAgentEpisodeRetentionClass(e.RetentionClass) {
		return errors.New("invalid retention_class")
	}
	return nil
}

func validAgentEpisodeOutcome(value string) bool {
	switch value {
	case "succeeded", "failed", "abandoned", "superseded", "unknown":
		return true
	default:
		return false
	}
}

func validAgentEpisodeRetentionClass(value string) bool {
	switch value {
	case "default_90d", "short_30d", "legal_hold", "no_persist":
		return true
	default:
		return false
	}
}

func validAgentEpisodeRedactionState(value string) bool {
	switch value {
	case "active", "redacted", "purged_tombstone":
		return true
	default:
		return false
	}
}

// validateEpisodeArtifacts enforces agent_episode_create.v1.schema.json's
// "artifacts" object: all three lists are required-present (a nil slice
// marshals to JSON null, which the schema's "type": "array" rejects) even
// when empty, each is bounded by its own maxItems, and each item is bounded
// by its own maxLength (artifact_uris items additionally require URI
// syntax, matching the schema's "format": "uri").
func validateEpisodeArtifacts(artifacts EpisodeArtifacts) error {
	if artifacts.FilesTouched == nil || len(artifacts.FilesTouched) > 500 {
		return errors.New("artifacts.files_touched violates v1 bounds")
	}
	for _, file := range artifacts.FilesTouched {
		if !stringLengthBetween(file, 0, 2048) {
			return errors.New("artifacts.files_touched violates v1 bounds")
		}
	}
	if artifacts.ArtifactURIs == nil || len(artifacts.ArtifactURIs) > 100 {
		return errors.New("artifacts.artifact_uris violates v1 bounds")
	}
	for _, uri := range artifacts.ArtifactURIs {
		if uri == "" || !optionalURI(uri, 2048) {
			return errors.New("artifacts.artifact_uris violates v1 bounds")
		}
	}
	if artifacts.TestsRun == nil || len(artifacts.TestsRun) > 200 {
		return errors.New("artifacts.tests_run violates v1 bounds")
	}
	for _, test := range artifacts.TestsRun {
		if !stringLengthBetween(test, 0, 2000) {
			return errors.New("artifacts.tests_run violates v1 bounds")
		}
	}
	return nil
}

func validateEpisodeTranscript(transcript TranscriptRef) error {
	switch transcript.Mode {
	case "none", "opaque_ref", "redacted_summary":
	default:
		return errors.New("invalid transcript mode")
	}
	if !stringLengthBetween(transcript.OpaqueRef, 0, 2048) || !stringLengthBetween(transcript.RedactedSummary, 0, 4000) {
		return errors.New("transcript violates v1 bounds")
	}
	return nil
}
