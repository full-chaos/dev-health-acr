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
	}
}

func TestIssueGateEdgeArmsUseTheKeyPrefixScope(t *testing.T) {
	scope := WorkGraphEdgeScopeSQL("")
	for name, statement := range map[string]string{"endpoint ids": WorkGraphIssueEndpointIDsSQL, "title map": workGraphIssueTitlesMapSQL} {
		if n := strings.Count(statement, scope); n != 1 {
			t.Errorf("issue gate %s carries the key-prefix edge scope %d times, want once", name, n)
		}
		if strings.Contains(strings.ReplaceAll(statement, scope, ""), "AND repo_id = {repo_id:UUID} AND source_type = 'issue'") || strings.Contains(strings.ReplaceAll(statement, scope, ""), "repo_id = {repo_id:UUID} AND target_type") {
			t.Errorf("issue gate %s filters an edge arm by a bare repo_id equality outside the scope", name)
		}
	}
	const seek = "source_type = 'issue' AND source_id IN (SELECT work_item_id FROM work_items WHERE org_id = {org_id:String} AND repo_id = {repo_id:UUID})"
	if !strings.Contains(WorkGraphIssueEndpointIDsSQL, seek) {
		t.Errorf("the issue-sourced arm does not seek by the repository's work item ids: %s", WorkGraphIssueEndpointIDsSQL)
	}
}
