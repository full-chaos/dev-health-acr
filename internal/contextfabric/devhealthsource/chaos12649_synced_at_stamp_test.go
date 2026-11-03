package devhealthsource

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12649: a table that pages on its rows' own last_synced stamps each entity
// of a kind it declares with that stamp, and the count check counts both sides per
// UTC day of it.

// A table stamps only if the walk pages it on its rows' own last_synced: the
// declared cursor expression must be that column, bare or under the table alias.
func TestOnlyTablesThatPageOnTheirOwnLastSyncedStamp(t *testing.T) {
	ownColumn := regexp.MustCompile(`^([a-z]+\.)?last_synced$`)
	stamped := 0
	for _, table := range entityTables {
		if !table.stampsSyncedAt {
			continue
		}
		stamped++
		cursor, ok := ingestCursorColumns[table.name]
		if !ok {
			t.Fatalf("%s stamps but declares no cursor column", table.name)
		}
		if !ownColumn.MatchString(cursor.expr) {
			t.Errorf("%s stamps its rows' sync time, but the walk pages it on %q, not its own last_synced", table.name, cursor.expr)
		}
		if len(table.subjectKinds) == 0 {
			t.Errorf("%s stamps but declares no entity kind", table.name)
		}
	}
	if stamped != 6 {
		t.Fatalf("%d tables stamp, want the 6 that page on their own last_synced", stamped)
	}
}

// The stamp is the row's walk position, written on a copy of the entity of a
// declared kind; an entity of another kind is not stamped.
func TestTheStampIsTheRowsWalkPosition(t *testing.T) {
	syncedAt := time.Date(2026, 10, 27, 8, 30, 0, 0, time.UTC)
	observedAt := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	owned := &contractsv1.ContextFabricEntityProjection{Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "ci_run:1"}}
	other := &contractsv1.ContextFabricEntityProjection{Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItemRef, CanonicalID: "work_item_ref:1"}}
	rows := []candidate{
		{observedAt: observedAt, cursorAt: syncedAt, entity: owned},
		{observedAt: observedAt, cursorAt: syncedAt, entity: other},
	}
	table := entityTable{name: "ci_pipeline_runs", stampsSyncedAt: true, subjectKinds: []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectCIRun}}
	stampSyncedAt(table, rows)
	stamp, ok := rows[0].entity.Properties[contextfabric.ProjectionSyncedAtProperty]
	if !ok || stamp.Integer == nil || *stamp.Integer != syncedAt.UnixNano() {
		t.Fatalf("stamp = %+v, want the row's sync time %d (not its observed time)", stamp, syncedAt.UnixNano())
	}
	if owned.Properties != nil {
		t.Fatal("the producer's entity was changed in place")
	}
	if _, ok := rows[1].entity.Properties[contextfabric.ProjectionSyncedAtProperty]; ok {
		t.Fatal("an entity of a kind the table does not declare was stamped")
	}
	unstamped := []candidate{{observedAt: observedAt, cursorAt: syncedAt, entity: owned}}
	stampSyncedAt(entityTable{name: "operational_incidents", subjectKinds: table.subjectKinds}, unstamped)
	if _, ok := unstamped[0].entity.Properties[contextfabric.ProjectionSyncedAtProperty]; ok {
		t.Fatal("a table that does not page on its own last_synced stamped its entity")
	}
}

// Every stamped kind counts its days over the same population as its total: the
// day statement holds the total's FROM/JOIN/WHERE and groups by the same key.
func TestEveryStampedKindCountsItsDaysOverItsTotalsPopulation(t *testing.T) {
	stampedKinds := map[contractsv1.ContextFabricSubjectKind]bool{}
	for _, table := range entityTables {
		if table.stampsSyncedAt {
			for _, kind := range table.subjectKinds {
				stampedKinds[kind] = true
			}
		}
	}
	seen := map[contractsv1.ContextFabricSubjectKind]bool{}
	for _, st := range clickHouseKindCountStatements {
		if !stampedKinds[st.kind] {
			if st.day != "" {
				t.Errorf("%s is not stamped but has a day statement", st.kind)
			}
			continue
		}
		seen[st.kind] = true
		from := st.sql[strings.Index(st.sql, ") FROM ")+2:]
		if st.day == "" || !strings.Contains(st.day, from+" GROUP BY ") {
			t.Errorf("%s day statement does not count the total's population:\ntotal: %s\nday:   %s", st.kind, st.sql, st.day)
		}
		// The projector's client throws past 1,000 rows: the newest days only.
		if !strings.HasSuffix(st.day, "ORDER BY day DESC LIMIT 900") || maxDayBuckets >= 1000 {
			t.Errorf("%s day statement is not bounded below the client's 1,000-row reply cap: %s", st.kind, st.day)
		}
	}
	for kind := range stampedKinds {
		if !seen[kind] {
			t.Errorf("stamped kind %s has no count statement", kind)
		}
	}
}
