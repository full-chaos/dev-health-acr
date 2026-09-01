package contextfabric

// CHAOS-4452 stage 2 (S7b-i), design §13.2 and §13.5.1: the compositional
// QuestionFrame and the SubjectExpression discriminated union.
//
// SHADOW ONLY -- see chaos4452_frame_vocab.go's package-level note. No
// wire surface, no schema, no migration; the frame rides
// ModelExecutionReceipt in phase 1.
//
// WHAT THIS OBJECT IS FOR, in the feedback's own words (§13.1): "We still
// do not have one authoritative representation of WHAT THE USER IS ASKING
// THE SYSTEM TO ESTABLISH, carried from interpretation through planning,
// evidence requirements, result qualification, and user-language
// disclosure." Stage 1's QuestionFamily is a CLASSIFICATION that mixes
// five independent axes -- subject topology, analytical operation,
// temporal operation, business domain and answer shape -- so a first-match
// classification either drops part of the question or grows into their
// cross-product. "Grouped cohort + trend" has no correct family row today,
// and adding one does not help, because the next question composes three
// axes. The frame sits ABOVE the family and the family is DERIVED from it
// (§13.4.1), which preserves every stage-1 slice: the family keeps its
// exact wire meaning and everything that reads it keeps reading it.

// QuestionFrame is the authoritative semantic object. It is what the user
// asked the system to ESTABLISH, and it survives interpretation ->
// planning -> evidence -> qualification -> disclosure unchanged.
//
// IMMUTABLE ONCE VALIDATED, and the boundary is precise: the frame becomes
// immutable at the END of PHASE A2, not at the end of A1 (law L4,
// §13.5.2). Normalization runs between A1 and A2 and is the last thing
// permitted to write to it. Nothing in phase B (post-resolution) or phase
// C (post-fact-read) may mutate it -- a phase-B or phase-C failure
// produces a clarification, a narrowed answer, or a requirement outcome
// with a disclosed impact, never a frame edit and never a refusal. That is
// why §13.2.3 forbids deriving any obligation from resolution or fact-read
// state: an obligation set that could change after the read would make the
// frame a moving target and every completeness claim derived from it
// unreproducible.
//
// AUTHORSHIP (§13.2.1). Goals, SubjectExpression, Temporal, Emphasis and
// Dimensions are MODEL-PROPOSED from closed vocabularies and
// server-validated. Obligations are SERVER-DERIVED from the whole frame; a
// model emission is admitted as WIDENING-ONLY and is advisory.
type QuestionFrame struct {
	// Goals is the closed, NON-EMPTY goal set. Order-insensitive: it is a
	// SET, normalized into vocabulary order by
	// SanitizeInvestigationGoals. An empty set after sanitization is a
	// phase-A1 failure (I15), NEVER a silent default -- round 2's P1-7
	// showed the old "unset defaults to assess_state" rule silently
	// turned "which teams are struggling?" into a status question,
	// losing the ranking operation with no repair, clarification or
	// refusal.
	Goals []InvestigationGoal `json:"goals"`

	// SubjectExpression is the closed discriminated union, §13.5.1.
	SubjectExpression SubjectExpression `json:"subject_expression"`

	// Temporal is the closed temporal axis, exactly one. Unset DERIVES
	// TemporalIntentCurrent during normalization.
	Temporal TemporalIntent `json:"temporal"`

	// Emphasis is the closed emphasis set; may be empty. Adds no
	// obligation and no fact read (§13.2.2a).
	Emphasis []AnswerEmphasis `json:"emphasis,omitempty"`

	// Dimensions is the closed set of the shipped nine HealthDimension
	// members; may be empty (= unconstrained). ADDITIVE-ONLY: it may ADD
	// an obligation (§13.2.3 table 3) and constrain which fact kinds
	// serve an existing one, and it may NEVER remove a derived
	// obligation.
	Dimensions []HealthDimension `json:"dimensions,omitempty"`

	// Obligations is SERVER-DERIVED and non-empty. Never read from model
	// output; a model-emitted obligation set enters through
	// WidenedObligations instead, so the two can never be confused at the
	// type level.
	Obligations []AnswerObligation `json:"obligations"`

	// WidenedObligations are the members a model emission ADDED beyond
	// the derived set. Every member here is `advisory` (§13.2.4 rule 1)
	// and an unsatisfied advisory requirement MAY NOT degrade answer
	// completeness. Kept as its own field rather than merged into
	// Obligations with a parallel requiredness map, because a merged list
	// is one careless range away from an advisory obligation being
	// treated as required -- which is the exact failure §13.2.4 exists to
	// prevent.
	WidenedObligations []AnswerObligation `json:"widened_obligations,omitempty"`

	// Version is the derivation-table version. It joins ReuseKey, so two
	// frames derived under different table versions are not
	// interchangeable for answer reuse.
	Version string `json:"version"`
}

