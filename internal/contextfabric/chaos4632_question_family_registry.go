package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4632 §3.1: the family definition table. SHADOW ONLY -- see
// chaos4632_question_family_vocab.go's package-level note.
//
// A FAMILY IS A DATA ROW, NOT A FUNCTION. That is the whole point of this
// file. Three judgements that decide answers today live in Go control flow
// and doc comments respectively -- subjectAxisAbsent, kindOfferMaterial's
// class gap, and the explicit_cohort boundary judgement (lane-4579's
// handoff §7 names all three). They become columns here, where a table-
// driven test can enumerate them and a reviewer can read them.
//
// NOTHING READS THIS TABLE IN THIS SLICE. S4 is where ApplicableAxes gates
// the offer builders and AskOrder orders the prompt; S5 is where FactRoles
// and Budget reach a plan. Declaring the columns first, with registry
// assertions, is deliberate: it is what lets S4 and S5 be behaviour
// changes to a reviewed table rather than to freshly-invented data.

// HealthDimension is the closed vocabulary of the nine canonical health
// dimensions.
//
// SOURCE, because this vocabulary is NOT derived from anything in this
// repository and must not be silently re-invented: the nine names below
// are verbatim, in published order, from the two canonical Linear
// documents named in AGENTS.md's North Star section --
//
//	"Ask Dev / Dev Health Ops North Star Summary" §19
//	"Dev Health Ops Purpose and Contract 2026-08-28" §3 ("Canonical Health
//	Dimensions")
//
// -- which carry identical lists. Before this file, NO enumeration of them
// existed anywhere in acr or in ops/docs; North Star §19 says so itself
// ("No such mapping exists as of 2026-08-28"), which is the premise of
// CHAOS-4468.
//
// SCOPE, stated exactly. CHAOS-4468's deliverable is three things: a
// dimension attribute on the FACTKIND registry, a generated
// dimension <-> FactKind <-> ranking-family mapping table in acr/docs, and
// both canonical docs updated to cite it. The design's slice plan (§9)
// puts that whole ticket in S3, NOT here. What lands here is only what §3.1
// requires of the FAMILY table: every family declares a dimension, and a
// registry test fails if one does not. The FactKind half, and the mapping
// that falls out of the two, is S3's.
//
// Do NOT confuse these nine with the older four-item FullChaos conceptual
// model (Delivery, Durability, Developer Well-being, Dynamics), which both
// documents also mention, nor with cohort_ranking.go's five ranking
// families. They are three different things.
type HealthDimension string

const (
	HealthDimensionExecutionCompletion HealthDimension = "execution_completion"
	HealthDimensionDeliveryFlow        HealthDimension = "delivery_flow"
	HealthDimensionReliabilityRelease  HealthDimension = "reliability_and_release"
	HealthDimensionReviewCIPressure    HealthDimension = "review_and_ci_pressure"
	HealthDimensionCodeOwnershipRisk   HealthDimension = "code_ownership_risk"
	HealthDimensionCognitiveWorkload   HealthDimension = "cognitive_workload_pressure"
	HealthDimensionInvestmentBalance   HealthDimension = "investment_balance"
	HealthDimensionDependenciesBlocked HealthDimension = "dependencies_and_blockers"
	HealthDimensionDataTrust           HealthDimension = "data_trust"
)

var healthDimensions = [...]HealthDimension{
	HealthDimensionExecutionCompletion,
	HealthDimensionDeliveryFlow,
	HealthDimensionReliabilityRelease,
	HealthDimensionReviewCIPressure,
	HealthDimensionCodeOwnershipRisk,
	HealthDimensionCognitiveWorkload,
	HealthDimensionInvestmentBalance,
	HealthDimensionDependenciesBlocked,
	HealthDimensionDataTrust,
}

// HealthDimensionCount is nine, and the registry test asserts it is nine --
// the documents call them "the nine canonical dimensions", so a tenth
// appearing here without those documents changing is a drift this build
// should not survive.
const HealthDimensionCount = len(healthDimensions)

