package contextfabric

// The B8 SHADOW GATE on `status`: a server-derived terminal status computed
// beside the model-authored one, with the fact that drove it named.
//
// IT REPORTS. IT DOES NOT ROUTE. `result.Status` is still copied from the
// synthesis draft and still mirrored into `completeness.terminal_status`
// exactly as today; `answerTerminalReason` is untouched. Nothing in this
// file is read by a decision, and the served document is byte-identical
// with and without it.
//
// WHY A SHADOW AND NOT THE FLIP. T6 moves `status` authorship from the
// MODEL to the SERVER. `status` is a REQUIRED wire field, consumers branch
// on `partial` versus `complete`, and it is the FIRST field of the
// comparison projection every rig tally is scored on. The ruling on it is
// recorded and is deliberately not a design decision: server authorship is
// not a strike-three violation in kind -- a status is a state label, not
// language -- but it moves on measured disagreement, never on an assertion.
// This is the instrument; the flip is decided later, on what it says, with
// the owner ruling if the rate is material.
//
// WHAT THIS DERIVATION IS NOT. T6's real rule derives completeness from
// requirement OUTCOMES, and `PlanRequirementOutcome` does not exist yet --
// it is a later slice's type. So this is NOT T6's derivation and does not
// claim to be. It is the closest server-side statement available at this
// slice: did the served document carry what the FRAME's derived obligations
// demanded of it. When the outcome type lands, this derivation is replaced
// by the real one and the series restarts under a new version.
//
// IT MEASURES AGAINST THE FRAME, NOT THE PLAN'S FLAGS, and that correction
// came from review. The first version held the answer against the plan's
// RequireDrivers/RequireRanking -- which the plan copies from the family
// registry row. Those flags are precisely the authority the design says a
// family may no longer be read for, because a family understates a composed
// question. So the shadow AGREED WITH THE DEFECT instead of measuring it:
// an answer with an empty driver set, for a frame that derived
// `principal_drivers`, was reported `served` because the registry row said
// drivers were not required. A shadow that reads the production flag cannot
// see the production gap.
//
// The frame's obligation set is the demand the question actually made, and
// it is what a later authorship flip would serve -- so it is the only thing
// a disagreement rate can be measured against and still mean something.
//
// HOW THE OBLIGATIONS GET HERE, since the plumbing was the reason the first
// version read the wrong thing. They ride `QuestionFamilyOutcome`, which is
// INTERNAL to this package (not a wire alias) and is already threaded from
// interpretation through to finalization. No contract surface, no consumer
// pin, no interface widened. Widening the answer plan would have been a
// contract change; widening the interpreter interface would have been a
// shipped-surface change; neither is acceptable for a shadow measurement.

// ServerStatusBasis names the FACT that drove the server-derived status.
// Closed vocabulary, telemetry-safe: no question text, no subject, no
// model prose.
//
// The basis is the point of the whole gate. "The two disagreed 4% of the
// time" is not something anyone can act on; "the two disagreed 4% of the
// time, and in every case the server saw a plan demanding drivers that the
// answer did not carry" is. Each member below names a check that can fail,
// and each has a fixture that lands in it.
type ServerStatusBasis string

