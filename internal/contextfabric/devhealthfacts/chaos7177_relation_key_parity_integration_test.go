package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/dependencyrelation"
)

// TestCHAOS7177RelationKeySQLAgreesWithGoAgainstRealClickHouse is the parity gate
// for the ONE canonical relation key. Facts compute dependencyrelation.Key in Go,
// the work_item_dependencies.v1 catalog builds the evidence id from
// dependencyrelation.KeySQL in ClickHouse, and the locator only resolves when the
// two are byte-identical. A spelling where they differ (Unicode case folding,
// Unicode whitespace) makes a fact cite an id the catalog never produces. This
// EXECUTES KeySQL in a real ClickHouse for every spelling and compares it with Key.
func TestCHAOS7177RelationKeySQLAgreesWithGoAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	_, direct := sharedClickHouseFixture(t)
	spellings := []string{
		"relates", "relates_to", " RELATES ", "RELATES", "Relates_To",
		"BLOCKED_BY", "is_blocked_by", " IS_BLOCKED_BY\t",
		"blocks", "BLOCKS",
		"requires", "Requires", "related_to",
		"", " ", "  ", " \t ", "\n",
		"ς", "σ", "Σ", // final sigma, sigma, capital sigma
		" requires ",         // NBSP-wrapped
		" relates ",          // em-space-wrapped alias
		"İstanbul", "K", "K", // dotted capital I, kelvin sign
		"blocked by", "relates to", // Jira raw spellings with a space
	}
	for _, raw := range spellings {
		var got string
		statement := "SELECT " + dependencyrelation.KeySQL("raw") + " FROM (SELECT ? AS raw)"
		if err := direct.QueryRow(ctx, statement, raw).Scan(&got); err != nil {
			t.Fatalf("KeySQL(%q) in ClickHouse: %v", raw, err)
		}
		if want := dependencyrelation.Key(raw); got != want {
			t.Errorf("Key/KeySQL disagree for %q: Go %q, ClickHouse %q", raw, want, got)
		}
		var blocker, inverse uint8
		predicateStatement := "SELECT toUInt8(" + dependencyrelation.BlockerPredicateSQL("raw") + "), toUInt8(" + dependencyrelation.BlockerInverseSQL("raw") + ") FROM (SELECT ? AS raw)"
		if err := direct.QueryRow(ctx, predicateStatement, raw).Scan(&blocker, &inverse); err != nil {
			t.Fatalf("BlockerPredicateSQL(%q) in ClickHouse: %v", raw, err)
		}
		key := dependencyrelation.Key(raw)
		if want := key == "blocks:fwd" || key == "blocks:inv"; (blocker == 1) != want {
			t.Errorf("BlockerPredicateSQL disagrees with Key for %q: predicate %d, key %q", raw, blocker, key)
		}
		if want := key == "blocks:inv"; (inverse == 1) != want {
			t.Errorf("BlockerInverseSQL disagrees with Key for %q: inverse %d, key %q", raw, inverse, key)
		}
	}
	// NULL relationship_type keys like the empty string.
	var got string
	nullStatement := "SELECT " + dependencyrelation.KeySQL("raw") + " FROM (SELECT CAST(NULL AS Nullable(String)) AS raw)"
	if err := direct.QueryRow(ctx, nullStatement).Scan(&got); err != nil {
		t.Fatalf("KeySQL(NULL) in ClickHouse: %v", err)
	}
	if want := dependencyrelation.Key(""); got != want {
		t.Errorf("Key/KeySQL disagree for NULL: Go %q, ClickHouse %q", want, got)
	}
}
