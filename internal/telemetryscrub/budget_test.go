package telemetryscrub

import (
	"fmt"
	"reflect"
	"testing"
)

func countNodes(v any) int {
	n := 1
	switch x := v.(type) {
	case map[string]any:
		for _, e := range x {
			n += countNodes(e)
		}
	case []any:
		for _, e := range x {
			n += countNodes(e)
		}
	}
	return n
}

func TestNodeBudgetBoundsCyclicAndWideValues(t *testing.T) {
	m := map[string]any{}
	for i := 0; i < 24; i++ {
		m[fmt.Sprintf("k%d", i)] = m
	}
	b := &budget{left: maxNodes}
	out := tree(reflect.ValueOf(m), 0, b)
	if got := countNodes(out); got > maxNodes+maxElements {
		t.Errorf("output nodes = %d, want <= %d", got, maxNodes+maxElements)
	}
	if b.left < 0 || b.left > maxNodes {
		t.Errorf("budget left = %d", b.left)
	}
	wide := make([]any, 10000)
	for i := range wide {
		wide[i] = map[string]any{"a": []any{1, 2, 3}}
	}
	b = &budget{left: maxNodes}
	if got := countNodes(tree(reflect.ValueOf(wide), 0, b)); got > maxNodes+maxElements {
		t.Errorf("wide output nodes = %d", got)
	}
	if b.left != 0 {
		t.Errorf("wide value left budget %d, want exhausted", b.left)
	}
}