const (
	// ServerStatusBasisServed: everything the plan demanded is present in
	// the served document. The server says complete.
	ServerStatusBasisServed ServerStatusBasis = "served"

	// ServerStatusBasisNoClaimedFacts: the plan named fact kinds to read
	// and the answer carries no claimed fact at all. The strongest signal
	// available here -- an answer with no facts cannot be complete about
	// anything the plan planned to read.
	ServerStatusBasisNoClaimedFacts ServerStatusBasis = "no_claimed_facts"

	// ServerStatusBasisDriversAbsent: the plan REQUIRES drivers (not
	// merely attempts them) and the answer carries none.
	ServerStatusBasisDriversAbsent ServerStatusBasis = "required_drivers_absent"

	// ServerStatusBasisRankingAbsent: the plan REQUIRES a ranked cohort
	// and the answer carries no cohort to have ranked.
	ServerStatusBasisRankingAbsent ServerStatusBasis = "required_ranking_absent"

	// ServerStatusBasisRemainingWorkAbsent: the FRAME derives remaining_work
	// and the answer lists none.
	ServerStatusBasisRemainingWorkAbsent ServerStatusBasis = "required_remaining_work_absent"

	// ServerStatusBasisReadinessAbsent: the FRAME derives readiness and the
	// answer lists no readiness gap.
	ServerStatusBasisReadinessAbsent ServerStatusBasis = "required_readiness_absent"

	// ServerStatusBasisCoverageDegraded: the coverage block declares a
	// degraded reason. The engine's own disclosure that it did not see
	// everything it meant to.
	ServerStatusBasisCoverageDegraded ServerStatusBasis = "coverage_degraded"

	// ServerStatusBasisLimitationDisclosed: the answer discloses a
	// limitation. Weaker than the four above and evaluated last, because a
	// limitation can be a note about scope rather than a gap.
	ServerStatusBasisLimitationDisclosed ServerStatusBasis = "limitation_disclosed"

	// ServerStatusBasisUnobservable: the frame requires an obligation this
	// derivation CANNOT observe in the served result, and everything it can
	// observe is satisfied.
	//
	// THE HONEST ANSWER TO "did the answer carry what the frame demanded",
	// and it is a DECLINED derivation rather than a verdict. Review found
	// the shadow reporting `served` for a frame requiring `count`: no arm
	// checks a count, so every predicate fell through to "everything is
	// present". That is the shadow asserting an answer complete on the
	// strength of not having looked.
	//
	// Reporting the inability is the difference between a measurement and a
	// guess, and it keeps the disagreement rate honest in the direction that
	// matters: an unobservable obligation must never be counted as
	// agreement, because the flip decision reads that rate.
	ServerStatusBasisUnobservable ServerStatusBasis = "unobservable_obligation"

	// ServerStatusBasisNoFrame: no validated frame reached finalization, so
	// there are no derived obligations to hold the answer against and the
	// server DECLINES.
	//
	// DISTINCT FROM no_plan, deliberately. A missing plan and a missing
	// frame are different failures with different remedies, and after the
	// shadow moved off the plan's flags onto the frame's obligations the
	// frame became the thing that can be absent. Collapsing them would put
	// two causes in one bucket in the distribution the flip is read from.
	ServerStatusBasisNoFrame ServerStatusBasis = "no_frame"

	// ServerStatusBasisNoPlan: no plan was stamped, so there is nothing to
	// hold the answer against and the server DECLINES to derive a status.
	//
	// Not "complete" and not "partial": a basis that cannot be computed
	// must be visible as such. Folding it into either verdict would put a
	// measurement failure into the disagreement rate, where it would read
	// as a fact about the model.
	ServerStatusBasisNoPlan ServerStatusBasis = "no_plan"
)

var serverStatusBases = [...]ServerStatusBasis{
	ServerStatusBasisNoPlan,
	ServerStatusBasisNoFrame,
	ServerStatusBasisUnobservable,
	ServerStatusBasisNoClaimedFacts,
	ServerStatusBasisDriversAbsent,
	ServerStatusBasisRankingAbsent,
	ServerStatusBasisRemainingWorkAbsent,
	ServerStatusBasisReadinessAbsent,
	ServerStatusBasisCoverageDegraded,
	ServerStatusBasisLimitationDisclosed,
	ServerStatusBasisServed,
}

// ServerStatusBasisCount is the closed vocabulary's size.
const ServerStatusBasisCount = len(serverStatusBases)

// ServerStatusBasisVocabulary returns the closed vocabulary in the order
// DeriveServerStatus evaluates it -- coarsest signal first, weakest last --
// so a test asserts the evaluation order against the vocabulary rather than
// against a copied list.
func ServerStatusBasisVocabulary() [ServerStatusBasisCount]ServerStatusBasis {
	return serverStatusBases
}

// ValidServerStatusBasis reports membership. The empty value is not a
// member: every derivation names a basis, because the derivation is total.
func ValidServerStatusBasis(value ServerStatusBasis) bool {
	for _, member := range serverStatusBases {
		if member == value {
			return true
		}
	}
	return false
}

// obligationObservation declares, for ONE obligation, whether this
// derivation can observe it in a served result.
type obligationObservation struct {
	// observed is whether a served result exposes this obligation at all.
	observed bool
	// field names the result field consulted, or the reason there is none.
	field string
}

