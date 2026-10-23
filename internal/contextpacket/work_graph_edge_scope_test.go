package contextpacket

import (
	"strings"
	"testing"
)

func TestWorkGraphReadsUseTheKeyPrefixEdgeScope(t *testing.T) {
	var catalog string
	for _, query := range SourceQueryCatalogV1 {
		if query.ID == "work_graph.v1" {
			catalog = query.Statement
		}
	}
	ingest := sourceIngestStatements["work_graph.v1"]
	if catalog == "" || ingest == "" {
		t.Fatal("measurement did not happen: work_graph.v1 statement or freshness read missing")
	}
	for name, c := range map[string]struct{ statement, qualifier string }{"statement": {catalog, "e."}, "freshness read": {ingest, ""}} {
		want := WorkGraphEdgeScopeSQL(c.qualifier)
		if !strings.Contains(c.statement, want) {
			t.Errorf("work_graph.v1 %s does not carry the key-prefix edge scope %q", name, want)
		}
		if strings.Contains(strings.ReplaceAll(c.statement, want, ""), c.qualifier+"repo_id = {repo_id:UUID}") {
			t.Errorf("work_graph.v1 %s also filters by a bare repo_id equality outside the scope", name)
		}
		for _, arm := range []string{
			"(" + c.qualifier + "source_type = 'pr' AND startsWith(" + c.qualifier + "source_id, concat(toString({repo_id:UUID}), '#pr')))",
			"(" + c.qualifier + "source_type = 'commit' AND startsWith(" + c.qualifier + "source_id, concat(toString({repo_id:UUID}), '@')))",
			c.qualifier + "source_type NOT IN ('pr', 'issue', 'commit')",
		} {
			if !strings.Contains(c.statement, arm) {
				t.Errorf("work_graph.v1 %s lacks the edge scope arm %q", name, arm)
			}
		}
		if got := strings.Count(c.statement, want); got != 1 {
			t.Errorf("work_graph.v1 %s carries the edge scope %d times, want exactly once (one shared predicate, no second copy)", name, got)
		}
	}
}

func TestIssueGateEdgeReadUsesTheKeyPrefixScope(t *testing.T) {
	scope := WorkGraphEdgeScopeSQL("")
	for name, statement := range map[string]string{"endpoint ids": WorkGraphIssueEndpointIDsSQL, "title map": workGraphIssueTitlesMapSQL} {
		if n := strings.Count(statement, scope); n != 1 {
			t.Errorf("issue gate %s carries the key-prefix edge scope %d times, want once", name, n)
		}
		outside := strings.ReplaceAll(statement, scope, "")
		if strings.Contains(outside, "repo_id = {repo_id:UUID} AND target_type") || strings.Contains(outside, "source_type = 'issue'") {
			t.Errorf("issue gate %s filters an edge read by a bare repo_id equality or an issue-sourced arm outside the scope", name)
		}
	}
	if strings.Contains(WorkGraphIssueEndpointIDsSQL, "work_items") || strings.Contains(WorkGraphIssueEndpointIDsSQL, "UNION") {
		t.Errorf("the issue gate edge read reads work_items or has a second arm, whose cost grows with the repository's work items: %s", WorkGraphIssueEndpointIDsSQL)
	}
}
