package memory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestPurgeExpired_purgesOldestExpiryFirstLikePostgres is the regression
// test for the Codex cross-system review finding X6: postgres.EpisodeStore's
// purgeExpired explicitly orders its candidates
// (`ORDER BY expires_at, episode_id`) before applying LIMIT, so a bounded
// purge call always tombstones the OLDEST-expiring rows first, deterministically.
// memory.EpisodeStore's purgeExpired instead ranged directly over the
// backing map (`for id, record := range s.byID`) with no ordering at all --
// Go deliberately randomizes map iteration order, so which subset of
// eligible rows got purged when more candidates existed than the limit was
// non-deterministic and had no relationship to expiry age.
//
// This seeds enough candidates (20) that a random subset landing on exactly
// the 5 oldest by chance is astronomically unlikely (about 1 in C(20,5) =
// 15504), so this test reliably fails against the pre-fix random-order
// implementation without needing to run many iterations to catch flakiness.
func TestPurgeExpired_purgesOldestExpiryFirstLikePostgres(t *testing.T) {
	store := NewEpisodeStore()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
	base := time.Now().Add(-time.Hour)

	const seeded = 20
	const limit = 5
	expected := make([]string, 0, limit)
	for i := range seeded {
		create := testEpisodeCreate()
		create.ClientEpisodeID = fmt.Sprintf("episode_order_%02d", i)
		create.IdempotencyKey = fmt.Sprintf("idempotency_order_%02d", i)
		// Each episode's expiry is base+i seconds -- strictly increasing, so
		// there is exactly one correct "oldest 5" answer.
		expiresAt := base.Add(time.Duration(i) * time.Second)
		created, _, err := store.CreateIdempotent(context.Background(), principal, create, &expiresAt)
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		if i < limit {
			expected = append(expected, created.EpisodeID)
		}
	}

	purged, err := store.PurgeExpiredForPrincipal(context.Background(), principal, time.Now(), limit)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if purged != limit {
		t.Fatalf("purged = %d, want %d", purged, limit)
	}

	for i, id := range expected {
		record := recordByID(store, id)
		if record.episode.RedactionState != "purged_tombstone" {
			t.Fatalf("episode %d (index %d, the %d-th oldest expiry) was not purged: redaction_state=%q, want purged_tombstone -- the bounded purge did not prefer the oldest-expiring candidates", i, i, i, record.episode.RedactionState)
		}
	}
	for i := limit; i < seeded; i++ {
		create := testEpisodeCreate()
		create.ClientEpisodeID = fmt.Sprintf("episode_order_%02d", i)
		clientKey, _ := episodeKeys("org_1", create)
		id, ok := store.byClient[clientKey]
		if !ok {
			t.Fatalf("episode %d not found in store", i)
		}
		record := recordByID(store, id)
		if record.episode.RedactionState == "purged_tombstone" {
			t.Fatalf("episode %d (a newer-expiring candidate, beyond the limit) was purged ahead of an older one", i)
		}
	}
}

// recordByID reads a record directly off the store's backing map,
// bypassing the read guards (a purged episode reads as not-found through
// the normal API, indistinguishable from "not yet purged but expired" --
// this test needs to see the real redaction_state).
func recordByID(store *EpisodeStore, id string) episodeRecord {
	return store.byID[id]
}
