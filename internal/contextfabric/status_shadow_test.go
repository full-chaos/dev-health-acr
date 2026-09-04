package contextfabric

import (
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The B8 shadow gate's own tests: every basis reachable, the served
// document unchanged, the declined derivations kept distinct from an
// agreement, and -- the point of the whole gate after review -- the shadow
// measuring against the FRAME rather than against the plan's copied flags.

func statusShadowPlan() AnswerPlan {
	return AnswerPlan{
		Family:        QuestionFamilySubjectInvestigation,
		FamilyVersion: QuestionFamilyTableVersion,
	}
}

// oneFact is a served claimed fact, so a fixture can get past the coarsest
// basis and reach the one it is actually testing.
func oneFact() []contractsv1.ContextFabricClaimedFact {
	return []contractsv1.ContextFabricClaimedFact{{}}
}

// baselineObligations demands nothing the bases below check, so a fixture
// aimed at one basis is not accidentally caught by another.
//
// EVERY MEMBER MUST BE OBSERVABLE. It previously included `state`, which is
// declared unobservable, so once the unobservable arm landed every fixture
// using this baseline fell into it -- including the `served` fixture. The
// arm was right and the helper was wrong. Asserted rather than assumed by
// TestTheBaselineObligationsAreAllObservable, so this cannot silently
// regress and quietly move every fixture into one basis again.
func baselineObligations() []AnswerObligation {
	return []AnswerObligation{ObligationEvidence, ObligationCoverage}
}

// TestTheBaselineObligationsAreAllObservable guards the helper above.
func TestTheBaselineObligationsAreAllObservable(t *testing.T) {
	t.Parallel()
	for _, obligation := range baselineObligations() {
		observation, declared := obligationObservations[obligation]
		if !declared || !observation.observed {
			t.Fatalf("baselineObligations includes %q, which this derivation cannot observe -- every fixture built on it would land on the unobservable basis regardless of what it was aiming at", obligation)
		}
	}
}

func withDriverObligation() []AnswerObligation {
	return append(baselineObligations(), ObligationPrincipalDrivers)
}

// TestEveryServerStatusBasisHasAFixtureThatLandsInIt is the dead-tier gate
// on the basis vocabulary.
func TestEveryServerStatusBasisHasAFixtureThatLandsInIt(t *testing.T) {
	t.Parallel()

	withPlan := func(mutate func(*AnswerPlan), result InvestigationResult) InvestigationResult {
		plan := statusShadowPlan()
		if mutate != nil {
			mutate(&plan)
		}
		result.AnswerPlan = &plan
		return result
	}

	cases := []struct {
		basis            ServerStatusBasis
		why              string
		result           InvestigationResult
		frameObligations []AnswerObligation
	}{
		{
			basis:            ServerStatusBasisNoPlan,
			why:              "a terminal exit stamps no plan, so there are no fact kinds to hold the answer against and the server DECLINES rather than guessing",
			result:           InvestigationResult{Status: InvestigationComplete},
			frameObligations: baselineObligations(),
		},
		{
			basis:            ServerStatusBasisNoFrame,
			why:              "no frame reached validation, so the obligations this derivation measures against do not exist. DECLINED -- treating an absent frame as an absence of demands would report `served` across the population most likely to be incomplete",
			result:           withPlan(nil, InvestigationResult{Status: InvestigationComplete, ClaimedFacts: oneFact()}),
			frameObligations: nil,
		},
		{
			basis: ServerStatusBasisNoClaimedFacts,
			why:   "the plan named fact kinds to read and the answer carries no claimed fact at all",
			result: withPlan(func(plan *AnswerPlan) {
				plan.FactKinds = []contractsv1.ContextFabricFactKind{contractsv1.ContextFabricFactHealth}
			}, InvestigationResult{Status: InvestigationComplete}),
			frameObligations: baselineObligations(),
		},
		{
			basis:            ServerStatusBasisDriversAbsent,
			why:              "the FRAME derives principal_drivers and the answer carries none. The plan's RequireDrivers is FALSE here, exactly as production copies it from the registry row -- this fixture IS the review finding, turned into a test",
			result:           withPlan(func(plan *AnswerPlan) { plan.RequireDrivers = false }, InvestigationResult{Status: InvestigationComplete, ClaimedFacts: oneFact()}),
			frameObligations: withDriverObligation(),
		},
		{
			basis:            ServerStatusBasisRankingAbsent,
			why:              "the FRAME derives ranking and there is no cohort to have ranked; again with the plan flag false",
			result:           withPlan(func(plan *AnswerPlan) { plan.RequireRanking = false }, InvestigationResult{Status: InvestigationComplete, ClaimedFacts: oneFact()}),
			frameObligations: append(baselineObligations(), ObligationRanking),
		},
		{
			basis:            ServerStatusBasisRemainingWorkAbsent,
			why:              "the FRAME derives remaining_work and the answer lists none",
			result:           withPlan(nil, InvestigationResult{Status: InvestigationComplete, ClaimedFacts: oneFact()}),
			frameObligations: append(baselineObligations(), ObligationRemainingWork),
		},
		{
			basis:            ServerStatusBasisReadinessAbsent,
			why:              "the FRAME derives readiness and the answer lists no readiness gap",
			result:           withPlan(nil, InvestigationResult{Status: InvestigationComplete, ClaimedFacts: oneFact()}),
			frameObligations: append(baselineObligations(), ObligationReadiness),
		},
		{
			basis:            ServerStatusBasisUnobservable,
			why:              "the frame requires `count`, which this derivation cannot observe in a served result. Review found this reported as `served` -- the shadow asserting an answer complete on the strength of not having looked",
			result:           withPlan(nil, InvestigationResult{Status: InvestigationComplete, ClaimedFacts: oneFact()}),
			frameObligations: []AnswerObligation{ObligationCount, ObligationEvidence, ObligationCoverage},
		},
		{
			basis: ServerStatusBasisCoverageDegraded,
			why:   "the engine's own coverage block declares it did not see everything it meant to",
			result: withPlan(nil, InvestigationResult{
				Status: InvestigationComplete, ClaimedFacts: oneFact(),
				Coverage: contractsv1.ContextFabricCoverage{DegradedReasons: []string{"a reason"}},
			}),
			frameObligations: baselineObligations(),
		},
		{
			basis: ServerStatusBasisLimitationDisclosed,
			why:   "a disclosed limitation, evaluated LAST because a limitation can be a note about scope rather than a gap",
			result: withPlan(nil, InvestigationResult{
				Status: InvestigationComplete, ClaimedFacts: oneFact(),
				Limitations: []string{"a limitation"},
			}),
			frameObligations: baselineObligations(),
		},
		{
			basis:            ServerStatusBasisServed,
			why:              "everything the frame demanded is present",
			result:           withPlan(nil, InvestigationResult{Status: InvestigationComplete, ClaimedFacts: oneFact()}),
			frameObligations: baselineObligations(),
		},
	}

	if len(cases) != ServerStatusBasisCount {
		t.Fatalf("%d fixtures for %d bases -- every basis needs one that LANDS in it", len(cases), ServerStatusBasisCount)
	}
	covered := map[ServerStatusBasis]bool{}
	for _, testCase := range cases {
		shadow := DeriveServerStatus(testCase.result, testCase.frameObligations)
		if shadow.Basis != testCase.basis {
			t.Errorf("fixture for %q landed on basis %q instead.\n  The fixture says: %s", testCase.basis, shadow.Basis, testCase.why)
		}
		if covered[testCase.basis] {
			t.Errorf("two fixtures declare basis %q; one basis is then untested", testCase.basis)
		}
		covered[testCase.basis] = true
		if shadow.Version != ServerStatusShadowVersion {
			t.Errorf("basis %q reported version %q -- every observation must name the rule that produced it, or two incomparable series get spliced", testCase.basis, shadow.Version)
		}
		if !ValidServerStatusBasis(shadow.Basis) {
			t.Errorf("basis %q is not a vocabulary member", shadow.Basis)
		}
	}
	for _, basis := range ServerStatusBasisVocabulary() {
		if !covered[basis] {
			t.Errorf("basis %q has no fixture that lands in it -- a closed token no input can produce is a dead tier that reads as green", basis)
		}
	}
}

// TestTheShadowMeasuresTheFrameNotThePlansCopiedFlag is the review finding,
// pinned as a property.
//
// The first version of this gate held the answer against the plan's
// RequireDrivers, which the plan copies from the family registry row -- the
// exact authority the design says a family may no longer be read for. So on
// the largest declared-loss case (a named-subject explain-drivers question,
// where the frame REQUIRES drivers and the registry row says they are not
// required) the shadow reported `served`. **It agreed with the production
// gap instead of measuring it.** A shadow that reads the production flag
// cannot see the production gap.
//
// This asserts the corrected direction on that exact case: plan flag FALSE,
// frame obligation PRESENT, empty driver set, and the shadow must call it.
func TestTheShadowMeasuresTheFrameNotThePlansCopiedFlag(t *testing.T) {
	t.Parallel()

	// The real registry value, READ rather than assumed: if it ever becomes
	// true this test stops probing the divergence it claims to probe, and
	// it must fail rather than pass vacuously.
	definition, ok := LookupQuestionFamily(QuestionFamilySubjectInvestigation)
	if !ok {
		t.Fatal("subject_investigation has no registry row")
	}
	if definition.RequireDrivers {
		t.Fatal("the registry row now REQUIRES drivers, so this test no longer exercises the plan-flag-vs-frame divergence it exists for")
	}

	plan := statusShadowPlan()
	plan.RequireDrivers = definition.RequireDrivers // false, as production copies it
	result := InvestigationResult{
		Status: InvestigationComplete, AnswerPlan: &plan,
		ClaimedFacts: oneFact(), Drivers: nil,
	}

	shadow := DeriveServerStatus(result, withDriverObligation())
	if shadow.Basis != ServerStatusBasisDriversAbsent {
		t.Fatalf("basis %q; the FRAME derived principal_drivers and the answer carries none, so the shadow must say so even though the plan flag is false", shadow.Basis)
	}
	if !shadow.Disagreed {
		t.Fatal("the shadow AGREED with a `complete` answer that omitted an operation the frame required -- the exact gap this gate exists to measure")
	}

	// The control. Without it, a shadow that flagged EVERYTHING would pass
	// the assertion above while measuring nothing.
	served := DeriveServerStatus(result, baselineObligations())
	if served.Basis != ServerStatusBasisServed {
		t.Fatalf("control basis %q; with no driver obligation the same answer must be `served`, or the gate flags regardless of the frame", served.Basis)
	}
}

// TestTheBasisOrderReportsTheMostSevereFailure pins the arm order.
func TestTheBasisOrderReportsTheMostSevereFailure(t *testing.T) {
	t.Parallel()
	plan := statusShadowPlan()
	plan.FactKinds = []contractsv1.ContextFabricFactKind{contractsv1.ContextFabricFactHealth}

	shadow := DeriveServerStatus(InvestigationResult{
		Status: InvestigationComplete, AnswerPlan: &plan,
		Coverage:    contractsv1.ContextFabricCoverage{DegradedReasons: []string{"a reason"}},
		Limitations: []string{"a limitation"},
	}, append(withDriverObligation(), ObligationRanking))

	if shadow.Basis != ServerStatusBasisNoClaimedFacts {
		t.Fatalf("basis %q; an answer with every failure at once must report the COARSEST one, not the first weak signal found", shadow.Basis)
	}
}

// TestADeclinedDerivationIsNotAnAgreement is the countability gate, over
// BOTH declined bases.
//
// A declined derivation means the server could not compute a verdict.
// Folding it into "agreed" would put a measurement failure into the
// numerator's complement and make the disagreement rate -- the number the
// flip is decided on -- quietly optimistic.
func TestADeclinedDerivationIsNotAnAgreement(t *testing.T) {
	t.Parallel()
	plan := statusShadowPlan()
	for _, declined := range []struct {
		name             string
		result           InvestigationResult
		frameObligations []AnswerObligation
	}{
		{"no plan", InvestigationResult{Status: InvestigationComplete}, baselineObligations()},
		{"no frame", InvestigationResult{Status: InvestigationComplete, AnswerPlan: &plan, ClaimedFacts: oneFact()}, nil},
	} {
		shadow := DeriveServerStatus(declined.result, declined.frameObligations)
		if shadow.Derived {
			t.Errorf("%s: reported Derived", declined.name)
		}
		if shadow.Disagreed {
			t.Errorf("%s: a declined derivation reported a disagreement", declined.name)
		}
		if shadow.ServerStatus != "" {
			t.Errorf("%s: produced server status %q -- a verdict it had no standing to reach", declined.name, shadow.ServerStatus)
		}
		counters := NewServerStatusCounters()
		counters.Observe(shadow)
		if counters.Observed != 1 || counters.Derived != 0 || counters.Disagreed != 0 {
			t.Errorf("%s: counters observed=%d derived=%d disagreed=%d, want 1/0/0", declined.name, counters.Observed, counters.Derived, counters.Disagreed)
		}
		for _, basis := range ServerStatusBasisVocabulary() {
			if _, present := counters.ByBasis[basis]; !present {
				t.Errorf("basis %q is absent from the distribution -- a distribution that omits its empty members cannot be told apart from one whose derivation never reaches them", basis)
			}
		}
	}
}

// TestANonCompleteModelStatusIsNeverContradicted pins the gate's direction.
func TestANonCompleteModelStatusIsNeverContradicted(t *testing.T) {
	t.Parallel()
	plan := statusShadowPlan()
	for _, status := range []InvestigationStatus{
		InvestigationPartial,
		contractsv1.ContextFabricInvestigationClarificationRequired,
		contractsv1.ContextFabricInvestigationNoMatch,
	} {
		shadow := DeriveServerStatus(InvestigationResult{
			Status: status, AnswerPlan: &plan, ClaimedFacts: oneFact(),
		}, withDriverObligation())
		if shadow.Disagreed {
			t.Errorf("model status %q was contradicted; the gate only ever contradicts `complete`", status)
		}
		if shadow.ServerStatus != status {
			t.Errorf("model status %q got server status %q; a non-complete status is agreed with, not overwritten", status, shadow.ServerStatus)
		}
		if shadow.Basis != ServerStatusBasisDriversAbsent {
			t.Errorf("model status %q reported basis %q; the BASIS is still reported on an agreement, or an operator cannot see why the two agreed", status, shadow.Basis)
		}
	}
}

// TestTheStatusShadowDoesNotTouchTheServedDocument is the "reports, does not
// route" property, asserted rather than claimed.
func TestTheStatusShadowDoesNotTouchTheServedDocument(t *testing.T) {
	t.Parallel()
	plan := statusShadowPlan()
	result := InvestigationResult{
		Status: InvestigationComplete, AnswerPlan: &plan, ClaimedFacts: oneFact(),
	}

	before := ComputeAnswerCompleteness(result)
	shadow := DeriveServerStatus(result, withDriverObligation())
	after := ComputeAnswerCompleteness(result)

	if !shadow.Disagreed {
		t.Fatal("the fixture must DISAGREE, or this test proves nothing about a disagreement leaving the document alone")
	}
	if result.Status != InvestigationComplete {
		t.Fatalf("the served status moved to %q", result.Status)
	}
	// reflect.DeepEqual, not !=: the block carries the outcome set and is
	// no longer comparable. Same assertion -- nothing in it moved.
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("the completeness block moved: %+v -> %+v", before, after)
	}
	if after.TerminalStatus != InvestigationComplete {
		t.Fatalf("completeness.terminal_status is %q; it must still mirror the MODEL status, which is the field consumers branch on", after.TerminalStatus)
	}
	if after.TerminalReason != "" {
		t.Fatalf("terminal_reason moved to %q; answerTerminalReason is untouched by this slice", after.TerminalReason)
	}
}

