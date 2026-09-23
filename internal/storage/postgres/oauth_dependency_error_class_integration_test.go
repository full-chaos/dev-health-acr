package postgres

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// TestSanitizeDatabaseError_realCheckViolationCarriesSafeErrorClass is
// CHAOS-6278's real-Postgres proof: a genuinely planted CHECK constraint
// violation on acr.oauth_clients (an invalid client_id, violating
// migration 0040's own `client_id ~ '^acrc_[0-9a-f]{32}$'` constraint --
// exactly the shape of real defect CHAOS-6233 cost hours to root-cause
// through Postgres's OWN log instead of acr-api's) must classify, through
// sanitizeDatabaseError, as storage.ErrUnavailable carrying a
// *storage.DependencyErrorClass naming the real SQLSTATE, class, constraint
// and table -- never a bare, unclassified storage.ErrUnavailable.
//
// RED on baseline: before this change, sanitizeDatabaseError collapsed
// every non-unique-violation Postgres failure to a bare storage.ErrUnavailable
// with nothing attached, so errors.As below found nothing and this test
// failed on the require.True line.
func TestSanitizeDatabaseError_realCheckViolationCarriesSafeErrorClass(t *testing.T) {
	// Given a real Postgres database with the acr schema migrated, and a
	// genuine INSERT that violates oauth_clients' own client_id CHECK
	// constraint -- not a hand-authored error, the actual driver error a
	// real constraint violation produces.
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)

	// When
	_, err := db.ExecContext(ctx, `
INSERT INTO acr.oauth_clients (client_id, client_name, redirect_uris, created_at)
VALUES ('not-a-valid-client-id', 'test client', '[]'::jsonb, now())`)
	require.Error(t, err, "the planted client_id must actually violate the real CHECK constraint")
	sanitized := sanitizeDatabaseError(err)

	// Then: still classifies as ErrUnavailable (a check violation is not a
	// conflict -- only 23505 unique_violation maps to storage.ErrConflict)...
	require.ErrorIs(t, sanitized, storage.ErrUnavailable)
	require.False(t, errors.Is(sanitized, storage.ErrConflict), "a check violation must never be misclassified as a conflict")

	// ...and the safe classification survives, extractable by any caller
	// (acr-api's oauth dependency-failure log, in particular) via errors.As.
	var class *storage.DependencyErrorClass
	require.True(t, errors.As(sanitized, &class), "the real Postgres check violation must carry a storage.DependencyErrorClass")
	require.Equal(t, "23514", class.SQLState, "23514 is Postgres's own SQLSTATE for check_violation")
	require.Equal(t, "check_violation", class.Class)
	require.Equal(t, "oauth_clients_client_id_check", class.Constraint)
	require.Equal(t, "oauth_clients", class.Table)
}

// TestSanitizeDatabaseError_uniqueViolationStillConflictsWithNoClassLeak
// proves the pre-existing 23505 -> storage.ErrConflict path is unchanged by
// this class-attaching change (idempotent client_id retry semantics several
// callers rely on via errors.Is), while ALSO now carrying the same safe
// class information as any other classified failure.
func TestSanitizeDatabaseError_uniqueViolationStillConflictsWithNoClassLeak(t *testing.T) {
	// Given a client already registered once.
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	insert := `
INSERT INTO acr.oauth_clients (client_id, client_name, redirect_uris, created_at)
VALUES ('acrc_00000000000000000000000000000000', 'test client', '[]'::jsonb, now())`
	_, err := db.ExecContext(ctx, insert)
	require.NoError(t, err)

	// When the same client_id is inserted again.
	_, err = db.ExecContext(ctx, insert)
	require.Error(t, err)
	sanitized := sanitizeDatabaseError(err)

	// Then it is still ErrConflict (unchanged behavior)...
	require.ErrorIs(t, sanitized, storage.ErrConflict)
	// ...and now also carries the safe class, same as the ErrUnavailable path.
	var class *storage.DependencyErrorClass
	require.True(t, errors.As(sanitized, &class))
	require.Equal(t, "23505", class.SQLState)
	require.Equal(t, "unique_violation", class.Class)
}

// TestSanitizeDatabaseError_connectionFailureCarriesSafeErrorClass is
// codex round cf-6278-r1b's P1 regression test: a dial-time failure (the
// server refuses the TCP connection outright) never reaches a point where
// Postgres could hand back a SQLSTATE at all, so it surfaces as a real
// *pgconn.ConnectError, not a *pgconn.PgError -- a shape
// classifyDatabaseError did not check for before this fix, so a genuine
// connection failure classified as NOTHING, indistinguishable in
// acr-api's own logs from an unrelated unclassified dependency failure
// (exactly the "distinguish a permission failure from a connection
// failure from a constraint violation" CHAOS-6278 itself asks for).
//
// RED on baseline: errors.As below found no *storage.DependencyErrorClass
// at all for a real connection refusal.
func TestSanitizeDatabaseError_connectionFailureCarriesSafeErrorClass(t *testing.T) {
	// Given a DSN that dials a port nothing listens on -- a real refused
	// TCP connection, not a hand-authored error.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// When
	_, dialErr := pgx.Connect(ctx, "postgres://acr:acr@127.0.0.1:1/acr?connect_timeout=1")
	require.Error(t, dialErr, "127.0.0.1:1 must actually refuse the connection for this to be a real repro")
	var connErr *pgconn.ConnectError
	require.True(t, errors.As(dialErr, &connErr), "the dial failure must be a real *pgconn.ConnectError, the shape this fix targets")
	sanitized := sanitizeDatabaseError(dialErr)

	// Then
	require.ErrorIs(t, sanitized, storage.ErrUnavailable)
	var class *storage.DependencyErrorClass
	require.True(t, errors.As(sanitized, &class), "a real connection refusal must carry a storage.DependencyErrorClass")
	require.Equal(t, "connection_failure", class.Class)
	require.Equal(t, "08000", class.SQLState)
	// The failed DSN/address is never echoed into the safe class -- only
	// the class name and a synthetic SQLSTATE bucket.
	require.Empty(t, class.Constraint)
	require.Empty(t, class.Table)
}