// HealthDimensionVocabulary returns the closed vocabulary in published
// order.
func HealthDimensionVocabulary() [HealthDimensionCount]HealthDimension {
	return healthDimensions
}

// ValidHealthDimension reports whether value is a member of the closed
// vocabulary. The empty value is not a member.
func ValidHealthDimension(value HealthDimension) bool {
	for _, member := range healthDimensions {
		if member == value {
			return true
		}
	}
	return false
}

// SubjectAxisKind names how many subjects a family answers about, and how
// they are obtained. Closed vocabulary (§3.1).
//
// This is the column that replaces subjectAxisAbsent -- today a Go
// predicate in chaos4579_cohort_structure_gate.go deciding, per request,
// whether subject_anchor/subject_handle offers may be composed at all. As
// a column it is decidable per FAMILY, before any offer builder runs,
// which is S4's whole subsumption of CHAOS-4579's after-the-fact filter.
type SubjectAxisKind string

const (
	// SubjectAxisNone: no subject axis at all.
	SubjectAxisNone SubjectAxisKind = "none"
	// SubjectAxisOne: exactly one named subject.
	SubjectAxisOne SubjectAxisKind = "one"
	// SubjectAxisManyNamed: two or more subjects, each named in the
	// question.
	SubjectAxisManyNamed SubjectAxisKind = "many_named"
	// SubjectAxisManyDiscovered: many subjects, discovered by the system
	// rather than named -- window is the only structure axis a caller can
	// usefully be asked about.
	SubjectAxisManyDiscovered SubjectAxisKind = "many_discovered"
	// SubjectAxisManyScoped: many subjects, scoped by ONE named parent of
	// a different kind (Q-B).
	SubjectAxisManyScoped SubjectAxisKind = "many_scoped"
	// SubjectAxisManyGrouped: many subjects, partitioned by a grouping
	// kind (Q-A).
	SubjectAxisManyGrouped SubjectAxisKind = "many_grouped"
)

func validSubjectAxisKind(value SubjectAxisKind) bool {
	switch value {
	case SubjectAxisNone, SubjectAxisOne, SubjectAxisManyNamed,
		SubjectAxisManyDiscovered, SubjectAxisManyScoped, SubjectAxisManyGrouped:
		return true
	default:
		return false
	}
}

// FactRole names which subject role a fact answers for. Closed vocabulary,
// declared here because §3.1 lists FactRoles as a family column.
//
// DECLARED, NOT CONSUMED, in this slice. The planner work that reads these
// is §6 (S3/S5). The three roles come from §5.3's own wording: a fact kind
// answers for a "subject", a cohort "member", or a "group".
type FactRole string

const (
	FactRoleSubject FactRole = "subject"
	FactRoleMember  FactRole = "member"
	FactRoleGroup   FactRole = "group"
)

func validFactRole(value FactRole) bool {
	switch value {
	case FactRoleSubject, FactRoleMember, FactRoleGroup:
		return true
	default:
		return false
	}
}

// PlanBudgetProfile names the plan-time budget shape a family needs.
// Closed vocabulary, declared here because §3.1 lists Budget as a family
// column.
//
// DECLARED, NOT CONSUMED. §6.3 (slice S5, and CHAOS-4624) owns the actual
// three-stage budget arithmetic; this slice only records which profile
// each family will need, so S5 changes numbers behind a name rather than
// inventing the names at the same time as the numbers. The design's own
// §6.3a records that the budget stage was specified three different ways
// across revisions -- which is exactly why the NAME is pinned here and the
// arithmetic is not.
type PlanBudgetProfile string

