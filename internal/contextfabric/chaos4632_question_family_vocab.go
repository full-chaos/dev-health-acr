package contextfabric

import (
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4632 (S2 of the CHAOS-4452 intent-engine design, §3/§4) question
// family vocabulary. SHADOW ONLY, and shadow in the strong sense the
// CHAOS-3900 W0 slice established in chaos3900_window_vocab.go: every type
// in this file and its siblings is defined DIRECTLY in package
// contextfabric with ZERO wire-contract surface. Nothing here appears in
// internal/contracts/v1, in contracts/jsonschema, in the OpenAPI document,
// or in the MCP manifest, and therefore nothing here can reach ask-dev's
// additionalProperties:false validator (CHAOS-4623) or require a two-step
// deploy.
//
// WHY THE SHADOW IS THE POINT, not a staging convenience. The design's
// §9-S2 gate cell is explicit that the largest assumption in the whole
// intent-engine design -- that a model will emit a SEMANTICALLY CORRECT
// GroupKind and scope anchor, and will NOT emit them on questions that
// have neither -- is unmeasured, and that it must be falsified BEFORE any
// contract moves:
//
//	"Cheaper falsification available first: capture both fields
//	 receipt-only, without widening the public interpretation, following
//	 the WindowClass W0 shadow precedent -- so the assumption is tested
//	 before any contract moves."
//
// So the two model-emitted signals this vocabulary keys on ride
// ModelExecutionReceipt (model_runtime.go), never InterpretedQuestion --
// which is a type ALIAS to contractsv1.ContextFabricInterpretedQuestion
// (model.go:299), i.e. adding a field to it IS the wire widening. The
// promotion to the public interpretation is a separate change, justified
// by the measurement this slice makes possible, exactly as W1 promoted
// W0's window fields.
//
// NOTHING IS GATED ON ANY OF THIS. The family is resolved, recorded and
// telemetered; no answer, plan, offer, render selection, or clarification
// changes because of it. Zero behaviour change is a required, provable
// property of this slice, and the comparison projection in §9 of the
// design is how it is proved.

// QuestionFamily is the closed vocabulary of question families (design
// §3). It does NOT replace InvestigationShape: Shape stays as the coarse
// structural signal and becomes one of the inputs a family is validated
// against (§4.2), so nothing that reads Shape today changes meaning.
//
// EIGHT members, not the nine an earlier revision of §3 tabulated:
// decision D1 (§10, DECIDED by the orchestrator 2026-08-30, option A)
// merged subject_status and subject_drivers into a single
// subject_investigation. The merge is not a simplification for its own
// sake -- the two had IDENTICAL conditions in the precedence table, so the
// gate could not discriminate them, and the repository's own acceptance
// case is `status_and_drivers` (acceptance_test.go:157-169), i.e. one
// question wanting both. Splitting them again later is one new row in this
// vocabulary plus one new precedence row, which is what a closed
// vocabulary in a table is FOR.
type QuestionFamily string

const (
	// QuestionFamilySubjectInvestigation is "what is the status of X?" and
	// "why is X struggling?" -- one named subject, current-state facts,
	// drivers ATTEMPTED but never required (decision D1's cost, accepted
	// with eyes open: North Star check 8's "never a bare score" moves from
	// a plan-level guarantee to a harness acceptance case).
	QuestionFamilySubjectInvestigation QuestionFamily = "subject_investigation"
	// QuestionFamilyDiscoveredCohortRanking is "which teams are
	// struggling, and why?" -- many subjects, discovered, window is the
	// ONLY applicable structure axis. This is exactly what CHAOS-4579
	// shipped as a filter applied after the fact; S4 derives it from this
	// family instead.
	QuestionFamilyDiscoveredCohortRanking QuestionFamily = "discovered_cohort_ranking"
	// QuestionFamilyScopedCohortStatus is "what are the statuses of the
	// fullchaos team's projects?" (acceptance question Q-B) -- many
	// subjects scoped by ONE named parent of a DIFFERENT kind. Never a
	// single-subject pick: the named term is the scope, not the answer's
	// subject, and that asymmetry is precedence row 2.
	QuestionFamilyScopedCohortStatus QuestionFamily = "scoped_cohort_status"
	// QuestionFamilyGroupedCohortStatus is "what are the project statuses
	// for each team, and what are the main drivers?" (acceptance question
	// Q-A) -- many subjects partitioned by a grouping kind.
	QuestionFamilyGroupedCohortStatus QuestionFamily = "grouped_cohort_status"
	// QuestionFamilyExplicitComparison is "compare X to Y over 90 days" --
	// two or more named subjects, matched fact set on BOTH sides, same
	// window, same measures. Never one-sided.
	QuestionFamilyExplicitComparison QuestionFamily = "explicit_comparison"
	// QuestionFamilyTrend is "how has X's cycle time moved?" -- needs a
	// fact declared time_series, which is S3's declaration work. DECLARED
	// AND DELIBERATELY UNREACHABLE from the precedence table in this
	// slice; see UnreachableQuestionFamilies below for why that is a
	// deliberate property with a precedent rather than dead code.
	QuestionFamilyTrend QuestionFamily = "trend"
	// QuestionFamilyInvestmentAllocation is "where did our effort go?" --
	// investment facts with a declared breakdown table. Declared and
	// deliberately unreachable in this slice, same as trend.
	QuestionFamilyInvestmentAllocation QuestionFamily = "investment_allocation"
	// QuestionFamilyUnclassified is the REFUSE-TO-GUESS member and the
	// vocabulary's own fallback. It is never a failure and never an
	// error: it is today's behaviour, unchanged, and it is what a split
	// consensus resolves to rather than breaking a tie by picking a side.
	// All axes are applicable to it, because nothing has been established
	// that could narrow them.
	QuestionFamilyUnclassified QuestionFamily = "unclassified"
)

// questionFamilies is the closed vocabulary in published order. The order
// is part of the contract of QuestionFamilyVocabulary and is relied on by
// the registry test's exhaustiveness assertion; append, never reorder --
// the same discipline ContextFabricStructureNeedKindVocabulary's own doc
// comment states for its late-added subject_candidate member.
var questionFamilies = [...]QuestionFamily{
	QuestionFamilySubjectInvestigation,
	QuestionFamilyDiscoveredCohortRanking,
	QuestionFamilyScopedCohortStatus,
	QuestionFamilyGroupedCohortStatus,
	QuestionFamilyExplicitComparison,
	QuestionFamilyTrend,
	QuestionFamilyInvestmentAllocation,
	QuestionFamilyUnclassified,
}

// QuestionFamilyCount is the closed vocabulary's size.
const QuestionFamilyCount = len(questionFamilies)

// QuestionFamilyVocabulary returns the closed vocabulary in its fixed,
// published order. An array return, copied on every call, per
// WindowClassVocabulary's own precedent.
func QuestionFamilyVocabulary() [QuestionFamilyCount]QuestionFamily {
	return questionFamilies
}

// ValidQuestionFamily reports whether value is a member of the closed
// vocabulary. The EMPTY value is deliberately NOT valid -- callers that
// treat "unset" as legal handle it explicitly, exactly as
// ValidWindowClass's own doc comment requires of its callers.
func ValidQuestionFamily(value QuestionFamily) bool {
	for _, member := range questionFamilies {
		if member == value {
			return true
		}
	}
	return false
}

// SanitizeQuestionFamily applies the same sanitize-before-validate control
// flow SanitizeWindowClass established for CHAOS-3900 W0 (design §4.1's
// "per sample: SanitizeQuestionFamily -- unknown string -> \"\" (unset),
// never an error"). An unrecognized value becomes the empty family plus
// unrecognized=true; it is NEVER a validation failure, because a model
// inventing a family name must not be able to reject an entire
// interpretation that is otherwise sound.
func SanitizeQuestionFamily(raw string) (family QuestionFamily, unrecognized bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	candidate := QuestionFamily(trimmed)
	if !ValidQuestionFamily(candidate) {
		return "", true
	}
	return candidate, false
}

// SanitizeGroupKind closes GroupKind against the EXISTING
// ContextFabricSubjectKind registry rather than inventing a parallel
// grouping vocabulary. Anything outside that registry becomes unset plus
// unrecognized=true, on the same never-fail-the-interpretation rule as
// SanitizeQuestionFamily.
//
// This is the field precedence row 1 fires on ALONE, which is exactly why
// the design's gate measures FALSE emission and not only correct emission:
// a model that spuriously emits a group kind on a plain single-subject
// status question routes that question to a grouped cohort. Closing the
// vocabulary bounds what a wrong value can BE; it cannot make a wrong
// value right, and no amount of sanitization substitutes for the labelled
// negative cases.
func SanitizeGroupKind(raw string) (kind SubjectKind, unrecognized bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	candidate := SubjectKind(trimmed)
	if !contractsv1.ValidContextFabricSubjectKind(candidate) {
		return "", true
	}
	return candidate, false
}

// ScopeAnchorTermMaxBytes bounds ScopeAnchorTerm. It matches the bound the
// model-facing contract already applies to the SubjectTerms this field is
// structurally identical to -- see SanitizeScopeAnchorTerm.
const ScopeAnchorTermMaxBytes = 200

// SanitizeScopeAnchorTerm trims and bounds a raw scope-anchor term.
//
// UNLIKE GroupKind, this is a FREE STRING and cannot be closed against any
// vocabulary -- the design says so in as many words, correcting an earlier
// draft that claimed both fields were closed-vocabulary sanitized (§4.2,
// "On the two new model-emitted fields -- corrected"). The defensible
// framing is narrower and is the one this implementation holds itself to:
//
//	ScopeAnchorTerm is a RETRIEVAL POINTER, NOT A VALUE.
//
// It is structurally identical to the SubjectTerms the model already emits
// and the graph already resolves. NOTHING downstream branches on its text.
// The only thing that ever branches is WHETHER IT RESOLVED, and to what --
// and in this shadow slice not even that, since nothing is gated at all.
// An unresolvable anchor is a scope_anchor clarification with real
// candidates (S4/S5's work), never a guess and never a silent fallback to
// a different family.
//
// Over-long input is TRUNCATED rather than rejected, for the same reason
// an out-of-vocabulary GroupKind sanitizes to unset rather than failing:
// this runs strictly after interpreted.Validate() has already succeeded,
// and a shadow capture must never become a new way for an otherwise valid
// interpretation to fail. Truncation is reported so it is countable rather
// than silent.
func SanitizeScopeAnchorTerm(raw string) (term string, truncated bool) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) <= ScopeAnchorTermMaxBytes {
		return trimmed, false
	}
	// Truncate on a rune boundary: a byte-sliced multi-byte rune would
	// put invalid UTF-8 into a telemetry field and a durable receipt.
	cut := ScopeAnchorTermMaxBytes
	for cut > 0 && !utf8RuneStart(trimmed[cut]) {
		cut--
	}
	return strings.TrimSpace(trimmed[:cut]), true
}

// utf8RuneStart reports whether b is the first byte of a UTF-8 rune (i.e.
// not a 10xxxxxx continuation byte).
func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }
