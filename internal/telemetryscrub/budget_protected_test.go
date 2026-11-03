package telemetryscrub

import (
	"fmt"
	"reflect"
	"testing"
)

func TestProtectedStructFieldsCountTowardBudget(t *testing.T) {
	fields := make([]reflect.StructField, 0, 2*maxNodes)
	for i := 0; i < 2*maxNodes; i++ {
		fields = append(fields, reflect.StructField{Name: fmt.Sprintf("Token%d", i), Type: reflect.TypeOf("")})
	}
	v := reflect.New(reflect.StructOf(fields)).Elem()
	b := &budget{left: maxNodes}
	out, _ := tree(v, 0, b, nil).(map[string]any)
	if len(out) > maxNodes+1 {
		t.Errorf("struct with %d protected fields emitted %d entries, want <= %d", 2*maxNodes, len(out), maxNodes+1)
	}
	if _, ok := out["[truncated]"]; !ok {
		t.Errorf("no [truncated] marker; budget left %d", b.left)
	}
}

// Map keys are already charged by the key node in tree(); this pins that.
func TestProtectedMapKeysCountTowardBudget(t *testing.T) {
	m := map[string]any{}
	for i := 0; i < 2*maxNodes; i++ {
		m[fmt.Sprintf("token%d", i)] = "x"
	}
	b := &budget{left: maxNodes}
	out, _ := tree(reflect.ValueOf(m), 0, b, nil).(map[string]any)
	if len(out) > maxNodes+1 {
		t.Errorf("map with %d protected keys emitted %d entries, want <= %d", 2*maxNodes, len(out), maxNodes+1)
	}
	if _, ok := out["[truncated]"]; !ok {
		t.Errorf("no [truncated] marker; budget left %d", b.left)
	}
}
