package devhealthsource_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Two dependency rows that share a watermark and whose ids hold ':' must not
// share a cursor key: the keyset predicate is a strict `>`, so a shared key
// drops the second row at a page boundary until a rebuild.
func TestDependencyCursorKeyIsInjectiveOverColonIDs(t *testing.T) {
	t.Parallel()
	first := devhealthsource.DependencyRowKeyForTest("repo-1", "a:b", "c", "RELATES_TO")
	second := devhealthsource.DependencyRowKeyForTest("repo-1", "a", "b:c", "RELATES_TO")
	if first == second {
		t.Fatalf("two distinct dependency rows share cursor key %q", first)
	}
	if want := identity.JoinSegments("repo-1", "a:b", "c", "RELATES_TO"); first != want {
		t.Fatalf("Go row key = %q, want the escaped join %q", first, want)
	}
}

func TestDependencyStatementPagesByTheEscapedRowKey(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tables := dependencyTablesOnly(t, at, [][]any{dependencyRow("WI-1", "WI-2", "RELATES_TO", at, created)})
	recorder := &statementRecordingClient{inner: &fakeClient{tables: tables}}
	source, err := devhealthsource.NewClickHouseProjectionSource(recorder)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	if _, _, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{
		OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: ingestSpaceCursorAfter(t, at, "after-key"),
	}); err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	var statement string
	for _, s := range recorder.statements {
		if strings.Contains(s, dependencyTable) {
			statement = s
		}
	}
	if statement == "" {
		t.Fatal("no statement read work_item_dependencies")
	}
	want := devhealthsource.RowKeySQLForTest("toString(w.repo_id)", "d.source_work_item_id", "d.target_work_item_id", "ifNull(d.relationship_type, 'related_to')")
	if devhealthsource.DependencyRowKeySQLForTest() != want {
		t.Fatalf("row key expression = %q, want rowKeySQL over (repo, source, target, type) = %q", devhealthsource.DependencyRowKeySQLForTest(), want)
	}
	if strings.Count(statement, want) != 2 {
		t.Fatalf("the keyset predicate and the ORDER BY must both use the escaped key, found %d uses:\n%s", strings.Count(statement, want), statement)
	}
}

// A page cut between two colliding-under-bare-concat rows resumes at the
// second row. The cursor After is the key the producer itself assigned to the
// first row; the row order and the predicate come from the escaped join.
func TestPageBoundaryBetweenColonIDRowsDoesNotSkipTheSecond(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := [][]any{
		dependencyRow("a:b", "c", "RELATES_TO", at, created),
		dependencyRow("a", "b:c", "RELATES_TO", at, created),
	}
	afterFirst := devhealthsource.DependencyRowKeyForTest("repo-1", "a:b", "c", "RELATES_TO")
	batch, available, err, _ := projectWithQuarantineLog(t, dependencyTablesOnly(t, at, rows), ingestSpaceCursorAfter(t, at, afterFirst))
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if !available {
		t.Fatal("the second row was skipped at the page boundary: no batch after the first row's key")
	}
	edges, sawSecondRow := 0, false
	for _, relationship := range batch.Relationships {
		if relationship.Type != contractsv1.ContextFabricRelationshipRelatesTo {
			continue
		}
		edges++
		if relationship.From.Label == "a" || relationship.To.Label == "a" {
			sawSecondRow = true
		}
	}
	if edges != 1 || !sawSecondRow {
		t.Fatalf("dependency edges after the boundary = %d (second row seen: %v), want exactly the second row's edge", edges, sawSecondRow)
	}
}

// ingestSpaceCursorAfter is a cursor in the current ingest position space; one
// without a space is read as a legacy cursor and re-read from the start.
func ingestSpaceCursorAfter(t *testing.T, since time.Time, after string) string {
	t.Helper()
	encoded, err := json.Marshal(struct {
		Since time.Time `json:"since"`
		After string    `json:"after"`
		Space string    `json:"space"`
	}{Since: since, After: after, Space: "ingest.v1"})
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}
