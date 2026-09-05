package contextfabric

// The obligation -> REQUIREMENT derivation. Design §13.15.2 (the excised
// CompletionScope x CompletionQuantifier split and the FactKinds seed),
// §13.11a O9, law L3.
//
// WHAT THIS LAYER ADDS TO subject_role.go. That file answers "which
// (role, subject kind) cells does this frame demand?" from the frame
// alone. This one crosses each cell with the REGISTRY -- through the seed
// generated from the producers' own declarations -- and answers "can any
// producer serve it, and if not, WHY NOT". The why-not is the part the
// design cared about: §13.15.1's executed trace found empty cells, and a
// bare emptiness count "would hand the next reader a number they cannot
// act on".
//
// THE DIMENSIONS INTERSECTION IS DELIBERATELY ABSENT. §13.15.2 leaves
// "may Dimensions narrow evidence at all?" to be decided by running the
// composed cases on real data, and §13.15.3 forbids writing an
// intersection rule before that. So this layer reads the seed unnarrowed,
// and there is no `dimension_excluded` member in the unavailable-reason
// vocabulary: a closed token that no input can currently produce is a dead
// tier, and a dead tier is indistinguishable from an enforcement check that
// always answers no. The token arrives with the fixture that lands in it.
//
// SHADOW ONLY. Nothing here gates an answer, a plan, a render or a
// clarification; the rows are derived, telemetered and asserted.

import (
	"fmt"
	"sort"
	"strings"
)

// CompletionScope names OVER WHAT a requirement must be completed (law
// L3's first half).
//
// Closed vocabulary, telemetry-safe.
//
// It is derived from the requirement's ROLE rather than directly from
// SubjectExpression.Kind, and the two agree by construction because the
// role is itself derived from the variant (frameRoleSlots). Keying on the
// role is strictly finer: a grouped frame has BOTH a member scope and a
// group scope, and a single key on the variant cannot say which of its two
// cells it is talking about -- which is the ambiguity §13.15.2 records as
// the reason the frozen CompletionRule "reading both inputs does not
// resolve conflicting outputs".
type CompletionScope string

const (
	// CompletionScopeSingleSubject: one named subject, or the
	// organization itself.
	CompletionScopeSingleSubject CompletionScope = "single_subject"
	// CompletionScopeEachOperand: every operand of a comparison. This is
	// the scope PropertyCompletionScopeEachOperand names as the compare
	// goal's discharge (§13.4.2: "a comparison is not answered by reading
	// state once") -- named there, built here.
	CompletionScopeEachOperand CompletionScope = "each_operand"
	// CompletionScopeEachMember: every member of a discovered, scoped or
	// grouped set.
	CompletionScopeEachMember CompletionScope = "each_member"
	// CompletionScopeEachGroup: every group of a grouped set.
	CompletionScopeEachGroup CompletionScope = "each_group"
)

var completionScopes = [...]CompletionScope{
	CompletionScopeSingleSubject,
	CompletionScopeEachOperand,
	CompletionScopeEachMember,
	CompletionScopeEachGroup,
}

// CompletionScopeCount is four.
const CompletionScopeCount = len(completionScopes)

// CompletionScopeVocabulary returns the closed vocabulary in design order.
func CompletionScopeVocabulary() [CompletionScopeCount]CompletionScope {
	return completionScopes
}

// scopeForRole is total over the SubjectRole vocabulary. Totality is
// asserted by a test rather than by a default arm: a default would give a
// future role a silently wrong scope, and the scope is what a completeness
// claim is made against.
var scopeForRole = map[SubjectRole]CompletionScope{
	SubjectRoleSubject: CompletionScopeSingleSubject,
	SubjectRoleOperand: CompletionScopeEachOperand,
	SubjectRoleMember:  CompletionScopeEachMember,
	SubjectRoleGroup:   CompletionScopeEachGroup,
}

// CompletionQuantifier names HOW MUCH evidence completes a requirement
// (law L3's second half).
//
// Closed vocabulary, telemetry-safe.
type CompletionQuantifier string

