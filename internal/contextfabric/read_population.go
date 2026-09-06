package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The READ POPULATION layer: for a requirement whose completion scope is
// DISTRIBUTIVE, who the population is, and which of them the turn actually
// read.
//
// THE DEFECT IT CLOSES. A requirement row publishes `Scope` -- `each_operand`,
// `each_member`, `each_group` -- and has done since S7b-ii, but nothing
// consumed it. The read evaluator counted FACT KINDS and never asked "for whom",
// and the fact registry mints ONE coverage observation per kind for all subjects
// at once (`fact_registry.go:1267`), so an evaluator reading only coverage
// cannot tell "both operands read" from "one operand read, keyed the same kind".
// A two-operand comparison served on one side reported `satisfied`, and the
// answer reported `complete`.
//
// THE POPULATION IS DECLARED BY ITS OWNER, NEVER BY THE FACTS THAT CAME BACK.
// That direction is the whole discipline of this file. The frame's operand
// slots own the operand population; the served cohort owns the member and group
// populations. Returned facts are POSITIVE WITNESSES ONLY -- deriving the
// denominator from them would make the requirement unfalsifiable, because a
// read that returned nothing would shrink the population it failed to cover
// until it covered it. `investigationScopeSubjectSet` (the INVOKED set) and
// `CanonicalFactBundle.Scope` are excluded for the same reason, and
// `result.ClaimedFacts` additionally because it is model output.
//
// WHAT THIS FILE IS NOT. It does not re-derive any owner's rule. The cohort's
// completeness predicate lives in `ComputeMembershipCardinality` and is CALLED,
// never restated -- a second copy of `!Complete || Truncated` here is exactly
// the two-authorities defect this package's review history is about.

// populationCensus says what is KNOWN about the population's extent -- a
// separate question from how many of it were read.
//
// CLOSED, three members, asserted total by a test rather than by a default arm:
// a census nobody ranked must not silently read as "fully enumerated", which is
// the one direction this vocabulary must never fail in.
type populationCensus string

const (
	// populationEnumerated: the owner named the whole population.
	populationEnumerated populationCensus = "enumerated"
	// populationIncomplete: the owner named a population AND reported that
	// it is not the whole of it (a truncated or incomplete cohort). The
	// count is honest over what was observed and is a floor on the world.
	populationIncomplete populationCensus = "incomplete"
	// populationAbsent: NOT ENUMERABLE. Nothing can name this population --
	// a scoped operand (its members are many subjects nothing enumerates),
	// a cohort that never resolved, a grouped answer with no groups, or a
	// binding so ambiguous that more subjects committed than the frame
	// named.
	//
	// It is NOT "the population is empty". An empty named-slot operand set
	// is ENUMERATED at zero: the frame said who the operands are and the
	// document states that none of them resolved. Telling those apart is
	// what keeps the absent arm rare and meaningful.
	populationAbsent populationCensus = "absent"
)

var populationCensusMembers = [...]populationCensus{
	populationEnumerated,
	populationIncomplete,
	populationAbsent,
}

// populationCensusVocabulary returns the closed vocabulary in published order.
// An ARRAY return, so the caller gets a copy.
func populationCensusVocabulary() [len(populationCensusMembers)]populationCensus {
	return populationCensusMembers
}

// readPopulation is ONE requirement's population, as its owner declares it.
type readPopulation struct {
	// Subjects are the population's members, in the owner's own order.
	// MAY BE EMPTY on an enumerated population -- see populationAbsent.
	Subjects []SubjectRef
	// Declared is the population's size AS THE OWNER DECLARES IT, which is
	// not always len(Subjects): the frame's slot count for operands (so a
	// retracted operand still counts against the denominator), and the
	// cardinality's own pre-narrowing count for members.
	Declared int
	// Census is what is known about the extent. See populationCensus.
	Census populationCensus
	// Basis and Overrun are the owner's own narrowing attribution, carried
	// rather than re-derived. Both empty when nothing narrowed.
	Basis   contractsv1.ContextFabricNarrowingBasis
	Overrun contractsv1.ContextFabricBudgetOverrun
}

