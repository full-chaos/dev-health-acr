package contextfabric

import (
	"context"
	"errors"
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4632 §4.1 mechanism-2 consensus tests. RED ON origin/main by
// compile failure -- none of these symbols exist there.

func groupedSample(kind SubjectKind) FamilySample {
	return FamilySample{Shape: ShapeDiscoveredCohort, GroupKind: kind}
}

func cohortSample() FamilySample { return FamilySample{Shape: ShapeDiscoveredCohort} }

func subjectSample() FamilySample { return FamilySample{Shape: ShapeSingleSubject} }

// TestConsensusRules is the table-driven proof of the aggregation rule.
func TestConsensusRules(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		samples []FamilySample
		family  QuestionFamily
		source  QuestionFamilySource
	}{
		{
			name:    "no samples at all: unclassified, source none",
			samples: nil,
			family:  QuestionFamilyUnclassified,
			source:  QuestionFamilySourceNone,
		},
		{
			// The DEGRADE PATH and the shipped default. One sample cannot
			// corroborate itself, so the precedence table's verdict stands
			// and the source says model, NOT model_consensus -- §4.1's
			// "a visibly weaker guarantee rather than an invisible cost".
			name:    "N=1 degrades to the precedence table with source=model",
			samples: []FamilySample{cohortSample()},
			family:  QuestionFamilyDiscoveredCohortRanking,
			source:  QuestionFamilySourceModel,
		},
		{
			name:    "N=3, 3-0: unanimous",
			samples: []FamilySample{cohortSample(), cohortSample(), cohortSample()},
			family:  QuestionFamilyDiscoveredCohortRanking,
			source:  QuestionFamilySourceModelConsensus,
		},
		{
			name:    "N=3, 2-1: strict majority",
			samples: []FamilySample{cohortSample(), cohortSample(), subjectSample()},
			family:  QuestionFamilyDiscoveredCohortRanking,
			source:  QuestionFamilySourceModelConsensus,
		},
		{
			// THE TIE. §4.1: "No strict majority -> unclassified, which is
			// the refuse-to-guess member and today's behaviour. A tie is
			// never broken by picking a side." On the six real captures
			// this is exactly Q-B at n=2 (1-1), and refusing there is the
			// behaviour we want -- the alternative is confidently picking
			// the wrong family.
			name:    "N=2, 1-1: NO majority, refuse to guess",
			samples: []FamilySample{cohortSample(), subjectSample()},
			family:  QuestionFamilyUnclassified,
			source:  QuestionFamilySourcePluralityRejected,
		},
		{
			// Strict means STRICT. Exactly half is not a majority.
			name:    "N=4, 2-2: no majority",
			samples: []FamilySample{cohortSample(), cohortSample(), subjectSample(), subjectSample()},
			family:  QuestionFamilyUnclassified,
			source:  QuestionFamilySourcePluralityRejected,
		},
		{
			// A PLURALITY IS NOT A MAJORITY. 2 of 5 is the largest block
			// and still loses. This is the case that would tempt an
			// implementation into "pick the most common", which is
			// precisely what refuse-to-guess forbids.
			name: "N=5, 2-1-1-1: a plurality is rejected",
			samples: []FamilySample{
				cohortSample(), cohortSample(), subjectSample(),
				{Shape: ShapeExplicitCohort},
				{Shape: ShapeSingleSubject, ComparisonTerms: []string{"a"}},
			},
			family: QuestionFamilyUnclassified,
			source: QuestionFamilySourcePluralityRejected,
		},
		{
			name:    "N=4, 3-1: strict majority",
			samples: []FamilySample{cohortSample(), cohortSample(), cohortSample(), subjectSample()},
			family:  QuestionFamilyDiscoveredCohortRanking,
			source:  QuestionFamilySourceModelConsensus,
		},
		{
			// unclassified is a REAL member and can itself win a
			// majority. It is not a failure code.
			name: "unclassified can win a majority like any other member",
			samples: []FamilySample{
				{Shape: ShapeExplicitCohort}, {Shape: ShapeExplicitCohort}, cohortSample(),
			},
			family: QuestionFamilyUnclassified,
			source: QuestionFamilySourceModelConsensus,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			outcome := ResolveQuestionFamily(testCase.samples)
			if outcome.Family != testCase.family {
				t.Errorf("Family = %q, want %q", outcome.Family, testCase.family)
			}
			if outcome.Source != testCase.source {
				t.Errorf("Source = %q, want %q", outcome.Source, testCase.source)
			}
			if outcome.Version != QuestionFamilyTableVersion {
				t.Errorf("Version = %q, want %q", outcome.Version, QuestionFamilyTableVersion)
			}
			// The per-sample distribution must always account for every
			// sample -- it is the denominator every later reading depends
			// on.
			total := 0
			for _, count := range outcome.SampleFamilies {
				total += count
			}
			if total != len(testCase.samples) {
				t.Errorf("SampleFamilies totals %d, want %d", total, len(testCase.samples))
			}
			if len(outcome.Samples) != len(testCase.samples) {
				t.Errorf("Samples has %d rows, want %d -- one row per sample", len(outcome.Samples), len(testCase.samples))
			}
		})
	}
}

