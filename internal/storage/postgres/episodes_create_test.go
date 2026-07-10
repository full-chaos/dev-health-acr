package postgres

import (
	"context"
	"database/sql/driver"
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

var fixedEpisodeTime = time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

func postgresPrincipal() storage.Principal {
	return storage.Principal{OrgID: "00000000-0000-0000-0000-000000000001", RepositoryScopes: []string{"owner/repo"}}
}

func postgresEpisodeCreate() contractsv1.AgentEpisodeCreate {
	return contractsv1.AgentEpisodeCreate{SchemaVersion: contractsv1.AgentEpisodeCreateSchema, ClientEpisodeID: "episode_01", IdempotencyKey: "idempotency_01", ContextPacketID: "packet_01", Goal: "bounded", Summary: "bounded", Repository: contractsv1.RepositoryRef{Slug: "owner/repo", RepoID: "00000000-0000-0000-0000-000000000002"}, Client: contractsv1.EpisodeClient{Name: "test", Version: "1", SidecarVersion: "1"}, StartedAt: fixedEpisodeTime, EndedAt: fixedEpisodeTime, Outcome: "succeeded", RetentionClass: "default_90d", Artifacts: contractsv1.EpisodeArtifacts{FilesTouched: []string{}, ArtifactURIs: []string{}, TestsRun: []string{}}, Transcript: contractsv1.TranscriptRef{Mode: "none"}}
}