// TestTheEmptyRequiredDriverSetIsCountedOverTheCorpus is the measurement the
// ruling asked for: how often a served answer would carry an empty driver
// set that the FRAME required.
//
// STRUCTURAL, not a production reading -- no answer has been served through
// the changed gate yet. What it establishes is the size of the population
// the production counter will measure over, so the first real number has
// something to sit against and "the counter never fired" stays
// distinguishable from "nothing disagreed".
//
// The plan in each row is built the way PRODUCTION builds it: the flag
// copied from the projected family's own registry row, whatever it says.
// That is what makes the count a statement about the real gap rather than
// about a fixture.
func TestTheEmptyRequiredDriverSetIsCountedOverTheCorpus(t *testing.T) {
	t.Parallel()
	frames := generateFrames(t)
	counters := NewServerStatusCounters()
	framesRequiringDrivers, flagWouldHaveMissed := 0, 0

	for _, generated := range frames {
		if !generated.frame.HasObligation(ObligationPrincipalDrivers) {
			continue
		}
		framesRequiringDrivers++
		projection := DeriveQuestionFamily(generated.frame)
		definition, ok := LookupQuestionFamily(projection.Family)
		if !ok {
			t.Fatalf("projected family %q has no registry row", projection.Family)
		}
		if !definition.RequireDrivers {
			flagWouldHaveMissed++
		}
		plan := AnswerPlan{Family: projection.Family, RequireDrivers: definition.RequireDrivers}
		counters.Observe(DeriveServerStatus(InvestigationResult{
			Status: InvestigationComplete, AnswerPlan: &plan,
			ClaimedFacts: oneFact(), Drivers: nil,
		}, generated.frame.Obligations))
	}

	if framesRequiringDrivers == 0 {
		t.Fatal("no frame in the corpus derives principal_drivers; this measurement is empty")
	}
	flagged := counters.ByBasis[ServerStatusBasisDriversAbsent]
	if flagged != framesRequiringDrivers {
		t.Errorf("%d of %d frames requiring drivers were flagged; every one must be, since none of them served a driver", flagged, framesRequiringDrivers)
	}
	if flagged != counters.Disagreed {
		t.Errorf("%d flagged on the drivers basis but %d disagreements recorded; the two must agree on this population", flagged, counters.Disagreed)
	}
	if flagWouldHaveMissed == 0 {
		t.Error("no frame requiring drivers sits on a family whose plan flag is false -- then this corpus cannot exhibit the gap the fix exists for, and the measurement below is not the one that was asked for")
	}
	t.Logf("EMPTY REQUIRED DRIVER SET over the corpus: %d of %d frames derive principal_drivers; ALL %d are flagged by the shadow on a served answer carrying none; %d of those sit on a family whose PLAN FLAG says drivers are not required -- those %d are the ones the previous version of this gate reported as `served`",
		framesRequiringDrivers, len(frames), flagged, flagWouldHaveMissed, flagWouldHaveMissed)
}