// QuestionFrameVersion is the derivation-table version this build
// implements. It is bumped whenever §13.2.3's obligation tables or
// §13.4.1's family rows change meaning, for the same reason
// RankingFormulaVersion and WindowInferenceVersion are versioned: a
// persisted frame must be replayable against the table that produced it.
const QuestionFrameVersion = "question-frame.v1"

// Requiredness reports whether an obligation is required or advisory for
// this frame. Total: an obligation that is in neither set returns false.
//
// Requiredness is DERIVED, NEVER EMITTED (§13.2.1). Derived obligations
// are required; model-widened ones are advisory. A goal-induced obligation
// is REQUIRED even though Goals are model-proposed, and §13.2.4 rule 3
// accepts that deliberately: "a goal is what the user asked, and a
// spurious goal is a misinterpretation, not a plan edit." It is governed
// the way every other misinterpretation is -- consensus over N samples,
// the shadow re-measure of goal correctness WITH negative cases, and
// telemetry that records the goal set per sample so a split is countable.
func (f QuestionFrame) Requiredness(obligation AnswerObligation) (ObligationRequiredness, bool) {
	for _, member := range f.Obligations {
		if member == obligation {
			return RequirednessRequired, true
		}
	}
	for _, member := range f.WidenedObligations {
		if member == obligation {
			return RequirednessAdvisory, true
		}
	}
	return "", false
}

// HasGoal reports whether the goal set contains value.
func (f QuestionFrame) HasGoal(value InvestigationGoal) bool {
	for _, member := range f.Goals {
		if member == value {
			return true
		}
	}
	return false
}

// HasAnyGoal reports whether the goal set intersects values. Invariant I8
// is stated as an intersection over {describe_trend, explain_change}, so
// the helper exists rather than being open-coded twice.
func (f QuestionFrame) HasAnyGoal(values ...InvestigationGoal) bool {
	for _, value := range values {
		if f.HasGoal(value) {
			return true
		}
	}
	return false
}

// HasDimension reports whether the dimension set contains value.
func (f QuestionFrame) HasDimension(value HealthDimension) bool {
	for _, member := range f.Dimensions {
		if member == value {
			return true
		}
	}
	return false
}

// HasObligation reports whether the DERIVED obligation set contains value.
// Deliberately does not consider WidenedObligations: every caller that
// asks "did the frame derive this?" is asking about the server's own
// derivation, and a widened member answering yes would let an advisory
// obligation satisfy an invariant (I14) that exists to check a derived
// one.
func (f QuestionFrame) HasObligation(value AnswerObligation) bool {
	for _, member := range f.Obligations {
		if member == value {
			return true
		}
	}
	return false
}

