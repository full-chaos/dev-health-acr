package devhealthfacts

// Test-only exports, so the EXTERNAL test package -- which owns the shared
// ClickHouse fixture -- can execute the statements this package builds instead
// of inspecting their text (codex r3 P3).
//
// This file is a _test.go file: nothing here ships, and the production package
// gains no exported surface.
var (
	// ProjectWorkItemSelectionSQLForTest is the project -> work_item selection
	// exactly as projectWorkItems sends it.
	ProjectWorkItemSelectionSQLForTest = projectWorkItemSelectionSQL
	// WorkItemAuthorizationExprSQLForTest is the pushed-down predicate.
	WorkItemAuthorizationExprSQLForTest = workItemAuthorizationExprSQL
	// WorkItemScopeSelectionColumnsForTest documents the column order the
	// scanner depends on.
	WorkItemScopeSelectionColumnsForTest = workItemScopeSelectionColumns
)
