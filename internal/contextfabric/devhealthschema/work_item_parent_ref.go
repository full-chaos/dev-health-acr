package devhealthschema

// WorkItemParentRefSQL is the id a work item row names as its parent: its
// parent_id, or its epic_id when parent_id is empty. One row names one parent;
// when a writer fills both, the parent_id wins. alias is the table alias of the
// work_items row.
func WorkItemParentRefSQL(alias string) string {
	return "if(" + alias + ".parent_id != '', " + alias + ".parent_id, " + alias + ".epic_id)"
}
