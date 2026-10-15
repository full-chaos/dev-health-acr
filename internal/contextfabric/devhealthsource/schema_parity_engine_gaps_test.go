package devhealthsource_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

// engineGap is one engine difference between the declaration and an
// ops-migrated ClickHouse that is known and ticketed. It matches only the
// exact (declared, live) pair: any other difference on the table, including
// one added on top of the known one, still fails the parity test.
type engineGap struct {
	declared string
	live     string
	ticket   string
}

func (g engineGap) matches(_, declared, live string) bool {
	return declared == g.declared && live == g.live
}

// knownEngineGaps is closed at two entries. Both close with CHAOS-9748 (the
// readers pick the current row per id, then the declaration takes the real
// engine and this map is deleted).
var knownEngineGaps = map[string]engineGap{
	"operational_incidents": {
		declared: "ReplacingMergeTree(source_version_at) ORDER BY (org_id, id) SETTINGS index_granularity = 8192",
		live:     "ReplacingMergeTree(ingest_revision) PRIMARY KEY (org_id, id) ORDER BY (org_id, id, source_revision, source_conflict_key) SETTINGS index_granularity = 8192",
		ticket:   "CHAOS-9748",
	},
	"operational_service_repository_mappings": {
		declared: "ReplacingMergeTree(source_version_at) ORDER BY (org_id, id) SETTINGS index_granularity = 8192",
		live:     "ReplacingMergeTree(ingest_revision) PRIMARY KEY (org_id, id) ORDER BY (org_id, id, source_revision, source_conflict_key) SETTINGS index_granularity = 8192",
		ticket:   "CHAOS-9748",
	},
}

func TestKnownEngineGapsStayClosedAtTwoNamedEntries(t *testing.T) {
	want := []string{"operational_incidents", "operational_service_repository_mappings"}
	if len(knownEngineGaps) != len(want) {
		t.Fatalf("knownEngineGaps has %d entries, want exactly %d (%v): a new gap needs its own ruling, not an entry", len(knownEngineGaps), len(want), want)
	}
	for _, table := range want {
		gap, ok := knownEngineGaps[table]
		if !ok {
			t.Fatalf("knownEngineGaps lacks %s", table)
		}
		if gap.ticket == "" {
			t.Errorf("%s: an allow-list entry names its ticket", table)
		}
		if declared := devhealthschema.EngineFull[table]; declared != gap.declared {
			t.Errorf("%s: the declaration is now %q, not the %q the entry covers -- delete the entry with the ticket", table, declared, gap.declared)
		}
	}
}

func TestKnownEngineGapMatchesOnlyTheExactDifference(t *testing.T) {
	gap := knownEngineGaps["operational_incidents"]
	declared := devhealthschema.EngineFull["operational_incidents"]
	if !gap.matches("operational_incidents", declared, gap.live) {
		t.Fatal("the exact known difference must be allowed")
	}
	planted := []string{
		strings.Replace(gap.live, "index_granularity = 8192", "index_granularity = 4096", 1),
		strings.Replace(gap.live, "ReplacingMergeTree(ingest_revision)", "MergeTree", 1),
		gap.live + ", deduplicate_merge_projection_mode = 'rebuild'",
		strings.Replace(gap.live, "source_conflict_key)", "source_conflict_key, extra_column)", 1),
	}
	for _, live := range planted {
		if live == gap.live {
			t.Fatalf("planted variant %q did not change the live string", live)
		}
		if gap.matches("operational_incidents", declared, live) {
			t.Errorf("an extra live difference %q was allowed", live)
		}
	}
	if gap.matches("operational_incidents", declared+" ", gap.live) {
		t.Error("a changed declaration was allowed")
	}
}