// TestEveryObligationDeclaresWhetherTheShadowObservesIt is the totality
// gate on the observation declaration.
//
// The derivation checked two obligations by name out of thirteen, and every
// other one fell through to `served` -- so eleven could be required by a
// frame, absent from the answer, and reported complete. Review found one of
// them. Adding an arm for that one would have left the other ten.
//
// The declaration must cover the CLOSED VOCABULARY, so an obligation added
// later cannot default into "silently served": this fails until someone
// says which it is.
func TestEveryObligationDeclaresWhetherTheShadowObservesIt(t *testing.T) {
	t.Parallel()
	observed, unobservable := 0, 0
	for _, obligation := range AnswerObligationVocabulary() {
		observation, declared := obligationObservations[obligation]
		if !declared {
			t.Errorf("obligation %q has no observation declaration -- it would default into `served` without anyone deciding that", obligation)
			continue
		}
		if observation.field == "" {
			t.Errorf("obligation %q declares neither a result field nor a reason there is none", obligation)
		}
		if observation.observed {
			observed++
		} else {
			unobservable++
		}
	}
	for obligation := range obligationObservations {
		if !ValidAnswerObligation(obligation) {
			t.Errorf("obligationObservations declares %q, which is not a vocabulary member -- a stale entry reads like a covered obligation", obligation)
		}
	}
	if observed+unobservable != AnswerObligationCount {
		t.Fatalf("%d observed + %d unobservable != %d obligations", observed, unobservable, AnswerObligationCount)
	}
	// Both partitions must be non-empty, or the declaration is decorative:
	// all-observed would mean the unobservable basis is dead, and
	// all-unobservable would mean the gate never measures anything.
	if observed == 0 || unobservable == 0 {
		t.Fatalf("declaration is one-sided: %d observed, %d unobservable", observed, unobservable)
	}
	t.Logf("OBSERVATION DECLARATION: %d of %d obligations observed, %d REPORTED as unobservable by this instrument", observed, AnswerObligationCount, unobservable)
}

