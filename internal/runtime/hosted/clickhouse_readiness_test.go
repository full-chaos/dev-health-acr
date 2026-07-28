package hosted

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

func TestCheckClickHouseRuntime_passesWhenMigration070DigestSchemaIsAvailable(t *testing.T) {
	// Given: a reachable ClickHouse with every migration-070 digest column.
	rows := &readinessRows{}
	client := &readinessClient{rows: rows}

	// When: the hosted runtime validates its ClickHouse dependency.
	err := checkClickHouseRuntime(context.Background(), func(context.Context) error { return nil }, client)

	// Then: readiness succeeds after issuing the fixed schema probe and closing rows.
	if err != nil {
		t.Fatalf("checkClickHouseRuntime() error = %v, want nil", err)
	}
	if client.statement != clickHouseReadinessSchemaProbe {
		t.Fatalf("schema probe = %q, want %q", client.statement, clickHouseReadinessSchemaProbe)
	}
	for _, requirement := range []string{
		"repos AS r", "r.ref_sha256", "git_pull_requests AS p", "p.head_branch_sha256", "p.base_branch_sha256",
		"ci_pipeline_runs AS c", "c.branch_sha256", "file_complexity_snapshots AS f", "f.ref_sha256",
	} {
		if !strings.Contains(client.statement, requirement) {
			t.Errorf("schema probe missing %q: %q", requirement, client.statement)
		}
	}
	if !rows.closed {
		t.Error("schema probe rows were not closed")
	}
}

func TestCheckClickHouseRuntime_failsWhenMigration070SchemaProbeFails(t *testing.T) {
	// Given: ClickHouse rejects the digest schema query because migration 070 is absent.
	client := &readinessClient{queryErr: errors.New("unknown identifier ref_sha256")}

	// When: the hosted runtime validates its ClickHouse dependency.
	err := checkClickHouseRuntime(context.Background(), func(context.Context) error { return nil }, client)

	// Then: the outward error is generic and does not disclose the database failure.
	if err == nil || err.Error() != "ClickHouse runtime catalog is unavailable" {
		t.Fatalf("checkClickHouseRuntime() error = %v, want generic catalog failure", err)
	}
}

func TestCheckClickHouseRuntime_failsWhenSchemaProbeRowsErrorOrCloseFails(t *testing.T) {
	for _, testCase := range []struct {
		name string
		rows *readinessRows
	}{
		{name: "iteration", rows: &readinessRows{err: errors.New("iteration failed")}},
		{name: "close", rows: &readinessRows{closeErr: errors.New("close failed")}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			// Given: the schema probe opened rows that fail during cleanup or iteration.
			client := &readinessClient{rows: testCase.rows}

			// When: the hosted runtime validates its ClickHouse dependency.
			err := checkClickHouseRuntime(context.Background(), func(context.Context) error { return nil }, client)

			// Then: readiness fails generically and still closes the rows.
			if err == nil || err.Error() != "ClickHouse runtime catalog is unavailable" {
				t.Fatalf("checkClickHouseRuntime() error = %v, want generic catalog failure", err)
			}
			if !testCase.rows.closed {
				t.Error("schema probe rows were not closed")
			}
		})
	}
}

type readinessClient struct {
	statement string
	queryErr  error
	rows      *readinessRows
}

func (c *readinessClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.statement = statement
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	return c.rows, nil
}

type readinessRows struct {
	err      error
	closeErr error
	closed   bool
}

func (r *readinessRows) Next() bool        { return false }
func (r *readinessRows) Scan(...any) error { return nil }
func (r *readinessRows) Err() error        { return r.err }
func (r *readinessRows) Close() error      { r.closed = true; return r.closeErr }
