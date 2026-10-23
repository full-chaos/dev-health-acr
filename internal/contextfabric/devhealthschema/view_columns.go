package devhealthschema

import "strings"

// ProjectMembershipPresenceViewColumnNames returns the view's output column
// names in order: the aliases of the first top-level SELECT list after the
// CTEs, read from ProjectMembershipPresenceViewDDL itself.
func ProjectMembershipPresenceViewColumnNames() []string {
	_, list, ok := strings.Cut(ProjectMembershipPresenceViewDDL, "\nSELECT\n")
	if !ok {
		panic("devhealthschema: the presence view DDL has no top-level SELECT list")
	}
	list, _, _ = strings.Cut(list, "\nFROM ")
	var names []string
	for _, line := range strings.Split(list, "\n") {
		item := strings.TrimSuffix(strings.TrimSpace(line), ",")
		if i := strings.LastIndex(item, " AS "); i >= 0 {
			item = item[i+4:]
		}
		names = append(names, item)
	}
	return names
}
