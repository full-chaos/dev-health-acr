package v1

import "fmt"

// The AnswerPlan (CHAOS-4636 / intent-engine design §6).
//
// S2 (CHAOS-4632) deliberately kept the question family SHADOW: resolved,
// persisted to a receipt, telemetered, with zero wire surface, because the
// design's largest assumption -- that a model emits a semantically correct
// GroupKind and scope anchor, and does NOT emit them on questions that have
// neither -- was unmeasured. That measurement has since been made on real
// data (0 false emissions across 18/20 labelled negative cases; 11/12
// resolved-family accuracy; 12/12 stability at N=1). S2's own vocabulary file
// names this promotion as the follow-on it was waiting for: "the promotion to
// the public interpretation is a separate change, justified by the
// measurement this slice makes possible, exactly as W1 promoted W0's window
// fields." This file is that promotion.
//
// The closed family and family-source vocabularies live HERE, and
// internal/contextfabric aliases them, rather than being restated in two
// places. A closed vocabulary duplicated across a package boundary is a
// vocabulary that will drift; the alias makes drift impossible to express.

// ContextFabricQuestionFamily is the closed vocabulary of question families.
// It does NOT replace ContextFabricInvestigationShape: Shape stays the coarse
// structural signal and is one of the inputs a family is validated against,
// so nothing that reads Shape changes meaning.
//
// EIGHT members. Decision D1 merged subject_status and subject_drivers into
// one subject_investigation: the two had IDENTICAL conditions in the
// precedence table, so nothing could discriminate them, and this repository's
// own acceptance case wants both at once.
type ContextFabricQuestionFamily string

const (
	ContextFabricQuestionFamilySubjectInvestigation    ContextFabricQuestionFamily = "subject_investigation"
	ContextFabricQuestionFamilyDiscoveredCohortRanking ContextFabricQuestionFamily = "discovered_cohort_ranking"
	ContextFabricQuestionFamilyScopedCohortStatus      ContextFabricQuestionFamily = "scoped_cohort_status"
	ContextFabricQuestionFamilyGroupedCohortStatus     ContextFabricQuestionFamily = "grouped_cohort_status"
	ContextFabricQuestionFamilyExplicitComparison      ContextFabricQuestionFamily = "explicit_comparison"
	ContextFabricQuestionFamilyTrend                   ContextFabricQuestionFamily = "trend"
	ContextFabricQuestionFamilyInvestmentAllocation    ContextFabricQuestionFamily = "investment_allocation"
	ContextFabricQuestionFamilyUnclassified            ContextFabricQuestionFamily = "unclassified"
)

// contextFabricQuestionFamilies is the closed vocabulary in published order.
// The order is part of this vocabulary's contract: append, never reorder.
var contextFabricQuestionFamilies = [...]ContextFabricQuestionFamily{
	ContextFabricQuestionFamilySubjectInvestigation,
	ContextFabricQuestionFamilyDiscoveredCohortRanking,
	ContextFabricQuestionFamilyScopedCohortStatus,
	ContextFabricQuestionFamilyGroupedCohortStatus,
	ContextFabricQuestionFamilyExplicitComparison,
	ContextFabricQuestionFamilyTrend,
	ContextFabricQuestionFamilyInvestmentAllocation,
	ContextFabricQuestionFamilyUnclassified,
}

// ContextFabricQuestionFamilyCount is the closed vocabulary's size.
const ContextFabricQuestionFamilyCount = len(contextFabricQuestionFamilies)

// ContextFabricQuestionFamilyVocabulary returns the closed vocabulary in its
// fixed, published order. An array return, copied on every call.
func ContextFabricQuestionFamilyVocabulary() [ContextFabricQuestionFamilyCount]ContextFabricQuestionFamily {
	return contextFabricQuestionFamilies
}

// ValidContextFabricQuestionFamily reports membership. The EMPTY value is
// deliberately NOT valid: callers that treat "unset" as legal handle it
// explicitly.
func ValidContextFabricQuestionFamily(value ContextFabricQuestionFamily) bool {
	for _, member := range contextFabricQuestionFamilies {
		if member == value {
			return true
		}
	}
	return false
}