const (
	// CompletionQuantifierAtLeastOne: one serving fact kind exists, so one
	// read completes the requirement.
	CompletionQuantifierAtLeastOne CompletionQuantifier = "at_least_one"
	// CompletionQuantifierCorroborated: two or more distinct fact kinds
	// serve the cell, so the requirement demands agreement across them.
	CompletionQuantifierCorroborated CompletionQuantifier = "corroborated"
	// CompletionQuantifierExact: a cardinality is exact or it is wrong.
	CompletionQuantifierExact CompletionQuantifier = "exact"
	// CompletionQuantifierAll: the computed step must cover the whole
	// population it orders.
	CompletionQuantifierAll CompletionQuantifier = "all"
	// CompletionQuantifierNone: no quantifier applies, because nothing
	// serves the cell. Paired ALWAYS with a non-empty Unavailable reason;
	// the pairing is asserted, so "no quantifier" can never read as a
	// silently satisfied requirement.
	CompletionQuantifierNone CompletionQuantifier = "none"
)

var completionQuantifiers = [...]CompletionQuantifier{
	CompletionQuantifierAtLeastOne,
	CompletionQuantifierCorroborated,
	CompletionQuantifierExact,
	CompletionQuantifierAll,
	CompletionQuantifierNone,
}

// CompletionQuantifierCount is five.
const CompletionQuantifierCount = len(completionQuantifiers)

// CompletionQuantifierVocabulary returns the closed vocabulary in design
// order.
func CompletionQuantifierVocabulary() [CompletionQuantifierCount]CompletionQuantifier {
	return completionQuantifiers
}

// RequirementUnavailableReason names WHY a required cell has no server.
//
// Closed vocabulary, telemetry-safe. §13.15's green condition is that a
// cell "either names >=1 fact kind a producer can serve for that subject
// kind, or names `unavailable` with a closed reason token -- never
// silently empty", so this vocabulary is half of oracle O9.
//
// EACH MEMBER IS ACTIONABLE BY A DIFFERENT PARTY, which is the test a
// reason token has to pass to earn its place. `subject_kind_unsupported`
// means no producer reaches that subject kind at all and a NEW PRODUCER is
// needed. `no_declaring_producer` means producers reach it but none claims
// the obligation, so a DECLARATION change could serve it. `table_shape_
// undeclared` means a producer claims the obligation for that subject kind
// but does not emit the table shape the obligation demands, so a QUERY
// change could serve it. Collapsing any two of them would produce the bare
// count §13.15.1 warns about.
type RequirementUnavailableReason string

const (
	// RequirementReasonSubjectKindUnsupported: no registered capability
	// lists this subject kind in SupportedSubjectKinds.
	RequirementReasonSubjectKindUnsupported RequirementUnavailableReason = "subject_kind_unsupported"
	// RequirementReasonNoDeclaringProducer: capabilities support the
	// subject kind, but none declares this obligation for it.
	RequirementReasonNoDeclaringProducer RequirementUnavailableReason = "no_declaring_producer"
	// RequirementReasonTableShapeUndeclared: a capability declares the
	// obligation for this subject kind, but the obligation demands a table
	// shape it does not declare there.
	RequirementReasonTableShapeUndeclared RequirementUnavailableReason = "table_shape_undeclared"
	// RequirementReasonComputedPopulationAbsent: a COMPUTED obligation whose
	// server step has nothing to run over.
	//
	// frame_vocab.go already says it: "a computed obligation has NO
	// FactKinds of its own and is unavailable only when ITS INPUTS ARE".
	// This is that sentence implemented. `ranking` orders a member set and
	// `count` counts one; the organization as a single subject is not a
	// population, so a frame asking to rank the organization asks for an
	// ordering with nothing to order. Reporting `rank_cohort` as the server
	// there would be a confident answer to an impossible question -- the
	// same mis-typing round 4 recorded as N3, in the other direction.
	RequirementReasonComputedPopulationAbsent RequirementUnavailableReason = "computed_population_absent"
)

var requirementUnavailableReasons = [...]RequirementUnavailableReason{
	RequirementReasonSubjectKindUnsupported,
	RequirementReasonNoDeclaringProducer,
	RequirementReasonTableShapeUndeclared,
	RequirementReasonComputedPopulationAbsent,
}

// RequirementUnavailableReasonCount is four.
const RequirementUnavailableReasonCount = len(requirementUnavailableReasons)

