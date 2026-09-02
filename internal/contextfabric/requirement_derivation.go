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

	// Step is the server step that satisfies a computed obligation.
	// Empty for a read obligation.
	Step ComputedObligationStep

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
	rows := make([]DerivedRequirement, 0, len(coordinates))
	for _, coordinate := range coordinates {
		rows = append(rows, deriveRequirement(coordinate, seed, capabilities))
	}
	return rows
}

func deriveRequirement(coordinate RequirementCoordinate, seed ObligationSeed, capabilities []FactCapability) DerivedRequirement {
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
		row.Step = step
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
	row.Quantifier = quantifierForCardinality(len(kinds))
	return row
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
