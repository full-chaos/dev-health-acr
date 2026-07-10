package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestPurgeExpiredScopesTheSQLToAuthorizedRepositories(t *testing.T) {
	db := openEpisodeExecDB(t, func(query string, args []driver.NamedValue) (driver.Result, error) {
		if !strings.Contains(query, "jsonb_array_elements_text") || len(args) != 4 || !strings.Contains(args[3].Value.(string), "owner/repo") {
			return nil, fmt.Errorf("purge query omitted repository scope: %s", query)
		}
		return driver.RowsAffected(1), nil
	})
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}
	purged, err := store.PurgeExpiredForPrincipal(context.Background(), postgresPrincipal(), fixedEpisodeTime, 10)
	if err != nil || purged != 1 {
		t.Fatalf("scoped purge = (%d, %v)", purged, err)
	}
}

func TestPurgeExpiredRejectsEmptyRepositoryScopeBeforeDatabaseUse(t *testing.T) {
	store, err := NewEpisodeStore(&sql.DB{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PurgeExpiredForPrincipal(context.Background(), storage.Principal{OrgID: "00000000-0000-0000-0000-000000000001"}, fixedEpisodeTime, 1); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("empty-scope purge error = %v", err)
	}
}

func TestPurgeExpiredRejectsEmptyOrganizationBeforeDatabaseUse(t *testing.T) {
	store, err := NewEpisodeStore(&sql.DB{})
	if err != nil {
		t.Fatal(err)
	}
	principal := postgresPrincipal()
	principal.OrgID = ""
	if _, err := store.PurgeExpiredForPrincipal(context.Background(), principal, fixedEpisodeTime, 1); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("empty-org purge error = %v", err)
	}
}

func TestPurgeExpiredRejectsUnscopedInternalCall(t *testing.T) {
	store, err := NewEpisodeStore(&sql.DB{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.purgeExpired(context.Background(), postgresPrincipal().OrgID, nil, fixedEpisodeTime, 1); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("unscoped internal purge error = %v", err)
	}
}

func TestRawPurgeMethodsFailClosedBeforeDatabaseUse(t *testing.T) {
	store, err := NewEpisodeStore(&sql.DB{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PurgeExpired(context.Background(), fixedEpisodeTime, 1); err == nil {
		t.Fatal("global purge was accepted")
	}
	if _, err := store.PurgeExpiredForOrg(context.Background(), postgresPrincipal(), fixedEpisodeTime, 1); err == nil {
		t.Fatal("org-only purge was accepted")
	}
}

func TestGetAndRedactRejectUnauthorizedRepositoryBeforeDecoding(t *testing.T) {
	db := openEpisodeTestDB(t, func(_ string, _ []driver.NamedValue) (driver.Rows, error) {
		return episodeRows([][]driver.Value{{"episode_01", "owner/repo", []byte(`not json`), fixedEpisodeTime, "active"}}), nil
	})
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}
	foreign := postgresPrincipal()
	foreign.RepositoryScopes = []string{"owner/other"}
	if _, err := store.GetByClientEpisodeID(context.Background(), foreign, "episode_01"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("foreign get error = %v", err)
	}
	if _, err := store.Redact(context.Background(), foreign, "episode_01", "request"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("foreign redact error = %v", err)
	}
}

func TestGetAndRedactRejectExpiredEpisode(t *testing.T) {
	payload := `{"idempotency_digest":"digest","episode":{"schema_version":"agent_episode_create.v1","client_episode_id":"episode_01","idempotency_key":"idempotency_01","context_packet_id":"packet_01","goal":"goal","repository":{"slug":"owner/repo"},"client":{"name":"test","version":"1","sidecar_version":"1"},"started_at":"2026-07-10T11:00:00Z","ended_at":"2026-07-10T11:01:00Z","outcome":"succeeded","summary":"summary","artifacts":{"files_touched":[],"artifact_uris":[],"tests_run":[]},"transcript":{"mode":"none"},"retention_class":"default_90d"}}`
	db := openEpisodeTestDB(t, func(query string, _ []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "UPDATE acr.agent_episodes") {
			return episodeRows([][]driver.Value{{"episode_01", []byte(payload), fixedEpisodeTime, "redacted"}}), nil
		}
		if strings.Contains(query, "expires_at IS NULL OR expires_at > NOW()") {
			return episodeRows(nil), nil
		}
		return episodeRows([][]driver.Value{{"episode_01", "owner/repo", []byte(payload), fixedEpisodeTime, "active"}}), nil
	})
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}
	principal := postgresPrincipal()
	if _, err := store.GetByClientEpisodeID(context.Background(), principal, "episode_01"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expired get error = %v", err)
	}
	if _, err := store.Redact(context.Background(), principal, "episode_01", "request"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expired redact error = %v", err)
	}
}
