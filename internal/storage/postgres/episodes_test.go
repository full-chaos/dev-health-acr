package postgres

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestScanEpisodeBuildsPublicProjectionAndRejectsTombstones(t *testing.T) {
	createdAt := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	payload := []byte(`{"schema_version":"agent_episode_create.v1","client_episode_id":"episode_01","idempotency_key":"idempotency_01","context_packet_id":"packet_01","goal":"goal","repository":{"slug":"owner/repo"},"scope":{},"client":{"name":"test","version":"1","sidecar_version":"1"},"started_at":"2026-07-10T11:00:00Z","ended_at":"2026-07-10T11:01:00Z","outcome":"succeeded","summary":"summary","artifacts":{"files_touched":[],"artifact_uris":[],"tests_run":[]},"transcript":{"mode":"none"},"retention_class":"default_90d"}`)
	episode, err := scanEpisode(episodeRow{values: []any{"episode_01", payload, createdAt, "active"}})
	if err != nil || episode.SchemaVersion != contractsv1.AgentEpisodeSchema || episode.EpisodeID != "episode_01" {
		t.Fatalf("scan active episode = (%#v, %v)", episode, err)
	}
	if _, err := scanEpisode(episodeRow{values: []any{"episode_01", []byte(`{}`), createdAt, "purged_tombstone"}}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("scan tombstone error = %v", err)
	}
}

func TestRedactedCreateRemovesConfidentialPayload(t *testing.T) {
	redacted := redactedCreate(contractsv1.AgentEpisodeCreate{
		Goal: "secret", TaskRef: "CHAOS-2904", Summary: "secret summary",
		Artifacts:  contractsv1.EpisodeArtifacts{FilesTouched: []string{"secret.go"}, ArtifactURIs: []string{"https://example.test/secret"}, TestsRun: []string{"secret test"}},
		Transcript: contractsv1.TranscriptRef{Mode: "opaque_ref", OpaqueRef: "https://example.test/transcript"},
	})
	if redacted.Goal != redactedEpisodeText || redacted.Summary != redactedEpisodeText || redacted.TaskRef != "" || redacted.Transcript.Mode != "none" || len(redacted.Artifacts.FilesTouched) != 0 {
		t.Fatalf("redacted episode = %#v", redacted)
	}
}

func TestEpisodePayloadDigestSurvivesDigestOnlyTombstone(t *testing.T) {
	create := contractsv1.AgentEpisodeCreate{SchemaVersion: contractsv1.AgentEpisodeCreateSchema, Goal: "bounded"}
	canonical, err := json.Marshal(create)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(episodePayload{Digest: episodeDigest(canonical), Tombstone: true})
	if err != nil {
		t.Fatal(err)
	}
	if episodePayloadDigest(payload) != episodeDigest(canonical) || string(payload) == string(canonical) {
		t.Fatalf("tombstone lost digest or retained payload: %s", payload)
	}
	if _, err := storedEpisodeCreate(payload); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("tombstone decode error = %v", err)
	}
}

type episodeRow struct {
	values []any
}

func (r episodeRow) Scan(destinations ...any) error {
	for index, destination := range destinations {
		switch target := destination.(type) {
		case *string:
			*target = r.values[index].(string)
		case *[]byte:
			*target = append([]byte(nil), r.values[index].([]byte)...)
		case *time.Time:
			*target = r.values[index].(time.Time)
		default:
			return errors.New("unsupported scan target")
		}
	}
	return nil
}
