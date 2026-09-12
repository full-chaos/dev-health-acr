package contextfabric

// The SUBJECT ROLE layer: which (role, subject kind) coordinates a frame's
// obligations attach to. Design §13.4.2a (the excised SubjectRole table),
// §13.15.2 (its evidence obligation), §13.11a O9.
//
// WHY THIS IS PRODUCTION CODE AND NOT A TEST PRIMITIVE. The declaration
// slice shipped a hand-written version of this mapping inside a test file
// and it was reviewed three times; each round found a hole in the code
// written to close the previous round's finding, and the scaffolding was
// removed rather than approximated a fourth time. The reason it kept
// failing is structural, not careless: a coordinate table that lives
// beside the test asserting it has no authority outside that file, so an
// edit to either side is invisible to the other. The mapping belongs where
// the frame lives, which is here, and the oracle then checks production
// rather than checking a second copy of the thing under test.
//
// WHAT IT READS. The frame's SubjectExpression and its DERIVED obligation
// set, and nothing else. No resolution state, no fact-read state, no
// registry (§13.2.3, law L4): the frame is immutable once validated, and a
// coordinate set that could change after the read would make every
// completeness claim derived from it unreproducible. The registry enters
// one layer up, in requirement_derivation.go, where a coordinate is
// crossed with what a producer can actually serve.
//
// SHADOW ONLY, on the same footing as the rest of the frame layer: no wire
// surface, no schema, no migration.

import (
	"fmt"
	"sort"
	"strings"
)

// SubjectRole names WHICH subject of a frame a requirement attaches to.
//
// Closed vocabulary, telemetry-safe: no prose, no identifiers.
//
// THERE IS NO `anchor` MEMBER, and its absence is a design decision rather
// than an omission. ScopedSetExpression.AnchorTerms are RETRIEVAL
// POINTERS, NEVER VALUES (frame.go's own words on both term fields): the
// anchor's subject kind is settled at resolution time, and the frame -- which
// is immutable before resolution -- cannot name it. A role whose subject kind
// is unknowable would have to be filled with a guess, and a guess in a
// coordinate is worse than an absent coordinate because it reads as
// coverage. The absence is reported, not silent: RenderRequirementCoordinates
// states it in the artifact header.
type SubjectRole string

const (
	// SubjectRoleSubject: the frame names one subject directly, or the
	// organization is itself the subject.
	SubjectRoleSubject SubjectRole = "subject"
	// SubjectRoleMember: a member of a discovered, scoped or grouped set.
	SubjectRoleMember SubjectRole = "member"
	// SubjectRoleGroup: the axis a grouped set is grouped BY.
	SubjectRoleGroup SubjectRole = "group"
	// SubjectRoleOperand: one operand of an explicit comparison.
	SubjectRoleOperand SubjectRole = "operand"
)

var subjectRoles = [...]SubjectRole{
	SubjectRoleSubject,
	SubjectRoleMember,
	SubjectRoleGroup,
	SubjectRoleOperand,
}

// SubjectRoleCount is four.
const SubjectRoleCount = len(subjectRoles)

// SubjectRoleVocabulary returns the closed vocabulary in design order.
func SubjectRoleVocabulary() [SubjectRoleCount]SubjectRole {
	return subjectRoles
}

// ValidSubjectRole reports membership. The empty value is not a member.
func ValidSubjectRole(value SubjectRole) bool {
	for _, member := range subjectRoles {
		if member == value {
			return true
		}
	}
	return false
}

// RequirementCoordinate is one cell of the requirement layer: an
// obligation, the role it attaches to, and that role's subject kind.
//
// It is COMPARABLE on purpose -- it is used as a map key for deduplication
// and for the telemetry histograms -- so it holds no slices.
type RequirementCoordinate struct {
	Obligation AnswerObligation
	Role       SubjectRole
	Subject    SubjectKind
}

// roleSlot is one (role, subject kind) pair the frame's topology offers.
// Derived from the SubjectExpression variant alone, before any obligation
// is considered.
type roleSlot struct {
	Role    SubjectRole
	Subject SubjectKind
}

