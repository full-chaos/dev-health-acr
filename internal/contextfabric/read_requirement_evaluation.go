package contextfabric

import (
	"log/slog"
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
	// ObservedKinds are WHICH kinds counted Observed above, in the
	// requirement's own declared order -- ServedKinds' twin, one level up.
	//
	// It exists so the DECLARED half of the observation-cover count
	// (servedObservationCover's "what was in play at all" denominator) can be
	// covered from the same identity list the numerator's ServedKinds is
	// drawn from, rather than re-derived from a bare integer that has
	// already lost which kinds it counted. A kind this file called pruned
	// never appears here, for the same reason it never increments Observed:
	// see the prune handling below.
	ObservedKinds []FactKind
	// Served is how many produced usable evidence IN FULL: available or
	// stale, AND not recorded as narrowed by the planner.
	//
	// "In full" is load-bearing and it is what the narrowing consultation
	// below buys. A kind the planner narrowed did return data, so it is
	// OBSERVED -- but it returned data for fewer subjects than the plan asked
	// for, so it did not serve the requirement in full, and counting it as
	// served is how a narrowed read reads `satisfied`.
	Served int
	// ServedKinds are WHICH kinds were counted Served above, in the
	// requirement's own declared order.
	//
	// It exists so the population layer can ask "was this kind read for THIS
	// subject" without re-deciding whether the kind was served at all. That
	// separation is the whole reason this file does not become a second
	// authority twice over: the kind-level arms below classify, and the
	// population layer only distributes. A kind this file called narrowed,
	// truncated, failed or pruned never appears here and therefore
	// contributes nothing in either direction to a per-subject test.
	ServedKinds []FactKind
	// Narrowed is how many declared kinds the PLANNER recorded as narrowed,
	// read from Coverage.Details rather than from a source state.
	//
	// A NARROW EXTENSION of this evaluator's stated Sources-only scope, taken
	// deliberately. `factDetailSpecForRead` records a planner narrowing in
	// Coverage.Details with `Narrowed` set and the skipped subject kinds
	// listed, while the observation's own STATE stays `available` -- the
	// provider did return data, for a narrowed subject set. Reading only the
	// state therefore publishes `satisfied` for a read the document itself
	// records as narrowed, which is the false-complete class this whole
	// change exists to remove, arriving through a different door.
	//
	// ONLY the Narrowed flag is consulted. No other field of a detail changes
	// any decision here: the detail's own code, scope outcome, policy and
	// counts belong to the coverage layer, and re-deciding them here would be
	// the second-authority defect this evaluator is careful not to become.
	Narrowed int
	// Truncated and Failed partition the rest, and they are kept apart
	// rather than summed because they name different causes and a reader
	// acts on the cause, not on the total.
	//
	Truncated int
	Failed    int
	// Pruned counts pruned observations FOR ONE PURPOSE ONLY: to tell "the
	// turn planned nothing that could serve this cell" apart from "everything
	// it planned was pruned". Those are different answers and they had
	// collapsed into one.
	//
	// IT MUST NEVER ENTER A LOSS TEST. A prune is not a loss -- see
	// evaluateReadRequirement -- and this counter was deliberately removed
	// once for exactly that reason. It is back because minting a
	// `requirement_read_not_planned` row for an all-pruned requirement
	// re-degraded what `factStateDegrades` refuses to degrade, which is the
	// same defect arriving through a different door. Read the two uses below
	// before adding a third.
	Pruned int
	// Cause is the coverage code for the worst observation: CARRIED from the
	// coverage layer's own detail where there is one, and only DEFAULTED from
	// the source state where there is not. Empty when nothing was observed.
	Cause contractsv1.ContextFabricCoverageDetailCode
	// UndeclaredCause is set when a detail carried a code outside the closed
	// vocabulary. It is not a state this service can currently reach -- the
	// fact registry mints declared codes only -- and it exists so that if it
	// ever becomes reachable the requirement emits NO row rather than a row
	// naming a cause nobody declared.
	UndeclaredCause bool
	// UndeclaredCode is the offending token, kept so the log line can NAME it.
	// It is a vocabulary token by construction -- never corpus content -- so
	// it is safe to emit; a producer that minted it is identifiable from one
	// grep rather than from a bisect.
	UndeclaredCode contractsv1.ContextFabricCoverageDetailCode
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
	// THE COVERAGE LAYER'S OWN RECORD, keyed by fact kind: its narrowing flag
	// and its AUTHORITATIVE CAUSE CODE.
	//
	// The code is CARRIED, never re-derived from a source state. The design of
	// record says so in as many words -- `CauseCoverage` is "carried from the
	// derivation's own reason, never re-classified" -- and the seed path has
	// always obeyed it (`unavailableRequirementCause` carries the derivation's
	// own token). This path did not, and re-deriving from the state published
	// the wrong MECHANISM: a scope-expansion failure, where no provider ran at
	// all, was reported as `fact_provider_reported`.
	//
	// GATED BY THE DECLARED VOCABULARY, and the gate is derived from the
	// vocabulary rather than hand-listed, so it cannot fall behind it. An
	// undeclared code is NOT remapped onto a declared one -- a remap is how a
	// code nobody declared becomes a code somebody did -- it is refused, and
	// the requirement emits no row at all (see readRequirementOutcomeRow). A
	// reach probe fails if that branch ever executes.
	declaredCodes := map[contractsv1.ContextFabricCoverageDetailCode]bool{}
	for _, code := range contractsv1.ContextFabricCoverageDetailCodeVocabulary() {
		declaredCodes[code] = true
	}
	narrowedKinds := map[FactKind]bool{}
	carriedCause := map[FactKind]contractsv1.ContextFabricCoverageDetailCode{}
	for _, detail := range coverage.Details {
		if detail.FactKind == "" {
			continue
		}
		if detail.Narrowed {
			narrowedKinds[detail.FactKind] = true
		}
		if detail.Code == "" {
			continue
		}
		if !declaredCodes[detail.Code] {
			evidence.UndeclaredCause = true
			evidence.UndeclaredCode = detail.Code
			continue
		}
		carriedCause[detail.FactKind] = detail.Code
	}

	worst := 0
	for _, kind := range requirement.FactKinds {
		state, seen := states[kind]
		if !seen {
			continue
		}
		// A PRUNE IS NOT EVIDENCE IN EITHER DIRECTION, and this is the one
		// place that has to be said out loud.
		//
		// `factStateDegrades` refuses to degrade on `SourcePruned`
		// DELIBERATELY, and states why: "A prune means the planner proved
		// the source had nothing to contribute to THIS question, so nothing
		// is missing and the answer is not degraded. Marking it partial
		// would train every consumer to treat a correctly-scoped
		// investigation as a compromised one."
		//
		// Counting it here as a loss re-degrades exactly what that decision
		// exists to protect, one layer up -- a second authority contradicting
		// a settled one, which is the defect class this whole change removes,
		// pointed the other way. So a pruned observation is skipped entirely:
		// it is not observed, not served, not a loss, and it does not rank in
		// the worst-observation table below (a prune must not name the cause
		// of a loss that came from somewhere else).
		//
		// A requirement whose every declared kind was pruned therefore
		// reaches no row at all and keeps only its planning seed, which the
		// completeness derivation reads as `partial`. That is the honest
		// floor: nothing was read, and nothing was lost either.
		if state == SourcePruned {
			evidence.Pruned++
			continue
		}
		evidence.Observed++
		evidence.ObservedKinds = append(evidence.ObservedKinds, kind)
		switch {
		case state == SourceAvailable || state == SourceStale:
			if narrowedKinds[kind] {
				// Returned data, but for fewer subjects than planned. Counted
				// as narrowed rather than served, so the row can state the
				// shortfall instead of claiming the cell was filled.
				evidence.Narrowed++
				break
			}
			evidence.Served++
			evidence.ServedKinds = append(evidence.ServedKinds, kind)
		case state == SourceTruncated:
			evidence.Truncated++
		default:
			evidence.Failed++
		}
		if severity := sourceStateSeverity(state); severity > worst {
			worst = severity
			if code, carried := carriedCause[kind]; carried {
				evidence.Cause = code
			} else {
				// NO DETAIL FOR THIS KIND. `Coverage.Details` is
				// optional-first, so a document written before it existed
				// carries none. The state mapping stands in, and the row
				// says so: a defaulted cause reports CauseObserved false,
				// which is exactly what that flag is for.
				evidence.Cause = readCoverageCauseFor(state)
			}
		}
	}
	// A NARROWED KIND RANKS AT ZERO SEVERITY, because its STATE is
	// `available` -- the narrowing lives in the detail, not the state. So the
	// worst-observation loop above never reaches for its cause, and without
	// this the planner's own code would be dropped and the shortfall arm
	// would default `fact_narrowed` with CauseObserved FALSE, understating a
	// cause that was genuinely reported.
	//
	// Taken only when nothing worse already named a cause: a truncation or a
	// failure outranks a narrowing, and the row names one mechanism.
	if evidence.Cause == "" && evidence.Narrowed > 0 {
		for _, kind := range requirement.FactKinds {
			if !narrowedKinds[kind] {
				continue
			}
			if code, carried := carriedCause[kind]; carried {
				evidence.Cause = code
				break
			}
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
//
// A thin wrapper over appendReadRequirementEvaluationsWithCover for the many
// callers that only want the rows -- see that function for the observation-cover
// diagnostic this one discards.
func appendReadRequirementEvaluations(
	rows []RequirementOutcomeRow,
	published []contractsv1.ContextFabricPlanRequirement,
	coverage Coverage,
	populations readPopulationEvidence,
) []RequirementOutcomeRow {
	rows, _ = appendReadRequirementEvaluationsWithCover(rows, published, coverage, populations)
	return rows
}

// appendReadRequirementEvaluationsWithCover is appendReadRequirementEvaluations's
// full form: it ALSO returns one ReadRequirementObservationCoverEvent per row it
// appends, in the same order, for finalizeResult to emit through e.telemetry.
//
// It stays a PURE FUNCTION -- no ctx, no principal, no I/O -- exactly like its
// sibling: readRequirementOutcomeRow already computes the cover diagnostic as
// a side-effect-free value (see that function's own third return), and this
// loop only COLLECTS what it returns rather than deciding anything new.
func appendReadRequirementEvaluationsWithCover(
	rows []RequirementOutcomeRow,
	published []contractsv1.ContextFabricPlanRequirement,
	coverage Coverage,
	populations readPopulationEvidence,
) ([]RequirementOutcomeRow, []ReadRequirementObservationCoverEvent) {
	if len(published) == 0 {
		return rows, nil
	}
	var added []RequirementOutcomeRow
	var events []ReadRequirementObservationCoverEvent
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
		row, ok, cover := readRequirementOutcomeRow(requirement, threshold, evaluateReadRequirement(requirement, coverage), populations)
		if !ok {
			continue
		}
		added = append(added, row)
		if cover != nil {
			events = append(events, *cover)
		}
	}
	return appendOutcomeRows(rows, added...), events
}

// servedObservationCover is the SERVED half of a read requirement's two
// threshold-comparison counts: the minimum observation cover of the kinds
// this evaluator called served, at the requirement's own subject kind --
// WITH ONE EXPLICIT REFINEMENT for a mixed-state alias.
//
// THE MIXED-STATE ALIAS RULE, PINNED. Two declared kinds can share an
// observation key and still reach this evaluator in DIFFERENT states: one
// served in full, the other narrowed, truncated or failed -- the fact
// registry mints one coverage observation per kind, so nothing stops a
// proxy pair from disagreeing about the SAME underlying observation.
// Covering ServedKinds alone would then let the served kind's key stand in
// for the whole observation and credit it as fully served, even though the
// SAME observation also backs a kind this evaluator counted as a loss --
// observationCover's own "already covered" behaviour
// (TestADuplicateAdapterCannotCreateCorroboration) is exactly what bites
// here: covering [served, lost] together is no larger than covering
// [served] alone when they share a key, so the loss disappears from the
// count and a `narrowed` row can read Served == Declared, which the outcome
// validator refuses as "not a reduction".
//
// THE RULE: WORST STATE WINS PER OBSERVATION, the same rule
// evaluateReadRequirement already applies PER KIND ("WORST STATE WINS when
// one kind is observed more than once"). An observation that backs any lost
// kind (narrowed, truncated or failed -- i.e. any kind in ObservedKinds that
// is not also in ServedKinds) is not credited as served.
//
// IT TAINTS THE OBSERVATION, NOT THE PRODUCER, and the difference is not
// hypothetical. A kind declares a LIST of keys, so a served kind can back
// several independent observations at once and share only ONE of them with
// something that was lost. `operational_deficiencies` at team is the measured
// case: it declares {risk, throughput, sustainability}, and `health` declares
// {risk} alone. If health is lost and deficiencies served IN FULL, tainting
// the whole PRODUCER drops deficiencies from the cover entirely and returns
// 0 -- publishing a row that says nothing was served when a producer read
// two untainted observations completely. Executed, before this rule was
// corrected: served cover = 0 where the truth is 1.
//
// So the tainted KEY is removed from every served kind's label set and the
// cover is recomputed over what remains. A served kind whose labels are ALL
// tainted contributes nothing -- every observation it stands for was lost
// somewhere. A served kind with any untainted label still covers those.
// An unkeyed lost kind taints nothing -- it declares no key to exclude by,
// exactly as observationCover's own "an unkeyed kind is its own observation"
// rule already keeps it from being folded into anything else; and an unkeyed
// SERVED kind is untaintable for the same reason, staying the singleton it
// always was.
func servedObservationCover(evidence readEvidence, subject SubjectKind, assignment observationKeyAssignment) int {
	servedSet := make(map[FactKind]bool, len(evidence.ServedKinds))
	for _, kind := range evidence.ServedKinds {
		servedSet[kind] = true
	}
	taintedKeys := map[ObservationKey]bool{}
	for _, kind := range evidence.ObservedKinds {
		if servedSet[kind] {
			continue
		}
		for _, key := range dedupeObservationKeys(assignment[kind][subject]) {
			taintedKeys[key] = true
		}
	}
	if len(taintedKeys) == 0 {
		return observationCover(evidence.ServedKinds, subject, assignment)
	}
	// Rebuild the assignment with every tainted key removed, then cover the
	// served kinds against THAT. A served kind whose labels all vanish is
	// dropped; one that keeps any label still covers the observations that
	// label stands for.
	untainted := make(observationKeyAssignment, len(evidence.ServedKinds))
	clean := make([]FactKind, 0, len(evidence.ServedKinds))
	for _, kind := range evidence.ServedKinds {
		declared := dedupeObservationKeys(assignment[kind][subject])
		if len(declared) == 0 {
			// Unkeyed: a singleton, untaintable, always counted.
			clean = append(clean, kind)
			continue
		}
		kept := make([]ObservationKey, 0, len(declared))
		for _, key := range declared {
			if !taintedKeys[key] {
				kept = append(kept, key)
			}
		}
		if len(kept) == 0 {
			// Every observation this kind stands for was lost somewhere.
			continue
		}
		untainted[kind] = map[SubjectKind][]ObservationKey{subject: kept}
		clean = append(clean, kind)
	}
	return observationCover(clean, subject, untainted)
}

// readRequirementOutcomeRow turns one requirement's counted evidence into its
// outcome row. The second return is false where no row is emitted.
//
// THE COUNTS ARE OBSERVATION-COVER COUNTS, NOT KIND COUNTS. Declared is
// max(the cover of everything OBSERVED, threshold) and Served is
// servedObservationCover -- the cover of everything served, worst-state-wins
// per observation. Two things have to be true at once and neither counting
// rule alone gives both: counting the whole observed catalogue would report
// a loss on every turn that planned fewer kinds than a requirement declares
// (the ordinary case), while counting only what was observed could not
// express a source SHORTFALL -- a `corroborated` requirement that planned
// one kind and got it would read 1/1, and the only outcome legal at 1/1 is
// `satisfied`, which is the standard silently lowered. Raising Declared to
// the standard's own demand makes `narrowed 1/2` both legal and true, and
// every arm below keeps Served < Declared wherever it claims `narrowed`,
// which the row validator requires.
//
// THIS IS THE STANDARD'S DEMAND, NOT A KIND COUNT, and that distinction is
// exactly what closes the collapse this change exists to close: three kinds
// declaring one observation and all coming back served would, counted by
// KIND, publish Declared=3 against a corroborated threshold of 2 and Served=1
// (the cover) -- an honest-looking `1/3` that actually understates how close
// the row came, because the "3" never existed as three independent sources.
// Covering the observed side too (declared = max(cover(ObservedKinds),
// threshold)) reports `1/2`: the standard's own demand, the way the shortfall
// arm below already reports it when fewer kinds were observed than the
// standard needs.
//
// readRequirementOutcomeRow's third return is the observation-cover
// diagnostic for the row it built, or nil when no row-building reached the
// point that computes one (every early return above the cover computation,
// and the guard branches that emit no row at all). It is a PURE VALUE, never
// logged here -- see ReadRequirementObservationCoverEvent's own doc comment
// for why the decision has to be reconstructible from a trace at all, and
// finalizeResult for where it is actually emitted through e.telemetry.
func readRequirementOutcomeRow(
	requirement contractsv1.ContextFabricPlanRequirement,
	threshold int,
	evidence readEvidence,
	populations readPopulationEvidence,
) (RequirementOutcomeRow, bool, *ReadRequirementObservationCoverEvent) {
	// NOTHING WAS READ FOR THIS REQUIREMENT, and it now says so.
	//
	// This arm was the one hole in the change: a requirement none of whose
	// declared kinds was read at all had no truthful cause in the shipped
	// vocabulary, so it emitted no row and kept only its planning seed. The
	// STATE was honest either way -- a planning-only READ identity derives
	// `partial` -- but the CAUSE was lost, and a reader was left with an
	// answer that was less than complete for a reason nothing named.
	//
	// `requirement_read_not_planned` is that reason, minted after the
	// count-population member landed so the two edits to this closed array
	// were sequenced rather than raced. See its own declaration for why each
	// neighbouring code would have been a plausible lie.
	//
	// `unavailable` with impact `dimension`: the reader asked for this cell
	// and gets none of it. Not `narrowed`, which claims a reduction of
	// something that was there, and not `not_attempted`, which belongs to the
	// gap-row builder for a turn that ENDED before reaching the requirement.
	// This turn ran to completion and never planned the cell.
	//
	// CauseObserved is FALSE, and that is not a technicality. Nothing
	// reported this: the evaluator inferred it from the absence of any
	// observation. The one field a reader has for telling a reported cause
	// from an inferred one must say inferred.
	//
	// Served/Declared are 0 and the requirement's own standard: zero sources
	// served, against the number the completion quantifier demands. Both
	// numbers are measured, neither is invented.
	// AN ALL-PRUNED REQUIREMENT IS NOT AN UNPLANNED ONE, and the difference
	// is the whole reason `Pruned` is counted.
	//
	// The planner CONSIDERED these kinds and proved they could not contribute;
	// `factStateDegrades` refuses to degrade on that, deliberately. Minting a
	// `requirement_read_not_planned` row here would say the turn never looked
	// -- false -- and would drive the answer to `degraded`, re-degrading
	// exactly what the fact layer protects. So it keeps its planning seed and
	// reads `partial`, as it did before this code existed.
	//
	// This arm is BEFORE the not-planned row for that reason: reaching the
	// row first is how the defect happened.
	if evidence.Observed == 0 && evidence.Pruned > 0 {
		return RequirementOutcomeRow{}, false, nil
	}

	if evidence.Observed == 0 {
		// THIS ROW EMITS ITS COVER LINE TOO, and the reason is the rule this
		// branch previously broke. The row IS published -- a consumer sees
		// `unavailable 0/N` in the answer -- so a trace with nothing in it
		// leaves an operator unable to tell an EVALUATED ZERO from a decision
		// that never ran. Missing is not zero; a cover of 0 over 0 observed
		// kinds is a real measurement and it says so.
		return RequirementOutcomeRow{
			Stage:         contractsv1.ContextFabricOutcomeStageAssembledResult,
			Requirement:   requirement.Requirement,
			Obligation:    requirement.Obligation,
			Outcome:       contractsv1.ContextFabricRequirementUnavailable,
			Impact:        contractsv1.ContextFabricAnswerImpactDimension,
			CauseCoverage: contractsv1.ContextFabricCoverageDetailRequirementReadNotPlanned,
			CauseObserved: false,
			Served:        0,
			Declared:      threshold,
		}, true, readRequirementObservationCoverEvent(
			requirement, threshold, 0, threshold, evidence, populations.assignment)
	}

	// AN UNDECLARED CAUSE CODE EMITS NO ROW.
	//
	// A code outside the closed vocabulary must not reach the wire, and it
	// must not be REMAPPED onto a declared one either -- a remap is precisely
	// how a code nobody declared becomes a code somebody did. So the
	// requirement keeps its planning seed, which the completeness derivation
	// reads as `partial`: the state stays honest and only the cause is lost,
	// the same trade the not-read arm above makes.
	//
	// UNREACHABLE TODAY. The fact registry mints declared codes only. This
	// exists so that if that ever stops being true, the failure is a missing
	// disclosure rather than an invented one -- and
	// TestAnUndeclaredCauseCodeEmitsNoRow is the reach probe that fails if the
	// branch starts executing.
	//
	// IT LOGS, and that is not optional. Dropping the row silently would make
	// this a swallowed signal: the answer would go out one disclosure short
	// with nothing anywhere saying why, and the reach probe only fires in a
	// test run. The line names the requirement and the code so the producer
	// that minted an undeclared code is identifiable from one grep, and the
	// code is logged because it is a VOCABULARY TOKEN, not corpus content.
	//
	// slog.Default() rather than a threaded logger, matching this package's
	// existing convention for a nil logger; the evaluator is a pure function
	// on the finalization path and has no engine handle to take one from.
	if evidence.UndeclaredCause {
		slog.Default().Warn("context fabric read requirement dropped for an undeclared coverage code",
			"requirement", SanitizeLogAttr(requirement.Requirement),
			"obligation", SanitizeLogAttr(requirement.Obligation),
			"undeclared_code", SanitizeLogAttr(string(evidence.UndeclaredCode)),
			"observed_kinds", evidence.Observed)
		return RequirementOutcomeRow{}, false, nil
	}

	// THE SNAPSHOT, read off `populations` -- the SAME snapshot the caller
	// captured once for this whole finalization (see finalizeResult and
	// readPopulationEvidence.assignment's own doc comment). A nil
	// `populations` (the zero value, e.g. every `single_subject`-only test
	// fixture in this package) carries a nil assignment, and observationCover
	// already treats a nil/unkeyed lookup as "no declared observation" -- so
	// every kind is its own singleton and the cover equals the kind count,
	// reproducing this function's pre-cover behaviour exactly.
	servedCover := servedObservationCover(evidence, requirement.Subject, populations.assignment)
	declared := observationCover(evidence.ObservedKinds, requirement.Subject, populations.assignment)
	if threshold > declared {
		declared = threshold
	}
	cover := readRequirementObservationCoverEvent(requirement, threshold, servedCover, declared, evidence, populations.assignment)
	row := RequirementOutcomeRow{
		Stage:       contractsv1.ContextFabricOutcomeStageAssembledResult,
		Requirement: requirement.Requirement,
		Obligation:  requirement.Obligation,
		Served:      servedCover,
		Declared:    declared,
	}

	// `Pruned` is deliberately absent from this conjunction. It is counted
	// now, but ONLY to tell an unplanned requirement from an all-pruned one;
	// adding it here would make a prune a loss, which is the thing the fact
	// layer refuses to do and this evaluator twice re-did by accident.
	lossless := servedCover >= threshold &&
		evidence.Truncated == 0 && evidence.Failed == 0 && evidence.Narrowed == 0
	if lossless {
		// THE KIND STANDARD IS MET. For a `single_subject` requirement that
		// is the whole question and the row is finished here, exactly as it
		// was before this change.
		//
		// For a DISTRIBUTIVE scope it is only half: the kinds were read, and
		// nothing yet says FOR WHOM. The population conjunct answers that,
		// and it runs HERE -- after the kind arms, never before -- so a
		// requirement that lost a kind takes the kind arm and this is never
		// consulted. One mechanism per row.
		if distributiveScope(requirement.Scope) {
			if !populations.Present {
				// A CALLER DEFECT, not an absent population: a distributive
				// requirement reached the evaluator with no population
				// evidence supplied at all, which means a finalization path
				// did not thread the bundle. Emitting the absent arm here
				// would publish a coverage claim about the ANSWER for what
				// is a wiring bug in this process, so the requirement keeps
				// its planning seed (deriving `partial`) and the line names
				// the site.
				//
				// It logs for the same reason the undeclared-code branch
				// does: dropping a row silently would send the answer out a
				// disclosure short with nothing anywhere saying why.
				slog.Default().Warn("context fabric distributive read requirement reached the evaluator with no population evidence",
					"requirement", SanitizeLogAttr(requirement.Requirement),
					"obligation", SanitizeLogAttr(requirement.Obligation),
					"scope", SanitizeLogAttr(requirement.Scope))
				return RequirementOutcomeRow{}, false, nil
			}
			population, owned := populations.populationFor(requirement)
			if !owned {
				slog.Default().Warn("context fabric distributive read requirement has no population owner",
					"requirement", SanitizeLogAttr(requirement.Requirement),
					"obligation", SanitizeLogAttr(requirement.Obligation),
					"scope", SanitizeLogAttr(requirement.Scope))
				return RequirementOutcomeRow{}, false, nil
			}
			return readPopulationOutcomeRow(row, population, populations, evidence.ServedKinds, threshold, requirement.Subject), true, cover
		}
		// Served in full at the declared standard. The counts are the
		// OBSERVATIONS THAT SERVED, not the catalogue: a satisfied row
		// reading "1 of 6" would describe a loss that did not happen, and
		// the six declared kinds are already published on the plan's own
		// requirement row for a reader who wants them.
		row.Outcome = contractsv1.ContextFabricRequirementSatisfied
		row.Impact = contractsv1.ContextFabricAnswerImpactNone
		// row.Served is already servedCover; Declared matches it exactly --
		// lossless means nothing was lost, so ObservedKinds == ServedKinds
		// (no kind is tainted) and the two covers already agree. Padding
		// Declared to the catalogue would describe a loss that did not
		// happen, per the comment above.
		row.Declared = row.Served
		return row, true, cover
	}

	row.CauseCoverage = evidence.Cause
	// OBSERVED means something REPORTED this cause for this cell, as against
	// this file inferring it. It is NOT the same question as which code was
	// carried, and conflating the two was an over-correction worth recording:
	//
	// carrying the code from the coverage detail fixed WHICH MECHANISM the row
	// names. It says nothing about provenance. A source state a provider
	// actually returned -- `unavailable`, `no_data`, `unconfigured`,
	// `truncated` -- is an OBSERVED fact whether or not a detail accompanied
	// it, and mapping that state onto its code is a translation, not an
	// invention. Reporting those as defaulted would understate provenance on
	// exactly the rows where a provider did speak.
	//
	// The one arm that genuinely defaults is the shortfall below, where
	// nothing reported anything and the evaluator infers a cause from
	// counting. That arm sets the flag false itself.
	row.CauseObserved = evidence.Cause != ""

	// FACT-BEARING IS THE QUESTION, not "did every source serve".
	//
	// `unavailable` means the reader asked for this cell and gets NONE of it.
	// Three states return data and are therefore not that:
	//   * a served kind, obviously;
	//   * a NARROWED kind -- the provider returned data for fewer subjects
	//     than planned;
	//   * a TRUNCATED kind -- the fact registry drops over-budget facts and
	//     marks the source truncated RATHER THAN failing the read, because
	//     "a partial, explicitly-truncated answer is the honest outcome".
	//
	// Truncation used to fall through to `unavailable` here, which told a
	// reader they got none of a cell they got part of, and degraded an answer
	// the fact layer had deliberately kept partial rather than failed. That is
	// the same defect as the prune: this file re-deciding a question another
	// layer had already settled and documented.
	factBearing := evidence.Served > 0 || evidence.Narrowed > 0 || evidence.Truncated > 0
	if !factBearing {
		// Nothing came back at all. Dimension: not fewer things, and not
		// less detail about the things that remain -- none of it.
		row.Outcome = contractsv1.ContextFabricRequirementUnavailable
		row.Impact = contractsv1.ContextFabricAnswerImpactDimension
		return row, true, cover
	}
	// Served, over less than the standard asked for. Depth rather than scope:
	// the subjects the answer covers are unchanged, and what stands behind
	// them is thinner.
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
		// AND NO REFINEMENT ON THIS ARM.
		//
		// A refinement is a BEFORE and an AFTER with a named step between
		// them: it says a population of `Before` was reduced to `After`.
		// Here `Declared` is the STANDARD's demand, not a population that
		// ever existed -- a `corroborated` requirement whose single planned
		// kind came back usable reads 1 of 2, and minting a refinement over
		// it would publish a 2 -> 1 reduction of a source set that never
		// held 2. Nothing was reduced; there was less to begin with.
		//
		// The counts still say so truthfully: `narrowed 1/2` against a
		// standard of 2 is exactly the shortfall, and the row's cause names
		// it. What is withheld is the claim that a reduction STEP occurred,
		// because none did.
		return row, true, cover
	}
	// The reduction step, derived from the row's own counts and cause so the
	// step and the row cannot state different things about one narrowing.
	// Reached only where the evidence itself reported the cause, so there is
	// a real before-and-after to record.
	return contractsv1.ContextFabricWithReductionRefinement(row), true, cover
}

// ReadRequirementObservationCoverEvent is the observation-cover decision for
// ONE read requirement's row, so it can be rebuilt FROM THE TRACE ALONE, per
// the observability bar: pre-entry (what was requested), pre-decision (what
// was measured), the decision and its reason, and post-decision (what was
// served).
//
// WHY THIS EVENT EXISTS AT ALL, stated plainly because it is the argument for
// the bar. The cover replaced a fact-KIND count with a MINIMUM COVER, and
// nothing about that substitution was observable: a row reading `narrowed 0/2`
// looked identical whether two kinds honestly collapsed onto one observation
// or the evaluator had a bug. One did -- the mixed-state rule tainted whole
// producers instead of observations and silenced a full read -- and it was
// findable only by reading the code, because the count that changed reached no
// log line. That is the defect class this event closes.
//
// THE DELTA IS THE POINT. Both the kind COUNT and the COVER are carried for
// each of served and observed, because the cover alone cannot say whether it
// collapsed anything: cover 2 of 2 kinds and cover 2 of 5 kinds are the same
// number describing completely different reads. The pair makes the collapse
// itself visible, and TaintedObservations says how much of any gap came from
// the mixed-state rule rather than from the declaration.
//
// EVERY DIMENSION IS A COUNT OR A CLOSED TOKEN. Requirement, obligation and
// subject kind are closed vocabularies; the rest are integers and one bool.
// No key VALUES and no kind lists ride here -- those would grow with the
// registry, and the numbers already answer the question the bar asks.
//
// It is built by the PURE evaluator (readRequirementObservationCoverEvent,
// called from readRequirementOutcomeRow), held on assemblyTelemetry by
// finalizeResult (one pass may run more than once per investigation -- the
// budget retry, the candidate-narrowing re-finalize -- and every pass's
// events are kept, never just the last), and published by (*Engine).emit,
// which holds the engine's configured logger -- never through slog.Default(),
// which is Go's process-wide fallback and not this service's own JSON stream
// (see PlanTelemetry.RecordReadRequirementObservationCover).
type ReadRequirementObservationCoverEvent struct {
	// Requirement and Obligation and Subject are the row's own identity,
	// copied from the requirement rather than re-derived, for the same
	// reason every sibling event on this interface copies them.
	Requirement string
	Obligation  string
	Subject     SubjectKind
	// Threshold is the completion quantifier's own demand.
	Threshold int
	// ObservedKinds and ServedKinds are the KIND counts -- the catalogue
	// size on each side of the decision, before any cover is taken.
	ObservedKinds int
	ServedKinds   int
	// ObservedCover and ServedCover are the MINIMUM COVER on each side: the
	// number of independent observations in play, worst-state-wins per
	// observation. See servedObservationCover's own doc comment for the
	// mixed-state alias rule this number depends on.
	ObservedCover int
	ServedCover   int
	// CollapsedObservations is ServedKinds minus ServedCover: how many served
	// KINDS shared an observation with another served kind, so the cover
	// counted them once. Zero whenever every served kind is independent.
	CollapsedObservations int
	// TaintedObservations is how many observations the mixed-state rule
	// excluded from ServedCover because they also backed a kind this turn
	// lost. It is the diagnostic half of servedObservationCover, and it is
	// what tells a reader a cover reduced by the DECLARATION apart from one
	// reduced by a LOSS.
	TaintedObservations int
	// Declared is the row's own published standard: the cover of everything
	// observed, raised to Threshold. DeclaredRaisedToStandard says whether
	// that raise actually happened -- true when the observed cover fell
	// short of the quantifier's demand.
	Declared                 int
	DeclaredRaisedToStandard bool
	// MeetsThreshold is the row's own pass/fail: ServedCover >= Threshold.
	MeetsThreshold bool
	// Pass is which finalization this event came from, in the order they ran
	// for this investigation (0 for the first synthesis, 1 for the one
	// bounded budget retry or a candidate-narrowing re-finalize that ran
	// without a retry, 2 for a candidate-narrowing re-finalize after a
	// retry). It is set by finalizeResult, never by the pure evaluator above,
	// because the evaluator has no notion of which attempt it is running
	// inside.
	Pass int
	// Served is whether THIS pass's result is the one the investigation
	// actually served. (*Engine).emit sets it true on the events from the
	// FINAL pass only -- the pass whose result is returned -- and false on
	// every earlier pass's, once it can see the whole set and knows which
	// pass that was. A row with Served=false still describes a real
	// decision: the answer that pass would have served, and why a later
	// pass replaced it.
	Served bool
}

// readRequirementObservationCoverEvent builds the observation-cover
// diagnostic for one row. A PURE FUNCTION, deliberately: it is called from
// readRequirementOutcomeRow, which has no engine handle and must stay a pure
// function on the finalization path, so the VALUE is built here and the I/O
// happens one layer up, in finalizeResult.
func readRequirementObservationCoverEvent(
	requirement contractsv1.ContextFabricPlanRequirement,
	threshold, servedCover, declared int,
	evidence readEvidence,
	assignment observationKeyAssignment,
) *ReadRequirementObservationCoverEvent {
	servedKinds := len(evidence.ServedKinds)
	observedKinds := len(evidence.ObservedKinds)
	observedCover := observationCover(evidence.ObservedKinds, requirement.Subject, assignment)
	return &ReadRequirementObservationCoverEvent{
		Requirement:              requirement.Requirement,
		Obligation:               requirement.Obligation,
		Subject:                  requirement.Subject,
		Threshold:                threshold,
		ObservedKinds:            observedKinds,
		ServedKinds:              servedKinds,
		ObservedCover:            observedCover,
		ServedCover:              servedCover,
		CollapsedObservations:    servedKinds - servedCover,
		TaintedObservations:      taintedObservationCount(evidence, requirement.Subject, assignment),
		Declared:                 declared,
		DeclaredRaisedToStandard: declared > observedCover,
		MeetsThreshold:           servedCover >= threshold,
	}
}

// taintedObservationCount is how many distinct observations the mixed-state
// rule actually excluded FROM THE SERVED COVER. It is the diagnostic half of
// servedObservationCover -- without it a reader cannot tell a cover reduced by
// the DECLARATION from one reduced by a LOSS.
//
// IT COUNTS ONLY KEYS THAT A SERVED KIND ALSO DECLARES, and the first version
// did not. Counting every lost kind's keys reports taint that changed nothing:
// with health served and flow lost, flow's `throughput` is not a key of any
// served kind, so excluding it removes nothing from the cover -- yet the field
// read 1. A diagnostic that reports an effect which did not occur is worse than
// no diagnostic, because a reader uses it to explain a shortfall it did not
// cause. The intersection with the served kinds' own keys is what makes the
// number mean what its name says.
func taintedObservationCount(evidence readEvidence, subject SubjectKind, assignment observationKeyAssignment) int {
	served := make(map[FactKind]bool, len(evidence.ServedKinds))
	for _, kind := range evidence.ServedKinds {
		served[kind] = true
	}
	// The keys the SERVED kinds actually stand on. A tainted key outside this
	// set excluded nothing, because there was nothing of it in the cover.
	servedKeys := map[ObservationKey]bool{}
	for _, kind := range evidence.ServedKinds {
		for _, key := range dedupeObservationKeys(assignment[kind][subject]) {
			servedKeys[key] = true
		}
	}
	tainted := map[ObservationKey]bool{}
	for _, kind := range evidence.ObservedKinds {
		if served[kind] {
			continue
		}
		for _, key := range dedupeObservationKeys(assignment[kind][subject]) {
			if servedKeys[key] {
				tainted[key] = true
			}
		}
	}
	return len(tainted)
}