// SubjectExpression is the discriminated union of subject topologies
// (design §13.5.1, chris's E1 ruling). It REPLACES CHAOS-4644's flat
// GroupKind/ScopeAnchorTerm promotion -- one contract migration instead of
// two.
//
// WHAT THE UNION BUYS OVER THE FIELD BAG IT REPLACES. Stage 1 recorded the
// failure mode from measured replicates
// (chaos4632_question_family_precedence.go:13-16): across two questions
// and six replicates, Shape took THREE distinct values and SubjectTerms
// was absent entirely in one replicate. The subject-terms-omission ticket
// is that class -- correct shape, correct judgment, correct group kind,
// subject_terms null. Under this union that object CANNOT BE CONSTRUCTED:
// a named_subject with no terms fails invariant I3 before any stage reads
// it.
//
// THE PREVENTION CLAIM IS NARROWED, exactly as §13.8b narrows it, and the
// narrowing is stated here so no reader takes the stronger version: the
// union makes the defect unrepresentable IN THE FRAME. It prevents it
// END-TO-END only once the flat fields are DERIVED from the frame, because
// resolution reads interpretation.SubjectTerms today
// (graphrank/resolve.go:1488). A valid union sitting beside
// subject_terms: null passes I3 and still starves resolution. That is
// seam 7 and it is the retrieval slice's work, not this file's.
type SubjectExpression struct {
	// Kind names the variant. Exactly one pointer below is non-nil and it
	// is the one Kind names -- invariant I1.
	Kind SubjectExpressionKind `json:"kind"`

	Named      *NamedSubjectExpression      `json:"named,omitempty"`      // named_subject
	Explicit   *ExplicitSetExpression       `json:"explicit,omitempty"`   // explicit_set
	Discovered *DiscoveredSetExpression     `json:"discovered,omitempty"` // discovered_kind
	Scoped     *ScopedSetExpression         `json:"scoped,omitempty"`     // children_of_scope
	Grouped    *GroupedSetExpression        `json:"grouped,omitempty"`    // grouped_members
	Org        *OrganizationScopeExpression `json:"org,omitempty"`        // organization_scope
}

// NamedSubjectExpression names one or more subjects directly.
type NamedSubjectExpression struct {
	// Terms are RETRIEVAL POINTERS, NEVER VALUES. Nothing branches on
	// their text; the only thing that ever branches is whether they
	// resolved, and to what. §13.5.1 carries that framing forward from
	// §4.2 verbatim, and it is the reason a free string is admissible
	// inside an otherwise closed union.
	Terms []string `json:"terms,omitempty"`
	// ExpectedKind is the kind the question expects, when the model
	// stated one. Optional: absent means the kind is not constrained,
	// which is a weaker claim than guessing one.
	ExpectedKind *SubjectKind `json:"expected_kind,omitempty"`
}

// ExplicitSetExpression enumerates the operands of a comparison.
//
// Operands are SubjectOperand, not bare named subjects, so that "compare
// team A's PROJECTS with team B's PROJECTS" is expressible (independent
// review R10). Depth is capped at 1 -- an operand may not itself be an
// explicit_set -- so the union stays finite and every oracle over it can
// be exhaustive rather than sampled.
type ExplicitSetExpression struct {
	Operands []SubjectOperand `json:"operands,omitempty"`
}

// SubjectOperandKind is the closed discriminator of a comparison operand.
// TWO members: an operand is a named subject or a scoped set.
type SubjectOperandKind string

const (
	// SubjectOperandNamed: the operand is a named subject.
	SubjectOperandNamed SubjectOperandKind = "named_subject"
	// SubjectOperandScoped: the operand is the members of a kind under a
	// named anchor.
	SubjectOperandScoped SubjectOperandKind = "children_of_scope"
)

var subjectOperandKinds = [...]SubjectOperandKind{
	SubjectOperandNamed,
	SubjectOperandScoped,
}

// SubjectOperandKindCount is two.
const SubjectOperandKindCount = len(subjectOperandKinds)

// SubjectOperandKindVocabulary returns the closed vocabulary in design
// order.
func SubjectOperandKindVocabulary() [SubjectOperandKindCount]SubjectOperandKind {
	return subjectOperandKinds
}

// ValidSubjectOperandKind reports membership. The empty value is not a
// member.
func ValidSubjectOperandKind(value SubjectOperandKind) bool {
	for _, member := range subjectOperandKinds {
		if member == value {
			return true
		}
	}
	return false
}