// RequirementUnavailableReasonVocabulary returns the closed vocabulary in
// design order.
func RequirementUnavailableReasonVocabulary() [RequirementUnavailableReasonCount]RequirementUnavailableReason {
	return requirementUnavailableReasons
}

// DerivedRequirement is one requirement row: a coordinate, what serves it,
// and how much of it completes.
//
// EXACTLY ONE OF FactKinds / Step / Unavailable IS MEANINGFUL, and the
// combination is asserted rather than trusted:
//   - a served READ row has FactKinds non-empty, Step empty, Unavailable empty
//   - a COMPUTED row has Step non-empty, FactKinds empty, Unavailable empty
//   - an unserved row has Unavailable non-empty and Quantifier `none`
//
// The invariant is checked by RequirementRowsAreWellFormed and asserted in
// the gate, because "silently empty" is precisely the state §13.15's green
// condition forbids and an unchecked struct admits it.
type DerivedRequirement struct {
	RequirementCoordinate

	// Kind is the obligation's classification, copied onto the row so a
	// reader of the row alone can tell a read from a computation without
	// re-consulting the vocabulary.
	Kind AnswerObligationKind

	// FactKinds are the kinds that can serve this cell, sorted. Empty for
	// a computed obligation and for an unavailable one.
	FactKinds []FactKind

	// Dimensions is what this requirement's EVIDENCE COVERS: the health
	// dimensions its serving fact kinds declare, deduplicated and in
	// vocabulary order. Empty for a computed obligation and for an
	// unavailable one, because neither reads a fact.
	//
	// THIS IS NOT QuestionFrame.Dimensions, AND THE DISTINCTION IS
	// DESIGN §13.3 -- two different things stage 1 called one. The frame's
	// Dimensions are WHAT THE QUESTION IS ABOUT ("how is delivery flow for
	// team X" names one; "how is team X doing" names none), model-proposed
	// and additive-only. This field is WHAT THE PLANNED EVIDENCE ACTUALLY
	// COVERS, derived by the SERVER from the producers' own
	// FactCapability.Dimension declarations and never model-authored.
	//
	// They must not be collapsed, and pluralizing one field would have
	// preserved stage 1's error with a slice. The first ADDS obligations
	// and constrains which fact kinds serve them; the second describes
	// what a planned requirement reads. A model that could write this
	// field could narrow the evidence its own answer is checked against,
	// which is the narrowing power round-1 F3 took away from the frame's
	// Dimensions and which must not reappear on the requirement.
	//
	// QuestionFamilyDefinition.Dimension -- the singular scalar on the
	// family registry row -- is untouched and stays a compatibility and
	// telemetry value. Nothing new reads it. §13.3 rules the intermediate
	// registry amendment SKIPPED rather than deferred, so the requirement
	// row is where per-requirement dimensions land, once.
	Dimensions []HealthDimension

	// Step is the server step that satisfies a computed obligation.
	// Empty for a read obligation.
	Step ComputedObligationStep

	// InputClass and InputFactKinds are the §13.2.3 amendment: WHAT THE
	// COMPUTATION CONSUMES. Both are empty on a read obligation and on an
	// unavailable one.
	//
	// THEY ARE DELIBERATELY NOT FactKinds. FactKinds means "kinds that can
	// SERVE this cell", and every existing reader -- the plan projection
	// included -- treats it as a planned READ. A computation's inputs are
	// facts some OTHER cell is responsible for reading; folding them into
	// FactKinds would make a computed row look like it planned reads of its
	// own, which is the mis-typing round 4 recorded as N3 arriving from the
	// opposite direction. Separate fields keep both statements true at once:
	// this cell reads nothing, AND these are the kinds its step consumes.
	//
	// Why the class is not inferable from the list: a step that consumes no
	// fact would otherwise be indistinguishable from a step nobody has
	// declared inputs for. See ComputedStepInputClass.
	InputClass ComputedStepInputClass
	// InputFactKinds are the kinds the step consumes, sorted in fact-kind
	// vocabulary order and deduplicated, so two runs of one frame produce
	// the same bytes in the regenerated artifact.
	InputFactKinds []FactKind
	// StepExecution says whether a server function actually RUNS this step.
	// Empty on a read obligation and on an unavailable one.
	//
	// It is on the row because a consumer reasoning about what the answer
	// depends on needs both halves together: a step that consumes nothing
	// and a step nobody executes are indistinguishable from the input fields
	// alone, and treating the second like the first is what would let an
	// unexecuted step's "consumes nothing" authorize retiring the thing that
	// actually causes the facts to be read.
	StepExecution ComputedStepExecution

	Scope      CompletionScope
	Quantifier CompletionQuantifier

	// Unavailable is the closed reason token, empty when the cell is
	// served.
	Unavailable RequirementUnavailableReason
}

