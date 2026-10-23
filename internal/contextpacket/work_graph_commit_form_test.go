package contextpacket

import (
	"strings"
	"testing"
)

func workGraphCatalogStatement(t *testing.T) string {
	t.Helper()
	for _, query := range SourceQueryCatalogV1 {
		if query.ID == "work_graph.v1" {
			return query.Statement
		}
	}
	t.Fatal("measurement did not happen: work_graph.v1 is not in the catalog")
	return ""
}

// A writer names a commit "<repo_id>@<sha>" (ops ids.go GenerateCommitID), so a
// commit_sha filter that compares only the bare sha never matches one.
func TestWorkGraphCommitShaFilterMatchesTheWriterForm(t *testing.T) {
	statement := workGraphCatalogStatement(t)
	for _, side := range []string{"e.source_id", "e.target_id"} {
		for _, form := range []string{
			side + " = {commit_sha:String}",
			side + " = concat(toString({repo_id:UUID}), '@', {commit_sha:String})",
		} {
			if !strings.Contains(statement, form) {
				t.Errorf("work_graph.v1 commit_sha filter lacks %q", form)
			}
		}
	}
}

// An endpoint id of the writers' commit form is named by the commit alone: the
// repository id inside it is an internal identifier and never reaches a label.
func TestWorkGraphCommitEndpointLabelDropsTheRepositoryId(t *testing.T) {
	label := workGraphEndpointLabelSQL("e.source_id", "e.source_type", "ps")
	for _, want := range []string{
		"match(e.source_id, '^[0-9a-fA-F-]{36}@[0-9a-fA-F]+$'), concat('commit ', substring(splitByString('@', e.source_id)[2], 1, 8))",
	} {
		if !strings.Contains(label, want) {
			t.Errorf("endpoint label lacks the commit branch %q", want)
		}
	}
	statement := workGraphCatalogStatement(t)
	if !strings.Contains(statement, label) {
		t.Errorf("work_graph.v1 does not label its source endpoint with workGraphEndpointLabelSQL")
	}
}
