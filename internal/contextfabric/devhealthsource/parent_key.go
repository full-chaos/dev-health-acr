package devhealthsource

import (
	"fmt"
	"regexp"
	"strings"
)

// qualifiedParentPrefixes are the integration prefixes of work_items.parent_id
// whose key is unique in the provider's own namespace: "linear:<IDENT>"
// (ops providers/linear/normalize.py:370) and "jira:<KEY>" (ops
// providersync/jira_work_items_rows.go:814). A parent id with one of these
// prefixes names its parent org-wide: one candidate is the parent, several are
// no parent. Any other shape (no known prefix, bare digits) is a per-container
// number and resolves in the child's own repository only. A prefix is added
// here only when a writer of parent_id exists for it.
var qualifiedParentPrefixes = []string{"linear:", "jira:"}

// qualifiedParentSQL is true for a parent id that carries a qualified prefix
// and a key after it.
func qualifiedParentSQL(column string) string {
	terms := make([]string, 0, len(qualifiedParentPrefixes))
	for _, prefix := range qualifiedParentPrefixes {
		terms = append(terms, fmt.Sprintf("(startsWith(%s, '%s') AND length(%s) > %d)", column, prefix, column, len(prefix)))
	}
	return "(" + strings.Join(terms, " OR ") + ")"
}

// emptyKeyParentSQL is true for a qualified prefix with no key ("jira:", which
// the Jira writer stores for a parent object that has no key): no parent.
func emptyKeyParentSQL(column string) string {
	quoted := make([]string, 0, len(qualifiedParentPrefixes))
	for _, prefix := range qualifiedParentPrefixes {
		quoted = append(quoted, "'"+prefix+"'")
	}
	return column + " IN (" + strings.Join(quoted, ", ") + ")"
}

var parentPrefixShape = regexp.MustCompile(`^[a-z][a-z0-9_-]*:`)

// unmappedParentPrefix returns the integration prefix of a parent id that is
// prefixed but not in qualifiedParentPrefixes; such an id takes the own
// repository rule.
func unmappedParentPrefix(parentID string) (string, bool) {
	prefix := parentPrefixShape.FindString(parentID)
	if prefix == "" {
		return "", false
	}
	for _, known := range qualifiedParentPrefixes {
		if prefix == known {
			return "", false
		}
	}
	return prefix, true
}