// Served reports whether the row has a server -- a fact kind for a read, a
// step for a computation.
func (r DerivedRequirement) Served() bool {
	return r.Unavailable == ""
}

// DeriveRequirements crosses a frame's requirement coordinates with the
// registry's own declarations.
//
// seed is the generated obligation seed (GenerateObligationSeed over the
// same capabilities); capabilities is the registry's declaration list, used
// ONLY to attribute an empty cell to a cause. Both are passed rather than
// read from a package global so the function is pure and total over its
// input, and so the gate can run it against the live registry while a
// fixture test runs it against a constructed one.
//
// It is deterministic: coordinates arrive sorted and nothing here consults
// map iteration order.
func DeriveRequirements(frame QuestionFrame, seed ObligationSeed, capabilities []FactCapability) []DerivedRequirement {
	coordinates := DeriveRequirementCoordinates(frame)
	// Whether THIS FRAME can produce a resolved member set at all. Only a
	// cohort variant is ever discovered into one, so an organization-scope
	// frame naming a member kind states a population nothing retrieves.
	memberSetResolvable := frame.SubjectExpression.IsCohortVariant()
	rows := make([]DerivedRequirement, 0, len(coordinates))
	for _, coordinate := range coordinates {
		rows = append(rows, deriveRequirement(coordinate, seed, capabilities, memberSetResolvable))
	}
	return rows
}

