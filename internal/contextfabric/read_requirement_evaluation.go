package contextfabric

import (
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The read-requirement evaluator: what the EVIDENCE says became of each thing
// the answer planned to read.
//
// THE DEFECT IT CLOSES. seedRequirementOutcomes mints `satisfied` from
// SERVEABILITY -- DerivedRequirement.Served() is `Unavailable == ""`, which
// says the registry declares a producer, not that anything was read. Nothing
// appended an assembled-result row for a READ requirement, so that planning
// -stage seed could be the LAST row an identity ever got, and a set of nothing
// but seeds derives `complete`. An answer whose every read failed could report
// the strongest completeness the vocabulary has.
//
// WHAT IT IS NOT. It does not decide whether a requirement COULD be served --
// the derivation already did that, and re-deciding it here is how two
// authorities for one fact begin. It reads what the turn actually observed and
// says what that implies, for requirements the derivation already called
// servable reads.
//
// THE INPUT IS THE PUBLISHED PLAN ARRAY, not a third derivation. The rows are
// stamped where the plan is created and arrive here on the plan finalization
// carries, so reading them cannot become a second opinion about what the
// requirements ARE -- the same rule SeedOutcomesFromPublishedPlanRequirements
// states for the gap fill. A third DeriveRequirements call would be a third
// evaluation point; two are already one more than the design wanted.

// readEvidence is one read requirement's observed evidence, counted.
//
// The counters are over the requirement's DECLARED fact kinds intersected with
// what the turn actually observed. Kinds the plan never asked for contribute
// nothing in either direction: planFactKinds derives the planned set from the
// family, the interpretation and (since the computed-step-inputs change) the
// computed rows' declared inputs -- never from a read requirement's own
// FactKinds -- so a requirement declaring six kinds on a turn that planned one
// is ORDINARY, not degraded, and counting the five absent kinds as losses
// would report a loss on almost every answer.
type readEvidence struct {
	// Observed is how many of the requirement's declared kinds produced a
	// coverage observation at all.
	Observed int
	// Served is how many produced USABLE evidence: available or stale.
	Served int
	// Truncated, Pruned and Failed partition the rest, and they are kept
	// apart rather than summed because they name different causes and a
	// reader acts on the cause, not on the total.
	Truncated int
	Pruned    int
	Failed    int
	// Cause is the coverage code the worst observation implies, by the
	// precedence table below. Empty when nothing was observed.
	Cause contractsv1.ContextFabricCoverageDetailCode
}

// canonicalFactSourcePrefix is how the fact registry names a canonical-fact
// observation. Declared once here rather than repeated, and asserted against
// the producer by a test, because a prefix that drifts silently turns every
// lookup below into a miss -- which would read as "nothing was observed" and
// mark every read requirement unavailable.
const canonicalFactSourcePrefix = "canonical_fact:"

// evaluateReadRequirement counts what the turn observed for ONE read
// requirement.
//
// Only canonical-fact observations are read. A graph source describes
// retrieval, not a declared fact read, and attributing one to a read
// requirement would be the wrong attribution appendProjectionOutcomes already
// refuses to make.
func evaluateReadRequirement(requirement contractsv1.ContextFabricPlanRequirement, coverage Coverage) readEvidence {
	states := make(map[FactKind]SourceState, len(requirement.FactKinds))
	for _, observation := range coverage.Sources {
		kind, ok := canonicalFactKindOf(observation.Source)
		if !ok {
			continue
		}
		// WORST STATE WINS when one kind is observed more than once.
		// A kind read twice -- once served, once failed -- has a failure
		// to disclose, and taking the first or the last observation would
		// make the row depend on the order the merge happened to produce.
		if previous, seen := states[kind]; seen && sourceStateSeverity(previous) >= sourceStateSeverity(observation.State) {
			continue
		}
		states[kind] = observation.State
	}

	evidence := readEvidence{}
	worst := 0
	for _, kind := range requirement.FactKinds {
		state, seen := states[kind]
		if !seen {
			continue
		}
		evidence.Observed++
		switch {
		case state == SourceAvailable || state == SourceStale:
			evidence.Served++
		case state == SourceTruncated:
			evidence.Truncated++
		case state == SourcePruned:
			evidence.Pruned++
		default:
			evidence.Failed++
		}
		if severity := sourceStateSeverity(state); severity > worst {
			worst = severity
			evidence.Cause = readCoverageCauseFor(state)
		}
	}
	return evidence
}

// canonicalFactKindOf returns the fact kind a coverage source names, and
// whether the source is a canonical-fact observation at all.
func canonicalFactKindOf(source string) (FactKind, bool) {
	rest, found := strings.CutPrefix(source, canonicalFactSourcePrefix)
	if !found || rest == "" {
		return "", false
	}
	return FactKind(rest), true
}

// sourceStateSeverity orders the source states from "served in full" to
// "nothing came back", so a requirement observed several times reports its
// WORST observation rather than its most recent.
//
// A TABLE, and total over the vocabulary by a test rather than by a default
// arm: an unranked state would sort as 0 and silently read as fully served,
// which is the one direction this ordering must never fail in.
func sourceStateSeverity(state SourceState) int {
	switch state {
	case SourceAvailable:
		return 0
	case SourceStale:
		return 1
	case SourceTruncated:
		return 2
	case SourceNotApplicable:
		return 3
	case SourceNoData:
		return 4
	case SourceConflicted:
		return 5
	case SourcePruned:
		return 6
	case SourceUnauthorized:
		return 7
	case SourceUnavailable:
		return 8
	case SourceUnconfigured:
		return 9
	}
	// An unranked member is a gap in this table, and the safe reading of a
	// state nobody ranked is "worse than everything ranked" -- never
	// "fully served". The vocabulary-totality test is what keeps this
	// branch unreachable; the value is what keeps it harmless if it is not.
	return 10
}

// readCoverageCauseFor maps an observed source state onto the shipped
// coverage-detail vocabulary.
//
// EXPLICIT, never a pass-through, for the reason unavailableRequirementCause
// gives one layer up: the two vocabularies are owned by different layers, and
// a silent cast would let a new source state reach the wire as a coverage code
// that vocabulary never declared.
func readCoverageCauseFor(state SourceState) contractsv1.ContextFabricCoverageDetailCode {
	switch state {
	case SourceUnconfigured:
		return contractsv1.ContextFabricCoverageDetailFactUnconfigured
	case SourcePruned:
		return contractsv1.ContextFabricCoverageDetailFactPruned
	case SourceTruncated, SourceNoData, SourceUnavailable,
		SourceUnauthorized, SourceConflicted, SourceNotApplicable:
		// The PROVIDER said so. `fact_provider_reported` is the code the
		// fact registry itself mints for exactly these states, so a
		// requirement row and the coverage detail beside it name the same
		// mechanism rather than two.
		return contractsv1.ContextFabricCoverageDetailFactProviderReported
	}
	return ""
}

// readQuantifierThreshold is how many independent serving sources a read
// requirement's completion standard demands.
//
// `at_least_one` and `corroborated` are the only quantifiers a READ obligation
// carries: `exact` and `all` belong to the two computed obligations, and `none`
// to an unservable cell. The second return says whether the quantifier was
// recognised at all, so an unrecognised one is SKIPPED rather than defaulted to
// 1 -- a default here would silently lower a standard, which is the precise
// inversion the quantifier law exists to remove.
//
// Skipping is fail-closed in the right direction: the requirement keeps only
// its planning-stage seed, and the completeness derivation reads a
// planning-only read identity as `partial`, never `complete`. So a quantifier
// this function does not recognise costs an accurate CAUSE, never an honest
// STATE.
func readQuantifierThreshold(quantifier string) (int, bool) {
	switch quantifier {
	case string(CompletionQuantifierAtLeastOne):
		return 1, true
	case string(CompletionQuantifierCorroborated):
		return 2, true
	}
	return 0, false
}

// hasEvaluatedReadOutcome reports whether this requirement already carries an
// assembled-result row.
//
// finalizeResult runs again on the synthesis retry and again after stage 3
// narrows and re-finalizes, so without this the same requirement would collect
// a row per pass. The guard is the shape appendMembershipCardinality already
// uses for the count row, and the test that protects it counts the TOTAL number
// of assembled-result rows for the identity rather than asserting the expected
// one exists -- a test that counts only what it expects cannot detect a surplus.
func hasEvaluatedReadOutcome(rows []RequirementOutcomeRow, identity string) bool {
	for _, row := range rows {
		if row.Requirement == identity && row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
			return true
		}
	}
	return false
}

