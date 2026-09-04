package v1

import (
	"fmt"
	"strings"
)

// The plan-requirement layer: WHAT THE ANSWER WAS PLANNED TO CONTAIN, per
// requirement, on the wire.
//
// The problem it solves is the one the outcome layer beside it cannot. An
// outcome row says what BECAME of a requirement -- satisfied, narrowed,
// unavailable -- and names it by a coordinate string. It does not say what
// the requirement WAS: which fact kinds could serve it, which server step
// computes it, what that step consumes, whether anything executes it, or why
// it was unservable. A reader of the served artifact therefore learns that
// something was reduced without learning what it was reduced FROM, and the
// derivation that knew all of it ran and was discarded.
//
// So this layer publishes the derivation's own rows, and it publishes them on
// the PLAN rather than beside the outcomes, because of what they are: a plan
// requirement is a constant of the turn. It is derived once, before any
// narrowing, from the frame and the registry's declarations. An outcome row
// is the opposite -- appended by each stage that reduces the document. Two
// different lifetimes, two different homes, joined by one identity string.
//
// THE JOIN. Every outcome row's Requirement resolves to exactly one plan
// requirement's Requirement, and every plan requirement is named by at least
// the planning-stage outcome row that seeded from it. Neither array mints an
// id: both carry the coordinate the derivation is itself keyed on, so there
// is one authority for which requirement a row is about and the two cannot
// drift.
//
// WHAT IS DELIBERATELY NOT HERE. No member kind. The cohort's member kind is
// written after subject resolution, and a requirement row is derived before
// it; capturing it here would publish a value that did not exist when the row
// was made. The rows are asserted byte-identical across a resolved and an
// unresolved member kind rather than merely documented as independent of it.

// contextFabricSubjectRoles MIRRORS internal/contextfabric's own closed
// SubjectRole vocabulary onto the wire.
//
// It is a mirror, not an import, for the reason the answer-obligation mirror
// beside it records: contextfabric already imports this package, so the
// dependency cannot run the other way. Every mirror in this file is held to
// the same both-directions parity test on the domain side, so a new domain
// member cannot ship without its wire mirror and a mirror entry cannot
// outlive the member it mirrors.
var contextFabricSubjectRoles = [...]string{
	"subject", "member", "group", "operand",
}

// ContextFabricSubjectRoleVocabulary returns the mirrored vocabulary, for the
// domain-side parity test.
func ContextFabricSubjectRoleVocabulary() [len(contextFabricSubjectRoles)]string {
	return contextFabricSubjectRoles
}

// ValidContextFabricSubjectRole reports membership in the mirror.
func ValidContextFabricSubjectRole(value string) bool {
	return stringInVocabulary(value, contextFabricSubjectRoles[:])
}

// contextFabricAnswerObligationKinds mirrors the domain's AnswerObligationKind:
// how an obligation is satisfied at all.
//
// It is on the row because it is what makes the three server fields legible.
// FactKinds populated and Step empty is a READ; Step populated and FactKinds
// empty is a COMPUTATION; both empty is an UNAVAILABLE cell. A reader can
// infer that from which fields are set, but inference over absence is exactly
// what the outcome layer's own header refuses -- an absent field must not
// carry two meanings. Naming the kind states it.
var contextFabricAnswerObligationKinds = [...]string{
	"read", "computed", "answer_contract",
}

// ContextFabricAnswerObligationKindVocabulary returns the mirrored vocabulary.
func ContextFabricAnswerObligationKindVocabulary() [len(contextFabricAnswerObligationKinds)]string {
	return contextFabricAnswerObligationKinds
}

// ValidContextFabricAnswerObligationKind reports membership in the mirror.
func ValidContextFabricAnswerObligationKind(value string) bool {
	return stringInVocabulary(value, contextFabricAnswerObligationKinds[:])
}

// contextFabricComputedObligationSteps mirrors the domain's named server
// steps.
var contextFabricComputedObligationSteps = [...]string{
	"rank_cohort", "membership_cardinality",
}

// ContextFabricComputedObligationStepVocabulary returns the mirrored
// vocabulary.
func ContextFabricComputedObligationStepVocabulary() [len(contextFabricComputedObligationSteps)]string {
	return contextFabricComputedObligationSteps
}