// TestSanitizeDatabaseError_connectionTimeoutCarriesSafeErrorClass is
// codex round cf-6278-r2's P1 regression test, executed repro: a
// dial-time TIMEOUT (the server ACCEPTS the TCP connection but never
// completes the Postgres handshake, so pgx's own deadline fires) produces
// a *pgconn.ConnectError that ALSO wraps context.DeadlineExceeded in its
// chain. sanitizeDatabaseError's bare context.Canceled/DeadlineExceeded
// short-circuits used to run BEFORE the connection classification, so this
// shape returned a bare context.DeadlineExceeded with no class at all --
// indistinguishable, in acr-api's logs, from an unrelated deadline.
//
// RED on baseline (before reordering the ConnectError check ahead of the
// context.* short-circuits): errors.As found no DependencyErrorClass and
// errors.Is(sanitized, storage.ErrUnavailable) was false too.
func TestSanitizeDatabaseError_connectionTimeoutCarriesSafeErrorClass(t *testing.T) {
	// Given a TCP listener that accepts connections but never speaks the
	// Postgres protocol, so pgx's handshake hangs until its own deadline.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()
	t.Cleanup(func() {
		select {
		case conn := <-accepted:
			_ = conn.Close()
		default:
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// When
	dsn := "postgres://acr:acr@" + listener.Addr().String() + "/acr?sslmode=disable"
	_, dialErr := pgx.Connect(ctx, dsn)
	require.Error(t, dialErr)
	var connErr *pgconn.ConnectError
	require.True(t, errors.As(dialErr, &connErr), "the stalled handshake must surface as a real *pgconn.ConnectError")
	require.True(t, errors.Is(dialErr, context.DeadlineExceeded), "the repro must be the deadline-wrapping shape this fix targets, not a plain refusal")
	sanitized := sanitizeDatabaseError(dialErr)

	// Then
	require.ErrorIs(t, sanitized, storage.ErrUnavailable)
	var class *storage.DependencyErrorClass
	require.True(t, errors.As(sanitized, &class), "a real dial-time timeout must carry a storage.DependencyErrorClass")
	require.Equal(t, "connection_failure", class.Class)
	require.Equal(t, "08000", class.SQLState)
}

// TestSanitizeDatabaseError_connectionLostAfterEstablishmentCarriesSafeErrorClass
// is the class sweep for codex rounds cf-6278-r1b and r2, which each found
// ONE dial-time variant of "a connection failure classifies as nothing"
// (a refusal, then a deadline-wrapped timeout). The remaining family
// members are failures AFTER the connection was established -- the server
// (or the network path to it) going away mid-query -- which are neither a
// *pgconn.PgError (the server that would produce a SQLSTATE is exactly
// what's gone) nor a *pgconn.ConnectError (dial-time only): a raw
// *net.OpError (reset, broken pipe), the stream closing (io.EOF /
// io.ErrUnexpectedEOF), and database/sql's own driver.ErrBadConn marker.
// All must classify as connection_failure, each wrapped the way a real
// caller's fmt.Errorf("...: %w", err) would wrap it.
func TestSanitizeDatabaseError_connectionLostAfterEstablishmentCarriesSafeErrorClass(t *testing.T) {
	// A real *net.OpError, produced by the standard library dialing a port
	// nothing listens on -- not hand-authored.
	_, dialErr := net.DialTimeout("tcp", "127.0.0.1:1", time.Second)
	require.Error(t, dialErr)
	var opErr *net.OpError
	require.True(t, errors.As(dialErr, &opErr), "net.Dial's own failure must be a real *net.OpError")

	cases := []struct {
		name string
		err  error
	}{
		{name: "net.OpError", err: dialErr},
		{name: "io.EOF", err: io.EOF},
		{name: "io.ErrUnexpectedEOF", err: io.ErrUnexpectedEOF},
		{name: "driver.ErrBadConn", err: driver.ErrBadConn},
		{name: "wrapped net.OpError", err: fmt.Errorf("query: %w", dialErr)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sanitized := sanitizeDatabaseError(tc.err)

			require.ErrorIs(t, sanitized, storage.ErrUnavailable)
			var class *storage.DependencyErrorClass
			require.True(t, errors.As(sanitized, &class), "a connection lost after establishment must carry a storage.DependencyErrorClass")
			require.Equal(t, "connection_failure", class.Class)
			require.Equal(t, "08006", class.SQLState)
		})
	}
}

// TestSanitizeDatabaseError_plainDeadlineStillReturnsBareDeadline proves
// the reordering above did not change the pre-existing bare
// context.DeadlineExceeded/Canceled behavior for errors that are NOT
// connection errors -- e.g. a query whose caller-supplied context expired
// mid-flight on an already-established connection.
func TestSanitizeDatabaseError_plainDeadlineStillReturnsBareDeadline(t *testing.T) {
	sanitized := sanitizeDatabaseError(context.DeadlineExceeded)
	require.Equal(t, context.DeadlineExceeded, sanitized)
	canceled := sanitizeDatabaseError(context.Canceled)
	require.Equal(t, context.Canceled, canceled)
}