// appendReadRequirementEvaluations appends ONE assembled-result row per served
// READ requirement, saying what the evidence made of it.
//
// UNSERVABLE REQUIREMENTS ARE NOT EVALUATED. The derivation already attributed
// those cells to a closed reason and the seed already published it through
// unavailableRequirementCause; re-deriving that from evidence would be a second
// authority for a cell no producer can serve, and the evidence has nothing to
// say about a read that was never planned because nothing could serve it.
//
// COMPUTED REQUIREMENTS ARE NOT EVALUATED EITHER. `count` is answered by
// appendMembershipCardinality, and `ranking` has no evaluator yet -- a gap that
// is disclosed rather than covered here, because inventing an evidence reading
// for a step this function cannot observe would be worse than saying nothing.
func appendReadRequirementEvaluations(
	rows []RequirementOutcomeRow,
	published []contractsv1.ContextFabricPlanRequirement,
	coverage Coverage,
) []RequirementOutcomeRow {
	if len(published) == 0 {
		return rows
	}
	var added []RequirementOutcomeRow
	for _, requirement := range published {
		if requirement.Kind != string(ObligationKindRead) || !requirement.Served() {
			continue
		}
		if hasEvaluatedReadOutcome(rows, requirement.Requirement) {
			continue
		}
		threshold, known := readQuantifierThreshold(requirement.Quantifier)
		if !known {
			continue
		}
		row, ok := readRequirementOutcomeRow(requirement, threshold, evaluateReadRequirement(requirement, coverage))
		if !ok {
			continue
		}
		added = append(added, row)
	}
	return appendOutcomeRows(rows, added...)
}