// frameRoleSlots reads the union's variant and returns the roles that
// variant has, with their subject kinds.
//
// EVERY BRANCH READS THE POINTER THE DISCRIMINATOR NAMES, and reads it
// defensively (a nil pointer yields no slot rather than panicking), because
// this function is exported through DeriveRequirementCoordinates and a
// caller can hand it a frame that never passed invariant I1.
func frameRoleSlots(expression SubjectExpression) []roleSlot {
	var slots []roleSlot
	switch expression.Kind {
	case SubjectExpressionNamed:
		// ExpectedKind is OPTIONAL: absent means the question did not
		// constrain the kind, "which is a weaker claim than guessing one"
		// (frame.go). No slot, therefore no coordinates -- the honest
		// encoding of a subject whose kind is not yet known.
		if expression.Named != nil && expression.Named.ExpectedKind != nil && *expression.Named.ExpectedKind != "" {
			slots = append(slots, roleSlot{Role: SubjectRoleSubject, Subject: *expression.Named.ExpectedKind})
		}
	case SubjectExpressionExplicitSet:
		if expression.Explicit == nil {
			break
		}
		for _, operand := range expression.Explicit.Operands {
			// BOTH OPERAND VARIANTS. SubjectOperand is itself a
			// discriminated union of named and scoped (frame.go, invariant
			// I19), and the declaration slice's version handled the named
			// one only: a scoped operand is valid in an explicit set,
			// carries its own MemberKind, and dropping it removed a real
			// operand's cells while every test stayed green -- because the
			// artifact was rendered by the same function that dropped it.
			// Both are read here, and the regenerated artifact is what
			// makes a future drop visible.
			if operand.Named != nil && operand.Named.ExpectedKind != nil && *operand.Named.ExpectedKind != "" {
				slots = append(slots, roleSlot{Role: SubjectRoleOperand, Subject: *operand.Named.ExpectedKind})
			}
			if operand.Scoped != nil && operand.Scoped.MemberKind != "" {
				slots = append(slots, roleSlot{Role: SubjectRoleOperand, Subject: operand.Scoped.MemberKind})
			}
		}
	case SubjectExpressionDiscoveredKind:
		if expression.Discovered != nil && expression.Discovered.MemberKind != "" {
			slots = append(slots, roleSlot{Role: SubjectRoleMember, Subject: expression.Discovered.MemberKind})
		}
	case SubjectExpressionChildrenOfScope:
		// The MEMBER kind only. See SubjectRole's doc comment for why the
		// anchor contributes no slot.
		if expression.Scoped != nil && expression.Scoped.MemberKind != "" {
			slots = append(slots, roleSlot{Role: SubjectRoleMember, Subject: expression.Scoped.MemberKind})
		}
	case SubjectExpressionGroupedMembers:
		if expression.Grouped != nil {
			if expression.Grouped.MemberKind != "" {
				slots = append(slots, roleSlot{Role: SubjectRoleMember, Subject: expression.Grouped.MemberKind})
			}
			if expression.Grouped.GroupKind != "" {
				slots = append(slots, roleSlot{Role: SubjectRoleGroup, Subject: expression.Grouped.GroupKind})
			}
		}
	case SubjectExpressionOrganizationScope:
		slots = append(slots, roleSlot{Role: SubjectRoleSubject, Subject: SubjectOrganization})
		// MemberKind is the COUNTED entity kind and invariant I17 requires
		// it for a counting goal. It is a member slot rather than a second
		// subject slot because it names a population, which is exactly
		// what `count` attaches to below.
		if expression.Org != nil && expression.Org.MemberKind != nil && *expression.Org.MemberKind != "" {
			slots = append(slots, roleSlot{Role: SubjectRoleMember, Subject: *expression.Org.MemberKind})
		}
	}
	return slots
}

