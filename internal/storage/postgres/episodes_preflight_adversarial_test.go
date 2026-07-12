package postgres

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestPreflightIdempotencyReturnsConflictForSameOrgCrossRepositoryCollision(t *testing.T) {
	// Given
	db := openEpisodeTestDB(t, func(query string, _ []driver.NamedValue) (driver.Rows, error) {
		if !strings.Contains(query, "idempotency_key") {
			return nil, fmt.Errorf("unexpected query: %s", query)
		}
		return episodeRows([][]driver.Value{{"owner/other", []byte(`{"idempotency_digest":"same"}`)}}), nil
	})
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}
	principal := postgresPrincipal()
	principal.RepositoryScopes = []string{"owner/repo", "owner/other"}

	// When
	result, err := store.PreflightIdempotency(context.Background(), principal, postgresEpisodeCreate())

	// Then
	if err != nil || result != storage.EpisodePreflightConflict || errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cross-repository preflight = (%v, %v)", result, err)
	}
}