// readRequirementOutcomeRow turns one requirement's counted evidence into its
// outcome row. The second return is false where no row is emitted.
//
// THE COUNTS. Declared is max(observed, threshold) and Served is the number of
// declared kinds that came back available or stale. Two things have to be true
// at once and neither counting rule alone gives both: counting the whole
// declared catalogue would report a loss on every turn that planned fewer kinds
// than a requirement declares (the ordinary case), while counting only what was
// observed could not express a source SHORTFALL -- a `corroborated` requirement
// that planned one kind and got it would read 1/1, and the only outcome legal
// at 1/1 is `satisfied`, which is the standard silently lowered. Raising
// Declared to the standard's own demand makes `narrowed 1/2` both legal and
// true, and every arm below keeps Served < Declared wherever it claims
// `narrowed`, which the row validator requires.
func readRequirementOutcomeRow(
	requirement contractsv1.ContextFabricPlanRequirement,
	threshold int,
	evidence readEvidence,
) (RequirementOutcomeRow, bool) {
	// TEMPORARY, and it is the one hole in this change.
	//
	// A requirement none of whose declared kinds was read at all has no
	// truthful cause in the shipped coverage vocabulary: every member
	// describes something that happened TO a read, and here no read
	// happened -- while `answer_terminated_before_attempt` says the turn
	// ENDED first, which is a different thing with a different remedy. The
	// honest code is minted in this branch's last commits, after the
	// count-population change lands, because both edit the same closed
	// array and one edit after theirs is safer than two racing.
	//
	// Until then the requirement keeps only its planning-stage seed, and
	// the completeness derivation reads a planning-only READ identity as
	// `partial`. So this interim loses the CAUSE and never the honesty of
	// the STATE. A reach probe fails if this branch stops executing, so it
	// cannot quietly become permanent, and that probe is INVERTED in the
	// commit that mints the code.
	if evidence.Observed == 0 {
		return RequirementOutcomeRow{}, false
	}

	declared := evidence.Observed
	if threshold > declared {
		declared = threshold
	}
	row := RequirementOutcomeRow{
		Stage:       contractsv1.ContextFabricOutcomeStageAssembledResult,
		Requirement: requirement.Requirement,
		Obligation:  requirement.Obligation,
		Served:      evidence.Served,
		Declared:    declared,
	}

	lossless := evidence.Served >= threshold &&
		evidence.Truncated == 0 && evidence.Pruned == 0 && evidence.Failed == 0
	if lossless {
		// Served in full at the declared standard. The counts are the
		// SOURCES THAT SERVED, not the catalogue: a satisfied row reading
		// "1 of 6" would describe a loss that did not happen, and the six
		// declared kinds are already published on the plan's own
		// requirement row for a reader who wants them.
		row.Outcome = contractsv1.ContextFabricRequirementSatisfied
		row.Impact = contractsv1.ContextFabricAnswerImpactNone
		row.Declared = evidence.Served
		return row, true
	}

	row.CauseCoverage = evidence.Cause
	// Observed: a provider reported these states for these kinds. Nothing
	// here defaulted, which is exactly what this flag exists to distinguish.
	row.CauseObserved = true
	if evidence.Served == 0 {
		// Nothing usable came back. Dimension: the reader asked for this
		// and gets none of it -- not fewer things, and not less detail
		// about the things that remain.
		row.Outcome = contractsv1.ContextFabricRequirementUnavailable
		row.Impact = contractsv1.ContextFabricAnswerImpactDimension
		return row, true
	}
	// Served, over a reduced set of sources. Depth rather than scope: the
	// subjects the answer covers are unchanged, and what stands behind them
	// is thinner.
	row.Outcome = contractsv1.ContextFabricRequirementNarrowed
	row.Impact = contractsv1.ContextFabricAnswerImpactDepth
	if row.CauseCoverage == "" {
		// A SOURCE SHORTFALL with nothing observed failing: every kind that
		// was read came back usable, and there were fewer of them than the
		// standard demands. `fact_narrowed` is the nearest shipped member
		// and it is used deliberately rather than precisely -- it names a
		// narrowing, which is what happened to the source set. A member
		// naming the shortfall itself is deferred with the not-planned code
		// above rather than minted here, so this change adds at most one
		// vocabulary member instead of two.
		row.CauseCoverage = contractsv1.ContextFabricCoverageDetailFactNarrowed
	}
	// The reduction step, derived from the row's own counts and cause so the
	// step and the row cannot state different things about one narrowing.
	return contractsv1.ContextFabricWithReductionRefinement(row), true
}