// ValidContextFabricComputedObligationStep reports membership in the mirror.
func ValidContextFabricComputedObligationStep(value string) bool {
	return stringInVocabulary(value, contextFabricComputedObligationSteps[:])
}

// contextFabricComputedStepInputClasses mirrors the domain's input class: WHAT
// A COMPUTATION CONSUMES, stated positively.
//
// The class is not inferable from the input list, and that is the whole
// reason it exists rather than being derived at read time: a step that
// consumes NO fact and a step whose inputs nobody has declared both present
// as an empty list. `resolved_member_set` says the first; an absent class
// says the second. Collapsing them is what would let an undeclared step read
// as a step that depends on nothing.
var contextFabricComputedStepInputClasses = [...]string{
	"fact_kinds", "resolved_member_set",
}

// ContextFabricComputedStepInputClassVocabulary returns the mirrored
// vocabulary.
func ContextFabricComputedStepInputClassVocabulary() [len(contextFabricComputedStepInputClasses)]string {
	return contextFabricComputedStepInputClasses
}

// ValidContextFabricComputedStepInputClass reports membership in the mirror.
func ValidContextFabricComputedStepInputClass(value string) bool {
	return stringInVocabulary(value, contextFabricComputedStepInputClasses[:])
}

// contextFabricComputedStepExecutions mirrors the domain's execution
// declaration: whether a server function actually RUNS the step.
//
// It travels with the input class because either alone misleads. A step
// declared to consume nothing and a step nobody executes are
// indistinguishable from the inputs, and treating the second like the first
// is what would let an unexecuted step's "consumes nothing" authorize
// retiring the thing that actually causes the facts to be read.
var contextFabricComputedStepExecutions = [...]string{
	"server_executed", "declared_only",
}

// ContextFabricComputedStepExecutionVocabulary returns the mirrored
// vocabulary.
func ContextFabricComputedStepExecutionVocabulary() [len(contextFabricComputedStepExecutions)]string {
	return contextFabricComputedStepExecutions
}

// ValidContextFabricComputedStepExecution reports membership in the mirror.
func ValidContextFabricComputedStepExecution(value string) bool {
	return stringInVocabulary(value, contextFabricComputedStepExecutions[:])
}

// contextFabricCompletionScopes mirrors the domain's completion scope: over
// what population the requirement completes.
var contextFabricCompletionScopes = [...]string{
	"single_subject", "each_operand", "each_member", "each_group",
}

// ContextFabricCompletionScopeVocabulary returns the mirrored vocabulary.
func ContextFabricCompletionScopeVocabulary() [len(contextFabricCompletionScopes)]string {
	return contextFabricCompletionScopes
}

// ValidContextFabricCompletionScope reports membership in the mirror.
func ValidContextFabricCompletionScope(value string) bool {
	return stringInVocabulary(value, contextFabricCompletionScopes[:])
}

// contextFabricCompletionQuantifiers mirrors the domain's completion
// quantifier: how much of that population must be served.
var contextFabricCompletionQuantifiers = [...]string{
	"at_least_one", "corroborated", "exact", "all", "none",
}

// ContextFabricCompletionQuantifierVocabulary returns the mirrored
// vocabulary.
func ContextFabricCompletionQuantifierVocabulary() [len(contextFabricCompletionQuantifiers)]string {
	return contextFabricCompletionQuantifiers
}

// ValidContextFabricCompletionQuantifier reports membership in the mirror.
func ValidContextFabricCompletionQuantifier(value string) bool {
	return stringInVocabulary(value, contextFabricCompletionQuantifiers[:])
}

// contextFabricRequirementUnavailableReasons mirrors the domain's unavailable
// reasons.
//
// EACH MEMBER IS ACTIONABLE BY A DIFFERENT PARTY, which is the test a reason
// token has to pass to earn its place, and the reason this is a vocabulary
// rather than a bare "unavailable" bit: `subject_kind_unsupported` needs a new
// producer, `no_declaring_producer` needs a declaration change,
// `table_shape_undeclared` needs a query change, and
// `computed_population_absent` needs no change at all because the question
// asked for an ordering with nothing to order.
var contextFabricRequirementUnavailableReasons = [...]string{
	"subject_kind_unsupported", "no_declaring_producer",
	"table_shape_undeclared", "computed_population_absent",
}