// readPopulationEvidence is everything the evaluator needs to decide a
// distributive row, assembled ONCE per finalization.
//
// It is built by readPopulationEvidenceFrom and passed down rather than
// recomputed per requirement, because the COMPARISON-WIDE operand set below
// cannot be derived from one requirement's own population and a second
// derivation of it would be a second authority.
type readPopulationEvidence struct {
	// Present distinguishes "no distributive requirement needed one" from
	// "a caller forgot to supply it". A distributive row reaching the
	// evaluator with Present false is a CALLER DEFECT: it emits no row and
	// logs, rather than guessing a population.
	Present bool
	// coverage is the per-subject read record: subject key -> kind -> worst
	// observed state.
	coverage map[string]map[FactKind]SourceState
	// memberPopulation and groupPopulation are the cohort-owned populations.
	memberPopulation readPopulation
	groupPopulation  readPopulation
	// operandPopulations is keyed by SUBJECT KIND, because an `each_operand`
	// requirement is per subject kind (the coordinate is
	// obligation/role/subject) while a comparison may name operands of
	// several kinds.
	operandPopulations map[SubjectKind]readPopulation
	// comparisonDeclared is how many operand SLOTS the frame names across ALL
	// kinds. It is the denominator the sameness gate needs: "is every operand
	// of this comparison read" cannot be answered from one kind's population,
	// which is the defect the gate below was written wrong for once already.
	comparisonDeclared int
	// comparisonOperands is the union of every operand population's subjects
	// ACROSS ALL KINDS, in a deterministic order.
	//
	// THIS IS THE SAMENESS CONJUNCT'S POPULATION, and it is comparison-wide
	// on purpose. §13.4.2 says a comparison "must read the SAME evidence on
	// every operand" -- a statement about the COMPARISON, not about one
	// subject kind within it. A per-kind intersection over a mixed-kind
	// comparison (team A vs project B) holds ONE operand and is trivially
	// its own kinds, so both rows would certify while the operands share no
	// evidence at all.
	comparisonOperands []SubjectRef
	// comparisonStandards is each operand kind's OWN declared standard, keyed
	// by subject kind and read off the published plan.
	//
	// READINESS AND SAMENESS ARE DIFFERENT QUESTIONS, and one clause used to
	// answer both. "Was every operand read" must be asked of each operand
	// against the catalog and quantifier ITS OWN requirement declares; "is the
	// evidence the same" is then asked comparison-wide at the CURRENT row's
	// standard. Judging readiness at the current row's standard measures an
	// operand against a catalog it was never declared over: a keystone review
	// reproduced a repository operand (declared over [health identity
	// metrics], read health+metrics) scoring 1 against the team row's [flow
	// health], failing the gate, and both rows falling through to
	// `satisfied/none 1/1` while the answer derived `complete` -- a false
	// complete on precisely the comparison this layer exists to disclose.
	//
	// KEYED BY DECLARED OPERAND KIND, AND THAT IS NOT AN IDENTITY BINDING.
	// The requirement coordinate IS obligation/role/subject, so there is
	// exactly one `each_operand` read requirement per subject kind and two
	// operands of the same kind share one standard by construction. Nothing
	// here correlates a committed ref to a particular operand SLOT: slots stay
	// COUNTED (`Declared` is still the frame's slot walk) and are never bound
	// to refs, so this layer acquires no second resolution authority. A
	// per-slot standard would REQUIRE that binding, which is why it is refused
	// rather than merely unbuilt.
	comparisonStandards map[SubjectKind]operandStandard
}

// operandStandard is one operand kind's declared completion standard: the
// catalog of kinds that can serve it, and how many of them its quantifier
// demands.
//
// CARRIED, NEVER RE-DERIVED. Both fields are read off the published plan
// requirement -- the same row the kind-level evaluator judges that operand by
// -- so the readiness gate and the operand's own row cannot disagree about
// what that operand's standard is.
type operandStandard struct {
	kinds     []FactKind
	threshold int
}

// populationFor returns the population a requirement's scope names, and
// whether that scope has one at all.
//
// `single_subject` deliberately returns false: it keeps today's fact-kind
// keying, and a population would be a claim nobody asked for.
func (e readPopulationEvidence) populationFor(requirement contractsv1.ContextFabricPlanRequirement) (readPopulation, bool) {
	switch requirement.Scope {
	case string(CompletionScopeEachOperand):
		population, found := e.operandPopulations[SubjectKind(requirement.Subject)]
		return population, found
	case string(CompletionScopeEachMember):
		return e.memberPopulation, true
	case string(CompletionScopeEachGroup):
		return e.groupPopulation, true
	case string(CompletionScopeSingleSubject):
		return readPopulation{}, false
	}
	// An unrecognised scope is NOT defaulted to kind-keying. It has no
	// population here and the caller treats that as the caller-defect path,
	// which is fail-closed in the direction that costs a cause rather than
	// inventing a certification. The switch-totality test over
	// CompletionScopeVocabulary() is what keeps this unreachable.
	return readPopulation{}, false
}

// distributiveScope reports whether a scope owns a population at all.
//
// DERIVED FROM THE VOCABULARY rather than a hand-listed set: `single_subject`
// is the one member that does not, so every future member is distributive by
// default -- fail-closed toward asking who, rather than toward certifying.
func distributiveScope(scope string) bool {
	for _, member := range CompletionScopeVocabulary() {
		if string(member) == scope {
			return member != CompletionScopeSingleSubject
		}
	}
	return false
}

// subjectReadCoverage folds the bundle's facts into a per-(subject, kind)
// record of the WORST state observed.
//
// Worst-state-wins is the same rule the kind-level fold already applies
// (`read_requirement_evaluation.go:131-138`) and for the same reason: a
// subject whose kind was read twice, once served and once failed, has a
// failure to disclose, and taking the first or last observation would make the
// row depend on the order the merge happened to produce.
func subjectReadCoverage(facts CanonicalFactBundle) map[string]map[FactKind]SourceState {
	coverage := make(map[string]map[FactKind]SourceState, len(facts.Facts))
	for _, fact := range facts.Facts {
		key := SubjectMapKey(fact.Subject)
		kinds, seen := coverage[key]
		if !seen {
			kinds = make(map[FactKind]SourceState, 4)
			coverage[key] = kinds
		}
		if previous, already := kinds[fact.Kind]; already &&
			sourceStateSeverity(previous) >= sourceStateSeverity(fact.SourceState) {
			continue
		}
		kinds[fact.Kind] = fact.SourceState
	}
	return coverage
}

