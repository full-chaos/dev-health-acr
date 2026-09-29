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
