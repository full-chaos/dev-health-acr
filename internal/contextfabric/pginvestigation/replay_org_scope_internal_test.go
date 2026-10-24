package pginvestigation

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"testing"
)

type recordedQuery struct {
	statement string
	args      []driver.NamedValue
}

type recordingConnector struct{ queries *[]recordedQuery }

func (c recordingConnector) Connect(context.Context) (driver.Conn, error) {
	return recordingConn(c), nil
}
func (recordingConnector) Driver() driver.Driver { return nil }

type recordingConn struct{ queries *[]recordedQuery }

func (recordingConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (recordingConn) Close() error                        { return nil }
func (recordingConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }
func (c recordingConn) QueryContext(_ context.Context, statement string, args []driver.NamedValue) (driver.Rows, error) {
	*c.queries = append(*c.queries, recordedQuery{statement: statement, args: args})
	return emptyRows{}, nil
}

type emptyRows struct{}

func (emptyRows) Columns() []string         { return []string{"payload", "semantic_state"} }
func (emptyRows) Close() error              { return nil }
func (emptyRows) Next([]driver.Value) error { return io.EOF }

func TestVerifyIdempotentReplayReadsOnlyTheCallersOrganizationRow(t *testing.T) {
	var queries []recordedQuery
	store := &Store{db: sql.OpenDB(recordingConnector{queries: &queries})}

	err := store.verifyIdempotentReplay(context.Background(), "org-2", "result-owned-by-org-1", []byte(`{}`), nil)

	if err == nil || len(queries) != 1 {
		t.Fatalf("err = %v, queries = %d", err, len(queries))
	}
	if !strings.Contains(queries[0].statement, "result_id = $1 AND org_id = $2") {
		t.Fatalf("existing-row read is not org scoped: %s", queries[0].statement)
	}
	if len(queries[0].args) != 2 || queries[0].args[0].Value != "result-owned-by-org-1" || queries[0].args[1].Value != "org-2" {
		t.Fatalf("args = %#v", queries[0].args)
	}
}