// ContextFabricQuestionFamilySource names HOW a family was reached. It is on
// the wire beside the family itself because "the model said so at N=1" and
// "carried from the previous turn" are different warrants for the same value,
// and a consumer reading a plan needs to know which it has.
type ContextFabricQuestionFamilySource string

const (
	ContextFabricQuestionFamilySourceModelConsensus      ContextFabricQuestionFamilySource = "model_consensus"
	ContextFabricQuestionFamilySourceModel               ContextFabricQuestionFamilySource = "model"
	ContextFabricQuestionFamilySourcePluralityRejected   ContextFabricQuestionFamilySource = "model_plurality_rejected"
	ContextFabricQuestionFamilySourceCarried             ContextFabricQuestionFamilySource = "carried"
	ContextFabricQuestionFamilySourceStructurePrecedence ContextFabricQuestionFamilySource = "structure_precedence"
	ContextFabricQuestionFamilySourceFallback            ContextFabricQuestionFamilySource = "fallback"
	ContextFabricQuestionFamilySourceNone                ContextFabricQuestionFamilySource = "none"
)

var contextFabricQuestionFamilySources = [...]ContextFabricQuestionFamilySource{
	ContextFabricQuestionFamilySourceModelConsensus,
	ContextFabricQuestionFamilySourceModel,
	ContextFabricQuestionFamilySourcePluralityRejected,
	ContextFabricQuestionFamilySourceCarried,
	ContextFabricQuestionFamilySourceStructurePrecedence,
	ContextFabricQuestionFamilySourceFallback,
	ContextFabricQuestionFamilySourceNone,
}

// ContextFabricQuestionFamilySourceCount is the closed vocabulary's size.
const ContextFabricQuestionFamilySourceCount = len(contextFabricQuestionFamilySources)

// ContextFabricQuestionFamilySourceVocabulary returns the closed vocabulary in
// published order.
func ContextFabricQuestionFamilySourceVocabulary() [ContextFabricQuestionFamilySourceCount]ContextFabricQuestionFamilySource {
	return contextFabricQuestionFamilySources
}

// ValidContextFabricQuestionFamilySource reports membership.
func ValidContextFabricQuestionFamilySource(value ContextFabricQuestionFamilySource) bool {
	for _, member := range contextFabricQuestionFamilySources {
		if member == value {
			return true
		}
	}
	return false
}

// ContextFabricPlanNarrowingStage is the CLOSED vocabulary of the three
// moments a plan may narrow. There are three because the budgets become
// knowable at three different times, and the design's own record (§6.3a)
// shows that every earlier specification failed by enforcing a budget where
// the quantity being budgeted did not yet exist.
type ContextFabricPlanNarrowingStage string

const (
	// ContextFabricPlanNarrowingCardinality is stage 1: PRE-READ. Only
	// cardinality is knowable here. Attention rank does not exist yet --
	// RankCohort runs after the fact read -- so this stage cannot take a
	// "top N" without inventing an undeclared ordering heuristic.
	ContextFabricPlanNarrowingCardinality ContextFabricPlanNarrowingStage = "cardinality"
	// ContextFabricPlanNarrowingSynthesisInput is stage 2: POST-READ,
	// PRE-SYNTHESIS. Synthesis is what CREATES Drivers, RemainingWork,
	// ReadinessGaps, Conflicts and ClaimedFacts -- the very terms the item
	// budget charges -- so before it runs they do not exist to be counted.
	// This stage therefore bounds what synthesis is GIVEN, leaving declared
	// headroom for what it will add.
	ContextFabricPlanNarrowingSynthesisInput ContextFabricPlanNarrowingStage = "synthesis_input"
	// ContextFabricPlanNarrowingAssembledResult is stage 3: the assembled
	// result is measured, and if it does not fit it is RE-SYNTHESIZED once
	// with a smaller input. It is never trimmed: by the time an answer is
	// composed, dropping a driver can orphan a render-shape point that
	// cites it and make the stored and served answers diverge.
	ContextFabricPlanNarrowingAssembledResult ContextFabricPlanNarrowingStage = "assembled_result"
)