// servedKindsForSubject returns, in the requirement's own declared order, the
// kinds this subject was READ for.
//
// TWO CONJUNCTS, and the first is what keeps this from becoming a second
// authority: a kind counts only if the KIND-LEVEL evaluation already counted it
// served (`evidence.ServedKinds`). A kind the evaluator called narrowed,
// truncated, failed or pruned contributes nothing in either direction here --
// this layer asks "for whom", never "was it good", which the kind layer has
// already decided.
func servedKindsForSubject(
	subject SubjectRef,
	servedKinds []FactKind,
	coverage map[string]map[FactKind]SourceState,
) []FactKind {
	kinds := coverage[SubjectMapKey(subject)]
	if len(kinds) == 0 {
		return nil
	}
	served := make([]FactKind, 0, len(servedKinds))
	for _, kind := range servedKinds {
		state, observed := kinds[kind]
		if !observed {
			continue
		}
		if state == SourceAvailable || state == SourceStale {
			served = append(served, kind)
		}
	}
	return served
}

// operandPopulation is the `each_operand` owner: THE FRAME'S OPERAND SLOTS.
//
// BOUND BY COUNT, NEVER BY IDENTITY. `Declared` is how many operand slots of
// this kind the frame names; `Subjects` is the committed refs of that kind.
// This claims only what the served document already states -- how many
// operands were required and how many committed -- and never claims WHICH
// committed ref answers WHICH slot.
//
// That restraint is deliberate and it is the alternative that was rejected:
// correlating a committed ref to a slot through its candidate's MatchedTerms
// would make the read evaluator join terms to refs, a SECOND RESOLUTION
// AUTHORITY. No operand->subject binding exists at this pin --
// `Explicit.Operands` carries retrieval TERMS, `Committed` is a flat
// []SubjectRef, and resolution works from a whole-question term bag -- and the
// outcome row has no field that could name a member anyway.
//
// SLOTS ARE WALKED EXACTLY AS frameRoleSlots WALKS THEM (subject_role.go:128-143),
// and explicitly NOT as len(DeriveRequirementCoordinates(...)): that function
// DEDUPS coordinates, and the dedup is precisely the collapse this whole defect
// rides on -- two team operands become one coordinate. Counting coordinates
// would reproduce the bug inside its own fix.
func operandPopulation(frame *QuestionFrame, committed []SubjectRef, kind SubjectKind) readPopulation {
	population := readPopulation{Census: populationAbsent}
	if frame == nil {
		return population
	}
	expression := frame.SubjectExpression
	if expression.Kind != SubjectExpressionExplicitSet || expression.Explicit == nil {
		return population
	}

	// THE COUNT COMES FROM frameRoleSlots ITSELF, not from a second walk that
	// happens to agree with it today. A mirrored walk is a second authority
	// for "how many operands does this frame name", and the two would drift
	// the first time the slot rules change -- which is the defect class this
	// whole file exists to avoid, reproduced inside it.
	slots := 0
	for _, slot := range frameRoleSlots(expression) {
		if slot.Role == SubjectRoleOperand && slot.Subject == kind {
			slots++
		}
	}
	// ENUMERABILITY is the one question frameRoleSlots cannot answer: it
	// reports (role, kind) and not which VARIANT produced the slot. A SCOPED
	// operand is not enumerable and its anchor is not a substitute -- a scoped
	// operand denotes the members under an anchor, a committed anchor is one
	// subject rather than that population, and the derivation artifact's own
	// header says the anchor cannot name it. So this walk asks ONLY that, and
	// counts nothing.
	enumerable := true
	for _, operand := range expression.Explicit.Operands {
		if operand.Scoped != nil && operand.Scoped.MemberKind == kind {
			enumerable = false
		}
	}
	if slots == 0 {
		return population
	}

	for _, subject := range committed {
		if subject.Kind == kind {
			population.Subjects = append(population.Subjects, subject)
		}
	}
	population.Declared = slots

	switch {
	case !enumerable:
		population.Census = populationAbsent
	case len(population.Subjects) > slots:
		// MORE COMMITTED THAN NAMED: the binding is ambiguous. The document
		// cannot say which committed refs are the operands, so nothing here
		// may claim a population -- this is an absence of knowledge, not a
		// count.
		population.Census = populationAbsent
	default:
		// A NAMED-SLOT OPERAND SET IS ALWAYS ENUMERABLE, INCLUDING AT ZERO.
		// The frame said who the operands are; `Declared` is measured
		// whatever committed. Zero committed here is reachable only through
		// a full retraction (applyCommitAffirmation is strictly
		// subtractive, and its own contract is that emptying `Committed`
		// still serves the caller the answer that was computed) -- on that
		// path the population WAS enumerated and the read DID run, so
		// reporting it as unverifiable would be false, and `degraded` would
		// absorb on the one path whose own code requires the caller still
		// be served.
		population.Census = populationEnumerated
	}
	return population
}