// TestAnUnobservableRequiredObligationIsNeverCalledServed is review finding
// R3-b, pinned.
//
// A frame requiring `count` has no arm to fall into, so every predicate
// reached the end and the shadow claimed `served` -- asserting an answer
// complete on the strength of not having looked.
func TestAnUnobservableRequiredObligationIsNeverCalledServed(t *testing.T) {
	t.Parallel()
	plan := AnswerPlan{Family: QuestionFamilyDiscoveredCohortRanking}
	result := InvestigationResult{
		Status: InvestigationComplete, AnswerPlan: &plan, ClaimedFacts: oneFact(),
	}

	shadow := DeriveServerStatus(result, []AnswerObligation{ObligationCount, ObligationEvidence, ObligationCoverage})
	if shadow.Basis == ServerStatusBasisServed {
		t.Fatal("a frame requiring `count` -- which this derivation cannot observe -- was reported `served`")
	}
	if shadow.Basis != ServerStatusBasisUnobservable {
		t.Fatalf("basis %q, want %q", shadow.Basis, ServerStatusBasisUnobservable)
	}
	if shadow.Derived {
		t.Fatal("an unobservable obligation produced a DERIVED verdict; it is a limit of the instrument, not a judgement about the answer")
	}
	if shadow.Disagreed {
		t.Fatal("an unobservable obligation was counted as a disagreement")
	}

	// The control: with only observable obligations, the same answer IS
	// served. Without it, a derivation that declined on everything would
	// pass the assertions above while measuring nothing.
	served := DeriveServerStatus(result, []AnswerObligation{ObligationEvidence, ObligationCoverage})
	if served.Basis != ServerStatusBasisServed {
		t.Fatalf("control basis %q; with only observable obligations the same answer must be `served`", served.Basis)
	}
}

