package devhealthfacts_test

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// findProvider locates the one provider registered for kind out of
// devhealthfacts.NewProviders' slice, so tests don't depend on that slice's
// element order.
func findProvider(t *testing.T, providers []contextfabric.FactProvider, kind contextfabric.FactKind) contextfabric.FactProvider {
	t.Helper()
	for _, provider := range providers {
		if provider.Capability().Kind == kind {
			return provider
		}
	}
	t.Fatalf("no provider registered for fact kind %q", kind)
	return nil
}

// fakeTable is one canned response a fakeClient.Query call can match against,
// mirroring the pattern devhealthsource's own tests use
// (internal/contextfabric/devhealthsource/clickhouse_test.go's fakeTable) --
// devhealthsource's version is unexported inside a _test.go file, so it
// can't be imported; this is a small equivalent for this package.
type fakeTable struct {
	match string
	rows  [][]any
	err   error
	// honorLimit cuts the canned rows at the statement's own trailing LIMIT,
	// as the server would.
	honorLimit bool
}

type capturedQuery struct {
	statement string
	bindings  []contextpacket.ClickHouseBinding
}

// pullRequestFallbackDecisionMarker is in the pull request project fallback
// decision statement and no other.
const pullRequestFallbackDecisionMarker = "argMaxIf(lk.provenance, lk.link_rank"

// linkedNativeCoverMarker is in the native cover statement of fallback links and no other.
const linkedNativeCoverMarker = "AND provenance = 'native'\n  AND (work_item_id IN {ids:Array(String)}"

const projectPopulationMarker = "-- project population"

// teamLinkCandidatesMarker is in the team link candidate statement and no other.
const teamLinkCandidatesMarker = "SELECT cp.work_item_id AS work_item_id FROM work_items AS cp WHERE"

type fakeClient struct {
	tables  []fakeTable
	queries []capturedQuery
}

func (c *fakeClient) Query(_ context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.queries = append(c.queries, capturedQuery{statement: statement, bindings: bindings})
	// A project population aggregate is answered only by a table that names
	// its marker; every other table's match also occurs inside its text.
	if strings.Contains(statement, projectPopulationMarker) {
		for _, table := range c.tables {
			if strings.Contains(table.match, projectPopulationMarker) {
				return &fakeScanner{rows: table.rowsFor(statement)}, nil
			}
		}
		return &fakeScanner{}, nil
	}
	// The team link candidates are answered only by a table that names their
	// marker; a test that did not seed them gets no row, and the link-row
	// statements after them are answered by their own tables.
	if strings.Contains(statement, teamLinkCandidatesMarker) {
		for _, table := range c.tables {
			if strings.Contains(table.match, teamLinkCandidatesMarker) {
				return &fakeScanner{rows: table.rows}, nil
			}
		}
		return &fakeScanner{}, nil
	}
	// A repository identity read also asks who owns the repositories. A test
	// that did not seed ownership gets "no owners", never a repos table row.
	if strings.Contains(statement, "owners_total") && !c.seedsOwnership() {
		return &fakeScanner{}, nil
	}
	// The counts by state of a team rollup read the same link rows the
	// window counts do, so the window-count canned rows must not answer them:
	// they get no rows unless a test seeds this marker itself.
	if strings.Contains(statement, "arraySort(groupArray((st, c)))") {
		for _, table := range c.tables {
			if strings.Contains(table.match, "arraySort(groupArray((st, c)))") {
				return &fakeScanner{rows: table.rows}, nil
			}
		}
		return &fakeScanner{}, nil
	}
	// The fallback-only decision of the pull request project rows below native
	// is read only when such a row exists; a test that did not seed it gets no
	// decision, so its rows below native keep no uncovered link.
	if strings.Contains(statement, pullRequestFallbackDecisionMarker) {
		for _, table := range c.tables {
			if strings.Contains(table.match, pullRequestFallbackDecisionMarker) {
				return &fakeScanner{rows: table.rows}, nil
			}
		}
		return &fakeScanner{}, nil
	}
	// The native cover of fallback links (one row of two arrays) is read only
	// when a fallback link exists; a test that did not seed it gets none covered.
	if strings.Contains(statement, linkedNativeCoverMarker) {
		for _, table := range c.tables {
			if strings.Contains(table.match, linkedNativeCoverMarker) {
				return &fakeScanner{rows: table.rows}, nil
			}
		}
		return &fakeScanner{rows: [][]any{{[]string{}, []string{}}}}, nil
	}
	for _, table := range c.tables {
		if strings.Contains(statement, table.match) {
			if table.err != nil {
				return nil, table.err
			}
			return &fakeScanner{rows: table.rowsFor(statement)}, nil
		}
	}
	return &fakeScanner{}, nil
}