func deriveRequirement(coordinate RequirementCoordinate, seed ObligationSeed, capabilities []FactCapability, memberSetResolvable bool) DerivedRequirement {
	kind, _ := KindOfObligation(coordinate.Obligation)
	row := DerivedRequirement{
		RequirementCoordinate: coordinate,
		Kind:                  kind,
		Scope:                 scopeForRole[coordinate.Role],
	}

	if kind == ObligationKindComputed {
		// A computed obligation needs a POPULATION to run over. The
		// organization as a single subject is not one.
		if !coordinateNamesAPopulation(coordinate) {
			row.Quantifier = CompletionQuantifierNone
			row.Unavailable = RequirementReasonComputedPopulationAbsent
			return row
		}
		// A computed obligation names its SERVER STEP and reads no fact
		// kind of its own: "a computed obligation has NO FactKinds of its
		// own and is unavailable only when its inputs are"
		// (frame_vocab.go). Modelling `ranking` as a read with a required
		// table shape is exactly the mis-typing round 4 recorded as N3 and
		// the reason Q2's defining obligation derived an empty set.
		step, named := StepForComputedObligation(coordinate.Obligation)
		if !named {
			// Unreachable while the two vocabulary tables agree, and the
			// registry test asserts they do. Handled rather than assumed,
			// because the alternative is a row that is neither served nor
			// unavailable.
			row.Quantifier = CompletionQuantifierNone
			row.Unavailable = RequirementReasonNoDeclaringProducer
			return row
		}
		// A STEP THAT RUNS OVER THE RESOLVED MEMBER SET NEEDS A FRAME THAT
		// PRODUCES ONE, and only a cohort variant does.
		//
		// Found by an adversarial round on the wiring slice. An
		// organization-scope frame naming a member kind ("how many
		// repositories are in the organization") derives this coordinate
		// legitimately -- the member ROLE names a population -- but nothing
		// discovers that population, so the step has no input and the cell
		// cannot be served. The row said `served` anyway, on the strength of
		// naming a step, and a complete answer then carried a count
		// requirement with no countable result.
		//
		// It reuses the reason a computed obligation with no population
		// already carries rather than minting a second token for the same
		// fact, and it is the SAME token the served answer's own outcome row
		// now carries when assembly finds no member set. One record, read in
		// two places.
		//
		// THE PREDICATE IS `RunsOverResolvedMemberSet`, NOT
		// `Class == ComputedInputResolvedMemberSet`, and the difference is
		// the whole of this cell's second defect. Class says what a step
		// READS. rank_cohort reads FACT KINDS -- its Class is `fact_kinds` --
		// and still runs only over a cohort, so the Class test covered
		// membership_cardinality and let rank_cohort through. A NAMED subject
		// is a population of one, so `coordinateNamesAPopulation` above
		// admits `ranking/subject/<named>`, and the row was then SERVED: it
		// named rank_cohort as its server, and `planningStageOutcomeRow`
		// seeds a served row `satisfied`. But `IsCohortVariant` is false for
		// `named_subject` (and for `organization_scope`), so the engine
		// resolves no cohort, RankCohort is never invoked, and
		// ComputedStepInputReads' five declared kinds are planned as reads
		// the fact request -- gated on the same cohort pointer -- never
		// carries. The cell claimed an ordering that nothing computed, over
		// facts that nothing read. Both halves close here, at the layer that
		// owns "a computed obligation is unavailable only when its inputs
		// are": an unavailable row is not Served, so ComputedStepInputReads
		// plans nothing for it and the seed says `unavailable` with a named
		// cause instead of a silent `satisfied`.
		if inputs, declared := InputsForComputedStep(step); declared &&
			inputs.RunsOverResolvedMemberSet && !memberSetResolvable {
			row.Quantifier = CompletionQuantifierNone
			row.Unavailable = RequirementReasonComputedPopulationAbsent
			// Step stays EMPTY, and that is the row invariant rather than an
			// omission: exactly one of FactKinds / Step / Unavailable is
			// meaningful, and a row that named both a step and a reason it
			// cannot run would be two answers to what became of the cell.
			return row
		}
		row.Step = step
		// The §13.2.3 amendment: the row records what the step CONSUMES,
		// not only what satisfies it. A step with no declaration would
		// leave the row exactly as it was before the amendment -- silently
		// -- so the vocabulary test asserts totality over the step list
		// rather than letting this branch fail open.
		if inputs, declared := InputsForComputedStep(step); declared {
			row.InputClass = inputs.Class
			row.InputFactKinds = inputs.FactKinds
			row.StepExecution = inputs.Execution
		}
		row.Quantifier = quantifierForComputed(coordinate.Obligation)
		return row
	}

	kinds := seed.KindsFor(coordinate.Obligation, coordinate.Subject)
	if len(kinds) == 0 {
		row.Quantifier = CompletionQuantifierNone
		row.Unavailable = classifyUnavailable(coordinate, capabilities)
		return row
	}
	row.FactKinds = kinds
	row.Dimensions = dimensionsOfFactKinds(kinds, capabilities)
	row.Quantifier = quantifierForCardinality(len(kinds))
	return row
}

