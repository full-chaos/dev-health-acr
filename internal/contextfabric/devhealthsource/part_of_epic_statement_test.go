package devhealthsource_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// The fake client does not evaluate joins, so the data-level proof of the epic
// parent is TestEdgeRetractionOnRealStores. This pins the statement: the
// parent reference is the one expression everywhere the hierarchy read names a
// parent, and the child's parent_id column is read nowhere else. A read of
// parent_id alone would leave every epic and Epic Link without an edge.
func TestHierarchyReadNamesTheParentByTheParentReference(t *testing.T) {
	t.Parallel()
	recorder := &statementRecordingClient{inner: &fakeClient{tables: baseTables(zeroTime)}}
	source, err := devhealthsource.NewClickHouseProjectionSource(recorder)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	if _, _, err := source.NextProjectionBatch(context.Background(),
		contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName}); err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	var statement string
	for _, s := range recorder.statements {
		if strings.Contains(s, "FROM work_items AS c FINAL") && strings.Contains(s, "AS pa ON") {
			statement = s
			break
		}
	}
	if statement == "" {
		t.Fatal("no statement queried the work item hierarchy")
	}
	ref := devhealthschema.WorkItemParentRefSQL("c")
	for _, want := range []string{
		"SELECT c.work_item_id, " + ref + ",",
		"p.work_item_id = " + ref + " AND",
		"pa.parent_key = " + ref,
		"startsWith(" + ref + ", 'gitlab:')",
		ref + " IN ('linear:'",
	} {
		if !strings.Contains(statement, want) {
			t.Errorf("the hierarchy read does not contain %q:\n%s", want, statement)
		}
	}
	if rest := strings.ReplaceAll(statement, ref, "<parent reference>"); strings.Contains(rest, "c.parent_id") || strings.Contains(rest, "c.epic_id") {
		t.Errorf("the hierarchy read names parent_id or epic_id outside the parent reference:\n%s", rest)
	}
}