func (t fakeTable) rowsFor(statement string) [][]any {
	if t.honorLimit {
		return limitedRows(statement, t.rows)
	}
	return t.rows
}

var trailingLimit = regexp.MustCompile(`\nLIMIT (\d+)\s*$`)

// limitedRows applies the statement's own trailing LIMIT to canned rows, as the
// server would, so a case that returns more rows than the statement admits
// reads only what the statement admits.
func limitedRows(statement string, rows [][]any) [][]any {
	if m := trailingLimit.FindStringSubmatch(statement); m != nil {
		if limit, err := strconv.Atoi(m[1]); err == nil && len(rows) > limit {
			return rows[:limit]
		}
	}
	return rows
}

func (c *fakeClient) seedsOwnership() bool {
	for _, table := range c.tables {
		if strings.Contains(table.match, "team_repo_ownership") {
			return true
		}
	}
	return false
}

// idsBinding returns the "ids" binding value of the last captured query, so
// a test can assert the query was scoped to exactly the subjects it asked
// about, never the whole organization.
func (c *fakeClient) idsBinding() []string {
	if len(c.queries) == 0 {
		return nil
	}
	last := c.queries[len(c.queries)-1]
	for _, binding := range last.bindings {
		if binding.Name == "ids" {
			if ids, ok := binding.Value.([]string); ok {
				return ids
			}
		}
	}
	return nil
}

func (c *fakeClient) orgIDBinding() string {
	if len(c.queries) == 0 {
		return ""
	}
	last := c.queries[len(c.queries)-1]
	for _, binding := range last.bindings {
		if binding.Name == "org_id" {
			if orgID, ok := binding.Value.(string); ok {
				return orgID
			}
		}
	}
	return ""
}

type fakeScanner struct {
	rows [][]any
	row  int
}

func (s *fakeScanner) Next() bool { return s.row < len(s.rows) }

func (s *fakeScanner) Scan(dest ...any) error {
	row := s.rows[s.row]
	for index, target := range dest {
		switch value := target.(type) {
		case *string:
			*value = row[index].(string)
		case *int64:
			*value = row[index].(int64)
		case *uint32:
			*value = row[index].(uint32)
		case *uint64:
			*value = row[index].(uint64)
		case *uint8:
			if index >= len(row) {
				// queryTeamScopeRows scans two trailing flags after the columns every canned row holds: is_scope_row
				// and is_org_row. A canned row without them is a key that holds only legacy rows, which answers both
				// questions with its one row (CHAOS-8863).
				*value = 1
				continue
			}
			*value = row[index].(uint8)
		case *time.Time:
			*value = row[index].(time.Time)
		case *float64:
			if n, ok := row[index].(int64); ok {
				*value = float64(n)
			} else {
				*value = row[index].(float64)
			}
		case **float64:
			if row[index] == nil {
				*value = nil
			} else {
				v := row[index].(float64)
				*value = &v
			}
		case *[]string:
			*value = row[index].([]string)
		case *[]int64:
			*value = row[index].([]int64)
		case *[]uint64:
			*value = row[index].([]uint64)
		case *[][]string:
			*value = row[index].([][]string)
		case *[]time.Time:
			*value = row[index].([]time.Time)
		case *[]float64:
			*value = row[index].([]float64)
		case *[]uint8:
			*value = row[index].([]uint8)
		case *map[string]float64:
			*value = row[index].(map[string]float64)
		default:
			return errors.New("devhealthfacts_test: unsupported scan destination")
		}
	}
	s.row++
	return nil
}

func (s *fakeScanner) Err() error   { return nil }
func (s *fakeScanner) Close() error { return nil }

// assertQueryScopedToOrgAndSubjects is the guard-sensitive structural check
// AC-3780-5 needs: it fails not only if the org_id/ids binding values are
// wrong, but if the SQL statement itself stops filtering by them -- e.g. if
// a future edit deletes "WHERE ... org_id = {org_id:String}" while still
// passing the org_id binding through clickhouseFacts.query, the org_id
// binding assertions alone would keep passing (the binding is always sent
// regardless of whether the statement uses it) even though the guard is
// gone. Checking the statement text closes that gap.
func assertQueryScopedToOrgAndSubjects(t *testing.T, statement string) {
	t.Helper()
	if !strings.Contains(statement, "org_id = {org_id:String}") {
		t.Fatalf("statement = %q, want it to filter by org_id = {org_id:String}", statement)
	}
	if !strings.Contains(statement, "IN {ids:Array(String)}") {
		t.Fatalf("statement = %q, want it to filter by ids IN {ids:Array(String)}", statement)
	}
}
