package contextfabric

import (
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-5720: answerability is decided per ROLE of the accepted reading.
//
// WHY ROLES AND NOT A KIND UNION. A single set -- the frame's member kind
// united with its group kind -- compared against every kind any offer channel
// carries is wrong in both directions as soon as a frame has more than one
// role:
//
//   - A children_of_scope question asks for the members of a kind under an
//     ANCHOR, and the anchor must be committed before a single member can be
//     discovered. The anchor's kind is never the member kind (vol. 2 phase-B
//     invariant I11), and the flat set held only the member kind -- so a turn
//     that offered exactly the anchor candidates the question needed was
//     refused as declared_kind_unmatched, although redeeming one of those
//     candidates commits the anchor and serves the scoped document. The
//     corpus rows cv-scoped-projects-by-team-bounded and qb-scoped have this
//     shape: declared kind project, offered kind team.
//   - A grouped_members question declares two kinds, and the flat set counted
//     an offer of EITHER as useful. Neither axis is decided by a caller picking
//     one subject: the partition is discovered by the server. Finding some
//     kind the frame mentions does not show that the offer advances the
//     question.
//
// THE RULE. An offer is redeemable for a reading only when it ADVANCES a role
// of that reading: the role is still open to a caller's pick, and the offer's
// kind is a kind that role admits. A kind match on a role that no pick decides
// neither satisfies the decision nor is counted toward it.
//
//	variant            role      state       admits
//	named_subject      subject   open        ExpectedKind; any kind when undeclared
//	discovered_kind    member    open        MemberKind (its members are the subjects it commits)
//	children_of_scope  anchor    open        the sample's anchor kind; when unknown, any kind but the member kind (I11)
//	                   member    population  -- discovered under the committed anchor
//	grouped_members    member    population  --
//	                   group     population  --
//	explicit_set       operand   open        named operand: ExpectedKind, any when undeclared;
//	                                         scoped operand: its anchor, any kind but that operand's member kind
//	organization_scope subject   resolved    -- the caller's organization; never a candidate
//	                   member    population  --
//
// A kind counts as DECLARED only when it is a member of the closed subject
// kind vocabulary, the same membership rule QuestionFrame.DeclaredKinds
// applies; anything else is undeclared, which is the weaker claim.
//
// ONE PREDICATE, TWO ADAPTERS. decideAnswerability takes a reading and a list
// of offers and nothing else. A composing turn builds both from the accepted
// frame, the winning sample's anchor kind and the gated offer material; a
// stored result builds them from its persisted semantic state and the offers
// it served. The decision itself exists once.

// answerabilityRole is the closed vocabulary of roles an answerability
// decision evaluates. Telemetry-safe: no prose, no identifiers.
//
// It is NOT SubjectRole. SubjectRole names the coordinates obligations attach
// to, and it has no anchor member by design -- an anchor contributes no
// requirement. An anchor IS a role a caller's pick decides, which is the one
// thing this vocabulary exists to name.
type answerabilityRole string

const (
	answerabilityRoleSubject answerabilityRole = "subject"
	answerabilityRoleAnchor  answerabilityRole = "anchor"
	answerabilityRoleMember  answerabilityRole = "member"
	answerabilityRoleGroup   answerabilityRole = "group"
	answerabilityRoleOperand answerabilityRole = "operand"
)

var answerabilityRoles = [...]answerabilityRole{
	answerabilityRoleSubject,
	answerabilityRoleAnchor,
	answerabilityRoleMember,
	answerabilityRoleGroup,
	answerabilityRoleOperand,
}

// answerabilityRoleState says how a role gets decided.
type answerabilityRoleState string

const (
	// answerabilityRoleOpen: a caller's pick decides the role. The only state
	// an offer can advance.
	answerabilityRoleOpen answerabilityRoleState = "open"
	// answerabilityRolePopulation: the server discovers the role's members;
	// no single pick decides it.
	answerabilityRolePopulation answerabilityRoleState = "population"
	// answerabilityRoleResolved: the reading itself fixes the role.
	answerabilityRoleResolved answerabilityRoleState = "resolved"
)

var answerabilityRoleStates = [...]answerabilityRoleState{
	answerabilityRoleOpen,
	answerabilityRolePopulation,
	answerabilityRoleResolved,
}

// answerabilityChannel is the closed vocabulary of offer channels a caller can
// redeem a subject answer through. Window options are not a member: a window
// option answers WHEN, never WHICH SUBJECT (chaos5660_declared_kind_terminal.go).
type answerabilityChannel string

const (
	answerabilityChannelKindOption       answerabilityChannel = "kind_option"
	answerabilityChannelAnchorOption     answerabilityChannel = "anchor_option"
	answerabilityChannelHandleOption     answerabilityChannel = "handle_option"
	answerabilityChannelCandidateOption  answerabilityChannel = "candidate_option"
	answerabilityChannelSubjectCandidate answerabilityChannel = "subject_candidate"
)

var answerabilityChannels = [...]answerabilityChannel{
	answerabilityChannelKindOption,
	answerabilityChannelAnchorOption,
	answerabilityChannelHandleOption,
	answerabilityChannelCandidateOption,
	answerabilityChannelSubjectCandidate,
}

// answerabilitySlot is one role of a reading, with what it admits.
type answerabilitySlot struct {
	Role  answerabilityRole
	State answerabilityRoleState
	// Kind is the role's declared kind, "" when undeclared.
	Kind SubjectKind
	// Excluded is the one kind an open, undeclared role can never be, "" when
	// there is none. Only an anchor carries one: the member kind it hangs
	// members of.
	Excluded SubjectKind
}

// admits reports whether an offer of kind advances this role.
func (s answerabilitySlot) admits(kind SubjectKind) bool {
	if s.State != answerabilityRoleOpen || kind == "" {
		return false
	}
	if s.Kind != "" {
		return kind == s.Kind
	}
	return s.Excluded == "" || kind != s.Excluded
}

// observable renders the slot as role:kind:state, with "undeclared" for an
// undeclared kind -- a word, never an empty segment.
func (s answerabilitySlot) observable() string {
	kind := string(s.Kind)
	if kind == "" {
		kind = "undeclared"
	}
	return string(s.Role) + ":" + kind + ":" + string(s.State)
}

// answerabilityOffer is one offer a caller could redeem: the channel it
// arrived on and the kind it carries.
type answerabilityOffer struct {
	Channel answerabilityChannel
	Kind    SubjectKind
}

// answerabilityAdvance is the first offer that advanced a role, and the role.
type answerabilityAdvance struct {
	Slot    answerabilitySlot
	Channel answerabilityChannel
	Kind    SubjectKind
}

// answerabilityReading is the accepted reading an answerability decision is
// taken under: the validated frame and the anchor kind retrieval was hinted
// with.
type answerabilityReading struct {
	Frame *QuestionFrame
	// AnchorKind is ScopeAnchorRetrievalKind's verdict: a vocabulary kind that
	// differs from the member kind of a children_of_scope frame with anchor
	// terms, or "".
	AnchorKind SubjectKind
}

// answerabilityReadingOf builds the reading from a frame and the anchor kind
// the accepted sample stated, through the SAME gate retrieval is hinted with,
// so the anchor this decision admits is the anchor retrieval searched for.
func answerabilityReadingOf(frame *QuestionFrame, sampleAnchorKind SubjectKind) answerabilityReading {
	return answerabilityReading{Frame: frame, AnchorKind: ScopeAnchorRetrievalKind(frame, sampleAnchorKind)}
}

// vocabularyKind returns kind when it is a closed-vocabulary member, else "".
func vocabularyKind(kind SubjectKind) SubjectKind {
	if !contractsv1.ValidContextFabricSubjectKind(kind) {
		return ""
	}
	return kind
}

// vocabularyKindOf is vocabularyKind over an optional kind.
func vocabularyKindOf(kind *SubjectKind) SubjectKind {
	if kind == nil {
		return ""
	}
	return vocabularyKind(*kind)
}

// slots derives the reading's roles, in derivation order. Nil when there is
// nothing to evaluate: no frame, an unknown variant, or a variant whose
// pointer is absent. Every branch reads the pointer the discriminator names
// and reads it defensively.
func (r answerabilityReading) slots() []answerabilitySlot {
	if r.Frame == nil {
		return nil
	}
	expression := r.Frame.SubjectExpression
	switch expression.Kind {
	case SubjectExpressionNamed:
		if expression.Named == nil {
			return nil
		}
		return []answerabilitySlot{{Role: answerabilityRoleSubject, State: answerabilityRoleOpen, Kind: vocabularyKindOf(expression.Named.ExpectedKind)}}
	case SubjectExpressionDiscoveredKind:
		if expression.Discovered == nil {
			return nil
		}
		return []answerabilitySlot{{Role: answerabilityRoleMember, State: answerabilityRoleOpen, Kind: vocabularyKind(expression.Discovered.MemberKind)}}
	case SubjectExpressionChildrenOfScope:
		if expression.Scoped == nil {
			return nil
		}
		member := vocabularyKind(expression.Scoped.MemberKind)
		return []answerabilitySlot{
			{Role: answerabilityRoleAnchor, State: answerabilityRoleOpen, Kind: vocabularyKind(r.AnchorKind), Excluded: member},
			{Role: answerabilityRoleMember, State: answerabilityRolePopulation, Kind: member},
		}
	case SubjectExpressionGroupedMembers:
		if expression.Grouped == nil {
			return nil
		}
		return []answerabilitySlot{
			{Role: answerabilityRoleMember, State: answerabilityRolePopulation, Kind: vocabularyKind(expression.Grouped.MemberKind)},
			{Role: answerabilityRoleGroup, State: answerabilityRolePopulation, Kind: vocabularyKind(expression.Grouped.GroupKind)},
		}
	case SubjectExpressionExplicitSet:
		if expression.Explicit == nil {
			return nil
		}
		var slots []answerabilitySlot
		for _, operand := range expression.Explicit.Operands {
			switch {
			case operand.Kind == SubjectOperandNamed && operand.Named != nil:
				slots = append(slots, answerabilitySlot{Role: answerabilityRoleOperand, State: answerabilityRoleOpen, Kind: vocabularyKindOf(operand.Named.ExpectedKind)})
			case operand.Kind == SubjectOperandScoped && operand.Scoped != nil:
				slots = append(slots, answerabilitySlot{Role: answerabilityRoleOperand, State: answerabilityRoleOpen, Excluded: vocabularyKind(operand.Scoped.MemberKind)})
			}
		}
		return slots
	case SubjectExpressionOrganizationScope:
		if expression.Org == nil {
			return nil
		}
		slots := []answerabilitySlot{{Role: answerabilityRoleSubject, State: answerabilityRoleResolved, Kind: SubjectOrganization}}
		if expression.Org.MemberKind != nil {
			slots = append(slots, answerabilitySlot{Role: answerabilityRoleMember, State: answerabilityRolePopulation, Kind: vocabularyKindOf(expression.Org.MemberKind)})
		}
		return slots
	default:
		return nil
	}
}

// answerabilityOffersOfTurn lists the offers a composing turn carries: all
// four structure option channels, then the subject candidates, in that order.
// Read from the GATED material the served document is composed from.
func answerabilityOffersOfTurn(resolution SubjectResolution, material StructureOfferMaterial) []answerabilityOffer {
	offers := make([]answerabilityOffer, 0, len(material.KindOptions)+len(material.AnchorOptions)+len(material.HandleOptions)+len(material.CandidateOptions)+len(resolution.Candidates))
	for _, option := range material.KindOptions {
		offers = append(offers, answerabilityOffer{Channel: answerabilityChannelKindOption, Kind: option.Kind})
	}
	for _, option := range material.AnchorOptions {
		offers = append(offers, answerabilityOffer{Channel: answerabilityChannelAnchorOption, Kind: option.Kind})
	}
	for _, option := range material.HandleOptions {
		offers = append(offers, answerabilityOffer{Channel: answerabilityChannelHandleOption, Kind: option.Kind})
	}
	for _, option := range material.CandidateOptions {
		offers = append(offers, answerabilityOffer{Channel: answerabilityChannelCandidateOption, Kind: option.Kind})
	}
	for _, candidate := range resolution.Candidates {
		offers = append(offers, answerabilityOffer{Channel: answerabilityChannelSubjectCandidate, Kind: candidate.Subject.Kind})
	}
	return offers
}

// decideAnswerability is the one answerability predicate: whether any offer
// advances a role of the reading.
//
// EVERY CHANNEL A CALLER CAN REDEEM, not a sample: a caller may answer through
// any of them, so one advancing offer on any channel makes the turn
// answerable. An offer with no kind is skipped -- it can advance nothing and
// is not evidence of a wrong kind either.
func decideAnswerability(reading answerabilityReading, offers []answerabilityOffer) declaredKindDecision {
	decision := declaredKindDecision{DeclaredKinds: reading.Frame.DeclaredKinds(), Roles: reading.slots()}
	seen := make(map[SubjectKind]bool, 4)
	for _, offer := range offers {
		if offer.Kind == "" {
			continue
		}
		decision.OffersEvaluated++
		if !seen[offer.Kind] {
			seen[offer.Kind] = true
			decision.OfferedKinds = append(decision.OfferedKinds, offer.Kind)
		}
		for _, slot := range decision.Roles {
			if !slot.admits(offer.Kind) {
				continue
			}
			decision.OffersAdvancing++
			if decision.Advance == nil {
				decision.Advance = &answerabilityAdvance{Slot: slot, Channel: offer.Channel, Kind: offer.Kind}
			}
			break
		}
	}
	decision.Unsatisfiable = len(decision.Roles) > 0 && decision.OffersEvaluated > 0 && decision.OffersAdvancing == 0
	decision.OrganizationScopeUnsupported = organizationScopeUnsupported(reading.Frame)
	return decision
}

// organizationScopeUnsupported reports whether a reading makes the
// organization itself the subject and counts nothing.
//
// D48. The organization-scope member set is served for a
// counting goal only (D46); every other organization-scope question -- its
// state, health or drivers -- has no capability behind it. Such a question
// that ends without a committed subject is refused on its own basis, whatever
// retrieval offered: no offer can make it answerable, because the subject is
// the caller's own organization and is never a candidate. A counting question
// that ends there keeps the role decision, because "counts are supported" is
// true of it.
func organizationScopeUnsupported(frame *QuestionFrame) bool {
	return frame != nil && frame.SubjectExpression.Kind == SubjectExpressionOrganizationScope && !frame.HasGoal(GoalCountOrAggregate)
}

// The organization-scope terminal: its reason token, its sentence, and its
// wire basis. One decision, one basis, one sentence -- the pairing the
// declared-kind terminal keeps (chaos5660_declared_kind_terminal.go).
const (
	organizationScopeTerminalReason     = "organization_scope_unsupported"
	organizationScopeTerminalLimitation = contractsv1.ContextFabricOrganizationScopeUnsupportedLimitation
	organizationScopeTerminalBasis      = contractsv1.ContextFabricRefusalBasisOrganizationScopeUnsupported
)

// SubjectlessTerminalAnswerability is the role half of the subjectless
// terminal line: which roles the decision evaluated, which role the winning
// offer advanced and on which channel, and how many offers it read. Composed
// by the caller from the one decision value; every string is a closed token
// or the explicit "none".
type SubjectlessTerminalAnswerability struct {
	EvaluatedRoles   string
	AdvancedRole     string
	AdvancingChannel string
	OffersEvaluated  int
	OffersAdvancing  int
}

// ObservableAnswerability renders the role half of the decision for the log
// line.
func (d declaredKindDecision) ObservableAnswerability() SubjectlessTerminalAnswerability {
	observed := SubjectlessTerminalAnswerability{
		EvaluatedRoles:   "none",
		AdvancedRole:     "none",
		AdvancingChannel: "none",
		OffersEvaluated:  d.OffersEvaluated,
		OffersAdvancing:  d.OffersAdvancing,
	}
	if len(d.Roles) > 0 {
		rendered := make([]string, 0, len(d.Roles))
		for _, slot := range d.Roles {
			rendered = append(rendered, slot.observable())
		}
		observed.EvaluatedRoles = strings.Join(rendered, ",")
	}
	if d.Advance != nil {
		observed.AdvancedRole = string(d.Advance.Slot.Role) + ":" + string(d.Advance.Kind)
		observed.AdvancingChannel = string(d.Advance.Channel)
	}
	return observed
}
