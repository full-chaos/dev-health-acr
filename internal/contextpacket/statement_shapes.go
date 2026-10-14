package contextpacket

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/columnprobe"
)

// statementShape is the older form of a catalog statement for a table that
// does not yet have the columns an ops migration adds. The catalog statement is
// the current form; the older one differs only in that the missing columns are
// selected as sentinel constants.
type statementShape struct {
	table   string
	columns []string
	replace map[string]string
	probe   *columnprobe.Probe
}

var statementShapes = map[string]*statementShape{
	"deployments.v1": newStatementShape("deployments", []string{"pull_request_tier"},
		map[string]string{deploymentTierExpression + " pull_request_tier": "'" + deploymentTierColumnAbsent + "' pull_request_tier"}),
}

func newStatementShape(table string, columns []string, replace map[string]string) *statementShape {
	return &statementShape{table: table, columns: columns, replace: replace, probe: columnprobe.New("catalog_"+table+"_columns", table, columns)}
}

// olderStatement is the statement for a table without the columns.
func (s *statementShape) olderStatement(current string) string {
	older := current
	for from, to := range s.replace {
		older = strings.Replace(older, from, to, 1)
	}
	return older
}

// resolveStatement returns the statement of query in the shape the table has,
// through the one shared column probe. A query with no declared shape, or a
// client that cannot probe, reads its catalog statement.
func resolveStatement(ctx context.Context, client ClickHouseQueryClient, query SourceQuery) string {
	shape, ok := statementShapes[query.ID]
	if !ok || client == nil {
		return query.Statement
	}
	if shape.probe.Available(ctx, func(ctx context.Context, statement string, names []string) (uint64, error) {
		rows, err := client.Query(ctx, statement, []ClickHouseBinding{{Name: "names", Value: names}})
		if err != nil {
			return 0, err
		}
		defer rows.Close()
		if !rows.Next() {
			return 0, fmt.Errorf("contextpacket: column probe returned no row: %w", rows.Err())
		}
		var count uint64
		if err := rows.Scan(&count); err != nil {
			return 0, err
		}
		return count, nil
	}, time.Now()) {
		return query.Statement
	}
	return shape.olderStatement(query.Statement)
}

// OlderStatement returns the statement of a catalog query for a table without
// the migration's columns, and whether the query has such a shape.
func OlderStatement(id string) (string, bool) {
	shape, ok := statementShapes[id]
	if !ok {
		return "", false
	}
	query := catalogSourceQuery(id)
	if query == nil {
		return "", false
	}
	return shape.olderStatement(query.Statement), true
}

// ResetStatementShapeProbesForTest forgets every cached probe answer, so a
// test that changes a table's shape is asked again.
func ResetStatementShapeProbesForTest() {
	for _, shape := range statementShapes {
		shape.probe = columnprobe.New("catalog_"+shape.table+"_columns", shape.table, shape.columns)
	}
}