// obligationObservations is the declaration, and it is asserted TOTAL over
// the closed obligation vocabulary by
// TestEveryObligationDeclaresWhetherTheShadowObservesIt.
//
// WHY A DECLARATION AND NOT MORE ARMS. The derivation checked two
// obligations by name out of thirteen, and every other one fell through to
// `served` -- so eleven obligations could be required by a frame, absent
// from the answer, and reported as complete. Review found one of them
// (`count`); adding an arm for `count` would have left the other ten, which
// is the per-instance fix this branch has now produced three times.
//
// Stating it as a declaration means an obligation added to the vocabulary
// later cannot default into "silently served": the totality test fails
// until someone says which it is. The unobservable ones are a REPORTED
// limit of this instrument, not a claim that nothing is missing.
//
// `trend_series` is deliberately unobservable even though a claimed fact
// can carry time-series rows: that pair is outside this slice's boundary,
// and adding a consumer of it here is a coupling this PR is not permitted
// to introduce. Named rather than silently omitted.
var obligationObservations = map[AnswerObligation]obligationObservation{
	ObligationPrincipalDrivers: {true, "result.Drivers"},
	ObligationRanking:          {true, "result.Cohort"},
	ObligationRemainingWork:    {true, "result.RemainingWork"},
	ObligationReadiness:        {true, "result.ReadinessGaps"},
	ObligationEvidence:         {true, "result.ClaimedFacts"},
	ObligationCoverage:         {true, "result.Coverage"},

	ObligationState:               {false, "no distinct result field -- the state is prose, and judging it would mean modelling what synthesis should have said"},
	ObligationCompletion:          {false, "no distinct result field"},
	ObligationHealth:              {false, "no distinct result field -- the health reading is carried inside the state prose"},
	ObligationCount:               {false, "no distinct result field -- a cardinality is carried in the answer text, not in a countable field"},
	ObligationAllocationBreakdown: {false, "carried inside claimed-fact rows, which this derivation does not interpret"},
	ObligationTrendSeries:         {false, "would require reading the time-series pair, which is outside this slice's boundary"},
	ObligationPeriodDelta:         {false, "no distinct result field"},
}

// unobservedRequiredObligation returns the first required obligation this
// derivation cannot observe, in vocabulary order so the answer is stable.
func unobservedRequiredObligation(required map[AnswerObligation]bool) (AnswerObligation, bool) {
	for _, obligation := range AnswerObligationVocabulary() {
		if !required[obligation] {
			continue
		}
		if observation, declared := obligationObservations[obligation]; !declared || !observation.observed {
			return obligation, true
		}
	}
	return "", false
}

// ServerStatusShadowVersion identifies THIS derivation.
//
// It is reported on every observation because the derivation is explicitly
// a placeholder for T6's requirement-outcome rule, and a disagreement rate
// measured under one rule may not be compared with one measured under
// another. Without the version, replacing the rule would silently splice
// two incomparable series together and the flip would be decided on the
// join.
//
// Bumped from `plan-demands.v1` when the derivation moved off the plan's
// registry-copied flags onto the frame's own derived obligations. A rate
// measured under the old rule is NOT comparable with one measured under
// this one -- the old rule could not see the case this one exists to count
// -- so the two series must never be spliced, and the version is what makes
// the splice visible.
const ServerStatusShadowVersion = "status-shadow.frame-obligations.v2"

// ServerStatusShadow is ONE observation: what the model said, what the
// server would say, why, and whether they differ.
type ServerStatusShadow struct {
	// ModelStatus is the status the served document actually carries --
	// copied from the synthesis draft, as today. THIS IS THE ONE THAT IS
	// SERVED.
	ModelStatus InvestigationStatus
	// ServerStatus is what the server would have said. Empty when the
	// basis is no_plan: a declined derivation has no verdict, and a zero
	// value that reads like a status is how a measurement failure becomes
	// a finding about the model.
	ServerStatus InvestigationStatus
	// Basis names the fact that drove ServerStatus.
	Basis ServerStatusBasis
	// Derived is whether a server status was computed at all.
	Derived bool
	// Disagreed is true only when a status WAS derived and differs from
	// the model's. A declined derivation is never a disagreement.
	Disagreed bool
	// Version identifies the derivation rule.
	Version string
}

// DeriveServerStatus computes the shadow status for one final result.
//
// PURE: reads the result and mutates nothing, exactly like
// ComputeAnswerCompleteness beside which it runs.
//
// THE ARM ORDER IS THE RULE, and it runs from the coarsest failure to the
// weakest, so the basis reported is the most SEVERE thing wrong rather than
// the first thing checked. An answer with no facts at all AND a disclosed
// limitation is reported as having no facts: telling an operator about the
// limitation would name the smaller of two problems.
//
// A NON-COMPLETE MODEL STATUS IS NOT SECOND-GUESSED. Where the model
// already said something other than `complete`, the server agrees with it
// and reports the basis it found -- the gate exists to find answers the
// model called COMPLETE that the plan's own demands say were not, which is
// the direction T6's authorship move actually changes. Manufacturing a
// disagreement in the other direction would measure this derivation's
// opinion about clarifications and refusals, which it has no standing to
// hold.
func DeriveServerStatus(result InvestigationResult, frameObligations []AnswerObligation) ServerStatusShadow {
	shadow := ServerStatusShadow{
		ModelStatus: result.Status,
		Version:     ServerStatusShadowVersion,
	}

	plan := result.AnswerPlan
	if plan == nil {
		// No plan, no fact kinds named, nothing to hold the answer against.
		shadow.Basis = ServerStatusBasisNoPlan
		return shadow
	}
	if len(frameObligations) == 0 {
		// No validated frame: the obligations this derivation measures
		// against do not exist. DECLINED, not "nothing was required" --
		// treating an absent frame as an absence of demands would report
		// `served` for every interpretation that failed frame validation,
		// which is the population most likely to be incomplete.
		shadow.Basis = ServerStatusBasisNoFrame
		return shadow
	}

	shadow.Basis = serverStatusBasis(result, *plan, frameObligations)
	if shadow.Basis == ServerStatusBasisUnobservable {
		// DECLINED, not a verdict: the derivation could not see what the
		// frame required. Counting it either way would put a limit of the
		// instrument into the rate the flip is decided on.
		return shadow
	}
	shadow.Derived = true
	if shadow.Basis == ServerStatusBasisServed {
		shadow.ServerStatus = InvestigationComplete
	} else {
		shadow.ServerStatus = InvestigationPartial
	}

	// Only a model `complete` can be contradicted. See the header.
	if result.Status == InvestigationComplete {
		shadow.Disagreed = shadow.ServerStatus != result.Status
	} else {
		shadow.ServerStatus = result.Status
	}
	return shadow
}

