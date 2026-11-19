package api

import (
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestDependencyErrorClassAttrsDropsUnsafeIdentifiers pins that only a plain
// lowercase identifier reaches the log as a table or constraint name.
func TestDependencyErrorClassAttrsDropsUnsafeIdentifiers(t *testing.T) {
	class := &storage.DependencyErrorClass{SQLState: "23505", Class: "unique_violation", Constraint: "bad\nname", Table: "Agent Episodes"}
	attrs := dependencyErrorClassAttrs(fmt.Errorf("wrap: %w", class))
	got := map[any]any{}
	for i := 0; i+1 < len(attrs); i += 2 {
		got[attrs[i]] = attrs[i+1]
	}
	if got["db_sqlstate"] != "23505" || got["db_error_class"] != "unique_violation" || len(got) != 2 {
		t.Fatalf("attrs = %v, want only db_sqlstate and db_error_class", attrs)
	}
	if attrs := dependencyErrorClassAttrs(nil); attrs != nil {
		t.Fatalf("nil error attrs = %v", attrs)
	}
	var typedNil *storage.DependencyErrorClass
	if attrs := dependencyErrorClassAttrs(fmt.Errorf("wrap: %w", typedNil)); attrs != nil {
		t.Fatalf("typed-nil class attrs = %v", attrs)
	}
}
