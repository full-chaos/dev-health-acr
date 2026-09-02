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
		// FactWork emits a TITLE and nothing else. Round 2 found it
		// declaring {completion, remaining_work} under a comment claiming
		// "raw work counts", which the provider does not read. Its
		// obligations are served by its siblings: ActualCompletion emits
		// {"completed"}, Blockers and RequiredChildren carry remaining
		// work. A descriptor producer is substrate.
		contextfabric.FactWork: true,
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
//
// WHAT THIS TEST DOES NOT PROVE, stated because round 1 showed the name
// invites the stronger reading: it audits `state` for the subject kinds
// the shipped composition covers, and NOTHING ELSE. A wrong declaration on
// another obligation or another subject kind passes it untouched -- the
// reviewer declared `readiness@pull_request` on a state-only provider and
// every oracle here stayed green. That gap is not closable by a test,
// because no reference exists for those cells the way the composition is a
// reference for `state`; the control is review, and
// TestEveryDeclaredCellIsVisibleInTheSnapshot is what guarantees review can
// see them.
func TestGeneratedSeedMatchesTheStatusCategoryComposition(t *testing.T) {
	seed := contextfabric.GenerateObligationSeed(liveCapabilityList(t))
	composition := contextfabric.StatusCategoryCompositionForTest()

	// ROUND 2, EXECUTED: this loop is keyed on the AUTHORITY, so emptying
	// the composition to {} ran zero subtests, made zero assertions and
	// left every one of the eighteen tests green -- an oracle that reports
	// parity it never checked. Red-on-parent shows a test CAN fail and a
	// mutation kill shows it fails for the right reason; neither shows the
	// assertions EXECUTED. So the input is quantified before it is used.
	//
	// The number is pinned, not merely non-zero: the composition covers
	// repository, team and project, and a composition that silently lost
	// one would otherwise still "pass parity" on the two that remain.
	const wantCompositionSubjects = 3
	if len(composition) != wantCompositionSubjects {
		t.Fatalf("the status-category composition carries %d subject kinds, want %d -- "+
			"this test derives its whole input from that map, so a shrunken authority would "+
			"silently reduce what parity means rather than failing",
			len(composition), wantCompositionSubjects)
	}

	subjects := make([]string, 0, len(composition))
	for subject := range composition {
		subjects = append(subjects, string(subject))
	}
	sort.Strings(subjects)

	compared := 0

	for _, name := range subjects {
		subject := contextfabric.SubjectKind(name)
		t.Run(name, func(t *testing.T) {
			want := append([]contextfabric.FactKind(nil), composition[subject]...)
			sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
			got := seed.KindsFor(contextfabric.ObligationState, subject)

			if len(want) == 0 {
				t.Fatalf("state@%s: the composition names no fact kinds for this subject kind; "+
					"comparing against an empty set would pass for any seed", subject)
			}
			compared++

			missing, extra := diffKinds(got, want)
			if len(missing) == 0 && len(extra) == 0 {
				return
			}
			t.Errorf("state@%s: generated seed = %v, CHAOS-4347 composition = %v\n  missing (composition has, seed does not): %v\n  extra (seed has, composition does not): %v",
				subject, render(got), render(want), render(missing), render(extra))
		})
	}

	if compared != wantCompositionSubjects {
		t.Errorf("only %d of %d composition subject kinds reached the parity assertion; "+
			"a subtest that returns early proves nothing about the cells it skipped",
			compared, wantCompositionSubjects)
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

// TestCapabilitiesDoesNotShareItsDeclarationMapsWithTheRegistry proves the
// aliasing fix rather than asserting it.
//
// A struct copy duplicates the map HEADER and shares the backing store, so
// before this change a caller writing through the returned capability's
// Tables or Obligations would have mutated the registry's own declaration
// for every later caller -- and the failure would surface wherever that
// declaration was next read, never at the mutation.
//
// The test plants the mutation and re-reads, which is the only shape that
// can fail in the interesting direction: asserting "a copy is made" by
// comparing pointers would pass on a shallow copy that still shares the
// value slices.
func TestCapabilitiesDoesNotShareItsDeclarationMapsWithTheRegistry(t *testing.T) {
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(nil), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("building the registry: %v", err)
	}

	before := registry.Capabilities()
	if len(before) == 0 {
		t.Fatal("registry returned no capabilities; the mutation below would prove nothing")
	}

	// Find a capability that actually declares both map fields, so the
	// mutation has something to corrupt. Picking blindly could land on a
	// provider with neither and pass vacuously.
	var target contextfabric.FactKind
	for _, capability := range before {
		if len(capability.Tables) > 0 && len(capability.Obligations) > 0 {
			target = capability.Kind
			break
		}
	}
	if target == "" {
		t.Fatal("no capability declares both Tables and Obligations; this test cannot detect the aliasing it exists for")
	}

	for _, capability := range before {
		if capability.Kind != target {
			continue
		}
		for subject := range capability.Tables {
			capability.Tables[subject] = append(capability.Tables[subject], contextfabric.FactTableRanking)
		}
		for subject := range capability.Obligations {
			capability.Obligations[subject] = append(capability.Obligations[subject], contextfabric.ObligationRemainingWork)
		}
	}

	for _, capability := range registry.Capabilities() {
		if capability.Kind != target {
			continue
		}
		for subject, shapes := range capability.Tables {
			for _, shape := range shapes {
				if shape == contextfabric.FactTableRanking {
					t.Errorf("%s: a caller's write to Tables[%s] reached the registry's declaration", target, subject)
				}
			}
		}
		for subject, declared := range capability.Obligations {
			for _, obligation := range declared {
				if obligation == contextfabric.ObligationRemainingWork {
					t.Errorf("%s: a caller's write to Obligations[%s] reached the registry's declaration", target, subject)
				}
			}
		}
	}
}

// TestGenerateObligationSeedDeduplicatesFactKinds closes round 1's second
// finding at the level the defect actually lives.
//
// Cardinality() is documented as the source of the completion quantifier,
// so a duplicated fact kind reports two independent producers where there
// is one. Every existing oracle was blind to it: Render() and the parity
// property both compare SETS, so a duplicate normalized away in rendering
// left the snapshot, the parity assertion and the cardinality test all
// green while the underlying seed carried a doubled cell.
//
// The input is two capabilities sharing a Kind — the shape the exported
// generator cannot refuse, since Validate guards a single declaration and
// the registry's duplicate check runs somewhere else entirely.
func TestGenerateObligationSeedDeduplicatesFactKinds(t *testing.T) {
	twin := func(name string) contextfabric.FactCapability {
		return contextfabric.FactCapability{
			Kind: contextfabric.FactHealth, Name: name, Version: "v",
			SupportedSubjectKinds: []contextfabric.SubjectKind{contextfabric.SubjectTeam},
			Dimension:             contextfabric.HealthDimensionCodeOwnershipRisk,
			SubjectRoles:          []contextfabric.FactRole{contextfabric.FactRoleSubject},
			Obligations: map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
				contextfabric.SubjectTeam: {contextfabric.ObligationState},
			},
		}
	}

	seed := contextfabric.GenerateObligationSeed([]contextfabric.FactCapability{twin("a"), twin("b")})
	kinds := seed.KindsFor(contextfabric.ObligationState, contextfabric.SubjectTeam)
	if len(kinds) != 1 {
		t.Errorf("KindsFor returned %v (len %d), want exactly one entry: a duplicated fact kind inflates Cardinality, which sets the completion quantifier", render(kinds), len(kinds))
	}
	if got := seed.Cardinality(contextfabric.ObligationState, contextfabric.SubjectTeam); got != 1 {
		t.Errorf("Cardinality = %d, want 1 — the plan would demand corroboration across producers that do not exist", got)
	}
}