// cohortMemberPopulation is the `each_member` owner: THE SERVED COHORT, read
// through the cardinality step that already owns it.
//
// ONE AUTHORITY BY CONSTRUCTION. Every number and every verdict below is the
// cardinality's own: the second return distinguishes "no resolved member set"
// from a count of zero, `PopulationIncomplete` is the cohort's own coverage
// disjunction, and `Declared`/`Basis`/`Overrun` are carried. This file never
// restates `!Complete || Truncated` -- a second copy would be a second
// authority that could drift, and the cross-layer agreement test pins both
// directions.
func cohortMemberPopulation(cohort *Cohort, narrowing []contractsv1.ContextFabricPlanNarrowing) readPopulation {
	cardinality, resolved := ComputeMembershipCardinality(cohort, narrowing)
	if !resolved {
		return readPopulation{Census: populationAbsent}
	}
	population := readPopulation{
		Declared: cardinality.Declared,
		Basis:    cardinality.Basis,
		Overrun:  cardinality.Overrun,
		Census:   populationEnumerated,
	}
	if cardinality.PopulationIncomplete {
		population.Census = populationIncomplete
	}
	for _, member := range cohort.Members {
		population.Subjects = append(population.Subjects, member.Subject)
	}
	return population
}

// cohortGroupPopulation is the `each_group` owner: THE GROUP ENTITIES.
//
// The population is `Groups[*].Subject` -- the team itself -- and NOT
// `MemberCanonicalIDs`. A group's members are a different population, owned by
// `each_member`, and folding them in here would answer a question nobody asked.
//
// ITS NARROWING IS THE GROUP-AXIS STEP, because `firstMemberNarrowing`
// deliberately SKIPS group steps (membership_cardinality.go:155-166). Reusing
// the member narrowing would publish a group `Declared` that a group-axis
// narrowing had already reduced.
//
// READ §(i)-H BEFORE "FIXING" A 0/N HERE. At this pin every group population
// reads zero read subjects, because group entities never enter the fact read:
// the read scope is request.Subjects else Cohort.Members, and groups are BUILT
// AFTER the read off member facts. `0/N` is the truthful disclosure. Projecting
// member facts onto the group entity to make it look served is FORBIDDEN -- it
// manufactures a witness for a subject no provider was ever asked about, which
// is the denominator-substitution defect arriving from the numerator side, and
// it would make `each_group` unfalsifiable.
func cohortGroupPopulation(cohort *Cohort, narrowing []contractsv1.ContextFabricPlanNarrowing) readPopulation {
	if cohort == nil || len(cohort.Groups) == 0 {
		return readPopulation{Census: populationAbsent}
	}
	population := readPopulation{
		Declared: len(cohort.Groups),
		Census:   populationEnumerated,
	}
	for _, group := range cohort.Groups {
		population.Subjects = append(population.Subjects, group.Subject)
	}
	// THE CENSUS IS THE OWNER'S, HERE TOO. This used to restate
	// `!cohort.Complete || cohort.Truncated` while the file header and the
	// member path both claimed the predicate is never restated -- a keystone
	// review caught the claim being stronger than the code. The cardinality
	// owner computes the same cohort-level fold (for groups, `Complete` and
	// `Truncated` are the conjunction/disjunction over the groups), so it is
	// ASKED rather than copied. Only `Declared` differs between the two axes,
	// and that is taken from the group narrowing below.
	if cardinality, resolved := ComputeMembershipCardinality(cohort, narrowing); resolved && cardinality.PopulationIncomplete {
		population.Census = populationIncomplete
	}
	if step, found := firstGroupNarrowing(narrowing); found && step.Before > population.Declared {
		population.Declared = step.Before
		population.Basis = step.Basis
		population.Overrun = step.Overrun
	}
	return population
}

// firstGroupNarrowing is firstMemberNarrowing's mirror for the GROUP axis: the
// earliest recorded step that narrowed groups, whose `Before` is therefore the
// largest group count this turn observed.
//
// A separate function rather than a boolean parameter on the member one,
// because the two answer different questions and a shared body with a flag is
// how they would later be "simplified" into disagreeing.
func firstGroupNarrowing(narrowing []contractsv1.ContextFabricPlanNarrowing) (contractsv1.ContextFabricPlanNarrowing, bool) {
	for _, step := range narrowing {
		if !step.Groups {
			continue
		}
		return step, true
	}
	return contractsv1.ContextFabricPlanNarrowing{}, false
}