// attachesToRole reports whether an obligation attaches to a role, for a
// frame of this variant.
//
// THIS IS THE WHOLE CORRECTION THE DECLARATION SLICE'S VERSION LACKED, and
// it is design finding S3 applied: the frozen SubjectRole table was keyed
// on the union discriminator ALONE, which "read Kind alone, which planned
// per-member reads for a count". A flat obligation x role product
// over-implies in both directions -- it puts `health` on a grouping axis,
// where a group is not a thing health is read of, and it multiplies a
// cardinality across every role in the frame.
//
// Keyed on (variant, role, obligation). Three rules carry the difference:
//
//  1. THE GROUP RULE. Only `state` attaches to SubjectRoleGroup. §13.15.2
//     records why the group role exists at all -- the frozen table carried
//     a group-role `state` row for grouped_members -- and every other read
//     obligation is read of the MEMBERS, then rolled up or not. Whether the
//     group `state` requirement is served by a named post-fact-read step or
//     by rolling up member facts is NOT decided here: groups exist only
//     after the fact read (§12 C2), that decision is question (b) of
//     §13.15.2, and it is settled by running Q-A on the rig. This layer
//     derives the coordinate and names it; it does not choose the serving.
//
//  2. THE COUNT EXCEPTION. `count` is COMPUTED -- a membership cardinality,
//     not a read (frame_vocab.go's kinds table) -- and it attaches to the
//     POPULATION being counted, exactly once. Never to a group (a grouping
//     axis is not a population), and never to the organization subject slot
//     when a counted member kind is present, because invariant I17 exists
//     precisely so "how many teams" and "how many repositories" do not
//     collapse to the same frame.
//
//  3. THE ORGANIZATION MEMBER RULE, above: that slot serves only the
//     population obligations, because naming a counted entity kind is the
//     whole reason the field exists.
//
// `ranking` follows the same population rule as `count` for the same
// reason: RankCohort orders a member set, and a comparison ranks its
// operands. It is stated separately rather than folded in, because the two
// obligations are unavailable for different reasons and a future edit to
// one must not silently move the other.
func attachesToRole(variant SubjectExpressionKind, role SubjectRole, obligation AnswerObligation, hasPopulationSlot bool) bool {
	// THE ORGANIZATION'S MEMBER SLOT NAMES A COUNTED POPULATION, NOTHING
	// ELSE. OrganizationScopeExpression.MemberKind is defined as "the entity
	// kind being COUNTED, when the goal set contains count_or_aggregate;
	// optional otherwise" -- so it exists to name what a cardinality runs
	// over, and invariant I17 is what makes it required for that case.
	//
	// Keying on the field's PRESENCE instead of on its meaning derived a
	// per-member READ requirement from an organization-level question:
	// {assess_state, organization_scope{member_kind: repository}} produced
	// `state / member / repository`, a read for every repository in answer
	// to a question about the organization. A review round constructed it,
	// and the justification for the slot was already written in this file --
	// the code simply did not follow it.
	//
	// This is why the table is keyed on the VARIANT as well as the role and
	// the obligation: what a role means is a joint property of the topology
	// and the obligation, the same way a producer's obligations turned out
	// to be a joint property of the producer and the subject kind.
	if variant == SubjectExpressionOrganizationScope && role == SubjectRoleMember {
		return obligation == ObligationCount || obligation == ObligationRanking
	}

	switch obligation {
	case ObligationCount, ObligationRanking:
		// The population is the member slot when the frame has one;
		// otherwise the named subject or each operand IS the population.
		if hasPopulationSlot {
			return role == SubjectRoleMember
		}
		return role == SubjectRoleSubject || role == SubjectRoleOperand
	case ObligationState:
		// The only obligation that reaches a grouping axis.
		return true
	default:
		return role != SubjectRoleGroup
	}
}