// TestNewlyObservableObligationsOverTheDriverPopulation reports the number
// the ruling asked for.
func TestNewlyObservableObligationsOverTheDriverPopulation(t *testing.T) {
	t.Parallel()
	frames := generateFrames(t)
	driverFrames, gainedAnObservable := 0, 0
	newlyObservable := []AnswerObligation{
		ObligationRemainingWork, ObligationReadiness, ObligationEvidence, ObligationCoverage,
	}
	perObligation := map[AnswerObligation]int{}

	for _, generated := range frames {
		if !generated.frame.HasObligation(ObligationPrincipalDrivers) {
			continue
		}
		driverFrames++
		gained := false
		for _, obligation := range newlyObservable {
			if generated.frame.HasObligation(obligation) {
				perObligation[obligation]++
				gained = true
			}
		}
		if gained {
			gainedAnObservable++
		}
	}
	if driverFrames == 0 {
		t.Fatal("no driver-requiring frames; the measurement is empty")
	}
	t.Logf("NEWLY OBSERVABLE over the %d driver-requiring frames: %d gain at least one newly observed obligation. Per obligation: remaining_work=%d readiness=%d evidence=%d coverage=%d",
		driverFrames, gainedAnObservable,
		perObligation[ObligationRemainingWork], perObligation[ObligationReadiness],
		perObligation[ObligationEvidence], perObligation[ObligationCoverage])
}