var contextFabricPlanNarrowingStages = [...]ContextFabricPlanNarrowingStage{
	ContextFabricPlanNarrowingCardinality,
	ContextFabricPlanNarrowingSynthesisInput,
	ContextFabricPlanNarrowingAssembledResult,
}

// ContextFabricPlanNarrowingStageCount is the closed vocabulary's size.
const ContextFabricPlanNarrowingStageCount = len(contextFabricPlanNarrowingStages)

// ContextFabricPlanNarrowingStageVocabulary returns the closed vocabulary in
// published order.
func ContextFabricPlanNarrowingStageVocabulary() [ContextFabricPlanNarrowingStageCount]ContextFabricPlanNarrowingStage {
	return contextFabricPlanNarrowingStages
}

// ValidContextFabricPlanNarrowingStage reports membership.
func ValidContextFabricPlanNarrowingStage(value ContextFabricPlanNarrowingStage) bool {
	for _, member := range contextFabricPlanNarrowingStages {
		if member == value {
			return true
		}
	}
	return false
}

// ContextFabricNarrowingBasis is the CLOSED vocabulary of orders a narrowing
// step may take members in. Every member of it is DISCLOSED to the caller,
// including -- especially -- the one that is arbitrary.
type ContextFabricNarrowingBasis string

const (
	// ContextFabricNarrowingBasisCanonicalIDLexical sorts by canonical id.
	//
	// This is an ARBITRARY order and the contract says so rather than
	// implying otherwise. It is not relevance. What a pre-read cardinality
	// guard actually needs is STABILITY, and this has it: the alternative
	// an earlier revision proposed -- "cohort discovery order" -- is not an
	// order at all, because hopWalk materialized its node slice by ranging
	// a Go map, and Go randomizes map iteration. That defect is fixed
	// (CHAOS-4630), but the lesson stands: a stated arbitrary order beats
	// an implied relevant one.
	ContextFabricNarrowingBasisCanonicalIDLexical ContextFabricNarrowingBasis = "canonical_id_lexical"
	// ContextFabricNarrowingBasisLargestGroupRoundRobin takes one member
	// from the largest group, then the next largest, and repeats -- so
	// EVERY group survives with at least one member for as long as the
	// budget allows any at all.
	//
	// This is decision D2, member-first, ruled 2026-08-30: "for each team"
	// is the question's own words, so dropping a team answers a question
	// that was not asked, while trimming members answers the asked question
	// less completely -- and "less completely" is a disclosure this
	// contract already knows how to make, via per-group Truncated.
	ContextFabricNarrowingBasisLargestGroupRoundRobin ContextFabricNarrowingBasis = "largest_group_round_robin"
	// ContextFabricNarrowingBasisAttentionRank is score order. It is
	// available ONLY at stage 3, because RankCohort runs after the fact
	// read; a plan claiming it earlier would be claiming an order that does
	// not exist yet.
	ContextFabricNarrowingBasisAttentionRank ContextFabricNarrowingBasis = "attention_rank"
	// ContextFabricNarrowingBasisOverlapAwareSetCover supersedes
	// largest_group_round_robin for a grouped narrowing (CHAOS-4678):
	// group membership is many-to-many, and a member shared by several
	// groups can cover all of them at once. Round-robin does not exploit
	// that -- with groups A={a,b}, B={b,c} and a one-member budget it keeps
	// TWO members (a and b) where the shared member b alone covers both.
	// This basis is an EXACT minimum (or budget-maximal) set cover over the
	// groups, ties broken by canonical_id_lexical, used whenever the group
	// count is at most ContextFabricSetCoverGroupGuard; beyond that guard
	// the selection falls back to largest_group_round_robin untouched and
	// reports that basis instead, never claiming an order it did not run.
	ContextFabricNarrowingBasisOverlapAwareSetCover ContextFabricNarrowingBasis = "overlap_aware_set_cover"
)

