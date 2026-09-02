package v1

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// answerBoundTable is the single source for both measured fixtures.
//
// It starts deliberately incomplete: TestEveryResultFieldIsInTheBoundTable
// enumerates ContextFabricInvestigationResult by reflection and fails naming
// every field not listed here. That is the point -- the table is not something
// a person remembers to update, it is something the build refuses to let them
// forget. A field added to the result struct tomorrow fails this test until
// somebody states what its smallest and largest legal values are.
func answerBoundTable() []answerBound {
	return []answerBound{}
}

// TestEveryResultFieldIsInTheBoundTable is the guard that makes an omission
// impossible rather than unlikely.
//
// The four defects this whole file replaces were omissions and inheritances --
// a field the fixture never set, or set from a fixture built for another
// purpose. Reading the fixture could not reveal them, because what was wrong
// was what the fixture did NOT say. Enumerating the struct can.
func TestEveryResultFieldIsInTheBoundTable(t *testing.T) {
	t.Parallel()

	listed := map[string]int{}
	for _, bound := range answerBoundTable() {
		listed[bound.Field]++
	}

	resultType := reflect.TypeOf(ContextFabricInvestigationResult{})
	var missing []string
	for i := 0; i < resultType.NumField(); i++ {
		name := resultType.Field(i).Name
		switch listed[name] {
		case 0:
			missing = append(missing, name)
		case 1:
		default:
			t.Errorf("%s appears %d times in the bound table; each field is stated once", name, listed[name])
		}
	}
	for name := range listed {
		if _, ok := resultType.FieldByName(name); !ok {
			t.Errorf("the bound table names %q, which is not a field of the result: the table has drifted from the struct", name)
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("%d of %d result fields are absent from the bound table:\n  %s\n\n"+
			"Every field must state its smallest and largest legal value, with the validator clause that fixes them. "+
			"A field with no bound to breach still gets an entry, with PastMax nil and the reason in Why -- an\n"+
			"exemption list is the shape this file exists to remove.",
			len(missing), resultType.NumField(), strings.Join(missing, "\n  "))
	}
}