// TestNoMajorityCarriesNoWinner pins the property that makes a rejected
// consensus SAFE: it must expose no winning sample at all.
//
// If a rejected consensus still handed back a WinningSample, a caller that
// read the sample without checking Source would silently plan from a
// GroupKind no majority ever agreed to -- and mechanism 3 would then carry
// that arbitrary value into every later turn. The refusal has to be
// structural, not a flag a reader might miss.
func TestNoMajorityCarriesNoWinner(t *testing.T) {
	t.Parallel()
	outcome := ResolveQuestionFamily([]FamilySample{
		groupedSample(contractsv1.ContextFabricSubjectTeam),
		subjectSample(),
	})
	if outcome.Source != QuestionFamilySourcePluralityRejected {
		t.Fatalf("Source = %q, want model_plurality_rejected", outcome.Source)
	}
	if outcome.WinningSampleIndex != -1 {
		t.Errorf("WinningSampleIndex = %d, want -1", outcome.WinningSampleIndex)
	}
	if !reflect.DeepEqual(outcome.WinningSample, FamilySample{}) {
		t.Errorf("WinningSample = %+v, want the zero value", outcome.WinningSample)
	}
	if outcome.Winner != (FamilySampleOutcome{}) {
		t.Errorf("Winner = %+v, want the zero value", outcome.Winner)
	}
}

// TestWinnerIsOneWholeSampleNeverFieldWiseMixed is THE round-3 finding,
// asserted directly.
//
// Three samples agree on grouped_cohort_status; they disagree on
// GroupKind. Field-wise aggregation would have no rule for the group axis
// -- or worse, would compose a "most common GroupKind" with a "most common
// anchor" drawn from DIFFERENT samples, producing a plan no model
// proposed. The rule is that every downstream field comes from ONE sample,
// and this test checks the actual pairing survives, not merely that some
// GroupKind came out.
func TestWinnerIsOneWholeSampleNeverFieldWiseMixed(t *testing.T) {
	t.Parallel()
	samples := []FamilySample{
		{Shape: ShapeDiscoveredCohort, GroupKind: contractsv1.ContextFabricSubjectTeam, ScopeAnchorTerm: "alpha"},
		{Shape: ShapeDiscoveredCohort, GroupKind: contractsv1.ContextFabricSubjectProject, ScopeAnchorTerm: "beta"},
		{Shape: ShapeDiscoveredCohort, GroupKind: contractsv1.ContextFabricSubjectRepository, ScopeAnchorTerm: "gamma"},
	}
	outcome := ResolveQuestionFamily(samples)
	if outcome.Family != QuestionFamilyGroupedCohortStatus {
		t.Fatalf("Family = %q, want grouped_cohort_status", outcome.Family)
	}
	winner := samples[outcome.WinningSampleIndex]
	if !reflect.DeepEqual(outcome.WinningSample, winner) {
		t.Fatalf("WinningSample = %+v, want the sample at the winning index %+v", outcome.WinningSample, winner)
	}
	// The pairing is what matters: whichever sample won, its OWN anchor
	// must have come with its OWN group kind.
	for _, sample := range samples {
		if sample.GroupKind == outcome.WinningSample.GroupKind && sample.ScopeAnchorTerm != outcome.WinningSample.ScopeAnchorTerm {
			t.Fatalf("winner pairs GroupKind %q with anchor %q, but no input sample had that combination -- fields were mixed across samples", outcome.WinningSample.GroupKind, outcome.WinningSample.ScopeAnchorTerm)
		}
	}
	// All three agreed on the family and disagreed on its parameters --
	// exactly the countable state §4.1 says should later justify ASKING
	// rather than assuming.
	if outcome.ConsensusFieldDivergence != 2 {
		t.Errorf("ConsensusFieldDivergence = %d, want 2 (both non-winning majority samples disagree)", outcome.ConsensusFieldDivergence)
	}
}