var contextFabricNarrowingBases = [...]ContextFabricNarrowingBasis{
	ContextFabricNarrowingBasisCanonicalIDLexical,
	ContextFabricNarrowingBasisLargestGroupRoundRobin,
	ContextFabricNarrowingBasisAttentionRank,
	ContextFabricNarrowingBasisOverlapAwareSetCover,
}

// ContextFabricNarrowingBasisCount is the closed vocabulary's size.
const ContextFabricNarrowingBasisCount = len(contextFabricNarrowingBases)

// ContextFabricNarrowingBasisVocabulary returns the closed vocabulary in
// published order.
func ContextFabricNarrowingBasisVocabulary() [ContextFabricNarrowingBasisCount]ContextFabricNarrowingBasis {
	return contextFabricNarrowingBases
}

// ValidContextFabricNarrowingBasis reports membership.
func ValidContextFabricNarrowingBasis(value ContextFabricNarrowingBasis) bool {
	for _, member := range contextFabricNarrowingBases {
		if member == value {
			return true
		}
	}
	return false
}

// ContextFabricAnswerPlanRenderKindsMaxCount bounds the authorized render
// kinds. A plan cannot authorize more kinds than the closed render vocabulary
// has members, so the bound is that vocabulary's size rather than a number
// chosen for it. TestAnswerPlanCollectionBoundsDeriveFromTheirVocabularies
// pins the derivation.
const ContextFabricAnswerPlanRenderKindsMaxCount = 8

// ContextFabricAnswerPlanAxesMaxCount bounds the applicable structure axes,
// derived from the structure-need vocabulary for the same reason.
const ContextFabricAnswerPlanAxesMaxCount = ContextFabricStructureNeedKindCount

// ContextFabricPlanNarrowingMaxCount bounds how many narrowing steps one plan
// may record. Three stages, each able to act on members and on groups, plus
// the one re-synthesis retry, cannot honestly exceed this.
const ContextFabricPlanNarrowingMaxCount = 8

// ContextFabricPlanNarrowing is ONE recorded narrowing step: which stage
// narrowed, on what declared basis, and from what to what.
//
// It is on the wire because it is the disclosure. "Showing 2 of 3 teams, 3 of
// N projects each" is a true, useful, partial answer (North Star check 5) and
// the difference between a partial answer and a wrong one is whether the
// caller is told (check 12). A narrowing that happened silently would be the
// group-blind truncation this slice exists to remove, moved one layer down.
type ContextFabricPlanNarrowing struct {
	Stage ContextFabricPlanNarrowingStage `json:"stage"`
	Basis ContextFabricNarrowingBasis     `json:"basis"`
	// Before and After are counts of the thing that was narrowed -- cohort
	// members, or groups.
	Before int `json:"before"`
	After  int `json:"after"`
	// Groups reports whether this step narrowed GROUPS rather than members.
	// Under decision D2 (member-first) a group-narrowing step should be
	// rare, and recording which it was is what makes that checkable rather
	// than asserted.
	Groups bool `json:"groups,omitempty"`
	// Overrun names which budget axis forced this step, when one did.
	// Absent for a stage-1 clamp, which is precautionary rather than
	// reactive: at stage 1 nothing has been measured yet.
	Overrun ContextFabricBudgetOverrun `json:"overrun,omitempty"`
}

// Validate enforces the closed vocabularies and the count arithmetic.
func (n ContextFabricPlanNarrowing) Validate() error {
	if !ValidContextFabricPlanNarrowingStage(n.Stage) {
		return fmt.Errorf("plan narrowing stage %q is not a member of the closed vocabulary", n.Stage)
	}
	if !ValidContextFabricNarrowingBasis(n.Basis) {
		return fmt.Errorf("plan narrowing basis %q is not a member of the closed vocabulary", n.Basis)
	}
	if n.Before < 0 || n.After < 0 || n.After > n.Before {
		return fmt.Errorf("plan narrowing must reduce a non-negative count, got before=%d after=%d", n.Before, n.After)
	}
	if n.Overrun != "" && !ValidContextFabricBudgetOverrun(n.Overrun) {
		return fmt.Errorf("plan narrowing overrun %q is not a member of the closed vocabulary", n.Overrun)
	}
	// Attention rank does not exist before the fact read, so a stage that
	// runs earlier cannot honestly claim to have used it. This is the one
	// mistake §6.3a records being made in EVERY earlier revision, stated
	// here as something the contract refuses to represent.
	if n.Basis == ContextFabricNarrowingBasisAttentionRank && n.Stage == ContextFabricPlanNarrowingCardinality {
		return fmt.Errorf("plan narrowing at the pre-read cardinality stage cannot use attention rank, which RankCohort computes only after the fact read")
	}
	return nil
}

