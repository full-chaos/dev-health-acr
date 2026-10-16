package devhealthsource

import (
	"strings"
	"testing"
)

// parentIDWriters is every writer of work_items.parent_id in ops main (the
// GitLab epic parent is stored in epic_id, providers/gitlab/normalize.py:781,
// and GitHub writes none), with a sample of the id it writes. A writer added in
// ops with a prefix this map does not know fails here until the prefix is
// mapped or the id is meant to resolve in the child's own repository.
var parentIDWriters = []struct {
	writer, sample string
}{
	{"ops providers/linear/normalize.py:370", "linear:CHAOS-12"},
	{"ops providersync/jira_work_items_rows.go:814", "jira:ABC-12"},
}

func TestEveryParentIDWriterPrefixIsMapped(t *testing.T) {
	for _, w := range parentIDWriters {
		if prefix, unmapped := unmappedParentPrefix(w.sample); unmapped {
			t.Errorf("%s writes %q: prefix %q is not in qualifiedParentPrefixes", w.writer, w.sample, prefix)
		}
	}
	for _, sample := range []string{"gitlab:grp:epic:3", "gh:acme/svc#1"} {
		if _, unmapped := unmappedParentPrefix(sample); !unmapped {
			t.Errorf("%q is a prefixed id the map does not know and must be reported unmapped", sample)
		}
	}
	for _, sample := range []string{"7", "ABC-12", ""} {
		if _, unmapped := unmappedParentPrefix(sample); unmapped {
			t.Errorf("%q has no integration prefix and must take the own-repository rule without a report", sample)
		}
	}
}

func TestParentKeySQLIsBuiltFromTheMap(t *testing.T) {
	q, empty := qualifiedParentSQL("c.parent_id"), emptyKeyParentSQL("c.parent_id")
	for _, prefix := range qualifiedParentPrefixes {
		if !strings.Contains(q, "startsWith(c.parent_id, '"+prefix+"')") || !strings.Contains(empty, "'"+prefix+"'") {
			t.Errorf("prefix %q is missing from the qualified or empty-key fragment:\n%s\n%s", prefix, q, empty)
		}
	}
	if strings.Contains(q, "gitlab:") {
		t.Errorf("gitlab has no parent_id writer and must not be mapped: %s", q)
	}
}
