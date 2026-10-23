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