// SubjectOperand is one operand of an explicit comparison.
//
// IT IS A DISCRIMINATED UNION LIKE SubjectExpression ITSELF, and that is
// round-3 finding 9 applied in the type rather than in a ledger: the
// frozen text added the operand TYPE with two optional pointers and no
// discriminator, so a nil/nil or a both-non-nil operand had no rejection
// rule and I3 (which validates only Named.Terms) could not be satisfied by
// a scoped operand at all. Kind names the variant and invariant I19
// enforces exactly-one, the same way I1 does for the outer union.
type SubjectOperand struct {
	Kind   SubjectOperandKind      `json:"kind"`
	Named  *NamedSubjectExpression `json:"named,omitempty"`
	Scoped *ScopedSetExpression    `json:"scoped,omitempty"`
}

// DiscoveredSetExpression asks for the members of a kind, discovered from
// the graph.
type DiscoveredSetExpression struct {
	// MemberKind is the kind to discover. Closed
	// ContextFabricSubjectKind -- and it is the field the retrieval slice
	// makes DiscoveredCohort read instead of a substring match over model
	// prose (L6 inventory row 1).
	MemberKind SubjectKind `json:"member_kind"`
}

// ScopedSetExpression asks for the members of a kind reachable from a
// named anchor.
type ScopedSetExpression struct {
	// AnchorTerms are retrieval pointers to the scope anchor, on the same
	// never-a-value framing as NamedSubjectExpression.Terms. An
	// unresolvable anchor produces a scope_anchor clarification with real
	// candidates -- never a guess, never a fallback to a different
	// family.
	AnchorTerms []string `json:"anchor_terms,omitempty"`
	// MemberKind is the kind being asked for UNDER that anchor.
	MemberKind SubjectKind `json:"member_kind"`
}

// GroupedSetExpression asks for members of one kind grouped by another.
//
// This is the topology stage 1 could not express at all: ContextFabricCohort
// is FLAT ({Kind, Members, Exclusions, Rationale, Complete, Truncated}) --
// one subject kind, one list, no grouping axis and no scope anchor. The
// design states the consequence plainly (§1.1(b)): "project statuses for
// each team" is not mis-answered today, it is UNREPRESENTABLE, which is
// why that question either 413s or garbles.
type GroupedSetExpression struct {
	// GroupKind is the axis members are grouped BY.
	GroupKind SubjectKind `json:"group_kind"`
	// MemberKind is the kind of the members themselves. Invariant I6
	// requires GroupKind != MemberKind: grouping a kind by itself is not
	// a grouping, and the engine already refuses a plan in that state
	// (contracts/v1 answer-plan Validate, "groups %q members by their own
	// kind").
	MemberKind SubjectKind `json:"member_kind"`
}

// OrganizationScopeExpression makes the organization itself the subject.
type OrganizationScopeExpression struct {
	// MemberKind is the entity kind being COUNTED, when the goal set
	// contains count_or_aggregate. Optional otherwise.
	//
	// Round 2's P1-4 is why this field exists: without it, "how many
	// teams are in the organization?" and "how many repositories are in
	// the organization?" produce the IDENTICAL frame
	// {count_or_aggregate, organization_scope, current, []} with nothing
	// to recover the counted entity kind. Invariant I17 requires it for
	// counting goals and permits its absence otherwise, where the org
	// itself is the subject and there is nothing to enumerate.
	MemberKind *SubjectKind `json:"member_kind,omitempty"`
}

