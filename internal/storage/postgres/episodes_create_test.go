package postgres

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestCreateIdempotentUsesStoredDigestForRetryAndConflict(t *testing.T) {
	var payload string
	phase := 0
	db := openEpisodeTestDB(t, func(query string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "INSERT INTO") {
			phase++
			if phase == 1 {
				payload = args[11].Value.(string)
				return episodeRows([][]driver.Value{{"episode_01", []byte(payload), fixedEpisodeTime, "active"}}), nil
			}
			return episodeRows(nil), nil
		}
		if !strings.Contains(query, "idempotency_key") {
			return nil, fmt.Errorf("unexpected query: %s", query)
		}
		return episodeRows([][]driver.Value{{"episode_01", "owner/repo", []byte(payload), fixedEpisodeTime, "active"}}), nil
	})
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}
	store.GenerateID = func() (string, error) { return "episode_01", nil }
	principal := postgresPrincipal()
	create := postgresEpisodeCreate()
	created, duplicate, err := store.CreateIdempotent(context.Background(), principal, create, nil)
	if err != nil || duplicate || created.EpisodeID != "episode_01" {
		t.Fatalf("create = (%#v, %t, %v)", created, duplicate, err)
	}
	if _, duplicate, err = store.CreateIdempotent(context.Background(), principal, create, nil); err != nil || !duplicate {
		t.Fatalf("retry = (%t, %v)", duplicate, err)
	}
	conflict := create
	conflict.ClientEpisodeID = "episode_02"
	if _, _, err := store.CreateIdempotent(context.Background(), principal, conflict, nil); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	conflict = create
	conflict.IdempotencyKey = "idempotency_02"
	if _, _, err := store.CreateIdempotent(context.Background(), principal, conflict, nil); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("client-episode conflict error = %v", err)
	}
}

func TestCreateIdempotentDerivesStorageRepositoryID(t *testing.T) {
	const clientRepoID = "client-repository-id"
	db := openEpisodeTestDB(t, func(query string, args []driver.NamedValue) (driver.Rows, error) {
		if !strings.Contains(query, "INSERT INTO") {
			return nil, fmt.Errorf("unexpected query: %s", query)
		}
		repoID, ok := args[2].Value.(string)
		if !ok || repoID == clientRepoID || !rfc4122UUID(repoID) {
			return nil, fmt.Errorf("untrusted repository ID was bound as UUID: %v", args[2].Value)
		}
		payload := args[11].Value.(string)
		return episodeRows([][]driver.Value{{"episode_01", []byte(payload), fixedEpisodeTime, "active"}}), nil
	})
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}
	store.GenerateID = func() (string, error) { return "episode_01", nil }
	create := postgresEpisodeCreate()
	create.Repository.RepoID = clientRepoID
	if _, _, err := store.CreateIdempotent(context.Background(), postgresPrincipal(), create, nil); err != nil {
		t.Fatalf("create with optional client repository ID = %v", err)
	}
}

func TestCreateIdempotentRejectsSplitKeyCollision(t *testing.T) {
	payload := `{"idempotency_digest":"digest","episode":{"schema_version":"agent_episode_create.v1"}}`
	db := openEpisodeTestDB(t, func(query string, _ []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "INSERT INTO") {
			return episodeRows(nil), nil
		}
		return episodeRows([][]driver.Value{
			{"episode_01", "owner/repo", []byte(payload), fixedEpisodeTime, "active"},
			{"episode_02", "owner/repo", []byte(payload), fixedEpisodeTime, "active"},
		}), nil
	})
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateIdempotent(context.Background(), postgresPrincipal(), postgresEpisodeCreate(), nil); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("split-key collision error = %v", err)
	}
}

func TestCreateNoPersistKeepsOnlyDigestTombstone(t *testing.T) {
	var payload string
	phase := 0
	db := openEpisodeTestDB(t, func(query string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "INSERT INTO") {
			phase++
			payload = args[11].Value.(string)
			if phase == 1 {
				return episodeRows([][]driver.Value{{"episode_01", []byte(payload), fixedEpisodeTime, "purged_tombstone"}}), nil
			}
			return episodeRows(nil), nil
		}
		return episodeRows([][]driver.Value{{"episode_01", "owner/repo", []byte(payload), fixedEpisodeTime, "purged_tombstone"}}), nil
	})
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}
	store.GenerateID = func() (string, error) { return "episode_01", nil }
	create := postgresEpisodeCreate()
	create.RetentionClass = "no_persist"
	if episode, duplicate, err := store.CreateIdempotent(context.Background(), postgresPrincipal(), create, nil); err != nil || duplicate || episode.EpisodeID != "" {
		t.Fatalf("no_persist create = (%#v, %t, %v)", episode, duplicate, err)
	}
	if strings.Contains(payload, create.Goal) {
		t.Fatalf("tombstone retained content: %s", payload)
	}
	if _, duplicate, err := store.CreateIdempotent(context.Background(), postgresPrincipal(), create, nil); err != nil || !duplicate {
		t.Fatalf("no_persist retry = (%t, %v)", duplicate, err)
	}
}