// ContextFabricRequirementUnavailableReasonVocabulary returns the mirrored
// vocabulary.
func ContextFabricRequirementUnavailableReasonVocabulary() [len(contextFabricRequirementUnavailableReasons)]string {
	return contextFabricRequirementUnavailableReasons
}

// ValidContextFabricRequirementUnavailableReason reports membership in the
// mirror.
func ValidContextFabricRequirementUnavailableReason(value string) bool {
	return stringInVocabulary(value, contextFabricRequirementUnavailableReasons[:])
}

// ContextFabricPlanRequirementsMaxCount bounds how many requirement rows one
// plan may publish.
//
// It is the outcome layer's own bound rather than a second number chosen
// here: the outcome set is seeded one row per derived requirement, so a plan
// carrying more requirements than the outcome set can hold would describe a
// turn whose seed could not be represented. Deriving it means the two cannot
// drift apart the way two independently chosen constants would.
const ContextFabricPlanRequirementsMaxCount = ContextFabricPlanRequirementOutcomeMaxCount

// ContextFabricPlanRequirement is ONE derived requirement: the coordinate,
// what serves it, and -- for a computation -- what that computation consumes
// and whether anything runs it.
//
// EXACTLY ONE OF FactKinds / Step / Unavailable IS MEANINGFUL, and the
// combination is enforced by Validate rather than trusted:
//   - a served READ row has FactKinds non-empty, Step empty, Unavailable empty
//   - a COMPUTED row has Step non-empty, FactKinds empty, Unavailable empty
//   - an unavailable row has Unavailable non-empty and Quantifier `none`
//
// "Silently empty" is the state this layer exists to make impossible, and an
// unchecked struct admits it.
type ContextFabricPlanRequirement struct {
	// Requirement is the row's identity: the obligation/role/subject
	// coordinate the derivation is keyed on, in that order, "/"-separated.
	//
	// It is the SAME string the outcome rows carry, and it is not minted
	// separately here. A second authority for which requirement a row is
	// about is a drift that shows up the first time the derivation changes.
	Requirement string `json:"requirement"`
	// Obligation, Role and Subject are the coordinate's three parts spelled
	// out, copied so a reader of the row alone can act on it without
	// parsing the identity string. Validate asserts they AGREE with it, so
	// the redundancy cannot become a disagreement.
	Obligation string                   `json:"obligation"`
	Role       string                   `json:"role"`
	Subject    ContextFabricSubjectKind `json:"subject"`
	// Kind is how the obligation is satisfied at all: read, computed, or
	// given by the answer contract itself.
	Kind string `json:"kind"`
	// FactKinds are the kinds that can SERVE this cell, sorted. Empty for a
	// computed obligation and for an unavailable one.
	FactKinds []ContextFabricFactKind `json:"fact_kinds,omitempty"`
	// Step is the server step that satisfies a computed obligation, and
	// StepExecution says whether a server function actually runs it. Both
	// empty on a read obligation and on an unavailable one.
	Step          string `json:"step,omitempty"`
	StepExecution string `json:"step_execution,omitempty"`
	// InputClass and InputFactKinds are WHAT THE COMPUTATION CONSUMES.
	//
	// THEY ARE DELIBERATELY NOT FactKinds. FactKinds means "kinds that can
	// serve this cell" and every existing reader treats it as a planned
	// READ. A computation's inputs are facts some OTHER cell is responsible
	// for reading; folding them together would make a computed row look
	// like it planned reads of its own. Separate fields keep both
	// statements true at once: this cell reads nothing, AND these are the
	// kinds its step consumes.
	InputClass     string                  `json:"input_class,omitempty"`
	InputFactKinds []ContextFabricFactKind `json:"input_fact_kinds,omitempty"`
	// Scope is the population the requirement completes over and
	// Quantifier is how much of it must be served.
	Scope      string `json:"scope"`
	Quantifier string `json:"quantifier"`
	// Unavailable is the closed reason token, empty when the cell is
	// served.
	Unavailable string `json:"unavailable,omitempty"`
}

// Served reports whether the row has a server -- a fact kind for a read, a
// step for a computation.
func (r ContextFabricPlanRequirement) Served() bool { return r.Unavailable == "" }

