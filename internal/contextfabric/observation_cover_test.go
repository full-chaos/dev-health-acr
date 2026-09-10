package contextfabric

import "testing"

// The three cells the ruling names, plus the monotonicity pair it requires.
//
// Every case here is an OUTCOME assertion on the cover count, not on the
// shape of the declaration -- a test that asserted the map's contents would
// pass against a cover function that ignored the map entirely.

const (
	testKeyRisk           ObservationKey = "team_risk_rollup"
	testKeyThroughput     ObservationKey = "team_project_throughput_rollup"
	testKeySustainability ObservationKey = "team_sustainability_rollup"
	testKeyRepoRollup     ObservationKey = "repository_metrics_rollup"
)

// teamAssignment mirrors the shipped declaration at subject kind team: the
// three-partner cell is what makes this a cover problem rather than a count.
func teamAssignment() observationKeyAssignment {
	return observationKeyAssignment{
		FactHealth:  {SubjectTeam: {testKeyRisk}, SubjectRepository: {testKeyRepoRollup}},
		FactMetrics: {SubjectTeam: {testKeySustainability}, SubjectRepository: {testKeyRepoRollup}},
		FactFlow:    {SubjectTeam: {testKeyThroughput}},
		FactOperationalDeficiencies: {SubjectTeam: {
			testKeyRisk, testKeyThroughput, testKeySustainability,
		}},
	}
}

func TestTheObservationCoverIsTheRuledCountForEveryNamedCell(t *testing.T) {
	t.Parallel()
	assignment := teamAssignment()
	for _, testCase := range []struct {
		name    string
		served  []FactKind
		subject SubjectKind
		want    int
	}{
		{
			// The ruling's first worked cell. `risk` alone covers both, so
			// two served kinds are ONE observation and a corroborated
			// requirement over them is NOT satisfied.
			name:    "deficiencies and health at team are one observation",
			served:  []FactKind{FactOperationalDeficiencies, FactHealth},
			subject: SubjectTeam, want: 1,
		},
		{
			// The ruling's second worked cell. health forces `risk`, flow
			// forces `throughput`; deficiencies is then covered by either,
			// so it adds nothing.
			name:    "deficiencies, health and flow at team are two observations",
			served:  []FactKind{FactOperationalDeficiencies, FactHealth, FactFlow},
			subject: SubjectTeam, want: 2,
		},
		{
			// The measured reason the declaration is per SUBJECT KIND:
			// health and metrics share repo_metrics_daily at repository and
			// are independent at team.
			name:   "health and metrics collapse at repository",
			served: []FactKind{FactHealth, FactMetrics}, subject: SubjectRepository, want: 1,
		},
		{
			name:   "health and metrics stay independent at team",
			served: []FactKind{FactHealth, FactMetrics}, subject: SubjectTeam, want: 2,
		},
		{
			// An unkeyed kind is its own observation, never folded in and
			// never dropped.
			name:    "an unkeyed kind contributes exactly one",
			served:  []FactKind{FactOperationalDeficiencies, FactHealth, FactStatus},
			subject: SubjectTeam, want: 2,
		},
		{
			name:   "only unkeyed kinds count one each",
			served: []FactKind{FactStatus, FactWork, FactReadiness}, subject: SubjectTeam, want: 3,
		},
		{
			name:   "no served kinds cover nothing",
			served: nil, subject: SubjectTeam, want: 0,
		},
		{
			name:   "the same kind twice is the same kind",
			served: []FactKind{FactHealth, FactHealth}, subject: SubjectTeam, want: 1,
		},
		{
			// A kind keyed at team but served at a subject kind it declares
			// nothing for is a SINGLETON there, not a collapse.
			name:   "a cell with no declaration is a singleton, not a merge",
			served: []FactKind{FactFlow, FactHealth}, subject: SubjectProject, want: 2,
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := observationCover(testCase.served, testCase.subject, assignment); got != testCase.want {
				t.Fatalf("observationCover(%v @ %s) = %d, want %d", testCase.served, testCase.subject, got, testCase.want)
			}
		})
	}
}

// TestRemovingAProducerCannotImproveCompleteness is D15's own monotonicity
// acceptance, in the direction that matters: the cover is monotone
// NON-DECREASING in the served set, so dropping a producer can never RAISE
// the count and therefore can never make an answer look better corroborated.
func TestRemovingAProducerCannotImproveCompleteness(t *testing.T) {
	t.Parallel()
	assignment := teamAssignment()
	full := []FactKind{FactOperationalDeficiencies, FactHealth, FactFlow, FactMetrics, FactStatus}
	whole := observationCover(full, SubjectTeam, assignment)
	for index := range full {
		reduced := append(append([]FactKind{}, full[:index]...), full[index+1:]...)
		got := observationCover(reduced, SubjectTeam, assignment)
		if got > whole {
			t.Fatalf("removing %v RAISED the cover from %d to %d -- a producer could be dropped to look better corroborated",
				full[index], whole, got)
		}
	}
	// The control: without it every assertion above passes against a
	// function that returns a constant.
	if whole == 0 {
		t.Fatal("the whole set covers nothing; the monotonicity sweep above is vacuous")
	}
}