// ComputedStepInputReads is what the derived rows PLAN TO READ on behalf of a
// computation: the declared inputs of every computed cell whose step the
// server actually executes.
//
// WHY THIS FUNCTION EXISTS AT ALL, and it is the whole of the ticket. The
// §13.2.3 amendment gave a computed row a place to say what its step
// CONSUMES, and said in the same breath that declaring an input is not
// planning a read -- so the declaration sat on the row with no consumer, and
// the six-authority parity proof recorded the gap as a blocking cell:
// `operational_deficiencies` is a declared `rank_cohort` input that no derived
// read row served, so retiring the authority that injects it would have
// dropped a real read. This is the consumer. Once the plan's fact kinds are
// built through it, the declaration IS the plan, and the proof's question --
// "would retiring this authority remove a read?" -- is answered by the rows.
//
// WHY NOT A READ ROW OF ITS OWN, which is the other closure design 14.3's H1
// weighs. A row needs a coordinate, a coordinate is obligation/role/subject,
// and that string is the wire join key -- `ranking/member/team` is already the
// computed row's identity, and the frame layer offers no second role or
// subject for the same cell. A new row therefore needs a new member in
// AnswerObligation, which is "the closed vocabulary of what an answer must
// ESTABLISH" (§13.2.2, thirteen members). A fact read only so RankCohort can
// order a cohort is not something the answer establishes, so minting an
// obligation for it would put a non-answer into the answer's own vocabulary.
// Routing it to an existing obligation that does serve the kind
// (`principal_drivers`, which serves `operational_deficiencies` for a team)
// fails the same way from the other side: it makes a rank-only answer
// responsible for establishing drivers, and drags in every other kind that
// obligation's seed carries. H1 asks whether the row closure "plans reads
// nobody needs"; on this corpus it does, and this is the closure that does
// not.
//
// TWO GUARDS, both of which decide a read and neither of which is inferable
// from the other:
//
//   - The row must be SERVED. An unavailable cell runs nothing, so reading its
//     declared inputs would fetch facts for a computation that cannot happen.
//     The derivation clears Step and StepExecution on an unavailable row, so
//     this guard is DEFENCE IN DEPTH against a row built by hand -- and it is
//     pinned by a fixture of exactly that shape rather than left as a rule no
//     test can reach.
//   - The step must be SERVER-EXECUTED. `declared_only` is the case D13 was
//     written about: a step named by the vocabulary and run by nothing. Its
//     inputs are exactly the read nobody needs, and planning them would be
//     this closure committing the error it exists to avoid. It is also what
//     keeps `computed_step_input_unserved` a live cause rather than a dead
//     tier -- a declared-only step's unserved input still blocks.
//
// Deduplicated and returned in fact-kind VOCABULARY order, like every other
// kind list this package hands out, so the plan, the artifact and the
// telemetry histogram all see one order. Returns nil when nothing is planned,
// never an empty slice.
func ComputedStepInputReads(rows []DerivedRequirement) []FactKind {
	var kinds []FactKind
	for _, row := range rows {
		if !row.Served() || row.StepExecution != ComputedStepServerExecuted {
			continue
		}
		kinds = append(kinds, row.InputFactKinds...)
	}
	return sortedFactKinds(kinds)
}

// dimensionsOfFactKinds reads the §13.3 requirement dimension off the
// producers' own declarations: the set of FactCapability.Dimension values
// carried by the kinds that actually serve this cell.
//
// SERVER-DERIVED, from declarations only. It consults no frame field, so a
// model emission cannot reach it -- which is the whole point of separating
// it from QuestionFrame.Dimensions.
//
// Deduplicated and returned in HealthDimension VOCABULARY order rather than
// in the order the kinds happen to arrive. A set whose order depends on its
// input's order is not a set, and the frame layer already paid for that
// lesson twice: a duplicate dimension produced a duplicate axis discharge,
// and a telemetry projection that kept the model's emission order made a
// "the frame is canonical" test pass while the recorded event was not.
func dimensionsOfFactKinds(kinds []FactKind, capabilities []FactCapability) []HealthDimension {
	if len(kinds) == 0 {
		return nil
	}
	serving := make(map[FactKind]bool, len(kinds))
	for _, kind := range kinds {
		serving[kind] = true
	}
	present := map[HealthDimension]bool{}
	for _, capability := range capabilities {
		if !serving[capability.Kind] {
			continue
		}
		if !ValidHealthDimension(capability.Dimension) {
			// A capability declaring a dimension outside the closed
			// vocabulary is DROPPED rather than propagated, on the same
			// rule the frame sanitizers follow: an unknown member is not
			// an error anywhere in this design, but carrying it would put
			// an unvalidated value into a closed field a reader trusts.
			continue
		}
		present[capability.Dimension] = true
	}
	if len(present) == 0 {
		return nil
	}
	out := make([]HealthDimension, 0, len(present))
	for _, dimension := range HealthDimensionVocabulary() {
		if present[dimension] {
			out = append(out, dimension)
		}
	}
	return out
}

// coordinateNamesAPopulation reports whether a coordinate names a set a
// server step can run over.
//
// A member set and a comparison's operands are populations. A NAMED subject
// is a population of one, which is degenerate but well defined -- counting
// it gives one, ordering it gives that one. The ORGANIZATION as a subject is
// not: it is the container, not the things in it, and invariant I17 exists
// precisely so "how many teams" and "how many repositories" do not collapse
// into the same organization-scoped frame. A frame that reaches here with
// the organization and no member kind has asked to order or count the
// container itself.
func coordinateNamesAPopulation(coordinate RequirementCoordinate) bool {
	if coordinate.Role == SubjectRoleSubject && coordinate.Subject == SubjectOrganization {
		return false
	}
	return coordinate.Role != SubjectRoleGroup
}

