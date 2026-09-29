package devhealthsource_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-7150: ops writes work_item_dependencies.relationship_type = 'relates'
// (its own spelling of the symmetric relation). The projector used to
// quarantine the uppercased RELATES as unknown_relationship_type and dropped
// the edge. RELATES is the same fact as RELATES_TO: it maps onto it, keeps its
// direction, and the edge carries the same work-item authorization.

func TestChaos7150RelatesMapsToRelatesToAndIsNotQuarantined(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 29, 4, 14, 1, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, spelling := range []string{"relates", "RELATES"} {
		t.Run(spelling, func(t *testing.T) {
			rows := [][]any{dependencyRow("WI-A", "WI-B", spelling, at, created)}
			batch, available, err, observations := projectWithQuarantineLog(t, dependencyTablesOnly(t, at, rows), testCursor(t, at.Add(-time.Hour), ""))
			if err != nil || !available {
				t.Fatalf("err=%v available=%v", err, available)
			}
			if len(observations) != 0 {
				t.Fatalf("RELATES must map, not quarantine: %+v", observations)
			}
			if len(batch.Relationships) != 1 {
				t.Fatalf("relationships = %d, want 1", len(batch.Relationships))
			}
			edge := batch.Relationships[0]
			if edge.Type != contractsv1.ContextFabricRelationshipRelatesTo {
				t.Fatalf("type = %q, want RELATES_TO", edge.Type)
			}
			if !strings.Contains(edge.From.CanonicalID, "WI-A") || !strings.Contains(edge.To.CanonicalID, "WI-B") {
				t.Fatalf("direction changed: %s -> %s", edge.From.CanonicalID, edge.To.CanonicalID)
			}
			// Same authorization as the RELATES_TO row for the same pair.
			native := [][]any{dependencyRow("WI-A", "WI-B", "relates_to", at, created)}
			nativeBatch, _, nativeErr, _ := projectWithQuarantineLog(t, dependencyTablesOnly(t, at, native), testCursor(t, at.Add(-time.Hour), ""))
			if nativeErr != nil || len(nativeBatch.Relationships) != 1 {
				t.Fatalf("native row: err=%v", nativeErr)
			}
			if fmt.Sprintf("%+v", edge.Authorization) != fmt.Sprintf("%+v", nativeBatch.Relationships[0].Authorization) {
				t.Fatalf("authorization differs from the RELATES_TO edge:\n%+v\n%+v", edge.Authorization, nativeBatch.Relationships[0].Authorization)
			}
			if edge.RelationshipID != nativeBatch.Relationships[0].RelationshipID {
				t.Fatalf("relates and relates_to must converge on one relationship id: %q vs %q", edge.RelationshipID, nativeBatch.Relationships[0].RelationshipID)
			}
		})
	}
}

// Both spellings of the same pair on one page are one fact: one edge, the
// redundant row dropped AND counted (never silent). The native row is the
// LOWERCASE 'relates_to' the venue ClickHouse actually holds (measured: the 7
// stale Jira 'relates' rows each have a live 'relates_to' twin), so this is
// the real shape, not a hand-picked spelling.
func TestChaos7150RelatesAndRelatesToOnOnePageCollapseAndCount(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 29, 4, 14, 1, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := [][]any{
		dependencyRow("WI-A", "WI-B", "relates", at, created),
		dependencyRow("WI-A", "WI-B", "relates_to", at.Add(time.Second), created),
	}
	batch, available, err, observations := projectWithQuarantineLog(t, dependencyTablesOnly(t, at, rows), testCursor(t, at.Add(-time.Hour), ""))
	if err != nil || !available {
		t.Fatalf("err=%v available=%v", err, available)
	}
	if len(batch.Relationships) != 1 {
		t.Fatalf("relationships = %d, want 1", len(batch.Relationships))
	}
	// The redundant edge and its healing tombstone are both counted.
	sawRelationship := false
	for _, entry := range observations {
		if fmt.Sprint(entry["quarantine_reason"]) != "duplicate_within_batch" {
			t.Fatalf("only duplicate_within_batch drops are expected: %+v", observations)
		}
		sawRelationship = sawRelationship || fmt.Sprint(entry["item_kind"]) == "relationship"
	}
	if !sawRelationship {
		t.Fatalf("the redundant relationship must be counted, never silent: %+v", observations)
	}
}

// An unresolved target (external key) takes the ref-form branch: RELATES maps
// there too.
func TestChaos7150RelatesMapsOnTheUnresolvedTargetBranch(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 29, 4, 14, 1, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := [][]any{unresolvedDependencyRow("WI-1", "EXT-1", "relates", at, created)}
	batch, available, err, observations := projectWithQuarantineLog(t, dependencyTablesOnly(t, at, rows), testCursor(t, at.Add(-time.Hour), ""))
	if err != nil || !available || len(observations) != 0 {
		t.Fatalf("err=%v available=%v observations=%+v", err, available, observations)
	}
	if len(batch.Relationships) != 1 || batch.Relationships[0].Type != contractsv1.ContextFabricRelationshipRelatesTo {
		t.Fatalf("relationships: %+v", batch.Relationships)
	}
	edge := batch.Relationships[0]
	// The id converges with the live lowercase relates_to row on this branch
	// too, and the evidence ref keeps the RAW source spelling.
	native := [][]any{unresolvedDependencyRow("WI-1", "EXT-1", "relates_to", at, created)}
	nativeBatch, _, nativeErr, _ := projectWithQuarantineLog(t, dependencyTablesOnly(t, at, native), testCursor(t, at.Add(-time.Hour), ""))
	if nativeErr != nil || len(nativeBatch.Relationships) != 1 {
		t.Fatalf("native row: err=%v", nativeErr)
	}
	if edge.RelationshipID != nativeBatch.Relationships[0].RelationshipID {
		t.Fatalf("unresolved branch: relates and relates_to must converge: %q vs %q", edge.RelationshipID, nativeBatch.Relationships[0].RelationshipID)
	}
	wantRef := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItemDependency, "repo-1:WI-1:EXT-1:relates")
	if len(edge.EvidenceRefIDs) != 1 || edge.EvidenceRefIDs[0] != wantRef {
		t.Fatalf("evidence ref must keep the raw spelling: got %v want %q", edge.EvidenceRefIDs, wantRef)
	}
}

// The quarantine stays loud for a genuinely unknown type: the alias is one
// entry, not an allowlist.
func TestChaos7150UnknownTypesStayQuarantinedAndCounted(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 29, 4, 14, 1, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := [][]any{
		dependencyRow("WI-A", "WI-B", "relates", at, created),
		dependencyRow("WI-C", "WI-D", "RELATES_WITH", at.Add(time.Second), created),
	}
	batch, _, err, observations := projectWithQuarantineLog(t, dependencyTablesOnly(t, at, rows), testCursor(t, at.Add(-time.Hour), ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Relationships) != 1 {
		t.Fatalf("relationships = %d, want only the mapped relates edge", len(batch.Relationships))
	}
	if len(observations) != 1 || fmt.Sprint(observations[0]["quarantine_reason"]) != "unknown_relationship_type" {
		t.Fatalf("RELATES_WITH must be quarantined loudly: %+v", observations)
	}
}
