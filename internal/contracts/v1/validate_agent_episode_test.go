package v1

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestAgentEpisodeCreateValidate_matches_v1_boundaries locks the invariants
// AgentEpisodeCreate.Validate already enforced before this change: the
// baseline the refactor in validate.go must not regress. Every mutation
// below was already rejected by the pre-refactor validator, so this test
// stays green across the refactor and proves the shared helper extraction
// preserved existing behavior exactly.
func TestAgentEpisodeCreateValidate_matches_v1_boundaries(t *testing.T) {
	base := loadFixture[AgentEpisodeCreate](t, "agent_episode_create.v1.json")
	if err := base.Validate(); err != nil {
		t.Fatalf("golden fixture: %v", err)
	}
	assertSchemaParity(t, "agent_episode_create.v1.schema.json", base)

	cases := []struct {
		name   string
		mutate func(*AgentEpisodeCreate)
	}{
		{name: "schema_version", mutate: func(v *AgentEpisodeCreate) { v.SchemaVersion = "wrong" }},
		{name: "client_episode_id_empty", mutate: func(v *AgentEpisodeCreate) { v.ClientEpisodeID = "" }},
		{name: "idempotency_key_empty", mutate: func(v *AgentEpisodeCreate) { v.IdempotencyKey = "" }},
		{name: "context_packet_id_empty", mutate: func(v *AgentEpisodeCreate) { v.ContextPacketID = "" }},
		{name: "goal_empty", mutate: func(v *AgentEpisodeCreate) { v.Goal = "" }},
		{name: "repository_slug_empty", mutate: func(v *AgentEpisodeCreate) { v.Repository.Slug = "" }},
		{name: "client_name_empty", mutate: func(v *AgentEpisodeCreate) { v.Client.Name = "" }},
		{name: "client_version_empty", mutate: func(v *AgentEpisodeCreate) { v.Client.Version = "" }},
		{name: "client_sidecar_version_empty", mutate: func(v *AgentEpisodeCreate) { v.Client.SidecarVersion = "" }},
		{name: "ended_before_started", mutate: func(v *AgentEpisodeCreate) { v.EndedAt = v.StartedAt.Add(-time.Minute) }},
		{name: "outcome_invalid", mutate: func(v *AgentEpisodeCreate) { v.Outcome = "invalid" }},
		{name: "transcript_mode_invalid", mutate: func(v *AgentEpisodeCreate) { v.Transcript.Mode = "invalid" }},
		{name: "retention_class_invalid", mutate: func(v *AgentEpisodeCreate) { v.RetentionClass = "invalid" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := loadFixture[AgentEpisodeCreate](t, "agent_episode_create.v1.json")
			tc.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("validator accepted schema-invalid agent_episode_create")
			}
		})
	}
}

// TestAgentEpisodeValidate_validGoldenResponsePasses locks in the positive
// case: the canonical golden response fixture must validate cleanly and
// stay schema-parity-tested against agent_episode.v1.schema.json.
func TestAgentEpisodeValidate_validGoldenResponsePasses(t *testing.T) {
	response := loadFixture[AgentEpisode](t, "agent_episode.v1.json")
	if err := response.Validate(); err != nil {
		t.Fatalf("golden fixture: %v", err)
	}
	assertSchemaParity(t, "agent_episode.v1.schema.json", response)
}

// TestAgentEpisodeValidate_duplicateTrueIsSchemaValid locks the Oracle
// follow-up finding directly: internal/api/episode_routes.go sets
// AgentEpisode.Duplicate = true for an idempotent-retry response, so the
// optional duplicate boolean must be both Go-valid and schema-parity-valid
// against agent_episode.v1.schema.json (which, before this change,
// declared additionalProperties: false with no duplicate property and so
// rejected every duplicate response). A duplicate response otherwise
// matches the golden fixture -- this proves the property is additive and
// does not disturb any other invariant.
func TestAgentEpisodeValidate_duplicateTrueIsSchemaValid(t *testing.T) {
	response := loadFixture[AgentEpisode](t, "agent_episode.v1.json")
	response.Duplicate = true
	if err := response.Validate(); err != nil {
		t.Fatalf("duplicate response: %v", err)
	}
	assertSchemaParity(t, "agent_episode.v1.schema.json", response)
}

// TestAgentEpisodeValidate_duplicateFalseOmitsProperty proves the default,
// non-duplicate case still round-trips through the wire encoding without
// ever emitting a "duplicate" key (json:",omitempty"), so the property's
// addition to the schema is purely additive for every existing caller that
// never sets it.
func TestAgentEpisodeValidate_duplicateFalseOmitsProperty(t *testing.T) {
	response := loadFixture[AgentEpisode](t, "agent_episode.v1.json")
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "duplicate") {
		t.Fatalf("non-duplicate response encoded a duplicate property: %s", encoded)
	}
}

