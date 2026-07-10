package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

var (
	packetDriverOnce sync.Once
	packetDriverID   atomic.Uint64
	packetStates     sync.Map
)

func openPacketDB(t *testing.T) (*sql.DB, *packetState) {
	t.Helper()
	packetDriverOnce.Do(func() { sql.Register("acr_packet_test", packetDriver{}) })
	name := fmt.Sprintf("packet-%d", packetDriverID.Add(1))
	state := &packetState{rows: map[string]packetRow{}}
	packetStates.Store(name, state)
	db, err := sql.Open("acr_packet_test", name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { packetStates.Delete(name); _ = db.Close() })
	return db, state
}

type packetDriver struct{}

func (packetDriver) Open(name string) (driver.Conn, error) {
	value, ok := packetStates.Load(name)
	if !ok {
		return nil, errors.New("missing packet state")
	}
	return packetConn{state: value.(*packetState)}, nil
}

type packetState struct {
	mu             sync.Mutex
	rows           map[string]packetRow
	audits         []packetAudit
	insertAttempts int
	insertError    error
}

type packetRow struct {
	orgID, repoID, slug string
	payload             []byte
	expiresAt           time.Time
}

type packetAudit struct {
	packetID string
	metadata string
}

type packetConn struct{ state *packetState }

func (packetConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("prepare unsupported") }
func (packetConn) Close() error                        { return nil }
func (packetConn) Begin() (driver.Tx, error)           { return packetTx{}, nil }

type packetTx struct{}

func (packetTx) Commit() error   { return nil }
func (packetTx) Rollback() error { return nil }

func (c packetConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if strings.Contains(query, "acr.audit_events") {
		c.state.audits = append(c.state.audits, packetAudit{packetID: args[3].Value.(string), metadata: args[4].Value.(string)})
		return driver.RowsAffected(1), nil
	}
	if strings.Contains(query, "INSERT INTO") {
		row, err := postgresPacketInsertRow(query, args)
		if err != nil {
			return nil, err
		}
		c.state.insertAttempts++
		if c.state.insertError != nil {
			return nil, c.state.insertError
		}
		id := args[0].Value.(string)
		if _, exists := c.state.rows[id]; exists {
			return driver.RowsAffected(0), nil
		}
		c.state.rows[id] = row
		return driver.RowsAffected(1), nil
	}
	if strings.Contains(query, "DELETE FROM") {
		before := args[0].Value.(time.Time)
		limit := int(args[1].Value.(int64))
		removed := 0
		for id, row := range c.state.rows {
			if removed < limit && !row.expiresAt.After(before) {
				delete(c.state.rows, id)
				removed++
			}
		}
		return driver.RowsAffected(removed), nil
	}
	return nil, errors.New("unexpected execute")
}

func (c packetConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if strings.Contains(query, "DELETE FROM") {
		before := args[0].Value.(time.Time)
		limit := int(args[1].Value.(int64))
		values := make([][]driver.Value, 0, limit)
		for id, row := range c.state.rows {
			if len(values) < limit && !row.expiresAt.After(before) {
				delete(c.state.rows, id)
				values = append(values, []driver.Value{id, row.orgID, row.repoID, row.expiresAt})
			}
		}
		return &packetRows{values: values}, nil
	}
	if !strings.Contains(query, "$2::uuid") || len(args) < 2 {
		return nil, errors.New("packet lookup must cast org_id to uuid")
	}
	packetID, ok := args[0].Value.(string)
	if !ok {
		return nil, errors.New("context_packet_id must be text")
	}
	orgID, err := postgresUUID(args[1].Value)
	if err != nil {
		return nil, err
	}
	row, ok := c.state.rows[packetID]
	if !ok || row.orgID != orgID {
		return &packetRows{}, nil
	}
	if len(args) == 2 {
		return &packetRows{values: [][]driver.Value{{row.payload}}}, nil
	}
	if !row.expiresAt.After(args[2].Value.(time.Time)) {
		return &packetRows{}, nil
	}
	return &packetRows{values: [][]driver.Value{{row.payload, row.slug}}}, nil
}

func postgresPacketInsertRow(query string, args []driver.NamedValue) (packetRow, error) {
	if !strings.Contains(query, "$2::uuid") || !strings.Contains(query, "$3::uuid") || !strings.Contains(query, "$11::jsonb") {
		return packetRow{}, errors.New("packet insert must cast organization, repository, and payload values")
	}
	if len(args) != 14 {
		return packetRow{}, fmt.Errorf("packet insert args = %d, want 14", len(args))
	}
	packetID, ok := args[0].Value.(string)
	if !ok || packetID == "" {
		return packetRow{}, errors.New("context_packet_id must be non-empty text")
	}
	orgID, err := postgresUUID(args[1].Value)
	if err != nil {
		return packetRow{}, err
	}
	repoID, err := postgresUUID(args[2].Value)
	if err != nil {
		return packetRow{}, err
	}
	slug, ok := args[3].Value.(string)
	if !ok || slug == "" {
		return packetRow{}, errors.New("repo_slug must be non-empty text")
	}
	if schemaVersion, ok := args[5].Value.(string); !ok || schemaVersion != contractsv1.ContextPacketSchema {
		return packetRow{}, errors.New("new row violates check constraint context_packet_snapshots_schema_version_check")
	}
	if resolution, ok := args[8].Value.(string); !ok || !validResolution(contractsv1.ScopeResolution(resolution)) {
		return packetRow{}, errors.New("new row violates check constraint context_packet_snapshots_scope_resolution_check")
	}
	if status, ok := args[9].Value.(string); !ok || !validStatus(contractsv1.PacketStatus(status)) {
		return packetRow{}, errors.New("new row violates check constraint context_packet_snapshots_status_check")
	}
	payload, ok := args[10].Value.(string)
	if !ok || !json.Valid([]byte(payload)) {
		return packetRow{}, errors.New("invalid input syntax for type json")
	}
	expiresAt, ok := args[12].Value.(time.Time)
	if !ok {
		return packetRow{}, errors.New("expires_at must be timestamptz")
	}
	return packetRow{orgID: orgID, repoID: repoID, slug: slug, payload: []byte(payload), expiresAt: expiresAt}, nil
}

func postgresUUID(value driver.Value) (string, error) {
	text, ok := value.(string)
	if !ok || !uuidPattern.MatchString(text) {
		return "", fmt.Errorf("invalid input syntax for type uuid: %q", value)
	}
	return strings.ToLower(text), nil
}

type packetRows struct {
	values [][]driver.Value
	index  int
}

func (r *packetRows) Columns() []string {
	if len(r.values) == 0 {
		return []string{"payload", "repo_slug"}
	}
	columns := make([]string, len(r.values[0]))
	for index := range columns {
		columns[index] = fmt.Sprintf("column_%d", index)
	}
	return columns
}

func (r *packetRows) Close() error { return nil }

func (r *packetRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}