// MemberKind returns the member kind the expression asks for, and whether
// the variant has one at all.
//
// THIS IS THE FUNCTION SEAM 7 EXISTS TO FEED. §13.8b's R1 finding is that
// the frame declares MemberKind and NOTHING READS IT: the cohort kind is
// decided by a substring match over RequestedJudgment + SubjectTerms that
// defaults to `team` and can never return `repository`. The retrieval
// slice deletes that matcher and calls this instead. It is defined here,
// in the slice that introduces the union, so that the deletion is a
// one-line substitution rather than a new mechanism invented under
// deadline.
func (e SubjectExpression) MemberKind() (SubjectKind, bool) {
	switch e.Kind {
	case SubjectExpressionDiscoveredKind:
		if e.Discovered == nil {
			return "", false
		}
		return e.Discovered.MemberKind, e.Discovered.MemberKind != ""
	case SubjectExpressionChildrenOfScope:
		if e.Scoped == nil {
			return "", false
		}
		return e.Scoped.MemberKind, e.Scoped.MemberKind != ""
	case SubjectExpressionGroupedMembers:
		if e.Grouped == nil {
			return "", false
		}
		return e.Grouped.MemberKind, e.Grouped.MemberKind != ""
	case SubjectExpressionOrganizationScope:
		if e.Org == nil || e.Org.MemberKind == nil {
			return "", false
		}
		return *e.Org.MemberKind, *e.Org.MemberKind != ""
	default:
		return "", false
	}
}

// GroupKind returns the grouping axis and whether the variant has one.
// Only grouped_members does.
func (e SubjectExpression) GroupKind() (SubjectKind, bool) {
	if e.Kind != SubjectExpressionGroupedMembers || e.Grouped == nil {
		return "", false
	}
	return e.Grouped.GroupKind, e.Grouped.GroupKind != ""
}

// SubjectTerms returns the retrieval pointers this expression offers to
// resolution, per §13.8b's derivation table: Named.Terms, the operands'
// terms, or Scoped.AnchorTerms per variant.
//
// The DERIVATION is defined here; the retrieval slice is what makes
// resolution CONSUME it instead of reading the flat
// interpretation.SubjectTerms. Nothing in this slice changes what
// resolution reads.
func (e SubjectExpression) SubjectTerms() []string {
	switch e.Kind {
	case SubjectExpressionNamed:
		if e.Named == nil {
			return nil
		}
		return append([]string(nil), e.Named.Terms...)
	case SubjectExpressionChildrenOfScope:
		if e.Scoped == nil {
			return nil
		}
		return append([]string(nil), e.Scoped.AnchorTerms...)
	case SubjectExpressionExplicitSet:
		if e.Explicit == nil {
			return nil
		}
		terms := make([]string, 0, len(e.Explicit.Operands))
		for _, operand := range e.Explicit.Operands {
			terms = append(terms, operand.Terms()...)
		}
		return terms
	default:
		return nil
	}
}

// ComparisonTerms returns the operands' terms for an explicit_set, and
// nothing for every other variant.
//
// A named operand contributes Named.Terms and a SCOPED OPERAND
// CONTRIBUTES Scoped.AnchorTerms. That second clause is the author's
// handoff item 4 applied in code: the frozen design said "operands' terms"
// and never said what a scoped operand contributes, so the derivation was
// undefined for exactly the operand shape R10 had just introduced.
func (e SubjectExpression) ComparisonTerms() []string {
	if e.Kind != SubjectExpressionExplicitSet || e.Explicit == nil {
		return nil
	}
	terms := make([]string, 0, len(e.Explicit.Operands))
	for _, operand := range e.Explicit.Operands {
		terms = append(terms, operand.Terms()...)
	}
	return terms
}

// Terms returns the operand's retrieval pointers, per its own variant.
func (o SubjectOperand) Terms() []string {
	switch o.Kind {
	case SubjectOperandNamed:
		if o.Named == nil {
			return nil
		}
		return append([]string(nil), o.Named.Terms...)
	case SubjectOperandScoped:
		if o.Scoped == nil {
			return nil
		}
		return append([]string(nil), o.Scoped.AnchorTerms...)
	default:
		return nil
	}
}

// IsCohortVariant reports whether the expression names a SET of subjects
// rather than a single one. §13.4.1's rows 1-4 are exactly this predicate,
// and oracle O10 quantifies over it, so it is named once here rather than
// re-spelled at each site.
func (e SubjectExpression) IsCohortVariant() bool {
	switch e.Kind {
	case SubjectExpressionGroupedMembers,
		SubjectExpressionChildrenOfScope,
		SubjectExpressionExplicitSet,
		SubjectExpressionDiscoveredKind:
		return true
	default:
		return false
	}
}
