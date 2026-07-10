package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"testing"
)

type episodeQuery func(string, []driver.NamedValue) (driver.Rows, error)
type episodeExec func(string, []driver.NamedValue) (driver.Result, error)

type episodeScript struct {
	query episodeQuery
	exec  episodeExec
}

type episodeTestDriver struct{}
type episodeTestConn struct{ script episodeScript }
type episodeTestTx struct{}

type episodeTestRows struct {
	values  [][]driver.Value
	index   int
	columns []string
}

var episodeDrivers sync.Map

func init() { sql.Register("episode-test", episodeTestDriver{}) }

func openEpisodeTestDB(t *testing.T, query episodeQuery) *sql.DB {
	t.Helper()
	dsn := t.Name()
	episodeDrivers.Store(dsn, episodeScript{query: query})
	db, err := sql.Open("episode-test", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { episodeDrivers.Delete(dsn); _ = db.Close() })
	return db
}

func openEpisodeExecDB(t *testing.T, exec episodeExec) *sql.DB {
	t.Helper()
	dsn := t.Name()
	episodeDrivers.Store(dsn, episodeScript{exec: exec})
	db, err := sql.Open("episode-test", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { episodeDrivers.Delete(dsn); _ = db.Close() })
	return db
}

func (episodeTestDriver) Open(name string) (driver.Conn, error) {
	query, ok := episodeDrivers.Load(name)
	if !ok {
		return nil, errors.New("test database script missing")
	}
	return episodeTestConn{script: query.(episodeScript)}, nil
}

func (episodeTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}

func (episodeTestConn) Close() error              { return nil }
func (episodeTestConn) Begin() (driver.Tx, error) { return episodeTestTx{}, nil }

func (c episodeTestConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.script.query == nil {
		return nil, errors.New("query unsupported")
	}
	return c.script.query(query, args)
}

func (c episodeTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.script.exec == nil {
		return nil, errors.New("exec unsupported")
	}
	return c.script.exec(query, args)
}

func (episodeTestTx) Commit() error   { return nil }
func (episodeTestTx) Rollback() error { return nil }

func (r *episodeTestRows) Columns() []string { return r.columns }
func (r *episodeTestRows) Close() error      { return nil }

func (r *episodeTestRows) Next(dest []driver.Value) error {
	if r.index == len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

func episodeRows(values [][]driver.Value) driver.Rows {
	width := 0
	if len(values) > 0 {
		width = len(values[0])
	}
	return &episodeTestRows{values: values, columns: make([]string, width)}
}

func rfc4122UUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}