// readPopulationEvidenceFrom assembles every population this document's
// distributive requirements could need, ONCE.
//
// Derived INSIDE the finalization from what is already in scope --
// `frame` for the operand slots, `result.SubjectResolution.Committed`,
// `result.Cohort`, `plan.Narrowing`, and the bundle for the per-subject read
// record. A second parameter carrying pre-derived populations was the
// alternative and was rejected: every input is already here, so a second
// carrier adds a way for the two to disagree without adding information.
func readPopulationEvidenceFrom(
	frame *QuestionFrame,
	result InvestigationResult,
	plan AnswerPlan,
	facts CanonicalFactBundle,
) readPopulationEvidence {
	evidence := readPopulationEvidence{
		Present:             true,
		coverage:            subjectReadCoverage(facts),
		memberPopulation:    cohortMemberPopulation(result.Cohort, plan.Narrowing),
		groupPopulation:     cohortGroupPopulation(result.Cohort, plan.Narrowing),
		operandPopulations:  map[SubjectKind]readPopulation{},
		comparisonStandards: map[SubjectKind]operandStandard{},
	}

	// EACH OPERAND KIND'S OWN STANDARD, off the published plan.
	//
	// The filter is the kind-level evaluator's own filter
	// (read_requirement_evaluation.go:418-427), deliberately: a requirement
	// that evaluator will not judge must not be a standard this gate judges
	// by, or the readiness question and the operand's own row would be
	// answered from different rows. An unknown quantifier is skipped here for
	// the same reason it is skipped there -- it yields no threshold, and the
	// gate below treats a missing standard as not-ready rather than inventing
	// one.
	for _, requirement := range plan.Requirements {
		if requirement.Kind != string(ObligationKindRead) || !requirement.Served() {
			continue
		}
		if requirement.Scope != string(CompletionScopeEachOperand) {
			continue
		}
		threshold, known := readQuantifierThreshold(requirement.Quantifier)
		if !known {
			continue
		}
		evidence.comparisonStandards[requirement.Subject] = operandStandard{
			kinds:     requirement.FactKinds,
			threshold: threshold,
		}
	}

	// The operand populations, one per subject kind the frame's explicit set
	// names. Built from the FRAME's slots, so a kind whose every operand was
	// retracted still gets a population with a non-zero Declared.
	if frame != nil && frame.SubjectExpression.Kind == SubjectExpressionExplicitSet &&
		frame.SubjectExpression.Explicit != nil {
		committed := result.SubjectResolution.Committed
		for _, kind := range operandKinds(frame) {
			population := operandPopulation(frame, committed, kind)
			evidence.operandPopulations[kind] = population
			// THE COMPARISON-WIDE SET: the union across kinds, in operand-kind
			// order then owner order, so it is deterministic without a sort
			// over a map.
			evidence.comparisonOperands = append(evidence.comparisonOperands, population.Subjects...)
			evidence.comparisonDeclared += population.Declared
		}
	}
	return evidence
}

// operandKinds returns the distinct subject kinds the frame's explicit set
// names, in first-appearance order.
//
// Order is the FRAME's, not a map's, because it seeds comparisonOperands and
// a map iteration there would make the sameness conjunct order-dependent.
func operandKinds(frame *QuestionFrame) []SubjectKind {
	var kinds []SubjectKind
	seen := map[SubjectKind]bool{}
	add := func(kind SubjectKind) {
		if kind == "" || seen[kind] {
			return
		}
		seen[kind] = true
		kinds = append(kinds, kind)
	}
	for _, operand := range frame.SubjectExpression.Explicit.Operands {
		if operand.Named != nil && operand.Named.ExpectedKind != nil {
			add(*operand.Named.ExpectedKind)
		}
		if operand.Scoped != nil {
			add(operand.Scoped.MemberKind)
		}
	}
	return kinds
}

// readSubjectCount returns how many of a population's subjects were READ --
// i.e. met the requirement's own kind standard on their own facts.
//
// Iterates `population.Subjects` and the requirement's declared kinds, NEVER
// the coverage map, so the count cannot depend on Go's map iteration order.
func readSubjectCount(
	population readPopulation,
	servedKinds []FactKind,
	threshold int,
	coverage map[string]map[FactKind]SourceState,
) int {
	read := 0
	for _, subject := range population.Subjects {
		if len(servedKindsForSubject(subject, servedKinds, coverage)) >= threshold {
			read++
		}
	}
	return read
}

// commonServedKinds is the SAMENESS conjunct's measurement: the kinds served
// for EVERY subject in the comparison-wide operand set.
//
// Returns the intersection in the requirement's own declared order. An EMPTY
// operand set yields no common kinds, which is the honest reading -- there is
// no evidence shared across operands that do not exist.
func commonServedKinds(
	operands []SubjectRef,
	servedKinds []FactKind,
	coverage map[string]map[FactKind]SourceState,
) []FactKind {
	if len(operands) == 0 {
		return nil
	}
	var common []FactKind
	for _, kind := range servedKinds {
		shared := true
		for _, subject := range operands {
			states := coverage[SubjectMapKey(subject)]
			state, observed := states[kind]
			if !observed || (state != SourceAvailable && state != SourceStale) {
				shared = false
				break
			}
		}
		if shared {
			common = append(common, kind)
		}
	}
	return common
}