const (
	// PlanBudgetSingleSubject: one subject, a bounded fact set.
	PlanBudgetSingleSubject PlanBudgetProfile = "single_subject"
	// PlanBudgetFlatCohort: one flat list of members.
	PlanBudgetFlatCohort PlanBudgetProfile = "flat_cohort"
	// PlanBudgetGroupedCohort: groups x members -- the profile whose
	// absence 413'd Q-A turn 2.
	PlanBudgetGroupedCohort PlanBudgetProfile = "grouped_cohort"
	// PlanBudgetMatchedPair: both sides of a comparison, same window,
	// same measures.
	PlanBudgetMatchedPair PlanBudgetProfile = "matched_pair"
	// PlanBudgetUnbounded: no family-derived bound -- unclassified only.
	PlanBudgetUnbounded PlanBudgetProfile = "unbounded"
)

func validPlanBudgetProfile(value PlanBudgetProfile) bool {
	switch value {
	case PlanBudgetSingleSubject, PlanBudgetFlatCohort, PlanBudgetGroupedCohort,
		PlanBudgetMatchedPair, PlanBudgetUnbounded:
		return true
	default:
		return false
	}
}

// NarrowingContinuationAxis names WHICH structural dimension of a question
// could be reduced to make its answer fit the response budget. Closed
// vocabulary, declared here because it is a per-family property and the
// registry is where a family's declared columns live.
//
// CHAOS-4735. This REPLACES `narrowerQuestionFor`, which switched on the
// family and returned one of five fixed English sentences that the route
// served verbatim as error.details.narrower_question in the 413 body. That
// was the banned shape -- a vocabulary-to-sentence table in the engine, on a
// user-facing wire -- under chris's rulings of 2026-08-31 13:35 and 13:40:
// language is the model layer's job at BOTH boundaries.
//
// WHY A TOKEN AND NOT A SENTENCE. The old sentences carried two things mixed
// together: a STRUCTURAL claim about what could be reduced, which the engine
// genuinely knows because it planned the answer, and an ENGLISH rendering of
// that claim, which the engine has no business authoring. The axis is the
// first half alone. A consumer -- or synthesis, under the same guards as
// coverage disclosures -- phrases it, or nothing is phrased at all.
//
// WHY IT LIVES IN THE REGISTRY AND NOT BESIDE THE REFUSAL. Reading the family
// to pick an axis is still reading the family, and the stage-2 amendment
// (design §13.4.3) keeps that read list closed at four purposes. A
// `switch plan.Family` in the budget stage would be a FIFTH site and would
// fail the sweep this ticket's criterion 4 installs -- correctly, because a
// switch returning a token today is one sentence away from being the table
// again. Declaring the axis as a registry column means the refusal LOOKS IT
// UP instead of deciding it, which is the move `planBudget` already makes for
// the budget profile.
type NarrowingContinuationAxis string

const (
	// NarrowingContinuationNone: no axis can be named, so the refusal
	// carries no continuation at all rather than a guess. The honest value
	// for `unclassified`, where nothing about the question's shape was
	// established in the first place.
	NarrowingContinuationNone NarrowingContinuationAxis = "none"
	// NarrowingContinuationEvidenceWindow: the same subject over less
	// time. The only axis available when there is exactly one subject and
	// the volume is in its facts.
	NarrowingContinuationEvidenceWindow NarrowingContinuationAxis = "evidence_window"
	// NarrowingContinuationResultCount: fewer members out of a discovered
	// cohort. Available here and not under scope_anchor because the
	// ranking is already computed, so a prefix of it is a real answer.
	NarrowingContinuationResultCount NarrowingContinuationAxis = "result_count"
	// NarrowingContinuationScopeAnchor: a narrower scope. Distinct from
	// result_count because a scoped cohort is not ranked -- cutting the
	// scope is the only principled reduction available to it.
	NarrowingContinuationScopeAnchor NarrowingContinuationAxis = "scope_anchor"
	// NarrowingContinuationGroupSelection: fewer groups. Decision D2
	// forbids dropping a group silently, so naming the group axis to the
	// caller is what remains once per-group narrowing is exhausted.
	NarrowingContinuationGroupSelection NarrowingContinuationAxis = "group_selection"
	// NarrowingContinuationComparisonPair: fewer subjects per comparison.
	NarrowingContinuationComparisonPair NarrowingContinuationAxis = "comparison_pair"
)

