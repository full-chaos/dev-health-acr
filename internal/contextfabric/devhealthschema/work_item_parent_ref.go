package devhealthschema

// WorkItemParentRefSQL is the id a work item row names as its parent: its
// parent_id, or its epic_id when parent_id is empty. One row names one parent;
// when a writer fills both, the parent_id wins. alias is the table alias of the
// work_items row, or empty for an unqualified column list.
func WorkItemParentRefSQL(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return "if(" + prefix + "parent_id != '', " + prefix + "parent_id, " + prefix + "epic_id)"
}
