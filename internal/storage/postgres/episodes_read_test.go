package postgres

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const readTestEpisodePayload = `{"idempotency_digest":"digest","episode":{"schema_version":"agent_episode_create.v1","client_episode_id":"episode_01","idempotency_key":"idempotency_01","context_packet_id":"packet_01","goal":"goal","repository":{"slug":"owner/repo"},"client":{"name":"test","version":"1","sidecar_version":"1"},"started_at":"2026-07-10T11:00:00Z","ended_at":"2026-07-10T11:01:00Z","outcome":"succeeded","summary":"summary","artifacts":{"files_touched":[],"artifact_uris":[],"tests_run":[]},"transcript":{"mode":"none"},"retention_class":"default_90d"}}`

func TestGetByEpisodeID_scopesToOrgAndFiltersRetentionInSQL(t *testing.T) {
	var capturedQuery string
	var capturedArgs []driver.NamedValue
	db := openEpisodeTestDB(t, func(query string, args []driver.NamedValue) (driver.Rows, error) {
		capturedQuery, capturedArgs = query, args
		return episodeRows([][]driver.Value{{"episode_01", "owner/repo", []byte(readTestEpisodePayload), fixedEpisodeTime, "active"}}), nil
	})
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}
	episode, err := store.GetByEpisodeID(context.Background(), postgresPrincipal(), "episode_01")
	if err != nil || episode.EpisodeID != "episode_01" || episode.RedactionState != "active" {
		t.Fatalf("get by episode id = (%#v, %v)", episode, err)
	}
	if !strings.Contains(capturedQuery, "org_id = $1::uuid AND episode_id = $2") ||
		!strings.Contains(capturedQuery, "redaction_state <> 'purged_tombstone'") ||
		!strings.Contains(capturedQuery, "expires_at IS NULL OR expires_at > NOW()") {
		t.Fatalf("query did not scope by org/episode or filter deleted/expired rows: %s", capturedQuery)
	}
	if len(capturedArgs) != 2 || capturedArgs[0].Value != postgresPrincipal().OrgID || capturedArgs[1].Value != "episode_01" {
		t.Fatalf("bound args = %#v", capturedArgs)
	}
}

func TestGetByEpisodeID_rejectsUnauthorizedRepositoryAsNotFound(t *testing.T) {
	db := openEpisodeTestDB(t, func(_ string, _ []driver.NamedValue) (driver.Rows, error) {
		return episodeRows([][]driver.Value{{"episode_01", "owner/repo", []byte(readTestEpisodePayload), fixedEpisodeTime, "active"}}), nil
	})
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}
	foreign := postgresPrincipal()
	foreign.RepositoryScopes = []string{"owner/other"}
	if _, err := store.GetByEpisodeID(context.Background(), foreign, "episode_01"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("out-of-scope repository error = %v, want ErrNotFound", err)
	}
}

func TestGetByEpisodeID_missingRowIsNotFound(t *testing.T) {
	db := openEpisodeTestDB(t, func(_ string, _ []driver.NamedValue) (driver.Rows, error) {
		return episodeRows(nil), nil
	})
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetByEpisodeID(context.Background(), postgresPrincipal(), "does-not-exist"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing row error = %v, want ErrNotFound", err)
	}
}

func TestList_scopesToOrgFiltersRepositoryAndBoundsLimitInSQL(t *testing.T) {
	var capturedQuery string
	var capturedArgs []driver.NamedValue
	db := openEpisodeTestDB(t, func(query string, args []driver.NamedValue) (driver.Rows, error) {
		capturedQuery, capturedArgs = query, args
		return episodeRows([][]driver.Value{
			{"episode_02", "owner/repo", []byte(readTestEpisodePayload), fixedEpisodeTime, "active"},
			{"episode_01", "owner/other", []byte(readTestEpisodePayload), fixedEpisodeTime, "active"},
		}), nil
	})
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}
	episodes, err := store.List(context.Background(), postgresPrincipal(), "owner/repo", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// episode_01 came back as owner/other, which postgresPrincipal() (scoped
	// to owner/repo only) is not authorized for -- scanAuthorizedEpisode must
	// have dropped it, not merely trusted the repo_slug SQL filter.
	if len(episodes) != 1 || episodes[0].EpisodeID != "episode_02" {
		t.Fatalf("authorized episodes = %#v, want exactly episode_02", episodes)
	}
	if !strings.Contains(capturedQuery, "org_id = $1::uuid") ||
		!strings.Contains(capturedQuery, "redaction_state <> 'purged_tombstone'") ||
		!strings.Contains(capturedQuery, "expires_at IS NULL OR expires_at > NOW()") ||
		!strings.Contains(capturedQuery, "$2 = '' OR repo_slug = $2") ||
		!strings.Contains(capturedQuery, "jsonb_array_elements_text($4::jsonb)") ||
		!strings.Contains(capturedQuery, "ORDER BY created_at DESC") ||
		!strings.Contains(capturedQuery, "LIMIT $3") {
		t.Fatalf("list query missing a required scope/order/bound clause: %s", capturedQuery)
	}
	// The repository-scope EXISTS clause must appear before LIMIT in the
	// query text -- Postgres applies WHERE (including a subquery EXISTS)
	// before LIMIT regardless of clause order, but this pins the intent
	// directly: LIMIT is not what stands between an out-of-scope row and the
	// page (review finding M3).
	if idx := strings.Index(capturedQuery, "jsonb_array_elements_text"); idx == -1 || idx > strings.Index(capturedQuery, "LIMIT $3") {
		t.Fatalf("repository-scope filter must precede LIMIT in the query text: %s", capturedQuery)
	}
	if len(capturedArgs) != 4 || capturedArgs[1].Value != "owner/repo" || capturedArgs[2].Value != int64(defaultEpisodeListLimit) || capturedArgs[3].Value != `["owner/repo"]` {
		t.Fatalf("bound args = %#v, want default limit applied for a non-positive caller limit and the principal's repository scopes bound as JSON", capturedArgs)
	}
}

func TestList_clampsOverLargeLimitToMax(t *testing.T) {
	var capturedArgs []driver.NamedValue
	db := openEpisodeTestDB(t, func(_ string, args []driver.NamedValue) (driver.Rows, error) {
		capturedArgs = args
		return episodeRows(nil), nil
	})
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(context.Background(), postgresPrincipal(), "", 10_000); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(capturedArgs) != 4 || capturedArgs[2].Value != int64(maxEpisodeListLimit) {
		t.Fatalf("bound limit = %#v, want clamped to %d", capturedArgs, maxEpisodeListLimit)
	}
}