// unreadSubjectCause names the mechanism behind an unread subject, when one
// REPORTED it.
//
// The distinction it draws is provenance, which is exactly what CauseObserved
// exists to carry: a subject that HAS a fact of a declared kind in a non-served
// state was reported on by a provider, and that state maps onto the coverage
// vocabulary through the evaluator's own mapping. A subject with no fact at all
// was not reported on -- its absence is INFERRED from silence, and the caller
// gets `fact_narrowed` with CauseObserved false rather than a mechanism nobody
// named.
func unreadSubjectCause(
	population readPopulation,
	servedKinds []FactKind,
	threshold int,
	coverage map[string]map[FactKind]SourceState,
) (contractsv1.ContextFabricCoverageDetailCode, bool) {
	worst := 0
	var code contractsv1.ContextFabricCoverageDetailCode
	for _, subject := range population.Subjects {
		if len(servedKindsForSubject(subject, servedKinds, coverage)) >= threshold {
			continue
		}
		states := coverage[SubjectMapKey(subject)]
		for _, kind := range servedKinds {
			state, observed := states[kind]
			if !observed || state == SourceAvailable || state == SourceStale {
				continue
			}
			if severity := sourceStateSeverity(state); severity > worst {
				worst = severity
				code = readCoverageCauseFor(state)
			}
		}
	}
	return code, code != ""
}

// readPopulationOutcomeRow decides a distributive read row once the KIND
// standard has already passed.
//
// IT IS CONSULTED ONLY AFTER `lossless`. The kind-level arms run first and a
// kind-level loss takes its own arm and never reaches here -- one mechanism per
// row, and the precedence is: kind-level loss > population not-enumerable >
// partially read > sameness failure > census incomplete > read everywhere.
//
// THE UNITS CHANGE WITH THE ARM, and that is stated rather than implied. On the
// arms this function certifies or reduces by POPULATION, `Served`/`Declared`
// carry population counts -- the kind counts stay on the plan row's own
// `FactKinds`, where the lossless arm's comment already sends a reader. On the
// SAMENESS-failure arm they carry KIND counts, because that row failed a
// standard about evidence rather than about coverage, and the quantity it is
// reporting is a source shortfall.
func readPopulationOutcomeRow(
	row RequirementOutcomeRow,
	population readPopulation,
	evidence readPopulationEvidence,
	servedKinds []FactKind,
	threshold int,
) RequirementOutcomeRow {
	// NOT ENUMERABLE: nothing can name this population, so nothing may
	// certify or reduce over it. `unavailable`/`dimension` with its own
	// cause, and 0/0 -- "nothing was counted", not "a zero-member
	// population", told apart by the outcome token exactly as the
	// cardinality's own unserved row does.
	if population.Census == populationAbsent {
		row.Outcome = contractsv1.ContextFabricRequirementUnavailable
		row.Impact = contractsv1.ContextFabricAnswerImpactDimension
		row.CauseCoverage = contractsv1.ContextFabricCoverageDetailReadPopulationUnverified
		// OBSERVED: the owner reported this. A scoped operand, a nil cohort
		// and an ambiguous binding are all facts the document states, not
		// inferences this file drew from silence.
		row.CauseObserved = true
		row.Served = 0
		row.Declared = 0
		return row
	}

	read := readSubjectCount(population, servedKinds, threshold, evidence.coverage)

	// PARTIALLY READ: fewer of the population were read than the owner
	// declared. Scope, not depth -- the caller is shown fewer subjects, and
	// what stands behind the ones that remain is unchanged.
	if read < population.Declared {
		row.Outcome = contractsv1.ContextFabricRequirementNarrowed
		row.Impact = contractsv1.ContextFabricAnswerImpactScope
		row.Served = read
		row.Declared = population.Declared
		if code, observed := unreadSubjectCause(population, servedKinds, threshold, evidence.coverage); observed {
			row.CauseCoverage = code
			row.CauseObserved = true
			return row
		}
		// Nothing reported anything about the unread subjects; their absence
		// is inferred from silence, and the flag says inferred.
		row.CauseCoverage = contractsv1.ContextFabricCoverageDetailFactNarrowed
		row.CauseObserved = false
		return row
	}

	// SAMENESS, and it is evaluated ONLY once every operand OF THE WHOLE
	// COMPARISON is read -- not merely every subject of THIS row's kind.
	//
	// THE GATE MUST BE COMPARISON-WIDE BECAUSE THE INTERSECTION IS. Gating on
	// this kind's population while intersecting across every kind is the
	// defect a keystone review reproduced: with a fully-read team and an
	// unread project, the team row satisfied its own `read == Declared`, took
	// the sameness arm, and reported `depth 0/2` -- telling a reader the
	// operands' evidence DIFFERS before the missing operand had any evidence
	// to differ with. The shortfall is the unread operand's to report, and its
	// own row says `scope 0/1`.
	//
	// So the intersection runs only when the comparison-wide set is complete
	// AND every member of it meets ITS OWN standard. Otherwise this row falls
	// through to its own arms, which describe what happened to ITS population
	// and claim nothing about the comparison.
	//
	// READINESS AT THE OPERAND'S STANDARD, SAMENESS AT THIS ROW'S. The gate
	// asks whether each operand was read as its own requirement demands; the
	// intersection below then asks whether what they share meets THIS row's
	// threshold. Asking both at this row's standard is the false-complete a
	// keystone review reproduced -- see comparisonStandards.
	// NO `len(evidence.comparisonOperands) > 0` CONJUNCT HERE, DELIBERATELY.
	//
	// It used to lead this condition and it was REDUNDANT WITH the predicate's
	// own first guard: `comparisonFullyRead` returns false when
	// `comparisonDeclared` is zero, so a requirement with no comparison can
	// never open this arm. A battery proved the redundancy the only way it
	// can be proved -- BOTH deletions survived the whole suite, each dead only
	// because the other lived, and no single-mutant test could have killed
	// either. Two tests pinning two clauses that individually change nothing
	// would have passed on the unfixed tree.
	//
	// The guard stays in the PREDICATE, not here: `comparisonFullyRead` should
	// be total on its own inputs rather than depend on a caller checking the
	// same thing first. That is now a load-bearing clause with a real arm
	// against it.
	if population.Census != populationIncomplete && evidence.comparisonFullyRead() {
		common := commonServedKinds(evidence.comparisonOperands, servedKinds, evidence.coverage)
		if len(common) < threshold {
			// Depth: the subjects the answer covers are unchanged, and what
			// stands behind them is thinner. KIND counts, and no refinement
			// -- no reduction STEP ran, there was simply less shared to
			// begin with.
			row.Outcome = contractsv1.ContextFabricRequirementNarrowed
			row.Impact = contractsv1.ContextFabricAnswerImpactDepth
			row.CauseCoverage = contractsv1.ContextFabricCoverageDetailFactNarrowed
			row.CauseObserved = false
			row.Served = len(common)
			row.Declared = threshold
			return row
		}
	}

	// CENSUS INCOMPLETE: everything enumerated was read, and the OWNER
	// reported that the enumeration is not the whole population. The counts
	// are the owner's own and are EQUAL -- which is legal only through the
	// census exception, and only because the owner observed the shortfall.
	if population.Census == populationIncomplete {
		row.Outcome = contractsv1.ContextFabricRequirementNarrowed
		row.Impact = contractsv1.ContextFabricAnswerImpactScope
		row.CauseCoverage = contractsv1.ContextFabricCoverageDetailPopulationTruncated
		row.CauseObserved = true
		row.Served = read
		row.Declared = population.Declared
		return row
	}

	// READ EVERYWHERE, at the declared standard, over a population its owner
	// enumerated in full.
	row.Outcome = contractsv1.ContextFabricRequirementSatisfied
	row.Impact = contractsv1.ContextFabricAnswerImpactNone
	row.CauseCoverage = ""
	row.CauseObserved = false
	row.Served = read
	row.Declared = population.Declared
	return row
}