var narrowingContinuationAxes = [...]NarrowingContinuationAxis{
	NarrowingContinuationNone,
	NarrowingContinuationEvidenceWindow,
	NarrowingContinuationResultCount,
	NarrowingContinuationScopeAnchor,
	NarrowingContinuationGroupSelection,
	NarrowingContinuationComparisonPair,
}

// NarrowingContinuationAxisCount is the closed vocabulary's size.
const NarrowingContinuationAxisCount = len(narrowingContinuationAxes)

// NarrowingContinuationAxisVocabulary returns the closed vocabulary in its
// published order. The return type is an ARRAY, so the value is copied to the
// caller and the vocabulary cannot be mutated in place.
func NarrowingContinuationAxisVocabulary() [NarrowingContinuationAxisCount]NarrowingContinuationAxis {
	return narrowingContinuationAxes
}

// ValidNarrowingContinuationAxis reports membership of the closed vocabulary.
// The EMPTY value is NOT a member: a family that declared no axis is a
// registry defect, and `none` is how "no axis" is said deliberately.
func ValidNarrowingContinuationAxis(value NarrowingContinuationAxis) bool {
	for _, member := range narrowingContinuationAxes {
		if member == value {
			return true
		}
	}
	return false
}

// The two NEW axes §3 names for the scoped and grouped families.
//
// THESE ARE NOT WIRE VALUES IN THIS SLICE. ContextFabricStructureNeedKind
// is a closed wire enum; adding a member to it is a contract change and a
// two-step deploy (CHAOS-4623), and it would immediately widen every
// StructureNeeds payload ask-dev validates. So they are declared as
// package-local constants of the aliased type and are asserted by the
// registry test to be ABSENT from the wire vocabulary -- see
// TestNewStructureAxesAreNotYetWireVocabularyMembers. S4, which is where
// an axis is actually disclosed to a caller, is where they are promoted
// into contracts/v1 with the schema/OpenAPI/MCP/fixture/parity work and
// the ask-dev pin bump that promotion requires.
const (
	StructureNeedScopeAnchor StructureNeedKind = "scope_anchor"
	StructureNeedGroupKind   StructureNeedKind = "group_kind"
)

// QuestionFamilyDefinition is one row of the §3.1 family table.
type QuestionFamilyDefinition struct {
	Family QuestionFamily
	// Dimension is the health dimension this family's answers speak to.
	Dimension HealthDimension
	// SubjectAxis is how many subjects, and how obtained.
	SubjectAxis SubjectAxisKind
	// ApplicableAxes is the CLOSED set of structure axes that can ever be
	// relevant to this family. S4 gates the offer builders on it.
	//
	// NEVER a cap on disclosure: §6.4's rule is that an axis is disclosed
	// iff applicable AND it has material. This column decides
	// applicability only.
	ApplicableAxes []StructureNeedKind
	// AskOrder is prompt PRIORITY only -- which applicable axis to lead
	// with. §3.1 is explicit that it is "NEVER a cap on how many needs are
	// disclosed". Every entry must be in ApplicableAxes; the registry test
	// enforces that.
	AskOrder []StructureNeedKind
	// FactRoles declares which subject roles this family's facts answer
	// for. Declared, not consumed (see FactRole).
	FactRoles []FactRole
	// RequireDrivers: the plan demands drivers, not merely attempts them.
	//
	// FALSE FOR subject_investigation, and that is decision D1's accepted
	// cost, recorded here rather than hidden: drivers are always
	// ATTEMPTED, never required, so North Star check 8 ("never a bare
	// score") is enforced by a harness acceptance case -- a why-phrased
	// question must show a non-empty driver set OR a disclosed absence
	// reason -- rather than by this column.
	RequireDrivers bool
	// RequireRanking: the answer is a RANKED cohort, not merely a set.
	RequireRanking bool
	// RenderKinds names the render kinds this family's answers may
	// produce. Members may be declared-unproduced; DeclaredUnproducedRenderKinds
	// says which, and the registry test requires every named kind to be
	// either produced or explicitly listed there.
	RenderKinds []contractsv1.ContextFabricRenderKind
	// Budget is the plan-time budget profile. Declared, not consumed.
	Budget PlanBudgetProfile
	// NarrowerContinuationAxis is the structural dimension a caller could
	// reduce if this family's answer does not fit the response budget.
	//
	// CONSUMED, unlike most of this table: the planned budget refusal reads
	// it (chaos4636_budget_stage3.go) and it reaches the 413 body as a
	// closed token. Every family must declare one; `none` is the way to
	// say there is no axis, and the registry test rejects the empty value
	// so a new family cannot inherit "no continuation" by omission.
	NarrowerContinuationAxis NarrowingContinuationAxis
	// CompatibleShapes is §4.2's structural gate: which InvestigationShape
	// values are consistent with this family.
	//
	// READ CAREFULLY: this is NOT what decides the family. The precedence
	// table decides, and it reads structure signals ABOVE Shape precisely
	// because Shape is the unstable variable (three distinct values across
	// six replicates of two questions). This column exists for the
	// per-sample sanity check and for the registry's own consistency
	// assertion -- never as a router.
	CompatibleShapes []InvestigationShape
}