// quantifierForComputed maps a computed obligation to its quantifier.
//
// `count` is EXACT because a cardinality that is approximately right is
// wrong; `ranking` is ALL because an ordering that covers part of its
// population is not an ordering of that population. Stated as a table
// rather than folded into one arm, so the two can diverge later without a
// silent change to the other.
func quantifierForComputed(obligation AnswerObligation) CompletionQuantifier {
	if obligation == ObligationCount {
		return CompletionQuantifierExact
	}
	return CompletionQuantifierAll
}

// quantifierForCardinality is law L3's rule, derived from the MEASURED
// cardinality of the generated seed.
//
// §13.15.2 records why the frozen constant could not stand: `state =
// corroborated` "cannot be met by a one-kind seed anywhere", and its escape
// clause ("or the registry declares only one kind") silently degraded it to
// at_least_one wherever it bit -- a bar asserted and then quietly lowered.
// Deriving it means the plan demands corroboration exactly where
// corroboration is AVAILABLE and says at_least_one where it is not, which
// is a claim a reader can check against the seed.
func quantifierForCardinality(cardinality int) CompletionQuantifier {
	if cardinality >= 2 {
		return CompletionQuantifierCorroborated
	}
	return CompletionQuantifierAtLeastOne
}

// classifyUnavailable attributes an empty cell to the FIRST clause that
// rejected it, walking the clauses from the coarsest to the finest.
//
// The order is what makes the attribution meaningful: a subject kind no
// producer reaches at all is not "a missing declaration", and a producer
// that declares the obligation but emits the wrong shape is not "no
// producer". Reversing any pair would report a cause whose remedy does not
// apply.
func classifyUnavailable(coordinate RequirementCoordinate, capabilities []FactCapability) RequirementUnavailableReason {
	supportsSubject := false
	declaresObligation := false
	for _, capability := range capabilities {
		if !capabilitySupports(capability, coordinate.Subject) {
			continue
		}
		supportsSubject = true
		for _, declared := range capability.Obligations[coordinate.Subject] {
			if declared == coordinate.Obligation {
				declaresObligation = true
				break
			}
		}
	}
	switch {
	case !supportsSubject:
		return RequirementReasonSubjectKindUnsupported
	case !declaresObligation:
		return RequirementReasonNoDeclaringProducer
	default:
		// A producer declares it for this subject kind, yet the generated
		// seed dropped the cell. GenerateObligationSeed has exactly one
		// filter that can do that: the required table shape
		// (obligationRequiredTableShape). This arm is therefore an
		// inference about a specific line of a specific function, and it
		// is pinned by a fixture test that constructs the case rather than
		// left as a comment.
		return RequirementReasonTableShapeUndeclared
	}
}

func capabilitySupports(capability FactCapability, subject SubjectKind) bool {
	for _, supported := range capability.SupportedSubjectKinds {
		if supported == subject {
			return true
		}
	}
	return false
}

