package devhealthfacts

// Test-only exports, so the EXTERNAL test package -- which owns the shared
// ClickHouse fixture -- can execute the statements this package builds instead
// of inspecting their text (codex r3 P3).
//
// This file is a _test.go file: nothing here ships, and the production package
// gains no exported surface.
import (
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

var (
	// WorkItemScopeSelectionColumnsForTest documents the column order the
	// scanner depends on.
	WorkItemScopeSelectionColumnsForTest = workItemScopeSelectionColumns
)

// ProjectWorkItemSelectionForTest is the project -> work_item selection and
// its authorization bindings exactly as projectWorkItems builds them for
// principal.
func ProjectWorkItemSelectionForTest(principal storage.Principal, limit int) (string, []readers.Binding) {
	rendered := readers.WorkItemScopeSQL(workItemRepositoryAuthorization(principal, nil))
	return projectWorkItemSelectionSQL(rendered, limit), rendered.Bindings
}
