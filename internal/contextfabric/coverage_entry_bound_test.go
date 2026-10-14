package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestCoverageEntryBoundCoversFourRowsPerFactKindAndTheGraphRows(t *testing.T) {
	const rowsPerKind, graphRows = 4, 5
	want := rowsPerKind*contractsv1.ContextFabricFactKindCount + graphRows
	if contractsv1.ContextFabricCoverageEntriesMaxCount < want {
		t.Fatalf("coverage entry bound = %d, want at least %d = %d rows per fact kind x %d kinds + %d graph rows: a grouped turn at the vocabulary maximum would shed rows",
			contractsv1.ContextFabricCoverageEntriesMaxCount, want, rowsPerKind, contractsv1.ContextFabricFactKindCount, graphRows)
	}
}
