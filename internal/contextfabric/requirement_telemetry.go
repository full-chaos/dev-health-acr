package contextfabric

// Telemetry for the obligation -> requirement derivation. Design §13.15's
// telemetry bar: rows, cells, and unavailable reasons as CLOSED TOKENS.
//
// WHY THE COUNTS ARE FIXED-LENGTH ARRAYS OVER THE CLOSED VOCABULARIES
// RATHER THAN MAPS. Two reasons, both of them defects this program has
// already paid for:
//
//  1. A map produces a different key order on every run, so two runs of one
//     frame do not produce a diffable row -- the property every other event
//     in this package is careful to have.
//  2. A map omits the members that counted zero, and an omitted zero is
//     indistinguishable from a tier that never ran. That is the "a gate tier
//     with no positive fixture can be dead for its whole life and read as
//     green" failure, applied to telemetry: an operator seeing no
//     `table_shape_undeclared` key cannot tell whether no cell hit that
//     cause or whether the classifier never reached it. Every closed token
//     gets a key on every event, so a zero is an OBSERVED zero.
//
// The arrays are indexed by vocabulary position, so a member added to a
// vocabulary changes the array length and every read of it fails to
// compile -- which is the same "a silent sink is a compile error"
// discipline FrameValidationTelemetry adopted.

// RequirementDerivationVersion is the derivation-table version this build
// implements: the role table in subject_role.go crossed with the
// completion rules in requirement_derivation.go.
//
// It is bumped whenever a role rule, a completion scope, a quantifier rule
// or an unavailable-reason classification changes MEANING, on the same
// ground as QuestionFrameVersion and RankingFormulaVersion: a persisted
// receipt must be replayable against the table that produced it, and two
// rows derived under different tables are not comparable.
const RequirementDerivationVersion = "requirement-derivation.v1"

// RequirementDerivationSummary is the requirement layer's telemetry row.
//
// COUNTS AND CLOSED TOKENS ONLY. No subject label, no canonical id, no
// question text, no fact-kind list -- the same rule frame_telemetry.go
// holds itself to. The cells are countable and their causes are named;
// WHICH team was unserved is not a telemetry question.
type RequirementDerivationSummary struct {
	// Derived is every requirement row the frame produced -- the
	// denominator. Served + Unserved always equals it, and the identity is
	// asserted rather than assumed.
	Derived int
	// Served is the rows that name a fact kind or a server step.
	Served int
	// Unserved is the rows that name an unavailable reason.
	Unserved int

	// UnavailableCells counts unserved cells per reason, indexed by
	// RequirementUnavailableReasonVocabulary position.
	UnavailableCells [RequirementUnavailableReasonCount]int
	// Quantifiers counts rows per completion quantifier, indexed by
	// CompletionQuantifierVocabulary position.
	Quantifiers [CompletionQuantifierCount]int
	// Roles counts rows per subject role, indexed by
	// SubjectRoleVocabulary position.
	Roles [SubjectRoleCount]int

	// Version is RequirementDerivationVersion, so an event can be read
	// against the table that produced it.
	Version string
}

// RequirementDerivationSummaryFrom folds a frame's rows into the telemetry
// row.
//
// Total over its input and safe on nil: a frame that produced no rows
// yields a zeroed summary CARRYING THE VERSION, so "the derivation ran and
// found nothing to require" is distinguishable from "the derivation never
// ran" (whose summary has an empty Version). That distinction is the whole
// reason Version is set unconditionally here rather than only when there
// are rows.
func RequirementDerivationSummaryFrom(rows []DerivedRequirement) RequirementDerivationSummary {
	summary := RequirementDerivationSummary{Version: RequirementDerivationVersion}
	summary.Derived = len(rows)
	for _, row := range rows {
		if row.Served() {
			summary.Served++
		} else {
			summary.Unserved++
			if index, ok := unavailableReasonIndex(row.Unavailable); ok {
				summary.UnavailableCells[index]++
			}
		}
		if index, ok := quantifierIndex(row.Quantifier); ok {
			summary.Quantifiers[index]++
		}
		if index, ok := subjectRoleIndex(row.Role); ok {
			summary.Roles[index]++
		}
	}
	return summary
}

// Balanced reports whether Served + Unserved accounts for every derived
// row.
//
// A row that is neither -- which an out-of-vocabulary reason token or a
// future third row state would produce -- is exactly the kind of silent
// loss the render-shape accounting field exists to catch elsewhere in this
// package, so the same positive statement is available here.
func (s RequirementDerivationSummary) Balanced() bool {
	return s.Served+s.Unserved == s.Derived
}

func unavailableReasonIndex(value RequirementUnavailableReason) (int, bool) {
	for index, member := range requirementUnavailableReasons {
		if member == value {
			return index, true
		}
	}
	return 0, false
}

func quantifierIndex(value CompletionQuantifier) (int, bool) {
	for index, member := range completionQuantifiers {
		if member == value {
			return index, true
		}
	}
	return 0, false
}

func subjectRoleIndex(value SubjectRole) (int, bool) {
	for index, member := range subjectRoles {
		if member == value {
			return index, true
		}
	}
	return 0, false
}

// RequirementDeriver is the port RuntimeQuestionInterpreter derives
// requirement rows through.
//
// AN EXPLICITLY-WIRED FIELD, never a type assertion on something else --
// the discipline FamilyTelemetry and FrameTelemetry beside it already
// follow, for the recorded reason that CommitAffirmationTelemetry was
// optional, nothing in production implemented it, every event failed its
// type assertion and the whole signal disappeared with tests passing
// throughout. *FactCapabilityRegistry implements this, and hosted/open.go
// wires the registry it already builds.
//
// It is an interface rather than the concrete registry so that the
// interpreter does not gain a dependency on fact reading -- it needs the
// DECLARATIONS, not the reader.
type RequirementDeriver interface {
	// DeriveRequirements returns the requirement rows a validated frame
	// demands, against the declarations this registry holds.
	DeriveRequirements(frame QuestionFrame) []DerivedRequirement
}

// DeriveRequirements implements RequirementDeriver against the registry's
// own declarations.
//
// The seed is GENERATED on each call rather than cached at construction.
// That is deliberate: the capability set is immutable after construction,
// so a cache would be safe, but it would be a SECOND COPY of derived state
// living beside the thing it is derived from -- the exact shape that let a
// hand table and its artifact drift apart through two review rounds of the
// declaration slice. Capabilities() already returns deep copies, the
// registry holds at most a few dozen of them, and this runs once per
// interpret call.
func (r *FactCapabilityRegistry) DeriveRequirements(frame QuestionFrame) []DerivedRequirement {
	if r == nil {
		return nil
	}
	capabilities := r.Capabilities()
	return DeriveRequirements(frame, GenerateObligationSeed(capabilities), capabilities)
}
