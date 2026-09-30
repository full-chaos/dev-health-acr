package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

// CHAOS-7249: CountPurgeRemaining is what the purge tick logs next to the
// deleted counts, so a tick that skipped every eligible row reads differently
// from a tick that found nothing. These tests run it against real PostgreSQL.

func (f *oauthPurgeFixture) remaining(now time.Time, limit int) OAuthPurgeRemaining {
	f.t.Helper()
	remaining, err := f.store.CountPurgeRemaining(f.ctx, now, oauthPurgeGrace, oauthPurgeIdle, limit)
	require.NoError(f.t, err)
	return remaining
}

// It counts what PurgeExpired would take now (an expired request with no live
// credential, an idle client) and nothing PurgeExpired would keep (a request
// inside the grace, a request backing a live credential, a young client, a
// client that still has a request row). After the purge nothing is left.
func TestOAuthStore_CountPurgeRemaining_countsWhatThePurgeWouldTake(t *testing.T) {
	// Given: a purge 40 days after t0; a credential issued at t0 for 45 days is live
	f := newOAuthPurgeFixture(t)
	purgeAt := f.t0.Add(40 * 24 * time.Hour)
	credentialEnd := f.t0.Add(45 * 24 * time.Hour)

	f.client(0xe1, f.t0)                       // idle: eligible
	f.client(0xe2, purgeAt.Add(-24*time.Hour)) // young: kept

	// its request is eligible; the client is not idle while that row exists
	withOldRequest := f.client(0xe3, f.t0)
	f.request(withOldRequest, f.device(), f.t0)

	// a request inside the grace: kept, and so is its client
	withRecentRequest := f.client(0xe4, f.t0)
	f.request(withRecentRequest, f.device(), purgeAt.Add(-time.Hour))

	// a request backing a live credential: kept, and so is its client
	liveClient := f.client(0xe5, f.t0)
	liveDevice, _ := f.redeemedDevice(&credentialEnd)
	f.request(liveClient, liveDevice, f.t0)

	// When / Then: before the purge, one request and one client are eligible
	require.Equal(t, OAuthPurgeRemaining{Requests: 1, Clients: 1, DeviceAuthorizations: 1}, f.remaining(purgeAt, 500))

	// And: the purge takes them (the client whose request just went, too), and nothing is left
	require.Equal(t, OAuthPurgeResult{Requests: 1, Clients: 2, DeviceAuthorizations: 1}, f.purge(purgeAt, 500))
	require.Equal(t, OAuthPurgeRemaining{}, f.remaining(purgeAt, 500))
}

// Each count reads at most limit+1 rows, so it is bounded by the batch limit
// and a value above limit means "more than one batch".
func TestOAuthStore_CountPurgeRemaining_isCappedAtOneBatchPlusOne(t *testing.T) {
	// Given: 6 expired requests (on a young client, each on its own device
	// authorization) and 6 idle clients
	f := newOAuthPurgeFixture(t)
	purgeAt := f.t0.Add(40 * 24 * time.Hour)
	requestClient := f.client(0xe6, purgeAt.Add(-time.Hour))
	for range 6 {
		f.request(requestClient, f.device(), f.t0)
	}
	for seed := byte(0xe7); seed < 0xe7+6; seed++ {
		f.client(seed, f.t0)
	}

	// When / Then: 6 of each are eligible; the count stops at limit+1 = 3
	require.Equal(t, OAuthPurgeRemaining{Requests: 3, Clients: 3, DeviceAuthorizations: 3}, f.remaining(purgeAt, 2))
	require.Equal(t, OAuthPurgeRemaining{Requests: 6, Clients: 6, DeviceAuthorizations: 6}, f.remaining(purgeAt, 500))
}

// The tick that motivates the count: every eligible row is held by a concurrent
// flow, so the purge deletes nothing (it skips locked rows instead of waiting
// for them) while rows are still eligible. The count must report them, and must
// not wait on the locks either. Once the locks are gone the next purge takes
// them and the count drops to zero.
func TestOAuthStore_CountPurgeRemaining_reportsRowsThePurgeSkippedBecauseTheyWereLocked(t *testing.T) {
	// Given: 2 expired requests on a young client, their 2 device authorizations,
	// and 2 idle clients, all locked by another transaction
	f := newOAuthPurgeFixture(t)
	purgeAt := f.t0.Add(40 * 24 * time.Hour)
	requestClient := f.client(0xf7, purgeAt.Add(-time.Hour))
	f.request(requestClient, f.device(), f.t0)
	f.request(requestClient, f.device(), f.t0)
	idleA := f.client(0xf8, f.t0)
	idleB := f.client(0xfa, f.t0)

	tx, err := f.db.BeginTx(f.ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(f.ctx, `SELECT 1 FROM acr.oauth_authorization_requests WHERE client_id = $1 FOR UPDATE`, requestClient)
	require.NoError(t, err)
	_, err = tx.ExecContext(f.ctx, `SELECT 1 FROM acr.device_authorizations FOR UPDATE`)
	require.NoError(t, err)
	_, err = tx.ExecContext(f.ctx, `SELECT 1 FROM acr.oauth_clients WHERE client_id = ANY($1::text[]) FOR KEY SHARE`, []string{idleA, idleB})
	require.NoError(t, err)

	// A bounded context: a purge or a count that waited on a locked row would hang here.
	ctx, cancel := context.WithTimeout(f.ctx, 20*time.Second)
	defer cancel()

	// When
	result, err := f.store.PurgeExpired(ctx, purgeAt, oauthPurgeGrace, oauthPurgeIdle, 500)
	require.NoError(t, err)
	remaining, err := f.store.CountPurgeRemaining(ctx, purgeAt, oauthPurgeGrace, oauthPurgeIdle, 500)
	require.NoError(t, err, "the count must read past locked rows, not wait for them")

	// Then: nothing deleted, everything still eligible: the two ticks are told apart
	require.Equal(t, OAuthPurgeResult{}, result)
	require.Equal(t, OAuthPurgeRemaining{Requests: 2, Clients: 2, DeviceAuthorizations: 2}, remaining)

	// And: released, the next purge takes them and nothing remains
	require.NoError(t, tx.Commit())
	require.Equal(t, OAuthPurgeResult{Requests: 2, Clients: 2, DeviceAuthorizations: 2}, f.purge(purgeAt, 500))
	require.Equal(t, OAuthPurgeRemaining{}, f.remaining(purgeAt, 500))
}

// The count refuses the windows the purge refuses, and a non-positive limit
// reads nothing, so a misconfigured loop fails the same way in both.
func TestOAuthStore_CountPurgeRemaining_refusesWhatThePurgeRefuses(t *testing.T) {
	f := newOAuthPurgeFixture(t)
	purgeAt := f.t0.Add(40 * 24 * time.Hour)

	for name, windows := range map[string][2]time.Duration{
		"zero grace":    {0, oauthPurgeIdle},
		"zero idle":     {oauthPurgeGrace, 0},
		"grace < idle":  {oauthPurgeIdle - time.Hour, oauthPurgeIdle},
		"negative both": {-time.Hour, -time.Hour},
	} {
		_, err := f.store.CountPurgeRemaining(f.ctx, purgeAt, windows[0], windows[1], 500)
		require.ErrorIsf(t, err, storage.ErrInvalidOAuthClient, "%s", name)
	}
	remaining, err := f.store.CountPurgeRemaining(f.ctx, purgeAt, oauthPurgeGrace, oauthPurgeIdle, 0)
	require.NoError(t, err)
	require.Equal(t, OAuthPurgeRemaining{}, remaining)
}
