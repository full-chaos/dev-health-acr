package contextfabric_test

// The generated obligation seed, its checked-in snapshot, and the N2
// parity property. Design §13.15.2 (the seed evidence obligation),
// §13.11a O9.

import (
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

var updateSeed = flag.Bool("update-seed", false, "rewrite the checked-in obligation seed snapshot from the live registry")

const seedSnapshotPath = "testdata/obligation_seed.txt"

func liveCapabilityList(t *testing.T) []contextfabric.FactCapability {
	t.Helper()
	providers := devhealthfacts.NewProviders(nil)
	capabilities := make([]contextfabric.FactCapability, 0, len(providers))
	for _, provider := range providers {
		capabilities = append(capabilities, provider.Capability())
	}
	return capabilities
}

// TestEveryRegisteredProviderDeclaresItsObligations is the registry test
// the declaration table's doc comment promises.
//
// It is a COVERAGE assertion, not a correctness one: it cannot know
// whether `health` should serve `state`, only that somebody decided. A new
// provider added without a decision falls into factKindObligations'
// default arm and silently declares nothing, which would look exactly like
// a deliberate substrate declaration. The two are distinguished by
// requiring the kind to be NAMED in the switch, which is what the explicit
// nil arms for membership and source_health are for -- so this test walks
// the vocabulary and the switch must have an arm for every registered
// kind, empty or not.
func TestEveryRegisteredProviderDeclaresItsObligations(t *testing.T) {
	capabilities := liveCapabilityList(t)
	if len(capabilities) == 0 {
		t.Fatal("registry is empty; the trace and the seed would both be vacuous")
	}

	// The substrate declarations, named here so that a provider silently
	// falling through to the default arm is a FAILURE rather than being
	// indistinguishable from a deliberate empty.
	declaredSubstrate := map[contextfabric.FactKind]bool{
		contextfabric.FactMembership:   true,
		contextfabric.FactSourceHealth: true,
	}

	for _, capability := range capabilities {
		if len(capability.Obligations) > 0 {
			continue
		}
		if !declaredSubstrate[capability.Kind] {
			t.Errorf("registered fact kind %q declares NO obligations and is not one of the named substrate producers: "+
				"either give it an arm in factKindObligations or add it to this test's substrate list with a reason",
				capability.Kind)
		}
	}
}

// TestEveryCapabilityValidatesWithItsDeclaredObligations proves the
// declarations actually pass the validator that guards them -- in
// particular that no producer declares a computed or answer-contract
// obligation.
func TestEveryCapabilityValidatesWithItsDeclaredObligations(t *testing.T) {
	for _, capability := range liveCapabilityList(t) {
		if err := capability.Validate(); err != nil {
			t.Errorf("capability %q does not validate: %v", capability.Kind, err)
		}
	}
}

// TestObligationSeedSnapshotIsRegenerated is the regenerate-and-diff test.
//
// The seed is never hand-written; this is what enforces that. The
// snapshot's only job is to make a declaration change legible in a review
// diff -- if a producer starts or stops serving an obligation, the reviewer
// sees which cells moved rather than having to re-derive them. Regenerate
// with:
//
//	go test ./internal/contextfabric/ -run TestObligationSeedSnapshotIsRegenerated -update-seed
func TestObligationSeedSnapshotIsRegenerated(t *testing.T) {
	generated := contextfabric.GenerateObligationSeed(liveCapabilityList(t)).Render()

	if *updateSeed {
		if err := os.MkdirAll(filepath.Dir(seedSnapshotPath), 0o755); err != nil {
			t.Fatalf("creating testdata directory: %v", err)
		}
		if err := os.WriteFile(seedSnapshotPath, []byte(generated), 0o644); err != nil {
			t.Fatalf("writing snapshot: %v", err)
		}
		t.Logf("wrote %s", seedSnapshotPath)
		return
	}

	committed, err := os.ReadFile(seedSnapshotPath)
	if err != nil {
		t.Fatalf("reading the committed snapshot: %v (regenerate with -update-seed)", err)
	}
	if string(committed) != generated {
		t.Errorf("the committed obligation seed does not match the one generated from the registry.\n"+
			"This is never fixed by editing %s: the snapshot is an OUTPUT. Either a producer's declaration changed "+
			"(regenerate with -update-seed and review the diff as part of that change) or the generator changed.\n\n--- committed ---\n%s\n--- generated ---\n%s",
			seedSnapshotPath, committed, generated)
	}
}

// TestGeneratedSeedMatchesTheStatusCategoryComposition is the N2 PARITY
// PROPERTY, stated as an oracle in §13.11a O9: the generated fact-kind set
// for `state` on a team and on a project must equal the CHAOS-4347
// composition set for that kind.
//
// WHY THIS IS THE ONE THAT MATTERS. composeStatusCategoryRequirements is
// the first of the SIX planning authorities (§13.8a) and the only one
// whose output is a directly comparable set. If the derived rows cannot
// reproduce it exactly, the derivation is not a candidate to replace it,
// and no amount of agreement elsewhere makes up for that. Equality is
// asserted in BOTH directions on purpose: a superset would quietly widen
// every status question's fact read, and a subset would quietly narrow it.
//
// The composition also covers `repository` and this test covers it too --
// the design names team and project because those are the acceptance
// questions' subjects, but a repository divergence would be just as real
// and is free to check.
func TestGeneratedSeedMatchesTheStatusCategoryComposition(t *testing.T) {
	seed := contextfabric.GenerateObligationSeed(liveCapabilityList(t))
	composition := contextfabric.StatusCategoryCompositionForTest()

	subjects := make([]string, 0, len(composition))
	for subject := range composition {
		subjects = append(subjects, string(subject))
	}
	sort.Strings(subjects)

	for _, name := range subjects {
		subject := contextfabric.SubjectKind(name)
		t.Run(name, func(t *testing.T) {
			want := append([]contextfabric.FactKind(nil), composition[subject]...)
			sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
			got := seed.KindsFor(contextfabric.ObligationState, subject)

			missing, extra := diffKinds(got, want)
			if len(missing) == 0 && len(extra) == 0 {
				return
			}
			t.Errorf("state@%s: generated seed = %v, CHAOS-4347 composition = %v\n  missing (composition has, seed does not): %v\n  extra (seed has, composition does not): %v",
				subject, render(got), render(want), render(missing), render(extra))
		})
	}

	// work_item is the one subject kind the composition deliberately does
	// NOT carry, and its absence is load-bearing rather than a gap: the
	// pre-existing 1:1 status -> FactStatus mapping already answers a work
	// item correctly, and the composition's own doc comment says it "only
	// ever ADDS coverage, never removes the existing 1:1 behavior for the
	// kind that already had it".
	//
	// So the parity property has a blind spot exactly where a regression
	// would be invisible: a producer that started declaring work_item state
	// would widen every work-item status read, and the three subtests above
	// would stay green. Asserted here rather than left to the map's key set.
	t.Run("work_item", func(t *testing.T) {
		got := seed.KindsFor(contextfabric.ObligationState, contextfabric.SubjectWorkItem)
		want := []contextfabric.FactKind{contextfabric.FactStatus}
		if missing, extra := diffKinds(got, want); len(missing) > 0 || len(extra) > 0 {
			t.Errorf("state@work_item: generated seed = %v, want exactly %v (the preserved 1:1 mapping)\n  missing: %v\n  extra: %v",
				render(got), render(want), render(missing), render(extra))
		}
	})
}

// TestQuantifierSourceIsTheMeasuredCardinality pins law L3's applied form:
// the completion quantifier per obligation comes from the generated seed's
// cardinality per subject kind, not from a constant.
//
// The frozen design fixed `state = corroborated` and then carried an
// escape clause -- "or the registry declares only one kind" -- which made
// it at_least_one wherever it actually bit, so the plan asserted a bar it
// silently lowered. Reading the bar off the measurement instead means a
// cell with one producer says one producer.
func TestQuantifierSourceIsTheMeasuredCardinality(t *testing.T) {
	seed := contextfabric.GenerateObligationSeed(liveCapabilityList(t))

	var report strings.Builder
	for _, obligation := range contextfabric.AnswerObligationVocabulary() {
		kind, known := contextfabric.KindOfObligation(obligation)
		if !known || kind != contextfabric.ObligationKindRead {
			continue
		}
		for _, subject := range seed.ServedSubjectKinds(obligation) {
			cardinality := seed.Cardinality(obligation, subject)
			if cardinality != len(seed.KindsFor(obligation, subject)) {
				t.Errorf("%s@%s: Cardinality disagrees with KindsFor", obligation, subject)
			}
			if cardinality == 0 {
				t.Errorf("%s@%s: ServedSubjectKinds returned a subject with a zero cardinality", obligation, subject)
			}
			report.WriteString("\n  " + string(obligation) + "@" + string(subject) + " -> " + itoa(cardinality))
		}
	}
	t.Logf("measured seed cardinality per served cell (the quantifier's source):%s", report.String())
}

func diffKinds(got, want []contextfabric.FactKind) (missing, extra []contextfabric.FactKind) {
	inGot := make(map[contextfabric.FactKind]bool, len(got))
	for _, kind := range got {
		inGot[kind] = true
	}
	inWant := make(map[contextfabric.FactKind]bool, len(want))
	for _, kind := range want {
		inWant[kind] = true
	}
	for _, kind := range want {
		if !inGot[kind] {
			missing = append(missing, kind)
		}
	}
	for _, kind := range got {
		if !inWant[kind] {
			extra = append(extra, kind)
		}
	}
	return missing, extra
}

func render(values []contextfabric.FactKind) string {
	if len(values) == 0 {
		return "[]"
	}
	rendered := make([]string, 0, len(values))
	for _, value := range values {
		rendered = append(rendered, string(value))
	}
	return "[" + strings.Join(rendered, " ") + "]"
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
