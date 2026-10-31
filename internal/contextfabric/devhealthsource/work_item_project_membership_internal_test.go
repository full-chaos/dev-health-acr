package devhealthsource

import (
	"reflect"
	"testing"
)

// CHAOS-12226: with work-item project membership off, the source's registry
// is entityTables itself -- the same tables, in the same order, with the same
// query functions -- so turning nothing on changes no statement and no page.
// With it on, the only differences are the work_items producer and one
// refresh table right after it.
func TestWorkItemProjectMembershipOffLeavesTheRegistryAsDeclared(t *testing.T) {
	t.Parallel()
	source, err := NewClickHouseProjectionSource(&statementRecorder{})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	off := source.tables()
	if len(off) != len(entityTables) || &off[0] != &entityTables[0] {
		t.Fatalf("membership off: tables() is not entityTables itself (len %d vs %d)", len(off), len(entityTables))
	}
	on := source.WithWorkItemProjectMembership(true).tables()
	if len(on) != len(entityTables)+1 {
		t.Fatalf("membership on: %d tables, want %d (one refresh table added)", len(on), len(entityTables)+1)
	}
	j := 0
	for _, declared := range entityTables {
		got := on[j]
		if got.name != declared.name || got.dimension != declared.dimension || got.carry != declared.carry || !reflect.DeepEqual(got.subjectKinds, declared.subjectKinds) {
			t.Fatalf("membership on: table %d = %q, want %q unchanged", j, got.name, declared.name)
		}
		sameQuery := reflect.ValueOf(got.query).Pointer() == reflect.ValueOf(declared.query).Pointer()
		if declared.name == "work_items" {
			if sameQuery || reflect.ValueOf(got.query).Pointer() != reflect.ValueOf(queryWorkItemsWithMembership).Pointer() {
				t.Fatal("membership on: work_items does not state membership")
			}
			j++
			if on[j].name != workItemProjectMembershipTable || reflect.ValueOf(on[j].query).Pointer() != reflect.ValueOf(queryWorkItemMembershipChanges).Pointer() {
				t.Fatalf("membership on: table after work_items = %q, want the refresh table", on[j].name)
			}
		} else if !sameQuery {
			t.Fatalf("membership on: %s's query changed; only work_items may", declared.name)
		}
		j++
	}
}