// serverStatusBasis holds the answer against the FRAME's derived
// obligations, not against the plan's flags.
//
// THE DIFFERENCE IS THE WHOLE POINT, and review found it the hard way. The
// plan's RequireDrivers/RequireRanking are copied from the family registry
// row, and the design's own position is that those flags are the wrong
// authority -- they are on the list of things the family may no longer be
// read for, precisely because a family understates a composed question.
// Measuring the served answer against them made this shadow agree with the
// production defect instead of measuring it: an answer with an empty driver
// set, for a frame that DERIVED `principal_drivers`, was reported `served`
// because the registry row said drivers were not required.
//
// The frame's obligation set is the demand the question actually made. That
// is what a later authorship flip would serve, so it is what the
// disagreement rate has to be measured against for the rate to mean
// anything.
//
// `FactKinds` still comes from the plan: it is the READ plan, a server
// derivation rather than a family flag, and the frame has no equivalent.
func serverStatusBasis(result InvestigationResult, plan AnswerPlan, frameObligations []AnswerObligation) ServerStatusBasis {
	required := make(map[AnswerObligation]bool, len(frameObligations))
	for _, obligation := range frameObligations {
		required[obligation] = true
	}
	if len(plan.FactKinds) > 0 && len(result.ClaimedFacts) == 0 {
		return ServerStatusBasisNoClaimedFacts
	}
	if required[ObligationPrincipalDrivers] && len(result.Drivers) == 0 {
		return ServerStatusBasisDriversAbsent
	}
	if required[ObligationRanking] && result.Cohort == nil {
		return ServerStatusBasisRankingAbsent
	}
	if required[ObligationRemainingWork] && len(result.RemainingWork) == 0 {
		return ServerStatusBasisRemainingWorkAbsent
	}
	if required[ObligationReadiness] && len(result.ReadinessGaps) == 0 {
		return ServerStatusBasisReadinessAbsent
	}
	if len(result.Coverage.DegradedReasons) > 0 {
		return ServerStatusBasisCoverageDegraded
	}
	if len(result.Limitations) > 0 {
		return ServerStatusBasisLimitationDisclosed
	}
	// LAST, and deliberately after every observable check: `served` may only
	// be claimed once nothing REQUIRED is unobservable. Everything this
	// derivation can see is satisfied; if the frame also demanded something
	// it cannot see, the honest answer is that it could not tell.
	if obligation, unobserved := unobservedRequiredObligation(required); unobserved {
		_ = obligation
		return ServerStatusBasisUnobservable
	}
	return ServerStatusBasisServed
}

// ServerStatusCounters tallies the shadow over a population.
//
// Every basis carries a count INCLUDING THE ZEROES, on the same rule the
// family-agreement counters follow: a distribution that omits its empty
// members cannot be told apart from one whose derivation never reaches
// them.
type ServerStatusCounters struct {
	// Observed is every result offered. The denominator.
	Observed int
	// Derived is how many produced a server status at all.
	Derived int
	// Disagreed is how many derived statuses differed from the model's.
	Disagreed int
	// ByBasis counts every basis in the closed vocabulary.
	ByBasis map[ServerStatusBasis]int
}

// NewServerStatusCounters returns counters with every basis at zero.
func NewServerStatusCounters() *ServerStatusCounters {
	counters := &ServerStatusCounters{ByBasis: make(map[ServerStatusBasis]int, ServerStatusBasisCount)}
	for _, basis := range serverStatusBases {
		counters.ByBasis[basis] = 0
	}
	return counters
}

// Observe records one shadow.
func (c *ServerStatusCounters) Observe(shadow ServerStatusShadow) {
	c.Observed++
	if shadow.Derived {
		c.Derived++
	}
	if shadow.Disagreed {
		c.Disagreed++
	}
	c.ByBasis[shadow.Basis]++
}