// TestWinnerSelectionIsIndependentOfInputOrder is why the winner is chosen
// by a TOTAL KEY rather than by position.
//
// The N samples run CONCURRENTLY, so the order they land in the slice
// depends on scheduling. If the winner were "the first one in the slice",
// the GroupKind that mechanism 3 carries into every later turn would
// depend on a race -- two identical runs of the same question could plan
// different group axes. Permuting the input must not change which SAMPLE
// wins.
func TestWinnerSelectionIsIndependentOfInputOrder(t *testing.T) {
	t.Parallel()
	a := FamilySample{Shape: ShapeDiscoveredCohort, GroupKind: contractsv1.ContextFabricSubjectTeam}
	b := FamilySample{Shape: ShapeDiscoveredCohort, GroupKind: contractsv1.ContextFabricSubjectProject}
	c := FamilySample{Shape: ShapeDiscoveredCohort, GroupKind: contractsv1.ContextFabricSubjectRepository}
	permutations := [][]FamilySample{
		{a, b, c}, {a, c, b}, {b, a, c}, {b, c, a}, {c, a, b}, {c, b, a},
	}
	var want FamilySample
	for i, samples := range permutations {
		outcome := ResolveQuestionFamily(samples)
		if i == 0 {
			want = outcome.WinningSample
			continue
		}
		if !reflect.DeepEqual(outcome.WinningSample, want) {
			t.Fatalf("permutation %d selected %+v, permutation 0 selected %+v -- the winner depends on input order", i, outcome.WinningSample, want)
		}
	}
}

// TestDowngradeCountAggregatesAcrossSamples pins that the counter counts
// SAMPLES, not investigations, and that per-sample rows keep their own
// distinct attempted family and reason.
//
// §4.3, round 2: "singular fields cannot represent two samples failing
// DIFFERENT precedence rows with DIFFERENT attempted families, which is
// exactly the diagnosis a split consensus needs."
func TestDowngradeCountAggregatesAcrossSamples(t *testing.T) {
	t.Parallel()
	samples := []FamilySample{
		{Shape: ShapeDiscoveredCohort, ModelFamily: QuestionFamilyTrend},
		{Shape: ShapeDiscoveredCohort, ModelFamilyUnrecognized: true},
		{Shape: ShapeDiscoveredCohort, ModelFamily: QuestionFamilyDiscoveredCohortRanking},
	}
	outcome := ResolveQuestionFamily(samples)
	if outcome.DowngradedCount != 2 {
		t.Fatalf("DowngradedCount = %d, want 2", outcome.DowngradedCount)
	}
	if outcome.Samples[0].IncompatibilityReason != FamilyIncompatibilityUnreachable {
		t.Errorf("sample 0 reason = %q, want declared_unreachable", outcome.Samples[0].IncompatibilityReason)
	}
	if outcome.Samples[1].IncompatibilityReason != FamilyIncompatibilityUnrecognized {
		t.Errorf("sample 1 reason = %q, want unrecognized_family", outcome.Samples[1].IncompatibilityReason)
	}
	if outcome.Samples[2].IncompatibilityReason != "" {
		t.Errorf("sample 2 reason = %q, want empty (the model agreed)", outcome.Samples[2].IncompatibilityReason)
	}
	// The two downgraded samples carry DIFFERENT attempted families -- one
	// a vocabulary member, one nothing at all because the raw string was
	// out of vocabulary and is never echoed into a closed-enum field.
	if outcome.Samples[0].AttemptedFamily != QuestionFamilyTrend || outcome.Samples[1].AttemptedFamily != "" {
		t.Errorf("attempted families = %q / %q, want trend / empty", outcome.Samples[0].AttemptedFamily, outcome.Samples[1].AttemptedFamily)
	}
}

// TestEnsembleSeedsAreDistinctAndDerived is the §4.1 seed property, and it
// is load-bearing rather than cosmetic.
//
// One shared pinned seed across all N samples would, against a provider
// that honours seeds at all, return near-identical samples: the consensus
// becomes VACUOUSLY UNANIMOUS, fakes the very stability this slice exists
// to measure, and defeats refuse-to-guess by never producing a tie.
// Arbitrary per-sample seeds fix the diversity but lose replay.
// Distinct-and-derived gives both.
func TestEnsembleSeedsAreDistinctAndDerived(t *testing.T) {
	t.Parallel()
	seeds := EnsembleSeeds("Which teams are struggling, and why?", 5)
	if len(seeds) != 5 {
		t.Fatalf("got %d seeds, want 5", len(seeds))
	}
	seen := map[int64]struct{}{}
	for i, seed := range seeds {
		if seed < 0 {
			t.Errorf("seed %d is negative (%d); providers reject negative seeds", i, seed)
		}
		if _, ok := seen[seed]; ok {
			t.Fatalf("seed %d (%d) is a duplicate -- a shared seed makes the consensus vacuously unanimous", i, seed)
		}
		seen[seed] = struct{}{}
	}
	// DERIVED, therefore replayable: the same question yields the same
	// seeds, so an entire turn is reproducible from the question alone.
	again := EnsembleSeeds("Which teams are struggling, and why?", 5)
	for i := range seeds {
		if seeds[i] != again[i] {
			t.Fatalf("seed %d is not reproducible for the same question", i)
		}
	}
	// Different questions must not collide.
	other := EnsembleSeeds("What is the status of the Dev Health Ops project?", 5)
	for i := range seeds {
		if seeds[i] == other[i] {
			t.Errorf("seed %d collides across two different questions", i)
		}
	}
}