// questionFamilyDefinitions is the table. Order matches
// QuestionFamilyVocabulary; the registry test asserts the two agree
// exactly, so a new family cannot be added to one and forgotten in the
// other.
var questionFamilyDefinitions = []QuestionFamilyDefinition{
	{
		Family: QuestionFamilySubjectInvestigation,
		// "What is the status of X?" and "why is X struggling?" both land
		// here after D1. Execution completion is the dimension a subject
		// status question speaks to first.
		Dimension:   HealthDimensionExecutionCompletion,
		SubjectAxis: SubjectAxisOne,
		ApplicableAxes: []StructureNeedKind{
			contractsv1.ContextFabricStructureNeedExpectedKind,
			contractsv1.ContextFabricStructureNeedSubjectHandle,
			contractsv1.ContextFabricStructureNeedSubjectCandidate,
			contractsv1.ContextFabricStructureNeedSubjectAnchor,
			contractsv1.ContextFabricStructureNeedWindow,
		},
		// §3's own AskOrder for this family: kind -> handle -> candidate
		// -> window.
		AskOrder: []StructureNeedKind{
			contractsv1.ContextFabricStructureNeedExpectedKind,
			contractsv1.ContextFabricStructureNeedSubjectHandle,
			contractsv1.ContextFabricStructureNeedSubjectCandidate,
			contractsv1.ContextFabricStructureNeedWindow,
		},
		FactRoles:      []FactRole{FactRoleSubject},
		RequireDrivers: false,
		RequireRanking: false,
		RenderKinds: []contractsv1.ContextFabricRenderKind{
			contractsv1.ContextFabricRenderKindTable,
			contractsv1.ContextFabricRenderKindSeries,
		},
		Budget:           PlanBudgetSingleSubject,
		CompatibleShapes: []InvestigationShape{ShapeSingleSubject},
		// One subject already. The only thing left to reduce is how much
		// history its facts cover -- which is exactly the live 413 this
		// axis has to answer for: a single team, all_time, 12 workload
		// facts assembling past the item ceiling.
		NarrowerContinuationAxis: NarrowingContinuationEvidenceWindow,
	},
	{
		Family:      QuestionFamilyDiscoveredCohortRanking,
		Dimension:   HealthDimensionDeliveryFlow,
		SubjectAxis: SubjectAxisManyDiscovered,
		// WINDOW ONLY. This is exactly what CHAOS-4579 shipped as a filter
		// applied after the offer builders had already run; here it is a
		// property of the family, decidable before they run.
		ApplicableAxes: []StructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
		AskOrder:       []StructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
		FactRoles:      []FactRole{FactRoleMember},
		RequireDrivers: true,
		RequireRanking: true,
		RenderKinds: []contractsv1.ContextFabricRenderKind{
			contractsv1.ContextFabricRenderKindSeries,
		},
		Budget:           PlanBudgetFlatCohort,
		CompatibleShapes: []InvestigationShape{ShapeDiscoveredCohort, ShapeOpen},
		// The cohort IS ranked, so a shorter prefix of it is a true answer
		// to the same question rather than a different question.
		NarrowerContinuationAxis: NarrowingContinuationResultCount,
	},
	{
		Family:      QuestionFamilyScopedCohortStatus,
		Dimension:   HealthDimensionExecutionCompletion,
		SubjectAxis: SubjectAxisManyScoped,
		// scope_anchor (which "fullchaos"?) and window. NEVER a
		// single-subject pick: subject_handle/subject_candidate are
		// deliberately absent, because offering them is precisely the
		// CHAOS-4622 §2 defect -- Q-B being asked to pick ONE subject when
		// the named term is the scope, not the answer.
		ApplicableAxes: []StructureNeedKind{
			StructureNeedScopeAnchor,
			contractsv1.ContextFabricStructureNeedExpectedKind,
			contractsv1.ContextFabricStructureNeedWindow,
		},
		AskOrder: []StructureNeedKind{
			StructureNeedScopeAnchor,
			contractsv1.ContextFabricStructureNeedWindow,
		},
		FactRoles:      []FactRole{FactRoleMember},
		RequireDrivers: false,
		RequireRanking: false,
		RenderKinds: []contractsv1.ContextFabricRenderKind{
			contractsv1.ContextFabricRenderKindTable,
		},
		Budget: PlanBudgetFlatCohort,
		// NOT result_count: a scoped cohort carries no ranking, so "the top
		// few" would be an arbitrary cut presented as a selection. The
		// scope is the caller's own term and is the honest thing to narrow.
		NarrowerContinuationAxis: NarrowingContinuationScopeAnchor,
		// Q-B's own replicates produced single_subject AND explicit_cohort
		// for the SAME question -- both are listed, which is the honest
		// statement of what this family is compatible with, and is also
		// why Shape does not decide it.
		CompatibleShapes: []InvestigationShape{ShapeSingleSubject, ShapeExplicitCohort, ShapeDiscoveredCohort},
	},
	{
		Family:      QuestionFamilyGroupedCohortStatus,
		Dimension:   HealthDimensionExecutionCompletion,
		SubjectAxis: SubjectAxisManyGrouped,
		// Window only when the grouping kind is unambiguous from the
		// question's own terms; otherwise group_kind. Both are applicable;
		// §6.4's applicable-AND-has-material rule decides what is actually
		// asked.
		ApplicableAxes: []StructureNeedKind{
			StructureNeedGroupKind,
			contractsv1.ContextFabricStructureNeedWindow,
		},
		AskOrder: []StructureNeedKind{
			StructureNeedGroupKind,
			contractsv1.ContextFabricStructureNeedWindow,
		},
		FactRoles:      []FactRole{FactRoleGroup, FactRoleMember},
		RequireDrivers: true,
		RequireRanking: false,
		RenderKinds: []contractsv1.ContextFabricRenderKind{
			contractsv1.ContextFabricRenderKindTable,
		},
		Budget: PlanBudgetGroupedCohort,
		// Decision D2 forbids the engine dropping a group to fit, so once
		// per-group narrowing is exhausted the group axis is the caller's
		// to cut, not ours.
		NarrowerContinuationAxis: NarrowingContinuationGroupSelection,
		// Q-A's replicates produced discovered_cohort AND explicit_cohort
		// for the SAME question.
		CompatibleShapes: []InvestigationShape{ShapeDiscoveredCohort, ShapeExplicitCohort, ShapeOpen},
	},
	{
		Family:      QuestionFamilyExplicitComparison,
		Dimension:   HealthDimensionDeliveryFlow,
		SubjectAxis: SubjectAxisManyNamed,
		ApplicableAxes: []StructureNeedKind{
			contractsv1.ContextFabricStructureNeedExpectedKind,
			contractsv1.ContextFabricStructureNeedSubjectAnchor,
			contractsv1.ContextFabricStructureNeedSubjectHandle,
			contractsv1.ContextFabricStructureNeedSubjectCandidate,
			contractsv1.ContextFabricStructureNeedWindow,
		},
		AskOrder: []StructureNeedKind{
			contractsv1.ContextFabricStructureNeedSubjectHandle,
			contractsv1.ContextFabricStructureNeedSubjectCandidate,
			contractsv1.ContextFabricStructureNeedWindow,
		},
		FactRoles:      []FactRole{FactRoleSubject},
		RequireDrivers: false,
		RequireRanking: false,
		RenderKinds: []contractsv1.ContextFabricRenderKind{
			contractsv1.ContextFabricRenderKindTable,
			contractsv1.ContextFabricRenderKindQuadrant,
		},
		Budget: PlanBudgetMatchedPair,
		// Both sides must carry the same measures over the same window, so
		// the pair count is the only dimension that can move.
		NarrowerContinuationAxis: NarrowingContinuationComparisonPair,
		CompatibleShapes:         []InvestigationShape{ShapeExplicitCohort, ShapeSingleSubject},
	},
	{
		Family:      QuestionFamilyTrend,
		Dimension:   HealthDimensionDeliveryFlow,
		SubjectAxis: SubjectAxisOne,
		ApplicableAxes: []StructureNeedKind{
			contractsv1.ContextFabricStructureNeedExpectedKind,
			contractsv1.ContextFabricStructureNeedSubjectHandle,
			contractsv1.ContextFabricStructureNeedSubjectCandidate,
			contractsv1.ContextFabricStructureNeedWindow,
		},
		AskOrder:       []StructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
		FactRoles:      []FactRole{FactRoleSubject},
		RequireDrivers: false,
		RequireRanking: false,
		RenderKinds: []contractsv1.ContextFabricRenderKind{
			contractsv1.ContextFabricRenderKindSeries,
		},
		Budget:           PlanBudgetSingleSubject,
		CompatibleShapes: []InvestigationShape{ShapeSingleSubject, ShapeDiscoveredCohort},
		// A trend is defined by its window; shortening it is the reduction
		// that keeps the question intact.
		NarrowerContinuationAxis: NarrowingContinuationEvidenceWindow,
	},
	{
		Family:      QuestionFamilyInvestmentAllocation,
		Dimension:   HealthDimensionInvestmentBalance,
		SubjectAxis: SubjectAxisOne,
		ApplicableAxes: []StructureNeedKind{
			contractsv1.ContextFabricStructureNeedSubjectAnchor,
			contractsv1.ContextFabricStructureNeedWindow,
		},
		AskOrder:       []StructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
		FactRoles:      []FactRole{FactRoleSubject},
		RequireDrivers: false,
		RequireRanking: false,
		// treemap and sunburst have NO producer today -- both are listed
		// in DeclaredUnproducedRenderKinds, which is what makes this
		// declaration honest rather than a promise.
		RenderKinds: []contractsv1.ContextFabricRenderKind{
			contractsv1.ContextFabricRenderKindTreemap,
			contractsv1.ContextFabricRenderKindSunburst,
		},
		Budget:           PlanBudgetSingleSubject,
		CompatibleShapes: []InvestigationShape{ShapeSingleSubject, ShapeOpen},
		// "Where did the effort go" is a window question; the allocation
		// denominator is the window itself.
		NarrowerContinuationAxis: NarrowingContinuationEvidenceWindow,
	},
	{
		Family:    QuestionFamilyUnclassified,
		Dimension: HealthDimensionDataTrust,
		// No subject axis has been established -- which is different from
		// "there is none". Nothing narrows, so ALL axes stay applicable
		// and today's behaviour is unchanged, which is exactly what
		// unclassified is for.
		SubjectAxis:    SubjectAxisNone,
		ApplicableAxes: allStructureNeedKinds(),
		AskOrder:       nil,
		FactRoles:      nil,
		RequireDrivers: false,
		RequireRanking: false,
		RenderKinds:    nil,
		Budget:         PlanBudgetUnbounded,
		// Nothing about the question's shape was established, so no axis
		// can be named. The refusal carries NO continuation rather than a
		// guess -- "missing is not healthy" applies to advice too.
		NarrowerContinuationAxis: NarrowingContinuationNone,
		CompatibleShapes:         allInvestigationShapes(),
	},
}

