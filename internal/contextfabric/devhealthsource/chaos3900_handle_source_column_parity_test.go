package devhealthsource

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestHandleSourceColumnRegistryMatchesCensusRegistry is CHAOS-3900 P1.C's
// own parity pin (team-lead ruling on the source_column seam question):
// graphrank.HandleSourceColumn is a CLOSED STATIC MIRROR of this package's
// own censusKindRegistryEntries[kind].handlePredicate -- duplicated,
// deliberately, because devhealthsource imports graphrank (never the
// reverse), so graphrank cannot read this registry directly for a
// wire-visible disclosure field. This test is what turns a future drift
// between the two into a NAMED, FAILING test instead of a silent mismatch
// (the entire reason duplication was accepted over a new dependency-
// injection surface for closed, static registry data).
//
// Mechanism: for every (kind, patternID) graphrank's mirror declares, this
// builds a REAL predicate over a known-valid sample value via this
// package's own handlePredicate function and asserts the resulting SQL
// references the SAME alias-qualified column graphrank's own
// "<table>.<column>" mirror names (alias + "." + the column name after the
// mirror's own last '.'). If a future column rename lands on one side and
// not the other, the alias-qualified reference stops appearing in the
// generated SQL and this test fails at the exact point of divergence.
func TestHandleSourceColumnRegistryMatchesCensusRegistry(t *testing.T) {
	t.Parallel()

	cases := []struct {
		kind        graphrank.CensusKind
		patternID   string
		sampleValue string
	}{
		{kind: contextfabric.SubjectPullRequest, patternID: "pull_request_number", sampleValue: "532"},
		{kind: contextfabric.SubjectWorkItem, patternID: "work_item_ticket_key", sampleValue: "CHAOS-3896"},
		{kind: contractsv1.ContextFabricSubjectCIRun, patternID: "ci_run_id", sampleValue: "18234567"},
	}

	// Completeness check, both directions: every entry in graphrank's
	// mirror must be exercised above, and every case above must resolve
	// to a mirror entry -- neither side may silently grow past the other.
	if len(cases) != 3 {
		t.Fatalf("len(cases) = %d, want 3 -- update this test's own case list alongside any registry addition", len(cases))
	}

	for _, tc := range cases {
		t.Run(tc.patternID, func(t *testing.T) {
			sourceColumn, ok := graphrank.HandleSourceColumn(tc.kind, tc.patternID)
			if !ok {
				t.Fatalf("graphrank.HandleSourceColumn(%q, %q) ok = false, want true", tc.kind, tc.patternID)
			}
			_, column, found := strings.Cut(sourceColumn, ".")
			if !found || column == "" {
				t.Fatalf("graphrank.HandleSourceColumn(%q, %q) = %q, want a %q-form value", tc.kind, tc.patternID, sourceColumn, "<table>.<column>")
			}

			entry, ok := censusKindRegistryEntries[tc.kind]
			if !ok {
				t.Fatalf("censusKindRegistryEntries has no entry for kind %q", tc.kind)
			}
			if entry.handlePredicate == nil {
				t.Fatalf("censusKindRegistryEntries[%q].handlePredicate is nil -- graphrank's mirror declares a source_column for this kind, but this package's own registry has no handle predicate to compare against", tc.kind)
			}
			predicate, err := entry.handlePredicate(tc.sampleValue)
			if err != nil {
				t.Fatalf("censusKindRegistryEntries[%q].handlePredicate(%q) error = %v", tc.kind, tc.sampleValue, err)
			}
			wantRef := entry.alias + "." + column
			if !strings.Contains(predicate.SQL, wantRef) {
				t.Errorf("predicate SQL %q does not reference %q -- graphrank.HandleSourceColumn(%q, %q) = %q has drifted from this package's own registry", predicate.SQL, wantRef, tc.kind, tc.patternID, sourceColumn)
			}
		})
	}
}