// TestAgentEpisodeValidateRejectsCreateSchemaVersionLeakage locks the
// Oracle gate finding directly: AgentEpisode embeds AgentEpisodeCreate, so
// the wire object has exactly one physical schema_version field (promoted
// by the embed). A response whose schema_version leaked the embedded
// create contract's own constant instead of the response contract's must
// be rejected -- the former sidecar workaround (copy the embedded create,
// force its SchemaVersion to the expected constant, then call
// AgentEpisodeCreate.Validate) could never detect this, because it
// silently overwrote the exact field this test flips before validating.
func TestAgentEpisodeValidateRejectsCreateSchemaVersionLeakage(t *testing.T) {
	response := loadFixture[AgentEpisode](t, "agent_episode.v1.json")
	response.SchemaVersion = AgentEpisodeCreateSchema
	if err := response.Validate(); err == nil {
		t.Fatal("validator accepted a response leaking the embedded create schema_version")
	}
}

// TestAgentEpisodeValidate_matches_v1_boundaries exercises every embedded
// AgentEpisodeCreate invariant (scope, timestamps, summary, artifacts,
// enums, transcript) plus the three response-only server fields
// (episode_id, created_at, redaction_state) that the former sidecar/API
// validation gap left unchecked.
func TestAgentEpisodeValidate_matches_v1_boundaries(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*AgentEpisode)
	}{
		{name: "schema_version_wrong", mutate: func(v *AgentEpisode) { v.SchemaVersion = "wrong" }},
		{name: "episode_id_missing", mutate: func(v *AgentEpisode) { v.EpisodeID = "" }},
		{name: "episode_id_too_short", mutate: func(v *AgentEpisode) { v.EpisodeID = "short" }},
		{name: "created_at_zero", mutate: func(v *AgentEpisode) { v.CreatedAt = time.Time{} }},
		{name: "redaction_state_invalid", mutate: func(v *AgentEpisode) { v.RedactionState = "invalid" }},
		{name: "goal_missing", mutate: func(v *AgentEpisode) { v.Goal = "" }},
		{name: "repository_slug_invalid", mutate: func(v *AgentEpisode) { v.Repository.Slug = "invalid-slug" }},
		{name: "scope_branch_too_long", mutate: func(v *AgentEpisode) { v.Scope.Branch = strings.Repeat("b", 513) }},
		{name: "client_name_missing", mutate: func(v *AgentEpisode) { v.Client.Name = "" }},
		{name: "started_at_zero", mutate: func(v *AgentEpisode) { v.StartedAt = time.Time{} }},
		{name: "ended_at_zero", mutate: func(v *AgentEpisode) { v.EndedAt = time.Time{} }},
		{name: "ended_before_started", mutate: func(v *AgentEpisode) { v.EndedAt = v.StartedAt.Add(-time.Minute) }},
		{name: "outcome_invalid", mutate: func(v *AgentEpisode) { v.Outcome = "invalid" }},
		{name: "summary_missing", mutate: func(v *AgentEpisode) { v.Summary = "" }},
		{name: "artifacts_files_touched_nil", mutate: func(v *AgentEpisode) { v.Artifacts.FilesTouched = nil }},
		{name: "artifacts_artifact_uris_nil", mutate: func(v *AgentEpisode) { v.Artifacts.ArtifactURIs = nil }},
		{name: "artifacts_tests_run_nil", mutate: func(v *AgentEpisode) { v.Artifacts.TestsRun = nil }},
		{name: "transcript_mode_invalid", mutate: func(v *AgentEpisode) { v.Transcript.Mode = "invalid" }},
		{name: "retention_class_invalid", mutate: func(v *AgentEpisode) { v.RetentionClass = "invalid" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := loadFixture[AgentEpisode](t, "agent_episode.v1.json")
			tc.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("validator accepted schema-invalid agent_episode response")
			}
		})
	}
}

// TestAgentEpisodeValidateRejectsOtherwiseEmptyValue locks the Oracle gate
// finding directly: a structurally-empty AgentEpisode value (only
// schema_version set) must never pass canonical validation, even though
// the old sidecar response validator only inspected schema_version, the
// two server-assigned string/time fields, and redaction_state -- never
// the embedded create invariants.
func TestAgentEpisodeValidateRejectsOtherwiseEmptyValue(t *testing.T) {
	empty := AgentEpisode{}
	empty.SchemaVersion = AgentEpisodeSchema
	if err := empty.Validate(); err == nil {
		t.Fatal("validator accepted an otherwise-empty agent_episode value")
	}
}

// TestAgentEpisodeValidateDoesNotMutateCaller proves Validate reads the
// embedded AgentEpisodeCreate directly rather than copying and rewriting
// it (the pattern the former sidecar workaround used to paper over the
// schema-version difference): calling Validate on a value must leave every
// field, including the embedded create's own SchemaVersion, unchanged.
func TestAgentEpisodeValidateDoesNotMutateCaller(t *testing.T) {
	response := loadFixture[AgentEpisode](t, "agent_episode.v1.json")
	before := response
	if err := response.Validate(); err != nil {
		t.Fatalf("golden fixture: %v", err)
	}
	if !reflect.DeepEqual(response, before) {
		t.Fatalf("Validate mutated the receiver: before=%#v after=%#v", before, response)
	}
}