// DeclaredUnproducedRenderKinds are render kinds a family may name that
// have NO producer in the codebase today.
//
// §3.1's registry assertion is "every RenderKind named has a producer or
// is explicitly listed as declared-unproduced". This list is the second
// half of that. It is not a TODO: CHAOS-4415 slice 1 deliberately declared
// seven render kinds without producers, and naming them here keeps the
// family table honest -- a reader can tell "this family will render a
// treemap" from "this family would render a treemap if one existed".
func DeclaredUnproducedRenderKinds() []contractsv1.ContextFabricRenderKind {
	return []contractsv1.ContextFabricRenderKind{
		contractsv1.ContextFabricRenderKindTable,
		contractsv1.ContextFabricRenderKindQuadrant,
		contractsv1.ContextFabricRenderKindTreemap,
		contractsv1.ContextFabricRenderKindSunburst,
		contractsv1.ContextFabricRenderKindSankey,
		contractsv1.ContextFabricRenderKindBurndown,
		contractsv1.ContextFabricRenderKindForecast,
	}
}

// QuestionFamilyDefinitions returns a copy of the family table.
func QuestionFamilyDefinitions() []QuestionFamilyDefinition {
	out := make([]QuestionFamilyDefinition, len(questionFamilyDefinitions))
	copy(out, questionFamilyDefinitions)
	return out
}

