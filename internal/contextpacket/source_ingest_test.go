package contextpacket

import "testing"

func TestEveryCatalogSourceDeclaresAnIngestBasisAndItsStatement(t *testing.T) {
	if len(SourceQueryCatalogV1) == 0 {
		t.Fatal("empty catalog: the scan measured nothing")
	}
	for _, query := range SourceQueryCatalogV1 {
		basis, declared := sourceIngestBasis[query.ID]
		if !declared {
			t.Errorf("%s declares no ingest basis: a new source must say whether it stores a sync time", query.ID)
			continue
		}
		_, hasStatement := sourceIngestStatements[query.ID]
		if (basis != IngestBasisNone) != hasStatement {
			t.Errorf("%s: basis %d and sync-time statement present=%v disagree", query.ID, basis, hasStatement)
		}
	}
	for id := range sourceIngestBasis {
		if catalogSourceQuery(id) == nil {
			t.Errorf("ingest basis names %s, which is not in the catalog", id)
		}
	}
	runStamps := 0
	for id, basis := range sourceIngestBasis {
		if basis == IngestBasisRunStamp {
			runStamps++
			if id != "repository_freshness.v1" {
				t.Errorf("%s claims a run stamp; only the repos row is rewritten on every repository sync", id)
			}
		}
	}
	if runStamps != 1 {
		t.Errorf("run-stamp sources = %d, want exactly repository_freshness.v1", runStamps)
	}
}
