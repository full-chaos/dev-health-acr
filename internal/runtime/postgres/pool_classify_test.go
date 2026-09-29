package postgres

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/stretchr/testify/require"
)

// fakePostgres answers every connection's startup message with an
// ErrorResponse carrying sqlState, and counts connections. It speaks the real
// wire protocol so Open's error path is the production one, not a stub.
func fakePostgres(t *testing.T, sqlState string) (dsn string, connections *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	connections = &atomic.Int32{}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			go func() {
				defer conn.Close()
				backend := pgproto3.NewBackend(conn, conn)
				if _, err := backend.ReceiveStartupMessage(); err != nil {
					return
				}
				backend.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: sqlState, Message: "fake"})
				_ = backend.Flush()
			}()
		}
	}()
	return "postgres://u:p@" + listener.Addr().String() + "/db?sslmode=disable", connections
}

// CHAOS-7168 review P1: only a transport failure (no server answer) is
// retryable, plus server answers class 08, class 53 and 57P03; every other
// server answer must be ErrRejected.
func TestOpen_classifiesServerAnswersByRetryability(t *testing.T) {
	for state, wantUnavailable := range map[string]bool{
		"28P01": false, // invalid_password
		"28000": false, // invalid_authorization_specification
		"3D000": false, // invalid_catalog_name
		"42501": false, // insufficient_privilege
		"57014": false, // query_canceled: class 57 but NOT 57P03
		"57P03": true,  // cannot_connect_now (starting up)
		"53300": true,  // too_many_connections
		"08006": true,  // connection_failure reported by the server
	} {
		dsn, _ := fakePostgres(t, state)
		_, err := Open(context.Background(), Config{DSN: dsn, PingTimeout: 2 * time.Second})
		require.Error(t, err, state)
		require.Equal(t, wantUnavailable, isUnavailable(err), "sqlstate %s: %v", state, err)
		require.Equal(t, !wantUnavailable, isRejected(err), "sqlstate %s: %v", state, err)
	}
}

func TestOpen_unreachableServerIsUnavailable(t *testing.T) {
	_, err := Open(context.Background(), Config{DSN: "postgres://u:p@127.0.0.1:1/db?sslmode=disable", PingTimeout: time.Second})
	require.True(t, isUnavailable(err), "%v", err)
}

func isUnavailable(err error) bool { return errors.Is(err, ErrUnavailable) }
func isRejected(err error) bool    { return errors.Is(err, ErrRejected) }

// fakeRawPostgres runs handler on every accepted connection and counts them.
func fakeRawPostgres(t *testing.T, sslmode string, handler func(net.Conn)) (dsn string, connections *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	connections = &atomic.Int32{}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			go func() { defer conn.Close(); handler(conn) }()
		}
	}()
	return "postgres://u:p@" + listener.Addr().String() + "/db?sslmode=" + sslmode, connections
}

// Review round 2 P1: a NON-transport, NON-PgError failure (the server refusing
// TLS negotiation, a malformed protocol response) is terminal; a connection
// that closes mid-startup and an unresolvable host are transport failures.
func TestOpen_classifiesNonServerErrorsByTransportVersusProtocol(t *testing.T) {
	readSome := func(c net.Conn) {
		buf := make([]byte, 64)
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		_, _ = c.Read(buf)
	}
	cases := []struct {
		name            string
		sslmode         string
		handler         func(net.Conn)
		wantUnavailable bool
	}{
		{"server refuses TLS", "require", func(c net.Conn) { readSome(c); _, _ = c.Write([]byte("N")) }, false},
		{"malformed protocol response", "disable", func(c net.Conn) { readSome(c); _, _ = c.Write([]byte("zzzzzzzzzzzz")) }, false},
		{"closes mid startup", "disable", func(c net.Conn) { readSome(c) }, true},
	}
	for _, tc := range cases {
		dsn, _ := fakeRawPostgres(t, tc.sslmode, tc.handler)
		_, err := Open(context.Background(), Config{DSN: dsn, PingTimeout: 2 * time.Second})
		require.Error(t, err, tc.name)
		require.Equal(t, tc.wantUnavailable, errors.Is(err, ErrUnavailable), "%s: %v", tc.name, err)
		require.Equal(t, !tc.wantUnavailable, errors.Is(err, ErrRejected), "%s: %v", tc.name, err)
	}
	_, err := Open(context.Background(), Config{DSN: "postgres://u:p@nonexistent.invalid:5432/db?sslmode=disable", PingTimeout: 2 * time.Second})
	require.ErrorIs(t, err, ErrUnavailable, "unresolvable host")
}