// countUnits says WHICH quantity a read row's two integers carry.
//
// CLOSED, two members, emitted on EVERY line. It exists because §(i)-E
// publishes population counts on some arms and KIND counts on others -- a
// `single_subject` row, any row that failed the kind standard, and the
// sameness-failure arm -- and without it an operator reading `row_served=1`
// cannot tell one served subject from one served fact kind.
//
// ONE NEUTRAL PAIR PLUS A DISCRIMINATOR, never two pairs with the inapplicable
// one zeroed: a `population_served=0` on a kind-counted row is a measurement
// nobody took, and this package already holds that "0" and "never ran" must not
// look alike. The token is derived from the same branch that chose the counts,
// so the label cannot disagree with the number beside it.
type countUnits string

const (
	countUnitsPopulation countUnits = "population"
	countUnitsKind       countUnits = "kind"
)

// ReadRequirementPopulationEvent is one distributive read row, as an operator
// reads it.
type ReadRequirementPopulationEvent struct {
	Family      QuestionFamily
	Requirement string
	Scope       string
	Outcome     contractsv1.ContextFabricPlanRequirementOutcome
	Impact      contractsv1.ContextFabricAnswerImpactKind
	Cause       contractsv1.ContextFabricCoverageDetailCode
	// CauseObserved distinguishes a mechanism that REPORTED the shortfall
	// from one this evaluator inferred from silence.
	CauseObserved bool
	// Served and Declared are the ROW's own two integers, whatever they
	// count, and Units says which.
	Served   int
	Declared int
	Units    countUnits
	// Census is read from the POPULATION AUTHORITY, never from the row's
	// cause. Under §(i)-E's precedence a partially-read row outranks a
	// census-incomplete one, so deriving the census from `Cause` would
	// publish `enumerated` for a cohort the owner REPORTED incomplete --
	// the event contradicting the authority it exists to report.
	Census populationCensus
	// CohortComplete and CohortTruncated are FALSE on every `each_operand`
	// line, because an explicit set never produces a cohort. A reader who
	// does not know that will read `false` as "the cohort was incomplete"
	// rather than "there was no cohort".
	CohortComplete  bool
	CohortTruncated bool
}

