package orgpurge

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
)

type failingDriver struct {
	mu    sync.Mutex
	execs []string
}

func (d *failingDriver) Open(string) (driver.Conn, error) { return &failingConn{d: d}, nil }

type failingConn struct{ d *failingDriver }

func (c *failingConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (c *failingConn) Close() error                        { return nil }
func (c *failingConn) Begin() (driver.Tx, error)           { return nil, errors.New("database unavailable") }
func (c *failingConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.d.mu.Lock()
	c.d.execs = append(c.d.execs, query)
	c.d.mu.Unlock()
	return driver.RowsAffected(1), nil
}

type droppingGraph struct{}

func (droppingGraph) CountOrganizationGraphs(context.Context, string) (int, error)  { return 2, nil }
func (droppingGraph) DeleteOrganizationGraphs(context.Context, string) (int, error) { return 2, nil }

type freeLocker struct{}

func (freeLocker) Lock(context.Context, string) (func() error, error) {
	return func() error { return nil }, nil
}

func TestFailureAfterTheGraphDropIsIncompleteNeverPurged(t *testing.T) {
	drv := &failingDriver{}
	db := sql.OpenDB(connectorFunc(func(context.Context) (driver.Conn, error) { return &failingConn{d: drv}, nil }))
	t.Cleanup(func() { _ = db.Close() })
	purger := &Purger{DB: db, Graph: droppingGraph{}, Set: fakeSet{}, Entitlements: fakeEntitlements{}, Locker: freeLocker{}}

	report, err := purger.Run(context.Background(), testOrg, Options{ConfirmOrgKey: OrgKey(testOrg)})
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Run() error = %v, want ErrIncomplete", err)
	}
	if report.Status != StatusIncomplete || report.Graphs != 2 || report.TotalRows != 0 {
		t.Fatalf("report = %+v, want status incomplete, 2 graphs dropped, no rows claimed", report)
	}
	if !report.AuditRowWritten || len(drv.execs) != 1 || !strings.Contains(drv.execs[0], "org_purge_incomplete") {
		t.Fatalf("the failed run must be audited on its own statement, got %v", drv.execs)
	}
}

type connectorFunc func(context.Context) (driver.Conn, error)

func (f connectorFunc) Connect(ctx context.Context) (driver.Conn, error) { return f(ctx) }
func (f connectorFunc) Driver() driver.Driver                            { return nil }