// DeriveRequirementCoordinates crosses the frame's DERIVED obligation set
// with the roles its subject expression offers.
//
// Answer-contract obligations (`evidence`, `coverage`) yield NO coordinate:
// they are satisfied by the answer contract itself and involve no subject
// (frame_vocab.go's kinds table). Computed obligations DO yield one --
// `count` and `ranking` are unavailable only when their inputs are, and a
// requirement row that names the server step is what oracle O9 checks.
//
// WIDENED OBLIGATIONS ARE NOT READ. Frame.Obligations is the server-derived
// set; WidenedObligations are advisory and "may not degrade answer
// completeness" (§13.2.4 rule 1). Deriving coordinates for them would put
// an advisory member into the same cell space as a required one, which is
// the exact confusion the two fields exist to prevent.
//
// The result is deduplicated and returned in a total, stable order, so two
// runs of one frame produce a diffable list -- the property the regenerated
// artifact depends on.
func DeriveRequirementCoordinates(frame QuestionFrame) []RequirementCoordinate {
	slots := frameRoleSlots(frame.SubjectExpression)
	hasPopulationSlot := false
	for _, slot := range slots {
		if slot.Role == SubjectRoleMember {
			hasPopulationSlot = true
			break
		}
	}

	seen := make(map[RequirementCoordinate]bool, len(slots)*len(frame.Obligations))
	coordinates := make([]RequirementCoordinate, 0, len(slots)*len(frame.Obligations))
	for _, obligation := range frame.Obligations {
		kind, known := KindOfObligation(obligation)
		if !known || kind == ObligationKindAnswerContract {
			continue
		}
		for _, slot := range slots {
			if !attachesToRole(frame.SubjectExpression.Kind, slot.Role, obligation, hasPopulationSlot) {
				continue
			}
			coordinate := RequirementCoordinate{Obligation: obligation, Role: slot.Role, Subject: slot.Subject}
			if seen[coordinate] {
				continue
			}
			seen[coordinate] = true
			coordinates = append(coordinates, coordinate)
		}
	}
	sortCoordinates(coordinates)
	return coordinates
}

// sortCoordinates imposes the total order the artifact and every histogram
// depend on. Every field participates, so no two distinct coordinates
// compare equal and the order cannot depend on map iteration.
func sortCoordinates(coordinates []RequirementCoordinate) {
	sort.Slice(coordinates, func(i, j int) bool {
		left, right := coordinates[i], coordinates[j]
		if left.Obligation != right.Obligation {
			return left.Obligation < right.Obligation
		}
		if left.Role != right.Role {
			return left.Role < right.Role
		}
		return left.Subject < right.Subject
	})
}

// RenderRequirementCoordinates writes one frame's coordinates as a stable
// text block.
//
// IT LIVES IN PRODUCTION BECAUSE IT IS THE ROT GUARD'S ONLY AUTHORITY. The
// checked-in artifact is regenerated by calling this function and diffed;
// there is deliberately no second table for a reviewer's edit to be
// coordinated across, which is the defect that survived two review rounds
// in the declaration slice. A change to the role rules moves the artifact,
// and the artifact is the review surface.
//
// label identifies the frame in the artifact. It is a STRUCTURAL label or a
// question id and never question text -- the corpus-safety rule the trace
// has carried since the entry gate.
func RenderRequirementCoordinates(label string, coordinates []RequirementCoordinate) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%s\n", label)
	if len(coordinates) == 0 {
		out.WriteString("  (no coordinates: the frame's subject expression offers no role with a known subject kind)\n")
		return out.String()
	}
	for _, coordinate := range coordinates {
		fmt.Fprintf(&out, "  %-22s %-9s %s\n", coordinate.Obligation, coordinate.Role, coordinate.Subject)
	}
	return out.String()
}

// ---------------------------------------------------------------------------
// COMPARISON OPERAND CLASSIFICATION
// ---------------------------------------------------------------------------
//
// PLACED BELOW frameRoleSlots DELIBERATELY, and this comment is the reason it
// must stay there. read_population.go cites frameRoleSlots' explicit-set walk
// by LINE RANGE ("SLOTS ARE WALKED EXACTLY AS frameRoleSlots WALKS THEM
// (subject_role.go:128-143)"). Inserting anything above that walk silently
// falsifies a cross-reference in a file this change does not touch, and the
// falsification is invisible -- nothing compiles differently and no test goes
// red. Everything this section adds goes at the END of the file.

// ComparisonOperandVariant names WHICH member of the operand union a slot
// carries. It mirrors SubjectOperandKind rather than aliasing it because the
// two answer different questions: SubjectOperandKind is the wire
// discriminator, this is the resolver's reading of it, and a future wire
// member this cut does not admit must be expressible as "not one of these"
// rather than silently arriving as a valid variant.
type ComparisonOperandVariant string

