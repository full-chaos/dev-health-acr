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
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestPacketStorePostgres_roundTripScopesAndExpiry(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	db, state := openPacketDB(t)
	current := now
	store, err := NewPacketStore(db, func() time.Time { return current })
	if err != nil {
		t.Fatal(err)
	}
	owner := storage.Principal{OrgID: "00000000-0000-0000-0000-000000000001", RepositoryScopes: []string{"example-org/widget-service"}}
	packet := postgresPacket(now, "pkt-postgres-001")
	if err := store.SaveSnapshot(context.Background(), owner, packet, now.Add(30*24*time.Hour)); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	want, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	packet.Goal = "mutated after save"
	got, err := store.GetSnapshot(context.Background(), owner, "pkt-postgres-001")
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	actual, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(want) {
		t.Fatalf("snapshot changed after save\n got: %s\nwant: %s", actual, want)
	}
	if _, err := store.GetSnapshot(context.Background(), storage.Principal{OrgID: "00000000-0000-0000-0000-000000000002", RepositoryScopes: []string{"*"}}, got.ContextPacketID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("foreign organization lookup error = %v, want not found", err)
	}
	if err := store.SaveSnapshot(context.Background(), storage.Principal{OrgID: "00000000-0000-0000-0000-000000000002", RepositoryScopes: []string{"*"}}, got, now.Add(30*24*time.Hour)); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("cross-organization save error = %v, want conflict", err)
	}
	expired := postgresPacket(now, "pkt-postgres-expired")
	current = now.Add(-time.Minute)
	if err := store.SaveSnapshot(context.Background(), owner, expired, now); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	current = now
	if _, err := store.GetSnapshot(context.Background(), owner, expired.ContextPacketID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expired lookup error = %v, want not found", err)
	}
	if removed, err := store.PurgeExpired(context.Background(), now, 1); err != nil || removed != 1 {
		t.Fatalf("purge expired = (%d, %v), want (1, nil)", removed, err)
	}
	state.mu.Lock()
	if len(state.audits) != 1 || state.audits[0].packetID != expired.ContextPacketID {
		state.mu.Unlock()
		t.Fatalf("unexpected purge audits: %#v", state.audits)
	}
	state.mu.Unlock()
}

func TestPacketStore_canonicalJSONTreatsKeyOrderAsIdempotent(t *testing.T) {
	if !sameJSON([]byte(`{"a":1,"b":2}`), []byte(`{"b":2,"a":1}`)) {
		t.Fatal("equivalent JSON key orders did not compare equal")
	}
}

func postgresPacket(now time.Time, id string) contractsv1.ContextPacket {
	return contractsv1.ContextPacket{
		SchemaVersion: contractsv1.ContextPacketSchema, ContextPacketID: id, RequestID: "req-postgres-001", GeneratedAt: now,
		Status: contractsv1.PacketComplete, Goal: "Investigate the fixture", Repository: contractsv1.RepositoryRef{RepoID: "00000000-0000-0000-0000-000000000101", Slug: "example-org/widget-service"},
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "00000000-0000-0000-0000-000000000101", RepoSlug: "example-org/widget-service", Resolution: contractsv1.ScopeBranchFiltered, FallbackReasons: []string{}},
		QueryVersion:  "context-query.v1", RankingVersion: "context-ranking.v1", Summary: "Saved exactly.", Items: []contractsv1.ContextPacketItem{},
		RequiredChecks: []contractsv1.RequiredCheck{}, RecommendedNextSteps: []contractsv1.RecommendedStep{}, Freshness: contractsv1.Freshness{AsOf: now, Watermarks: []contractsv1.SourceWatermark{}},
		Coverage: contractsv1.Coverage{SourcesConsidered: []string{}, SourcesAvailable: []string{}, SourcesUnavailable: []contractsv1.UnavailableSource{}, DegradedReasons: []string{}},
		Budget:   contractsv1.PacketBudget{MaxItems: 1, MaxOutputTokens: 500, MaxSerializedBytes: 8192}, Warnings: []string{},
		Compatibility: contractsv1.Compatibility{ServiceVersion: "test", MinimumSidecarVersion: "0.1.0", SupportedSchemaVersions: []string{contractsv1.ContextPacketSchema}},
	}
}

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
	mu     sync.Mutex
	rows   map[string]packetRow
	audits []packetAudit
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
		id := args[0].Value.(string)
		if _, exists := c.state.rows[id]; exists {
			return driver.RowsAffected(0), nil
		}
		c.state.rows[id] = packetRow{orgID: args[1].Value.(string), repoID: args[2].Value.(string), slug: args[3].Value.(string), payload: []byte(args[10].Value.(string)), expiresAt: args[12].Value.(time.Time)}
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
	row, ok := c.state.rows[args[0].Value.(string)]
	if !ok || row.orgID != args[1].Value.(string) {
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
