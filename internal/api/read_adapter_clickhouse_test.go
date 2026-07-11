package api

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type fixtureClickHouseClient struct {
	repoID, repository string
}

func (c fixtureClickHouseClient) Query(_ context.Context, query string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	switch {
	case query == contextpacket.RepositoryScopeQueryV1:
		return &fixtureClickHouseRows{values: [][]any{{c.repoID, c.repository, "main"}}}, nil
	case strings.Contains(query, "arrayExists(scope"):
		return &fixtureClickHouseRows{values: [][]any{{c.repoID, c.repository}}}, nil
	case strings.Contains(query, "FROM ci_pipeline_runs FINAL"):
		return &fixtureClickHouseRows{values: [][]any{fixtureEvidenceRow()}}, nil
	default:
		return &fixtureClickHouseRows{}, nil
	}
}

type fixtureClickHouseRows struct {
	values [][]any
	index  int
}

func (r *fixtureClickHouseRows) Next() bool { return r.index < len(r.values) }

func (r *fixtureClickHouseRows) Scan(destinations ...any) error {
	if r.index >= len(r.values) || len(destinations) != len(r.values[r.index]) {
		return errors.New("fixture ClickHouse scan mismatch")
	}
	for index, destination := range destinations {
		reflect.ValueOf(destination).Elem().Set(reflect.ValueOf(r.values[r.index][index]))
	}
	r.index++
	return nil
}

func (r *fixtureClickHouseRows) Err() error   { return nil }
func (r *fixtureClickHouseRows) Close() error { return nil }

func fixtureEvidenceRow() []any {
	return []any{
		"acr:v1:ci:run-4821", "dev_health", "ci_pipeline_run", "run-4821", "CI run-4821", "", "native", 1.0,
		"failure", time.Date(2026, 7, 10, 11, 0, 0, 0, time.UTC),
	}
}