func TestPreflightIdempotencyClassifiesStoredTombstoneWithoutPayloadLeakage(t *testing.T) {
	// Given
	create := postgresEpisodeCreate()
	payload, err := json.Marshal(episodePayload{Digest: episodeDigest(mustJSON(t, create)), Tombstone: true})
	if err != nil {
		t.Fatal(err)
	}
	db := openEpisodeTestDB(t, func(query string, _ []driver.NamedValue) (driver.Rows, error) {
		if !strings.Contains(query, "idempotency_key") {
			return nil, fmt.Errorf("unexpected query: %s", query)
		}
		return episodeRows([][]driver.Value{{"owner/repo", payload}}), nil
	})
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}

	// When
	identical, identicalErr := store.PreflightIdempotency(context.Background(), postgresPrincipal(), create)
	conflicting := create
	conflicting.Summary = "different bounded summary"
	conflict, conflictErr := store.PreflightIdempotency(context.Background(), postgresPrincipal(), conflicting)

	// Then
	if identicalErr != nil || identical != storage.EpisodePreflightIdentical {
		t.Fatalf("identical preflight = (%v, %v)", identical, identicalErr)
	}
	if conflictErr != nil || conflict != storage.EpisodePreflightConflict {
		t.Fatalf("conflict preflight = (%v, %v)", conflict, conflictErr)
	}
}

func TestPreflightIdempotencyReturnsMissForAbsentEpisode(t *testing.T) {
	// Given
	db := openEpisodeTestDB(t, func(query string, _ []driver.NamedValue) (driver.Rows, error) {
		if !strings.Contains(query, "idempotency_key") {
			return nil, fmt.Errorf("unexpected query: %s", query)
		}
		return episodeRows(nil), nil
	})
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}

	// When
	result, err := store.PreflightIdempotency(context.Background(), postgresPrincipal(), postgresEpisodeCreate())

	// Then
	if err != nil || result != storage.EpisodePreflightMiss {
		t.Fatalf("miss preflight = (%v, %v)", result, err)
	}
}

func TestPreflightIdempotencyReturnsCanceledContext(t *testing.T) {
	// Given
	db := openEpisodeTestDB(t, func(string, []driver.NamedValue) (driver.Rows, error) {
		return nil, errors.New("preflight queried after cancellation")
	})
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// When
	_, err = store.PreflightIdempotency(ctx, postgresPrincipal(), postgresEpisodeCreate())

	// Then
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preflight error = %v", err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestCreateIdempotentSameOrgCrossRepoCollision(t *testing.T) {
	// This test verifies that the repo_slug filter in existing() is applied.
	// When an INSERT fails due to conflict, existing() is called to check
	// if there's a matching episode. The repo_slug filter ensures we only
	// match episodes in the same repository.
	payload := `{"idempotency_digest":"digest1","episode":{"schema_version":"agent_episode_create.v1"}}`
	db := openEpisodeTestDB(t, func(query string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "INSERT INTO") {
			// INSERT fails (ON CONFLICT DO NOTHING)
			return episodeRows(nil), nil
		}
		if strings.Contains(query, "idempotency_key") {
			// Return existing episode in repo_b (matching the repo_slug filter)
			// existing() query expects: episode_id, repo_slug, payload, created_at, redaction_state
			return episodeRows([][]driver.Value{{"episode_01", "owner/repo_b", []byte(payload), fixedEpisodeTime, "active"}}), nil
		}
		return nil, fmt.Errorf("unexpected query: %s", query)
	})
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}

	// Principal with access to both repos
	principal := storage.Principal{OrgID: "00000000-0000-0000-0000-000000000001", RepositoryScopes: []string{"owner/repo", "owner/repo_b"}}
	create := postgresEpisodeCreate()
	create.Repository.Slug = "owner/repo_b"
	if _, _, err := store.CreateIdempotent(context.Background(), principal, create, nil); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("cross-repo collision error = %v", err)
	}
}

var fixedEpisodeTime = time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

func postgresPrincipal() storage.Principal {
	return storage.Principal{OrgID: "00000000-0000-0000-0000-000000000001", RepositoryScopes: []string{"owner/repo"}}
}

func postgresEpisodeCreate() contractsv1.AgentEpisodeCreate {
	return contractsv1.AgentEpisodeCreate{SchemaVersion: contractsv1.AgentEpisodeCreateSchema, ClientEpisodeID: "episode_01", IdempotencyKey: "idempotency_01", ContextPacketID: "packet_01", Goal: "bounded", Summary: "bounded", Repository: contractsv1.RepositoryRef{Slug: "owner/repo", RepoID: "00000000-0000-0000-0000-000000000002"}, Client: contractsv1.EpisodeClient{Name: "test", Version: "1", SidecarVersion: "1"}, StartedAt: fixedEpisodeTime, EndedAt: fixedEpisodeTime, Outcome: "succeeded", RetentionClass: "default_90d", Artifacts: contractsv1.EpisodeArtifacts{FilesTouched: []string{}, ArtifactURIs: []string{}, TestsRun: []string{}}, Transcript: contractsv1.TranscriptRef{Mode: "none"}}
}