// TestEnsembleSeedsFollowQuestionCanonicalization pins the choice of hash
// input: CanonicalizeQuestion's own hash, not the raw string.
//
// Two questions the reuse key considers IDENTICAL must seed identically,
// or a replay of a stored answer's question would draw a different
// ensemble than the run that produced it.
func TestEnsembleSeedsFollowQuestionCanonicalization(t *testing.T) {
	t.Parallel()
	a := EnsembleSeeds("What is the status of the project?", 3)
	b := EnsembleSeeds("  What IS the Status of the project?!  ", 3)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("seed %d differs for two questions that canonicalize identically", i)
		}
	}
}

// TestBoundEnsembleSizeDegradesRatherThanFailing pins §4.1's cost rule: a
// misconfiguration must degrade to a weaker guarantee, never fail an
// investigation and never silently spend beyond the ceiling.
func TestBoundEnsembleSizeDegradesRatherThanFailing(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct{ in, want int }{
		{0, QuestionFamilyEnsembleDefault},
		{-7, QuestionFamilyEnsembleDefault},
		{1, 1},
		{3, 3},
		{QuestionFamilyEnsembleMax, QuestionFamilyEnsembleMax},
		{QuestionFamilyEnsembleMax + 100, QuestionFamilyEnsembleMax},
	} {
		if got := BoundEnsembleSize(testCase.in); got != testCase.want {
			t.Errorf("BoundEnsembleSize(%d) = %d, want %d", testCase.in, got, testCase.want)
		}
	}
	if QuestionFamilyEnsembleDefault != 1 {
		t.Errorf("QuestionFamilyEnsembleDefault = %d, want 1 -- the shipped default must make ZERO extra model calls, which is what makes zero behaviour change provable", QuestionFamilyEnsembleDefault)
	}
}

// TestEnsembleDropsFailedSamplesRatherThanSubstituting pins the failure
// rule: a sampler error yields NO sample.
//
// Substituting a zero-valued sample would inject a phantom unclassified
// vote no model produced -- the same field-wise fabrication the
// whole-winning-sample rule forbids one level up. Here 2 of 3 samplers
// succeed and agree, so the majority is over TWO, not three-with-a-ghost.
func TestEnsembleDropsFailedSamplesRatherThanSubstituting(t *testing.T) {
	t.Parallel()
	var calls int
	outcome, samples := ResolveQuestionFamilyEnsemble(context.Background(), "q", 3,
		func(_ context.Context, seed int64) (FamilySample, error) {
			// Fail exactly one sample, chosen by seed parity so the
			// choice does not depend on call order.
			calls++
			if seed%2 == 0 {
				return FamilySample{}, errors.New("sampler failed")
			}
			return cohortSample(), nil
		})
	if calls != 3 {
		t.Fatalf("sampler called %d times, want 3", calls)
	}
	if len(samples) == 3 {
		t.Skip("all three seeds were odd for this question; the drop path was not exercised")
	}
	if len(samples) != len(outcome.Samples) {
		t.Fatalf("outcome has %d sample rows for %d collected samples", len(outcome.Samples), len(samples))
	}
	for _, sample := range samples {
		if reflect.DeepEqual(sample, FamilySample{}) {
			t.Fatal("a zero-valued sample reached the consensus -- failed samples must be DROPPED, never substituted")
		}
	}
}

// TestEnsembleWithAllSamplesFailingReportsNothingMeasured: if every sample
// fails, the honest report is the zero-sample outcome, not a confident
// unclassified-by-consensus.
func TestEnsembleWithAllSamplesFailingReportsNothingMeasured(t *testing.T) {
	t.Parallel()
	outcome, samples := ResolveQuestionFamilyEnsemble(context.Background(), "q", 3,
		func(context.Context, int64) (FamilySample, error) { return FamilySample{}, errors.New("down") })
	if len(samples) != 0 {
		t.Fatalf("collected %d samples, want 0", len(samples))
	}
	if outcome.Source != QuestionFamilySourceNone {
		t.Errorf("Source = %q, want none -- nothing was measured", outcome.Source)
	}
	if outcome.Family != QuestionFamilyUnclassified {
		t.Errorf("Family = %q, want unclassified", outcome.Family)
	}
}