// RequirementRowsAreWellFormed checks the row invariant described on
// DerivedRequirement and returns the first violation.
//
// IT EXISTS AS A FUNCTION RATHER THAN AS ASSERTIONS INSIDE A TEST because
// "silently empty" is the state the design forbids, and a check that lives
// in one test file only guards the frames that test happens to build. Every
// caller that derives rows can run it over what it derived.
func RequirementRowsAreWellFormed(rows []DerivedRequirement) error {
	for _, row := range rows {
		served := row.Unavailable == ""
		switch {
		case !served:
			if row.Quantifier != CompletionQuantifierNone {
				return fmt.Errorf("unavailable requirement %s/%s/%s carries quantifier %q, which reads as a satisfiable requirement", row.Obligation, row.Role, row.Subject, row.Quantifier)
			}
			if len(row.FactKinds) > 0 || row.Step != "" {
				return fmt.Errorf("unavailable requirement %s/%s/%s also names a server", row.Obligation, row.Role, row.Subject)
			}
		case row.Kind == ObligationKindComputed:
			if row.Step == "" {
				return fmt.Errorf("computed requirement %s/%s/%s names no server step", row.Obligation, row.Role, row.Subject)
			}
			if len(row.FactKinds) > 0 {
				return fmt.Errorf("computed requirement %s/%s/%s names fact kinds; a computed obligation reads none of its own", row.Obligation, row.Role, row.Subject)
			}
		default:
			if len(row.FactKinds) == 0 {
				return fmt.Errorf("served read requirement %s/%s/%s names no fact kind and no unavailable reason -- silently empty", row.Obligation, row.Role, row.Subject)
			}
			if row.Step != "" {
				return fmt.Errorf("read requirement %s/%s/%s names a server step", row.Obligation, row.Role, row.Subject)
			}
		}
		if row.Scope == "" {
			return fmt.Errorf("requirement %s/%s/%s has no completion scope", row.Obligation, row.Role, row.Subject)
		}
	}
	return nil
}

// RequirementCauseCount is one line of the cause distribution: an
// obligation, a reason, and how many cells it accounts for.
type RequirementCauseCount struct {
	Obligation AnswerObligation
	Reason     RequirementUnavailableReason
	Cells      int
}

// RequirementCauseDistribution decomposes the unserved cells by obligation
// and cause, in a total order.
//
// §13.15.1's own lesson, in its own words: publishing the bare count
// "would have handed the next reader a number they cannot act on". The
// distribution is also what makes the count DIAGNOSABLE when it does not
// move -- a producer gaining a subject kind while another loses one nets to
// zero, and only the causes say so.
func RequirementCauseDistribution(rows []DerivedRequirement) []RequirementCauseCount {
	type key struct {
		obligation AnswerObligation
		reason     RequirementUnavailableReason
	}
	counts := map[key]int{}
	for _, row := range rows {
		if row.Served() {
			continue
		}
		counts[key{row.Obligation, row.Unavailable}]++
	}
	distribution := make([]RequirementCauseCount, 0, len(counts))
	for at, cells := range counts {
		distribution = append(distribution, RequirementCauseCount{Obligation: at.obligation, Reason: at.reason, Cells: cells})
	}
	sort.Slice(distribution, func(i, j int) bool {
		if distribution[i].Obligation != distribution[j].Obligation {
			return distribution[i].Obligation < distribution[j].Obligation
		}
		return distribution[i].Reason < distribution[j].Reason
	})
	return distribution
}

// RenderRequirements writes one frame's requirement rows as a stable text
// block, for the regenerated trace artifact.
//
// Like RenderRequirementCoordinates it lives in production because it is the
// artifact's only authority. label is a question id or a structural label,
// never question text.
func RenderRequirements(label string, rows []DerivedRequirement) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%s\n", label)
	if len(rows) == 0 {
		out.WriteString("  (no requirement rows)\n")
		return out.String()
	}
	for _, row := range rows {
		server := "-"
		switch {
		case !row.Served():
			server = "UNAVAILABLE " + string(row.Unavailable)
		case row.Kind == ObligationKindComputed:
			server = "step:" + string(row.Step)
		default:
			names := make([]string, 0, len(row.FactKinds))
			for _, kind := range row.FactKinds {
				names = append(names, string(kind))
			}
			server = strings.Join(names, " ")
		}
		fmt.Fprintf(&out, "  %-22s %-9s %-14s %-15s %-13s %s\n",
			row.Obligation, row.Role, row.Subject, row.Scope, row.Quantifier, server)
	}
	return out.String()
}

// RenderRequirementCauseDistribution writes the cause decomposition in
// words, for the foot of the trace artifact.
func RenderRequirementCauseDistribution(distribution []RequirementCauseCount) string {
	if len(distribution) == 0 {
		return "# every derived requirement cell is served; no cause distribution to report.\n"
	}
	var out strings.Builder
	out.WriteString("# cause distribution over the unserved cells:\n")
	for _, line := range distribution {
		fmt.Fprintf(&out, "#   %-22s %-26s %d\n", line.Obligation, line.Reason, line.Cells)
	}
	return out.String()
}