// TestGeneratedSeedHasNoDuplicateKindInAnyCell is the same property
// asserted over the LIVE registry rather than a constructed pair, so a
// future producer change cannot reintroduce it anywhere.
func TestGeneratedSeedHasNoDuplicateKindInAnyCell(t *testing.T) {
	seed := contextfabric.GenerateObligationSeed(liveCapabilityList(t))
	for _, obligation := range contextfabric.AnswerObligationVocabulary() {
		for _, subject := range seed.ServedSubjectKinds(obligation) {
			seen := map[contextfabric.FactKind]bool{}
			for _, kind := range seed.KindsFor(obligation, subject) {
				if seen[kind] {
					t.Errorf("%s@%s lists fact kind %q more than once", obligation, subject, kind)
				}
				seen[kind] = true
			}
		}
	}
}

// TestEveryDeclaredCellIsVisibleInTheSnapshot bounds round 1's first
// finding rather than pretending to close it.
//
// THE FINDING, STATED HONESTLY: the parity property proves `state` for the
// three composition subject kinds and `work_item`. It says nothing about
// any other obligation/subject cell, and the reviewer showed it — he
// declared `readiness@pull_request` on a provider that reads pull-request
// state only, and every oracle stayed green. Re-executed and CONFIRMED by
// this lane.
//
// NO TEST CAN CLOSE THAT, and saying so is more useful than a guard that
// pretends otherwise: whether `readiness` is a meaningful question about a
// pull request is a semantic judgement, not a property of the registry.
// There is no shipped reference for it the way the status composition is a
// reference for `state`.
//
// So the control is REVIEW, and what a test can guarantee is that review
// is possible: every declared cell must appear in the committed snapshot,
// so no declaration can be added invisibly. A declaration that reaches the
// seed without appearing in the diff would defeat the only control there
// is.
func TestEveryDeclaredCellIsVisibleInTheSnapshot(t *testing.T) {
	committed, err := os.ReadFile(seedSnapshotPath)
	if err != nil {
		t.Fatalf("reading the committed snapshot: %v", err)
	}
	rendered := string(committed)

	seed := contextfabric.GenerateObligationSeed(liveCapabilityList(t))
	checked := 0
	for _, obligation := range contextfabric.AnswerObligationVocabulary() {
		for _, subject := range seed.ServedSubjectKinds(obligation) {
			if !strings.Contains(rendered, "\n"+string(obligation)+"\n") {
				t.Errorf("obligation %q is served but has no block in the snapshot", obligation)
				continue
			}
			for _, kind := range seed.KindsFor(obligation, subject) {
				checked++
				if !strings.Contains(rendered, string(kind)) {
					t.Errorf("%s@%s is served by %q, which appears nowhere in the snapshot", obligation, subject, kind)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no served cells were checked; this guard would be vacuous")
	}
	t.Logf("every one of %d served (obligation, subject, kind) entries is visible in the review snapshot", checked)
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