// LookupQuestionFamily returns the definition for family.
func LookupQuestionFamily(family QuestionFamily) (QuestionFamilyDefinition, bool) {
	for _, definition := range questionFamilyDefinitions {
		if definition.Family == family {
			return definition, true
		}
	}
	return QuestionFamilyDefinition{}, false
}

// QuestionFamilyTableVersion is the definition-table version string. It is
// the value telemetry reports as family_version and the value ReuseKey
// carries as QuestionFamilyVersion.
//
// BUMP THIS whenever any row above changes in a way that could change an
// answer -- ApplicableAxes, AskOrder, RequireDrivers, RequireRanking,
// RenderKinds, Budget, or the precedence table in
// chaos4632_question_family_precedence.go. In THIS slice nothing is gated,
// so no answer can change; the version exists now so that S4 and S5 inherit
// a reuse fence that already works, rather than adding one after the
// family starts deciding things. Same reasoning
// ReuseKey.RankingFormulaVersion's own migration (0035) records for the
// ranking formula.
const QuestionFamilyTableVersion = "question-family.v1"

func allStructureNeedKinds() []StructureNeedKind {
	wire := contractsv1.ContextFabricStructureNeedKindVocabulary()
	out := make([]StructureNeedKind, 0, len(wire)+2)
	out = append(out, wire[:]...)
	return append(out, StructureNeedScopeAnchor, StructureNeedGroupKind)
}

func allInvestigationShapes() []InvestigationShape {
	return []InvestigationShape{ShapeSingleSubject, ShapeExplicitCohort, ShapeDiscoveredCohort, ShapeOpen}
}