const (
	// ComparisonOperandNamed: the operand names a subject by term.
	ComparisonOperandNamed ComparisonOperandVariant = "named_subject"
	// ComparisonOperandScoped: the operand is the members of a kind under an
	// anchor. Recognised so it can be HELD, never so it can be resolved --
	// see ClassifyComparisonOperands' own doc comment.
	ComparisonOperandScoped ComparisonOperandVariant = "children_of_scope"
)

// ComparisonAdmission is the closed outcome vocabulary of the classifier.
//
// EVERY REFUSAL IS ITS OWN MEMBER. A single "not admitted" would make the
// three reasons indistinguishable to telemetry and to the reader, and two of
// them (a scoped operand, and an operand count outside the cut) are
// deliberately-out-of-scope shapes rather than failures -- a distinction the
// prompt and the logs both need to keep.
type ComparisonAdmission string

const (
	// ComparisonNotAComparison: the frame is not an explicit set at all.
	// Every other expression variant takes this, including the ones that are
	// cohort-shaped.
	ComparisonNotAComparison ComparisonAdmission = "not_a_comparison"
	// ComparisonAdmittedNamedPair: exactly two named operands, each stating
	// its kind. This is the cut, and the only member that resolution acts on.
	ComparisonAdmittedNamedPair ComparisonAdmission = "admitted_named_pair"
	// ComparisonHeldScopedOperand: the pair contains a scoped operand. Held
	// BEFORE any operand retrieval -- admitting it would flip the whole
	// answer to the absorbing degraded state, so a resolver limitation would
	// reach the user as a data problem.
	ComparisonHeldScopedOperand ComparisonAdmission = "held_scoped_operand"
	// ComparisonOutOfCutOperandCount: an explicit set of other than two
	// operands. Named as out of scope, not as broken.
	ComparisonOutOfCutOperandCount ComparisonAdmission = "out_of_cut_operand_count"
	// ComparisonOutOfCutUnstatedKind: an operand whose kind the question did
	// not state. An absent expected kind is a WEAKER CLAIM than a guessed
	// one (frame.go), and this cut is defined over questions that state both.
	ComparisonOutOfCutUnstatedKind ComparisonAdmission = "out_of_cut_unstated_kind"
)

// ComparisonOperandSlot is ONE operand's structural description, in the
// frame's own operand order.
//
// Position is the frame's index and is the ONLY ordering authority downstream:
// published committed order follows it, so nothing may reconstruct operand
// order by iterating a subject map.
type ComparisonOperandSlot struct {
	Position int
	Variant  ComparisonOperandVariant
	// Kind is the STATED kind: a named operand's ExpectedKind, or a scoped
	// operand's MemberKind. Never inferred, never defaulted -- an operand
	// with no stated kind puts the whole classification out of cut instead.
	Kind SubjectKind
	// Terms are this operand's OWN retrieval terms, and they are the reason
	// this type exists: frameRoleSlots carries the role/kind projection but
	// not the terms, and a slot resolved from the whole-question term bag
	// instead of from its own terms is the defect this work removes.
	//
	// For a scoped operand these are the ANCHOR terms, which are retrieval
	// POINTERS, NEVER VALUES. They are carried so the hold can name the side
	// it is holding, never so the anchor can be resolved into an operand.
	Terms []string
}

// ComparisonOperands is the classifier's whole answer.
type ComparisonOperands struct {
	Admission ComparisonAdmission
	// Slots is populated for every explicit set, INCLUDING the out-of-cut and
	// held ones, because the clarification has to name the sides it is
	// declining to resolve. Empty only for ComparisonNotAComparison.
	Slots []ComparisonOperandSlot
}

// Admitted reports whether resolution may act on this classification.
func (c ComparisonOperands) Admitted() bool {
	return c.Admission == ComparisonAdmittedNamedPair
}