// Validate enforces every mirrored vocabulary, the coordinate's agreement
// with its own identity string, and the exactly-one-server invariant.
func (r ContextFabricPlanRequirement) Validate() error {
	if !stringLengthBetween(r.Requirement, 1, ContextFabricRequirementIdentityMaxLength) ||
		!stringLengthBetween(r.Obligation, 1, ContextFabricRequirementObligationMaxLength) {
		return fmt.Errorf("plan requirement identity or obligation violates v1 bounds")
	}
	// The VALUE DOMAIN, not just the length. A vocabulary is closed over
	// its members; what may carry it is a separate question, and left
	// unstated it is open by default.
	if !ValidContextFabricAnswerObligation(r.Obligation) {
		return fmt.Errorf("plan requirement obligation %q is not a vocabulary member", r.Obligation)
	}
	if !ValidContextFabricSubjectRole(r.Role) {
		return fmt.Errorf("plan requirement role %q is not a vocabulary member", r.Role)
	}
	if !validContextFabricSubjectKind(r.Subject) {
		return fmt.Errorf("plan requirement subject kind %q is not a vocabulary member", r.Subject)
	}
	if !ValidContextFabricAnswerObligationKind(r.Kind) {
		return fmt.Errorf("plan requirement kind %q is not a vocabulary member", r.Kind)
	}
	if !ValidContextFabricCompletionScope(r.Scope) {
		return fmt.Errorf("plan requirement scope %q is not a vocabulary member", r.Scope)
	}
	if !ValidContextFabricCompletionQuantifier(r.Quantifier) {
		return fmt.Errorf("plan requirement quantifier %q is not a vocabulary member", r.Quantifier)
	}
	// The identity IS the coordinate, so the three copied fields must
	// reproduce it exactly. A row whose identity disagreed with its own
	// obligation, role or subject would give a reader two answers to
	// "which requirement is this" -- the same defect the outcome row's own
	// first-segment check exists to prevent, extended to all three parts
	// because this row copies all three.
	segments := strings.Split(r.Requirement, "/")
	if len(segments) != contextFabricRequirementIdentitySegments {
		return fmt.Errorf("plan requirement %q is not an obligation/role/subject coordinate", r.Requirement)
	}
	if segments[0] != r.Obligation || segments[1] != r.Role || segments[2] != string(r.Subject) {
		return fmt.Errorf("plan requirement %q disagrees with its own obligation/role/subject %q/%q/%q",
			r.Requirement, r.Obligation, r.Role, r.Subject)
	}
	if len(r.FactKinds) > ContextFabricFactKindCount {
		return fmt.Errorf("plan requirement declares more fact kinds than the closed vocabulary has")
	}
	for _, kind := range r.FactKinds {
		if !validFactKind(kind) {
			return fmt.Errorf("plan requirement fact kind %q is not a vocabulary member", kind)
		}
	}
	if len(r.InputFactKinds) > ContextFabricFactKindCount {
		return fmt.Errorf("plan requirement declares more input fact kinds than the closed vocabulary has")
	}
	for _, kind := range r.InputFactKinds {
		if !validFactKind(kind) {
			return fmt.Errorf("plan requirement input fact kind %q is not a vocabulary member", kind)
		}
	}
	if r.Step != "" && !ValidContextFabricComputedObligationStep(r.Step) {
		return fmt.Errorf("plan requirement step %q is not a vocabulary member", r.Step)
	}
	if r.StepExecution != "" && !ValidContextFabricComputedStepExecution(r.StepExecution) {
		return fmt.Errorf("plan requirement step_execution %q is not a vocabulary member", r.StepExecution)
	}
	if r.InputClass != "" && !ValidContextFabricComputedStepInputClass(r.InputClass) {
		return fmt.Errorf("plan requirement input_class %q is not a vocabulary member", r.InputClass)
	}
	if r.Unavailable != "" && !ValidContextFabricRequirementUnavailableReason(r.Unavailable) {
		return fmt.Errorf("plan requirement unavailable reason %q is not a vocabulary member", r.Unavailable)
	}
	return r.validateServerInvariant()
}