// ContextFabricAnswerPlanBudget is the ceiling the plan was built against and
// the headroom it reserved. It is persisted so an over-budget answer is
// diagnosable from its own artifact: after this slice, a 413 is a PLANNER
// defect, and the plan on the result is what says which number was wrong.
type ContextFabricAnswerPlanBudget struct {
	// MaxItems and MaxSerializedBytes are the EFFECTIVE ceilings for this
	// request -- the service configuration already narrowed by anything the
	// caller asked for.
	MaxItems           int   `json:"max_items"`
	MaxSerializedBytes int64 `json:"max_serialized_bytes"`
	// MaxMembers is what stage 1 clamped the cohort to before the fact
	// read.
	MaxMembers int `json:"max_members"`
	// SynthesisHeadroom is the item allowance stage 2 held back for the
	// drivers, findings and claimed facts synthesis will ADD -- the terms
	// that do not exist until it has run. It is a measured constant per
	// family, not a guess; this slice's rig gate is what measures it.
	SynthesisHeadroom int `json:"synthesis_headroom"`
	// NarrowingBasis is the order stage 1 declared it would take members
	// in, disclosed whether or not it needed to act.
	NarrowingBasis ContextFabricNarrowingBasis `json:"narrowing_basis"`
}

// Validate enforces the budget's own arithmetic.
func (b ContextFabricAnswerPlanBudget) Validate() error {
	if b.MaxItems < 0 || b.MaxSerializedBytes < 0 || b.MaxMembers < 0 || b.SynthesisHeadroom < 0 {
		return fmt.Errorf("answer plan budget fields must be non-negative")
	}
	if b.NarrowingBasis != "" && !ValidContextFabricNarrowingBasis(b.NarrowingBasis) {
		return fmt.Errorf("answer plan narrowing basis %q is not a member of the closed vocabulary", b.NarrowingBasis)
	}
	if b.MaxItems > 0 && b.SynthesisHeadroom > b.MaxItems {
		return fmt.Errorf("answer plan reserved %d items of synthesis headroom against a %d-item budget", b.SynthesisHeadroom, b.MaxItems)
	}
	return nil
}

// ContextFabricAnswerPlan is what the question was PLANNED to be answered
// with, produced by a deterministic stage -- not a model call -- between
// interpretation and discovery, and persisted on the result.
//
// It exists so that three things stop being implicit. Which render kinds the
// question AUTHORIZES, so a chart is never drawn merely because the geometry
// allowed it (North Star check 10). Which fact roles the question needs, so a
// family can be planned or refusably absent BEFORE the read rather than
// discovered empty after it. And what budget the answer was built against, so
// an answer that did not fit names the number that was wrong.
type ContextFabricAnswerPlan struct {
	Family        ContextFabricQuestionFamily       `json:"family"`
	FamilySource  ContextFabricQuestionFamilySource `json:"family_source"`
	FamilyVersion string                            `json:"family_version"`
	// GroupKind is set only for a grouped family: the kind the answer is
	// partitioned BY (a team), never the kind of the members.
	GroupKind ContextFabricSubjectKind `json:"group_kind,omitempty"`
	// MemberKind is the kind of the cohort members themselves.
	MemberKind ContextFabricSubjectKind `json:"member_kind,omitempty"`
	// RequireDrivers and RequireRanking are the family's declared answer
	// requirements. RequireRanking is FALSE for a grouped status list:
	// a cross-group ranking there is not merely wrong, it was not
	// requested, and the plan is where that is said.
	RequireDrivers bool `json:"require_drivers"`
	RequireRanking bool `json:"require_ranking"`
	// RenderKinds is the closed set of render kinds this question
	// authorizes. A selected shape outside it is refused, not drawn.
	RenderKinds []ContextFabricRenderKind `json:"render_kinds,omitempty"`
	// FactKinds is the union of every fact kind the plan asked for, across
	// roles. It is the plan item a fact read must trace back to.
	FactKinds []ContextFabricFactKind `json:"fact_kinds,omitempty"`
	// Axes is the set of structure axes applicable to this family. An axis
	// outside it is unreachable by construction rather than filtered after
	// the fact.
	Axes   []ContextFabricStructureNeedKind `json:"axes,omitempty"`
	Budget ContextFabricAnswerPlanBudget    `json:"budget"`
	// Narrowing records every step actually taken, in order. Empty means
	// the answer fit as planned.
	Narrowing []ContextFabricPlanNarrowing `json:"narrowing,omitempty"`
}

