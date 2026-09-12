package v1

import (
	"strings"
	"testing"
	"time"
)

// THE PREDICATE AND THE VALIDATOR ARE ONE RULE, proven by execution over a
// domain rather than by reading that one calls the other: for every value,
// ValidContextFabricEntityAlias agrees with whether an otherwise-valid entity
// carrying that single alias passes Validate. A producer that screens with the
// predicate therefore can never emit an alias the validator rejects, and can
// never drop one it would have accepted.
func TestTheEntityAliasPredicateAgreesWithTheValidator(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"chaos-draw", "BILL", "full.chaos/chaos-ops",
		"", " ", " leading", "trailing ", "\ttab", "line\nbreak",
		"a|b", "|", strings.Repeat("a", ContextFabricEntityAliasMaxRunes),
		strings.Repeat("a", ContextFabricEntityAliasMaxRunes+1),
		strings.Repeat("é", ContextFabricEntityAliasMaxRunes),
		strings.Repeat("é", ContextFabricEntityAliasMaxRunes+1),
	} {
		entity := validEntityCarryingOneAlias(value)
		validatorAccepts := entity.Validate() == nil
		if got := ValidContextFabricEntityAlias(value); got != validatorAccepts {
			t.Errorf("value %q (%d runes): predicate=%v, validator accepts=%v -- the two must be one rule", preview(value), len([]rune(value)), got, validatorAccepts)
		}
	}
}

// THE BOUND IS RUNES, NOT BYTES. A multi-byte value at exactly the bound is
// admitted and one rune past it is not; a byte-counting rule would reject the
// first and a producer screening with it would drop a valid alias.
func TestTheEntityAliasBoundCountsRunes(t *testing.T) {
	t.Parallel()
	atBound := strings.Repeat("é", ContextFabricEntityAliasMaxRunes)
	if !ValidContextFabricEntityAlias(atBound) {
		t.Fatalf("a %d-rune multi-byte alias (%d bytes) was rejected -- the bound is in runes", ContextFabricEntityAliasMaxRunes, len(atBound))
	}
	if ValidContextFabricEntityAlias(atBound + "é") {
		t.Fatal("one rune past the bound was admitted")
	}
}

// validEntityCarryingOneAlias is an entity that passes Validate on every rule
// EXCEPT the one under test, so the alias is the only thing that can decide the
// outcome. The control below proves the base is valid; without it a fixture
// rejected for some other reason would make every comparison agree with a
// predicate that returns false, and the parity test would pass vacuously.
func validEntityCarryingOneAlias(alias string) ContextFabricEntityProjection {
	return ContextFabricEntityProjection{
		Subject:        ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_alias_parity", Label: "Parity"},
		Aliases:        []string{alias},
		Authorization:  ContextFabricAuthorizationScope{RepositorySlugs: []string{"team-a/repo"}},
		EvidenceRefIDs: []string{"evidence_parity_1234"},
		ObservedAt:     time.Unix(1, 0).UTC(),
		SourceVersion:  "ops-v1",
	}
}

// THE BASE ENTITY IS VALID, so the parity test above is comparing against the
// alias rule and nothing else.
func TestTheParityBaseEntityIsValid(t *testing.T) {
	t.Parallel()
	if err := validEntityCarryingOneAlias("chaos-draw").Validate(); err != nil {
		t.Fatalf("the parity base entity is itself invalid (%v) -- every comparison would agree with a rejecting predicate", err)
	}
}

func preview(value string) string {
	if len([]rune(value)) > 16 {
		return string([]rune(value)[:16]) + "…"
	}
	return value
}
