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
// Keyed on (role, obligation). Two rules carry the entire difference:
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
// `ranking` follows the same population rule as `count` for the same
// reason: RankCohort orders a member set, and a comparison ranks its
// operands. It is stated separately rather than folded in, because the two
// obligations are unavailable for different reasons and a future edit to
// one must not silently move the other.
func attachesToRole(role SubjectRole, obligation AnswerObligation, hasPopulationSlot bool) bool {
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
			if !attachesToRole(slot.Role, obligation, hasPopulationSlot) {
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