// validateServerInvariant enforces EXACTLY ONE OF FactKinds / Step /
// Unavailable, plus the couplings each of those three arms carries.
//
// It is separated from the vocabulary checks above so that a failure names
// which of the two families rejected: a token from no vocabulary and a row
// whose fields contradict each other are different defects with different
// fixes, and one compound predicate covering both would emit one message for
// either.
func (r ContextFabricPlanRequirement) validateServerInvariant() error {
	reads := len(r.FactKinds) > 0
	computes := r.Step != ""
	unavailable := r.Unavailable != ""
	servers := 0
	for _, set := range []bool{reads, computes, unavailable} {
		if set {
			servers++
		}
	}
	if servers != 1 {
		return fmt.Errorf(
			"plan requirement %q must have exactly one of fact_kinds, step or unavailable, got fact_kinds=%t step=%t unavailable=%t",
			r.Requirement, reads, computes, unavailable)
	}
	switch {
	case unavailable:
		// An unavailable cell serves none of its population. Any other
		// quantifier would claim a completion the row's own reason token
		// says did not happen.
		if r.Quantifier != contextFabricCompletionQuantifierNone {
			return fmt.Errorf("plan requirement %q is unavailable and must carry quantifier %q, got %q",
				r.Requirement, contextFabricCompletionQuantifierNone, r.Quantifier)
		}
		if r.InputClass != "" || len(r.InputFactKinds) > 0 || r.StepExecution != "" {
			return fmt.Errorf("plan requirement %q is unavailable and must declare no computation inputs or execution", r.Requirement)
		}
	case computes:
		// A step's inputs and its execution declaration travel together
		// with it: the class says what it consumes, the execution says
		// whether anything runs it, and a step missing either is the
		// half-stated shape this layer exists to remove.
		if r.InputClass == "" || r.StepExecution == "" {
			return fmt.Errorf("plan requirement %q names step %q and must declare both an input class and an execution", r.Requirement, r.Step)
		}
		// The class is a POSITIVE statement about the inputs, so the two
		// must agree in both directions: `fact_kinds` with no kinds
		// declares a consumption it does not name, and
		// `resolved_member_set` with kinds names facts it says it does
		// not read.
		if (r.InputClass == contextFabricComputedInputFactKinds) != (len(r.InputFactKinds) > 0) {
			return fmt.Errorf("plan requirement %q input class %q disagrees with its %d declared input fact kinds",
				r.Requirement, r.InputClass, len(r.InputFactKinds))
		}
		if r.Kind != contextFabricObligationKindComputed {
			return fmt.Errorf("plan requirement %q names a server step but its kind is %q, not %q",
				r.Requirement, r.Kind, contextFabricObligationKindComputed)
		}
	case reads:
		if r.InputClass != "" || len(r.InputFactKinds) > 0 || r.StepExecution != "" {
			return fmt.Errorf("plan requirement %q is a read and must declare no computation inputs or execution", r.Requirement)
		}
	}
	return nil
}

// The three mirrored members this file compares against by name. They are
// named constants rather than literals at the comparison sites so a mirror
// edit that drops one fails to compile instead of silently making a coupling
// check unsatisfiable.
const (
	contextFabricCompletionQuantifierNone = "none"
	contextFabricComputedInputFactKinds   = "fact_kinds"
	contextFabricObligationKindComputed   = "computed"
)

// ValidateContextFabricPlanRequirements validates the whole array and its
// count bound, and enforces that identities are UNIQUE.
//
// Uniqueness is a property of the array rather than of a row, which is why it
// cannot live in Validate: two rows can each be individually well formed and
// still make the join ambiguous. The join is the whole reason this array and
// the outcome array can be read together, so an ambiguous one is a defect in
// the document, not a tidy-up.
func ValidateContextFabricPlanRequirements(rows []ContextFabricPlanRequirement) error {
	if len(rows) > ContextFabricPlanRequirementsMaxCount {
		return fmt.Errorf("answer plan carries %d requirements, more than the %d bound",
			len(rows), ContextFabricPlanRequirementsMaxCount)
	}
	seen := make(map[string]bool, len(rows))
	for index, row := range rows {
		if err := row.Validate(); err != nil {
			return fmt.Errorf("requirement %d: %w", index, err)
		}
		if seen[row.Requirement] {
			return fmt.Errorf("answer plan carries requirement %q twice; the outcome join would be ambiguous", row.Requirement)
		}
		seen[row.Requirement] = true
	}
	return nil
}