// TestEnforcedVersusReportedIsStated is the table the stop rule requires.
//
// Round 3 found a classifier arm over-claiming for the third time across
// three rounds. The ruling was: do not patch it a fourth time -- narrow the
// classifier to what its negative controls prove, and LIST the over-claims.
// This is that list, as an executable statement rather than a paragraph, so
// a claim added later without an entry fails.
func TestEnforcedVersusReportedIsStated(t *testing.T) {
	t.Parallel()
	type claim struct {
		subject  string
		enforced string
		reported string
	}
	claims := []claim{
		{
			subject:  "precedence_comparison_row",
			enforced: "the precedence comparison row fired and the projection read the topology instead",
			reported: "WHICH of the row's two conditions fired -- a >=2 distinct-subject-term count (behaviour change B6) or a single non-empty comparison term -- is NOT distinguished. Distinguishing them needs the sample's terms, which are free-text model output and never reach a telemetry field, so this is a permanent limit of the class rather than an unfixed defect.",
		},
		{
			subject:  "status shadow obligation coverage",
			enforced: "six of the thirteen obligations are checked against a named result field",
			reported: "the other seven are declared unobservable and reported as such; a frame requiring one of them yields `unobservable_obligation`, never `served`.",
		},
		{
			subject:  "projection topology losses",
			enforced: "every (family, condition) topology mismatch in the corpus is enumerated and declared",
			reported: "none is re-routed. Changing which family a question reaches is a routing decision belonging to the slice that performs the flip.",
		},
		{
			subject:  "laws L7 and L8",
			enforced: "asserted over the surfaces this change adds, where they hold because those surfaces recompute no completeness flag and validate no model-emitted reference",
			reported: "their real counterexamples live in files this change does not touch and are owned by other work; the reported table names each site.",
		},
	}
	if len(claims) == 0 {
		t.Fatal("the enforced-versus-reported table is empty")
	}
	for _, c := range claims {
		if c.enforced == "" || c.reported == "" {
			t.Errorf("claim %q states only one side; a claim with no reported limit is the over-claim this table exists to prevent", c.subject)
		}
	}
	t.Logf("enforced-versus-reported: %d claims, each stating both what is proven and what is only reported", len(claims))
}