// ClassifyComparisonOperands reads a validated frame's operand structure.
//
// STRUCTURAL ONLY. It performs NO identity matching, consults no graph, and
// resolves nothing: it reports which operands the question names, in which
// positions, of which variants, with which stated kinds and which of their own
// terms. Deciding WHICH SUBJECT an operand denotes is resolution's job and
// happens one layer down, per operand, against that operand's own terms.
//
// THE KIND PROJECTION IS frameRoleSlots', NOT A PARALLEL ONE. This function
// walks the operands for what frameRoleSlots does not carry -- position,
// variant, terms -- and takes role/kind from frameRoleSlots itself. That is
// not tidiness: the declaration slice already shipped a coordinate derivation
// whose oracle passed while a whole operand variant was never derived, and it
// passed because the artifact was rendered by the same function that dropped
// the variant. A second, independently-written kind walk here would be exactly
// that shape again, and the two would drift apart silently the next time the
// union gains a member.
//
// IT ALSO USES frameRoleSlots' OWN DROPPING BEHAVIOUR AS A SIGNAL. That
// function emits no slot for an operand whose kind is unstated, so an operand
// slot count below the operand count IS the unstated-kind case -- read off the
// authority rather than re-tested here with a second copy of the same
// condition.
//
// A nil or non-explicit-set frame is not a comparison, and says so rather than
// returning a zero value a caller could mistake for an admitted empty pair.
func ClassifyComparisonOperands(frame *QuestionFrame) ComparisonOperands {
	if frame == nil || frame.SubjectExpression.Kind != SubjectExpressionExplicitSet || frame.SubjectExpression.Explicit == nil {
		return ComparisonOperands{Admission: ComparisonNotAComparison}
	}
	operands := frame.SubjectExpression.Explicit.Operands

	// The role/kind projection, from the single authority. Filtered to the
	// operand role: an explicit set offers only operand slots today, and
	// reading the role explicitly means a future variant that adds another
	// role here cannot silently be counted as an operand.
	var operandKinds []SubjectKind
	for _, slot := range frameRoleSlots(frame.SubjectExpression) {
		if slot.Role == SubjectRoleOperand {
			operandKinds = append(operandKinds, slot.Subject)
		}
	}

	slots := make([]ComparisonOperandSlot, 0, len(operands))
	kindIndex := 0
	scoped := false
	for position, operand := range operands {
		slot := ComparisonOperandSlot{Position: position}
		switch {
		case operand.Named != nil:
			slot.Variant = ComparisonOperandNamed
			slot.Terms = append([]string(nil), operand.Named.Terms...)
		case operand.Scoped != nil:
			slot.Variant = ComparisonOperandScoped
			slot.Terms = append([]string(nil), operand.Scoped.AnchorTerms...)
			scoped = true
		default:
			// Neither pointer set: a frame that never passed invariant I1.
			// It contributes a positioned slot with no variant and no kind
			// so the count stays honest, and the missing kind puts the
			// classification out of cut below.
			slots = append(slots, slot)
			continue
		}
		if kindIndex < len(operandKinds) {
			slot.Kind = operandKinds[kindIndex]
			kindIndex++
		}
		slots = append(slots, slot)
	}

	switch {
	case len(operands) != comparisonCutOperandCount:
		return ComparisonOperands{Admission: ComparisonOutOfCutOperandCount, Slots: slots}
	// The unstated-kind check runs BEFORE the scoped hold, deliberately. A
	// scoped operand whose member kind is absent is out of cut for the same
	// reason a named one is -- it is not a shape this cut can describe at all
	// -- and reporting it as a scoped hold would claim the classifier
	// understood a frame it did not.
	case len(operandKinds) != len(operands):
		return ComparisonOperands{Admission: ComparisonOutOfCutUnstatedKind, Slots: slots}
	case scoped:
		return ComparisonOperands{Admission: ComparisonHeldScopedOperand, Slots: slots}
	}
	return ComparisonOperands{Admission: ComparisonAdmittedNamedPair, Slots: slots}
}

// comparisonCutOperandCount is the operand count this cut is defined over.
// Named rather than inlined so the two places that care -- the classifier and
// its tests -- cannot disagree about what "the pair" means.
const comparisonCutOperandCount = 2
