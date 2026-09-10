package devhealthfacts_test

import (
	"sort"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

// TestObservationKeyDeclarationsPairUpAcrossTheRegistry is modelled on
// TestGeneratedDimensionFactKindRankingFamilyTableMatchesDoc: it enumerates
// every registered capability from the PRODUCER
// (devhealthfacts.NewProviders), never from a hand-written list of expected
// pairs, and proves the new per-(fact kind, subject kind) ObservationKey
// declaration is internally consistent across the WHOLE live registry, not
// just the five pairs this change hand-verified in isolation.
//
// It also proves every live capability still validates with its declared
// ObservationKey -- the same coverage TestEveryCapabilityValidatesWithIts
// DeclaredObligations already gives Obligations.
//
// It fails CI on exactly the mistake class a totality test for this field
// exists to catch: an ObservationKey declared on one side of a pairing
// without a matching declaration on the OTHER side, at the SAME subject
// kind -- a forgotten counterpart, or a typo in one arm's key string. A
// declared observation with no counterpart might as well have no entry at
// all: nothing else in the registry can ever agree with it, so the
// registered fact kind that carries it effectively "has no entry" for the
// pairing it claims to make (this field's own version of the dimension
// mapping test's "a registered FactKind is missing here").
func TestObservationKeyDeclarationsPairUpAcrossTheRegistry(t *testing.T) {
	providers := devhealthfacts.NewProviders(nil)
	if len(providers) == 0 {
		t.Fatal("registry is empty; this test would vacuously pass")
	}
	capabilities := make([]contextfabric.FactCapability, 0, len(providers))
	for _, provider := range providers {
		capabilities = append(capabilities, provider.Capability())
	}

	type cell struct {
		subject contextfabric.SubjectKind
		key     contextfabric.ObservationKey
	}
	// membership is built ENTIRELY from the live capabilities' declared
	// ObservationKey field, so a pairing this test's author did not
	// anticipate is still counted correctly -- the enumeration is the
	// registry, never a hand-written expectation table.
	membership := map[cell][]contextfabric.FactKind{}
	declaredEntries := 0
	for _, capability := range capabilities {
		if err := capability.Validate(); err != nil {
			t.Fatalf("capability %q does not validate: %v", capability.Kind, err)
		}
		for subject, keys := range capability.ObservationKey {
			for _, key := range keys {
				membership[cell{subject, key}] = append(membership[cell{subject, key}], capability.Kind)
				declaredEntries++
			}
		}
	}

	if declaredEntries == 0 {
		t.Fatal("no ObservationKey declarations found anywhere in the live registry -- this test would vacuously pass")
	}

	for c, kinds := range membership {
		if len(kinds) < 2 {
			sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
			t.Errorf("observation key %q at subject kind %q is declared by only %v -- every ObservationKey entry must be matched by at least one OTHER registered kind's identical declaration at the same subject kind, or it is an orphan (a forgotten counterpart, or a typo in one side's key string)", c.key, c.subject, kinds)
		}
	}
}