// Narrowed reports whether the plan had to act. It is the one-line question a
// consumer asks before deciding whether to render a completeness caveat.
func (p ContextFabricAnswerPlan) Narrowed() bool { return len(p.Narrowing) > 0 }

// Validate enforces every closed vocabulary the plan carries plus its own
// internal consistency.
func (p ContextFabricAnswerPlan) Validate() error {
	if !ValidContextFabricQuestionFamily(p.Family) {
		return fmt.Errorf("answer plan family %q is not a member of the closed vocabulary", p.Family)
	}
	if !ValidContextFabricQuestionFamilySource(p.FamilySource) {
		return fmt.Errorf("answer plan family source %q is not a member of the closed vocabulary", p.FamilySource)
	}
	if p.FamilyVersion == "" || len(p.FamilyVersion) > 64 {
		return fmt.Errorf("answer plan family version must be a non-empty string of at most 64 bytes")
	}
	if p.GroupKind != "" && !validContextFabricSubjectKind(p.GroupKind) {
		return fmt.Errorf("answer plan group kind %q is not a member of the closed vocabulary", p.GroupKind)
	}
	if p.MemberKind != "" && !validContextFabricSubjectKind(p.MemberKind) {
		return fmt.Errorf("answer plan member kind %q is not a member of the closed vocabulary", p.MemberKind)
	}
	// A grouped plan whose group kind equalled its member kind would be
	// claiming to partition a set by itself, which no grouping can mean.
	if p.GroupKind != "" && p.GroupKind == p.MemberKind {
		return fmt.Errorf("answer plan groups %q members by their own kind", p.GroupKind)
	}
	if len(p.RenderKinds) > ContextFabricAnswerPlanRenderKindsMaxCount {
		return fmt.Errorf("answer plan declares more render kinds than the closed vocabulary has")
	}
	for _, kind := range p.RenderKinds {
		if !validContextFabricRenderKind(kind) {
			return fmt.Errorf("answer plan render kind %q is not a member of the closed vocabulary", kind)
		}
	}
	if len(p.FactKinds) > ContextFabricFactKindCount {
		return fmt.Errorf("answer plan declares more fact kinds than the closed vocabulary has")
	}
	for _, kind := range p.FactKinds {
		if !validFactKind(kind) {
			return fmt.Errorf("answer plan fact kind %q is not a member of the closed vocabulary", kind)
		}
	}
	if len(p.Axes) > ContextFabricAnswerPlanAxesMaxCount {
		return fmt.Errorf("answer plan declares more axes than the closed structure-need vocabulary has")
	}
	for _, axis := range p.Axes {
		if !ValidContextFabricStructureNeedKind(axis) {
			return fmt.Errorf("answer plan axis %q is not a member of the closed vocabulary", axis)
		}
	}
	if err := p.Budget.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	if len(p.Narrowing) > ContextFabricPlanNarrowingMaxCount {
		return fmt.Errorf("answer plan records %d narrowing steps, more than the %d a plan can honestly take", len(p.Narrowing), ContextFabricPlanNarrowingMaxCount)
	}
	for _, step := range p.Narrowing {
		if err := step.Validate(); err != nil {
			return fmt.Errorf("narrowing: %w", err)
		}
	}
	return nil
}