// readRequirementPopulationEventsFrom builds one event per distributive read
// row of the SERVED document.
//
// THE FRAME IS A PARAMETER, NOT OPTIONAL: operand slot counts come from the
// frame's own explicit set and cannot be recovered from `result` alone.
//
// It reads the row's numbers OFF THE ROW and asks the population authority ONLY
// for `Census`. That split is load-bearing: `Declared`, `Basis` and `Overrun`
// CAN diverge between row time and emit time, because a candidate-narrowing
// step is appended to the plan AFTER the row is computed and
// `firstMemberNarrowing` does not skip it -- so a `Declared` re-read here can
// be a different number from the one the row carries. `Census` is immune: it
// reads no narrowing at all.
func readRequirementPopulationEventsFrom(
	frame *QuestionFrame,
	result InvestigationResult,
	plan AnswerPlan,
	facts CanonicalFactBundle,
	family QuestionFamily,
) []ReadRequirementPopulationEvent {
	if result.AnswerPlan == nil {
		return nil
	}
	populations := readPopulationEvidenceFrom(frame, result, plan, facts)
	byIdentity := make(map[string]contractsv1.ContextFabricPlanRequirement, len(result.AnswerPlan.Requirements))
	for _, requirement := range result.AnswerPlan.Requirements {
		byIdentity[requirement.Requirement] = requirement
	}

	var events []ReadRequirementPopulationEvent
	for _, row := range result.Completeness.Outcomes {
		if row.Stage != contractsv1.ContextFabricOutcomeStageAssembledResult {
			continue
		}
		requirement, joined := byIdentity[row.Requirement]
		if !joined || requirement.Kind != string(ObligationKindRead) || !distributiveScope(requirement.Scope) {
			continue
		}
		population, owned := populations.populationFor(requirement)
		event := ReadRequirementPopulationEvent{
			Family:        family,
			Requirement:   row.Requirement,
			Scope:         requirement.Scope,
			Outcome:       row.Outcome,
			Impact:        row.Impact,
			Cause:         row.CauseCoverage,
			CauseObserved: row.CauseObserved,
			Served:        row.Served,
			Declared:      row.Declared,
			Units:         rowCountUnits(row),
		}
		if owned {
			event.Census = population.Census
		}
		if result.Cohort != nil {
			event.CohortComplete = result.Cohort.Complete
			event.CohortTruncated = result.Cohort.Truncated
		}
		events = append(events, event)
	}
	return events
}

// rowCountUnits says which quantity a distributive row's two integers carry.
//
// IMPACT ALONE CANNOT DECIDE IT, and believing it could was a defect a keystone
// review reproduced through the engine: `dimension` is carried BOTH by this
// layer's not-enumerable arm (population units, 0/0) AND by two KIND-level arms
// that never reach this layer at all -- the not-planned arm and the
// non-fact-bearing arm, which keep source counts. Labelling those `population`
// told an operator the wrong denominator on 21 measured combinations.
//
// The CAUSE is what separates them, because only this layer emits the
// population-absence code:
//
//	none                                        -> population  (read everywhere)
//	scope                                       -> population  (partially read / census incomplete)
//	depth                                       -> kind        (sameness shortfall)
//	dimension + read_population_unverified      -> population  (not enumerable, 0/0)
//	dimension + any other cause                 -> kind        (kind-level arms)
//
// A SWEEP TEST DRIVES EVERY ARM THROUGH THE EVENT BUILDER and asserts its
// units, because this function reads a FINISHED ROW rather than being told by
// the branch that chose the counts -- which is precisely how it drifted. The
// row is a contract type and cannot carry a units field without a wire change,
// so the sweep is the guard that a new arm cannot acquire a silent label.
func rowCountUnits(row RequirementOutcomeRow) countUnits {
	switch row.Impact {
	case contractsv1.ContextFabricAnswerImpactDepth:
		return countUnitsKind
	case contractsv1.ContextFabricAnswerImpactDimension:
		if row.CauseCoverage == contractsv1.ContextFabricCoverageDetailReadPopulationUnverified {
			return countUnitsPopulation
		}
		// A kind-level arm: nothing here was measured over a population.
		return countUnitsKind
	}
	return countUnitsPopulation
}

// comparisonFullyRead reports whether EVERY operand the frame names, across all
// subject kinds, was read to THAT OPERAND'S OWN declared standard.
//
// TWO CONJUNCTS, and both are needed. Every committed operand must meet its own
// threshold -- and the committed set must be the whole named set, because an
// operand that never resolved has no subject to test and would otherwise be
// skipped into a false "all read".
//
// THE STANDARD IS THE OPERAND'S, NOT THE CALLING ROW'S. This function takes no
// servedKinds/threshold parameters on purpose: it once did, and judging every
// operand at the calling row's standard is what let a fully-read operand score
// short against a catalog it was never declared over and suppress the very
// disclosure it was gated for. The comparison-wide SET stays comparison-wide;
// only the STANDARD each member is judged by became its own.
func (e readPopulationEvidence) comparisonFullyRead() bool {
	if e.comparisonDeclared == 0 || len(e.comparisonOperands) != e.comparisonDeclared {
		return false
	}
	for _, subject := range e.comparisonOperands {
		// NO STANDARD IS NOT-READY, never a pass. An operand whose kind
		// published no read requirement this turn has nothing to be measured
		// against, and certifying it read would be a claim from an absence.
		standard, declared := e.comparisonStandards[subject.Kind]
		if !declared {
			return false
		}
		if len(servedKindsForSubject(subject, standard.kinds, e.coverage)) < standard.threshold {
			return false
		}
	}
	return true
}
