package devhealthfacts_test

import (
	"fmt"
	"slices"
	"sort"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

// THE ENUMERATED SURFACE of the observation-key declaration, printed and
// asserted from the PRODUCER -- devhealthfacts.NewProviders' own capability
// list -- never from a list written beside it.
//
// The prompt of record's ENUMERATE-THE-SURFACE clause requires every element of
// the input surface a change touches to be exercised, never a sample. For this
// change the surface is: every registered fact kind, crossed with every subject
// kind that kind actually serves. That is what this walks, and it FAILS when a
// cell is uncovered rather than skipping it -- an instrument that enumerates
// only what its author thought of proves nothing about what shipped.
func TestTheObservationKeySurfaceIsEnumeratedFromTheRegistry(t *testing.T) {
	t.Parallel()
	providers := devhealthfacts.NewProviders(nil)
	if len(providers) == 0 {
		t.Fatal("no providers: the enumeration below would be vacuously true")
	}

	type cell struct {
		kind    contextfabric.FactKind
		subject contextfabric.SubjectKind
		keys    []contextfabric.ObservationKey
	}
	var cells []cell
	keyed, unkeyed := 0, 0
	everyKey := map[contextfabric.ObservationKey]int{}

	for _, provider := range providers {
		capability := provider.Capability()
		if len(capability.SupportedSubjectKinds) == 0 {
			t.Fatalf("%s declares no supported subject kinds; its observation-key surface is undefined", capability.Kind)
		}
		for _, subject := range capability.SupportedSubjectKinds {
			keys := capability.ObservationKey[subject]
			cells = append(cells, cell{capability.Kind, subject, keys})
			if len(keys) == 0 {
				unkeyed++
				continue
			}
			keyed++
			for _, key := range keys {
				if key == "" {
					t.Fatalf("%s@%s declares an empty observation key", capability.Kind, subject)
				}
				everyKey[key]++
			}
		}
	}

	sort.Slice(cells, func(a, b int) bool {
		if cells[a].kind != cells[b].kind {
			return cells[a].kind < cells[b].kind
		}
		return cells[a].subject < cells[b].subject
	})
	for _, c := range cells {
		t.Logf("SURFACE %-28s @ %-12s keys=%v", c.kind, c.subject, c.keys)
	}
	t.Logf("SURFACE TOTAL cells=%d keyed=%d unkeyed=%d distinct_keys=%d", len(cells), keyed, unkeyed, len(everyKey))

	// EVERY DECLARED KEY IS SHARED. A key naming exactly one (kind, subject)
	// cell declares that cell to be one observation with nothing -- which is
	// what an UNKEYED cell already means, so the key is either a typo or a
	// half-finished pairing. This is the assertion a hand list cannot make.
	for key, count := range everyKey {
		if count < 2 {
			t.Errorf("observation key %q names only %d cell; a key that pairs with nothing is either a typo or half a pairing -- leave the cell unkeyed instead", key, count)
		}
	}

	// THE SURFACE IS NON-TRIVIAL in both directions. Without this the loops
	// above pass against a registry that declares nothing at all, and against
	// one that keys every cell.
	if keyed == 0 {
		t.Fatal("no cell declares an observation key; every assertion above is vacuous")
	}
	if unkeyed == 0 {
		t.Fatal("every cell declares a key; the unkeyed-is-a-singleton path is unexercised by the real registry")
	}
	if len(everyKey) < 2 {
		t.Fatalf("only %d distinct key in the whole registry; the cover cannot be exercised above 1", len(everyKey))
	}
}

// TestEveryDeclaredObservationKeyCollapsesItsOwnCellsAndNothingElse exercises
// the COVER over every declared key's real cells, from the registry -- so the
// arithmetic is checked against what shipped, not against a fixture.
//
// For each key: the cells that declare it must cover to 1 (they are one
// observation, by declaration), and adding any cell that does NOT declare it
// must raise the cover. The second half is the discriminating control: without
// it the first half passes against a cover that always returns 1.
func TestEveryDeclaredObservationKeyCollapsesItsOwnCellsAndNothingElse(t *testing.T) {
	t.Parallel()
	providers := devhealthfacts.NewProviders(nil)

	// THIS TEST PREVIOUSLY DID NOT CALL THE COVER AT ALL. It grouped the
	// declarations, logged them, and asserted only that groups existed --
	// so mutating observationCover to return a constant 1 left it green while
	// its name promised the collapse was exercised against the real registry.
	// A test that cannot fail on the property it names is worse than no test,
	// because its name is cited as coverage. It now calls the real counter, via
	// the exported ObservationCoverForTest seam, on cells taken from the
	// registry rather than from a fixture.
	type group struct {
		subject contextfabric.SubjectKind
		key     contextfabric.ObservationKey
		kinds   []contextfabric.FactKind
	}
	groups := map[string]*group{}
	assignment := map[contextfabric.FactKind]map[contextfabric.SubjectKind][]contextfabric.ObservationKey{}
	for _, provider := range providers {
		capability := provider.Capability()
		assignment[capability.Kind] = capability.ObservationKey
		for _, subject := range capability.SupportedSubjectKinds {
			for _, key := range capability.ObservationKey[subject] {
				id := fmt.Sprintf("%s|%s", subject, key)
				if groups[id] == nil {
					groups[id] = &group{subject: subject, key: key}
				}
				groups[id].kinds = append(groups[id].kinds, capability.Kind)
			}
		}
	}
	if len(groups) == 0 {
		t.Fatal("no (subject, key) group found; nothing is exercised")
	}

	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	exercised := 0
	for _, id := range ids {
		g := groups[id]
		if len(g.kinds) < 2 {
			continue // reported by the surface test above
		}
		// THE COLLAPSE, EXECUTED: every kind declaring one key is ONE
		// observation, so the cover over exactly those kinds must be 1.
		if got := contextfabric.ObservationCoverForTest(g.kinds, g.subject, assignment); got != 1 {
			t.Errorf("%s: cover over %v = %d, want 1 -- they all declare this one key", id, g.kinds, got)
		}
		exercised++

		// THE DISCRIMINATING CONTROL: adding a kind from the SAME subject kind
		// that does NOT declare this key must raise the cover. Without it the
		// assertion above passes against a counter that always returns 1 --
		// which is exactly the mutation this test used to survive.
		for _, provider := range providers {
			other := provider.Capability()
			if len(other.ObservationKey[g.subject]) != 0 {
				continue // keyed at this subject kind; may legitimately collapse
			}
			if !slices.Contains(other.SupportedSubjectKinds, g.subject) {
				continue
			}
			widened := append(append([]contextfabric.FactKind{}, g.kinds...), other.Kind)
			if got := contextfabric.ObservationCoverForTest(widened, g.subject, assignment); got != 2 {
				t.Errorf("%s: adding unkeyed %s gave cover %d, want 2 -- an unkeyed kind is its own observation", id, other.Kind, got)
			}
			break
		}
	}
	if exercised == 0 {
		t.Fatal("no (subject, key) group had two or more cells; the collapse is never exercised against the real registry")
	}
	t.Logf("EXERCISED %d shared-observation groups from the registry, cover executed on each", exercised)
}

// TestNoTwoKindsPairThroughSharedAncestryAlone is the pin for the defect the
// surface enumeration above found: a single key name serving two DIFFERENT
// one-hop relations declares a third pairing by transitivity.
//
// The five declared pairings, from the design's own table, are:
//
//	health + metrics                  @ repository
//	health + operational_deficiencies @ team
//	flow   + workload                 @ team, project
//	flow   + operational_deficiencies @ team
//	metrics+ operational_deficiencies @ team
//
// Every OTHER (kind, kind, subject) triple must NOT collapse. The pair this
// test exists for is workload + operational_deficiencies at team: both descend
// from work_item_metrics_daily, neither is built from the other, so they are
// two observations. Before the key split they covered to 1.
func TestNoTwoKindsPairThroughSharedAncestryAlone(t *testing.T) {
	t.Parallel()
	providers := devhealthfacts.NewProviders(nil)

	keys := map[contextfabric.FactKind]map[contextfabric.SubjectKind][]contextfabric.ObservationKey{}
	served := map[contextfabric.SubjectKind][]contextfabric.FactKind{}
	for _, provider := range providers {
		capability := provider.Capability()
		keys[capability.Kind] = capability.ObservationKey
		for _, subject := range capability.SupportedSubjectKinds {
			served[subject] = append(served[subject], capability.Kind)
		}
	}

	// The declared pairings, by (subject, kind, kind). Written out because it
	// is the DESIGN's table, not a copy of the code under test -- if the code
	// declares a pairing that is not here, that is the finding.
	declared := map[string]bool{
		"repository|health|metrics":             true,
		"team|health|operational_deficiencies":  true,
		"team|flow|workload":                    true,
		"project|flow|workload":                 true,
		"team|flow|operational_deficiencies":    true,
		"team|metrics|operational_deficiencies": true,
	}
	pairKey := func(subject contextfabric.SubjectKind, a, b contextfabric.FactKind) string {
		first, second := string(a), string(b)
		if second < first {
			first, second = second, first
		}
		return fmt.Sprintf("%s|%s|%s", subject, first, second)
	}

	checked, collapsed := 0, 0
	for subject, kinds := range served {
		sort.Slice(kinds, func(a, b int) bool { return kinds[a] < kinds[b] })
		for i := 0; i < len(kinds); i++ {
			for j := i + 1; j < len(kinds); j++ {
				a, b := kinds[i], kinds[j]
				aKeys, bKeys := keys[a][subject], keys[b][subject]
				shares := false
				for _, ak := range aKeys {
					for _, bk := range bKeys {
						if ak == bk {
							shares = true
						}
					}
				}
				checked++
				id := pairKey(subject, a, b)
				if shares {
					collapsed++
					if !declared[id] {
						t.Errorf("UNDECLARED PAIRING %s: these two kinds share an observation key but the design declares no one-hop built-from relation between them. A key name serving two different relations declares a third by transitivity -- give each relation its own key.", id)
					}
					continue
				}
				if declared[id] {
					t.Errorf("MISSING PAIRING %s: the design declares a one-hop relation but no shared key encodes it", id)
				}
			}
		}
	}
	t.Logf("checked %d (subject, kind, kind) pairs, %d collapse", checked, collapsed)
	if collapsed == 0 {
		t.Fatal("no pair collapses; every assertion above is vacuous")
	}
	if len(declared) != collapsed {
		t.Errorf("the design declares %d pairings but %d collapse in the registry", len(declared), collapsed)
	}
}