// TestADuplicateAdapterCannotCreateCorroboration is the other direction:
// adding a kind whose labels are ALREADY covered must not raise the count.
func TestADuplicateAdapterCannotCreateCorroboration(t *testing.T) {
	t.Parallel()
	assignment := teamAssignment()
	base := []FactKind{FactHealth}
	before := observationCover(base, SubjectTeam, assignment)
	// deficiencies at team declares `risk` among its labels, which health
	// already forces -- so it is covered for free.
	after := observationCover(append(append([]FactKind{}, base...), FactOperationalDeficiencies), SubjectTeam, assignment)
	if after != before {
		t.Fatalf("adding an already-covered producer moved the cover %d -> %d; a duplicate adapter must not create corroboration", before, after)
	}
	if before != 1 {
		t.Fatalf("the base cover is %d, want 1 -- the assertion above is only meaningful against a known base", before)
	}
	// And the control in the same shape: a kind that is NOT already covered
	// MUST raise it, or the assertion above would also pass against a
	// function that never grows.
	grown := observationCover([]FactKind{FactHealth, FactFlow}, SubjectTeam, assignment)
	if grown != 2 {
		t.Fatalf("an independent producer left the cover at %d, want 2", grown)
	}
}

// TestTheCoverIsTheMINIMUMNotTheSetCount is the pin that kills the two
// arithmetic mutants the ruling names by hand: count = number of lists, and
// count = size of the union of all labels.
func TestTheCoverIsTheMINIMUMNotTheSetCount(t *testing.T) {
	t.Parallel()
	assignment := teamAssignment()
	served := []FactKind{FactOperationalDeficiencies, FactHealth}
	got := observationCover(served, SubjectTeam, assignment)
	if got == 2 {
		t.Fatal("cover = 2 is the NUMBER OF LISTS (one per served kind); the ruled predicate is the minimum cover, which is 1")
	}
	if got == 3 {
		t.Fatal("cover = 3 is the UNION SIZE of every declared label; the ruled predicate is the minimum cover, which is 1")
	}
	if got != 1 {
		t.Fatalf("cover = %d, want 1", got)
	}
}

// TestATaintedObservationDoesNotSilenceAProducersOTHERObservations pins the
// mixed-state alias rule at the boundary it was originally wrong at.
//
// `operational_deficiencies` at team declares {risk, throughput,
// sustainability}; `health` declares {risk} alone. When health is lost and
// deficiencies served IN FULL, `risk` is tainted -- but throughput and
// sustainability were not lost by anyone, and deficiencies read them
// completely. Tainting the PRODUCER rather than the OBSERVATION returned 0
// here, which publishes "nothing was served" over a full read.
func TestATaintedObservationDoesNotSilenceAProducersOTHERObservations(t *testing.T) {
	t.Parallel()
	assignment := teamAssignment()
	evidence := readEvidence{
		ObservedKinds: []FactKind{FactOperationalDeficiencies, FactHealth},
		ServedKinds:   []FactKind{FactOperationalDeficiencies},
	}
	got := servedObservationCover(evidence, SubjectTeam, assignment)
	if got == 0 {
		t.Fatal("served cover = 0: the whole PRODUCER was tainted by one shared key, so a kind that read two untainted observations in full counted for nothing")
	}
	if got != 1 {
		t.Fatalf("served cover = %d, want 1 -- deficiencies still covers throughput/sustainability with risk tainted", got)
	}

	// Control 1: nothing lost, so nothing tainted, and the two kinds collapse
	// onto `risk` as one observation. Without this the assertion above also
	// passes against a function that ignores taint entirely.
	both := readEvidence{
		ObservedKinds: []FactKind{FactOperationalDeficiencies, FactHealth},
		ServedKinds:   []FactKind{FactOperationalDeficiencies, FactHealth},
	}
	if got := servedObservationCover(both, SubjectTeam, assignment); got != 1 {
		t.Fatalf("both served = %d, want 1 -- they share `risk`", got)
	}

	// Control 2: a served kind whose ONLY key is tainted must still drop out,
	// or the fix has simply disabled the rule.
	onlyKeyTainted := readEvidence{
		ObservedKinds: []FactKind{FactHealth, FactOperationalDeficiencies},
		ServedKinds:   []FactKind{FactHealth},
	}
	if got := servedObservationCover(onlyKeyTainted, SubjectTeam, assignment); got != 0 {
		t.Fatalf("served cover = %d, want 0 -- health's only observation (`risk`) is tainted by the lost deficiencies read", got)
	}
}
