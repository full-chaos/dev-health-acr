package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
)

const fixturePostgresDriverName = "acr-api-fixture-postgres"

var (
	fixturePostgresRegister sync.Once
	fixturePostgresSequence atomic.Uint64
	fixturePostgresStates   sync.Map
)

type fixturePostgresState struct {
	mu        sync.Mutex
	tokenHash string
	row       []driver.Value
}

type fixturePostgresDriver struct{}

type fixturePostgresConn struct{ state *fixturePostgresState }

type fixturePostgresTx struct{}

type fixturePostgresRows struct {
	row  []driver.Value
	done bool
}

func openFixturePostgres() (*sql.DB, *fixturePostgresState, error) {
	fixturePostgresRegister.Do(func() { sql.Register(fixturePostgresDriverName, fixturePostgresDriver{}) })
	name := "fixture-" + strings.Repeat("x", int(fixturePostgresSequence.Add(1)))
	state := &fixturePostgresState{}
	fixturePostgresStates.Store(name, state)
	database, err := sql.Open(fixturePostgresDriverName, name)
	return database, state, err
}

func (fixturePostgresDriver) Open(name string) (driver.Conn, error) {
	value, ok := fixturePostgresStates.Load(name)
	if !ok {
		return nil, errors.New("fixture PostgreSQL state not found")
	}
	return &fixturePostgresConn{state: value.(*fixturePostgresState)}, nil
}

func (c *fixturePostgresConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fixture PostgreSQL prepared statements are unsupported")
}

func (c *fixturePostgresConn) Close() error { return nil }

func (c *fixturePostgresConn) Begin() (driver.Tx, error) {
	return fixturePostgresTx{}, nil
}

func (fixturePostgresTx) Commit() error   { return nil }
func (fixturePostgresTx) Rollback() error { return nil }

func (c *fixturePostgresConn) Ping(context.Context) error { return nil }

func (c *fixturePostgresConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (c *fixturePostgresConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if strings.Contains(query, "INSERT INTO acr.client_credentials") {
		c.state.tokenHash = args[4].Value.(string)
		c.state.row = []driver.Value{
			args[0].Value, args[2].Value, args[3].Value, args[1].Value,
			[]byte(args[5].Value.(string)), []byte(args[6].Value.(string)),
			args[8].Value, args[9].Value, args[10].Value, args[11].Value,
			// workload_binding_id ($15, CHAOS-4013): none of this fixture's
			// callers issue a workload-exchanged credential, so this stays
			// nil (SQL NULL) -- matches insertCredential's real 15th
			// parameter position.
			args[14].Value,
		}
	}
	return driver.RowsAffected(1), nil
}

func (c *fixturePostgresConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if !strings.Contains(query, "WHERE token_hash = $1") || len(args) != 1 || args[0].Value != c.state.tokenHash || c.state.row == nil {
		return &fixturePostgresRows{}, nil
	}
	return &fixturePostgresRows{row: append([]driver.Value(nil), c.state.row...)}, nil
}

func (r *fixturePostgresRows) Columns() []string {
	return []string{"credential_id", "name", "token_prefix", "org_id", "repository_scopes", "scopes", "created_at", "expires_at", "revoked_at", "last_used_at", "workload_binding_id"}
}

func (r *fixturePostgresRows) Close() error { return nil }

func (r *fixturePostgresRows) Next(destination []driver.Value) error {
	if r.done || r.row == nil {
		return io.EOF
	}
	copy(destination, r.row)
	r.done = true
	return nil
}
