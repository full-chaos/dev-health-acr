package graphrank

import (
	"regexp"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CensusKind is CHAOS-3896/3899's closed census-kind registry: the subject
// kinds the Slice-A shadow evidence round may census (design brief v5
// §1.2/§1.3 -- "closed registry: slice 1: the 4 stall kinds"). A type alias
// of contextfabric.SubjectKind, not a new type, so a census kind is
// interchangeable with every other subject-kind-typed value in this
// package without conversion.
type CensusKind = contextfabric.SubjectKind

// The closed, slice-1 census-kind registry (brief §0/§1.3(4)): the four
// stall kinds with existing source tables (ci_pipeline_runs,
// git_pull_requests, git_pull_request_reviews, work_items). Growable in
// principle (brief §9 risk 3, §6 Slice E), but adding a kind here alone is
// NOT sufficient -- it also needs a devhealthsource census registry entry
// (chaos3899_census_registry.go) and, for a decisive handle class, a
// handleGrammarRegistry entry below; IsCensusKindRegistered is the single
// gate every caller must consult before treating an unrecognized kind as
// censusable (brief §4 census_kind_unregistered).
var censusKindRegistry = map[CensusKind]bool{
	contextfabric.SubjectPullRequest:                  true,
	contextfabric.SubjectWorkItem:                     true,
	contractsv1.ContextFabricSubjectCIRun:             true,
	contractsv1.ContextFabricSubjectPullRequestReview: true,
}

// IsCensusKindRegistered reports whether kind is in the closed slice-1
// census-kind registry. A hypothesis kind outside this registry is never
// censused -- the round refuses decisive outcomes for it
// (census_kind_unregistered, brief §1.2/§4) and its pooled hypotheses (if
// any) block no_match rather than being silently ignored (brief §3(2)).
func IsCensusKindRegistered(kind CensusKind) bool {
	return censusKindRegistry[kind]
}

// DegradationReason is CHAOS-3896's closed shadow/degradation vocabulary
// (design brief v5 §4). Every reason the shadow evidence round can refuse a
// decisive outcome for, or demote one, is one of these closed values --
// never a free string -- so a caller can branch on it exactly like every
// other closed-vocabulary contract type in this codebase.
type DegradationReason string

const (
	ReasonNoDiscriminators          DegradationReason = "no_discriminators"
	ReasonHistoricalAxisSkip        DegradationReason = "historical_axis_skip"
	ReasonScopedVisibility          DegradationReason = "scoped_visibility"
	ReasonHandleGrammarUnbound      DegradationReason = "handle_grammar_unbound"
	ReasonAnchorNotUnique           DegradationReason = "anchor_not_unique"
	ReasonCensusKindUnregistered    DegradationReason = "census_kind_unregistered"
	ReasonMultiHandle               DegradationReason = "multi_handle"
	ReasonJoinedColumnDiscriminator DegradationReason = "joined_column_discriminator"
	ReasonCensusClosureMismatch     DegradationReason = "census_closure_mismatch"
	ReasonCensusOverBudget          DegradationReason = "census_over_budget"
	ReasonCensusError               DegradationReason = "census_error"
	ReasonGraphMissingSatisfier     DegradationReason = "graph_missing_satisfier"
	ReasonProbeError                DegradationReason = "probe_error"
	ReasonBudgetExhausted           DegradationReason = "budget_exhausted"
	// ReasonKindSensitiveOutcome (CHAOS-3972 P3, pivot-intent design brief
	// §2.0/§4: "kind_sensitive_outcome") demotes a would_commit/would_no_match
	// outcome reached under EXPLICIT (non-receipt) kind narrowing when the
	// §2.0 kind-insensitivity proof, re-run over the pre-narrowing kind
	// set, does not agree -- the beside-window_sensitive_outcome sibling
	// the brief names explicitly. See kindInsensitivityProof
	// (chaos3900_structure_offers.go) for the proof itself, and
	// RunShadowEvidenceRound's own decisive switch for where it is
	// consulted.
	ReasonKindSensitiveOutcome DegradationReason = "kind_sensitive_outcome"
	// ReasonAnchorCollision is CHAOS-3898 S3's addition (design brief v4.1
	// §1.4: "anchor_collision typed non-decisive census outcome at BIND
	// time"), not yet in 3896 brief v6's own §4 vocabulary table -- 3898
	// exposes the surface (this reason plus
	// devhealthsource.AnchorCollision's detection primitive); a future
	// 3896 Slice B/C consumes both to refuse a project-anchored round
	// whose raw source id resolves to more than one provider, distinct
	// from ReasonAnchorNotUnique (claimant-COUNT ambiguity at the graph
	// alias-lookup layer -- a different failure class from a
	// provider-collided SOURCE id).
	ReasonAnchorCollision DegradationReason = "anchor_collision"
)

// BoundHandle is one grammar-bound handle term: the CLOSED registry entry
// that matched, the kind it maps to, the grammar-extracted VALUE (the
// literal to equality-match at the source -- e.g. "532", "CHAOS-3896", a
// run-id token), and the EXACT source span against request.Question (brief
// §1.2's "verbatim spans" -- R3). The span/Value pair is in-process
// provenance ONLY: neither this struct nor anything derived from it may
// ever reach a ResolutionTraceEvent field (corpus-safety rule, resolve.go's
// ResolutionTracer doc comment) -- a trace may carry counts/enums/hashes
// derived from a BoundHandle, never the handle text itself.
type BoundHandle struct {
	Kind      CensusKind
	Grammar   string // the registry entry's own fixed name -- safe to trace, never derived from question text
	Value     string
	SpanStart int
	SpanEnd   int
}

type handleGrammarEntry struct {
	name       string
	kind       CensusKind
	pattern    *regexp.Regexp
	valueGroup int // 0 = whole match is the value; N>0 = capture group N
	// valuePattern (CHAOS-3900 P1.E) matches the CAPTURED VALUE alone --
	// e.g. \d+ for a bare PR number, with no surrounding "PR #" context --
	// distinct from pattern, which matches the value IN its original
	// question-text CONTEXT (literal keyword + \b boundaries).
	// ValidateHandleGrammar (redemption-time re-validation) only ever has
	// the bare stored value, never the original question text, so it
	// needs this narrower, context-free shape rather than re-running
	// pattern against a value it was never anchored to match alone.
	valuePattern *regexp.Regexp
	// sourceColumn (CHAOS-3900 P1.C') is "the keyed source column [this
	// handle] would bind" (design brief §2.1's own HandleOption
	// description) -- disclosed on the wire so a caller can see what a
	// confirmed handle would check, without exposing internal SQL alias
	// details. "<base table>.<column>" form, a CLOSED STATIC MIRROR of
	// devhealthsource's own chaos3899_census_registry.go
	// censusKindRegistryEntries[kind].handlePredicate -- devhealthsource
	// imports graphrank (for CensusFunc), so graphrank cannot read that
	// registry directly (the same import-direction wall as every other
	// contextfabric/graphrank/devhealthsource seam this epic works
	// around). Team-lead ruling (P1.C' Q2): duplicate rather than plumb a
	// new dependency-injection surface for closed, static registry data --
	// TestHandleSourceColumnRegistryMatchesCensusRegistry
	// (devhealthsource, since it already imports graphrank) is the parity
	// test that turns drift into a named, failing test instead of a
	// silent mismatch.
	sourceColumn string
}

// handleGrammarRegistry is the CLOSED handle->kind registry (design brief
// v5 §1.2/§8: "3 handle patterns"). Every pattern anchors on \b (Go RE2
// word-boundary) around the token it binds, giving MAXIMAL-MUNCH,
// WORD-BOUNDARY semantics (R3): \d+ is greedy, and a \b immediately after
// the last consumed digit means a longer run of digits is *always* matched
// in full before the boundary is satisfied -- "PR 532" can never bind
// handle "PR 53" (the boundary after "53" inside "532" is not a word
// boundary at all, since '3' follows immediately; RE2's greedy \d+ has
// already consumed to "532" before \b is even evaluated). This is a
// property of the regex engine, not application logic, which is exactly
// why the R3 fix is structural rather than a follow-on substring guard.
//
// An unrecognized handle shape, or a handle-shaped term whose kind mapping
// is not in this closed registry, contributes NO discriminator -- BindHandles
// only ever returns handles this registry explicitly maps; there is no
// "matched something, don't know what" case to fall through to
// handle_grammar_unbound from inside this file (Slice A's minimal shadow
// interpretation -- see BindHandles' own doc comment for the residual this
// leaves).
//
//   - pull_request_number: "PR 532", "PR#532", "pr 532", "pull request
//     532", "pull request #532" -> pull_request, Value is the bare digits
//     ("532"). The spelled-out "pull request" alternative (CHAOS-4012
//     widening) is grounded in the kind's own name (ContextFabricSubjectPullRequest)
//     and base table (git_pull_requests, chaos3899_census_registry.go) --
//     not a guessed synonym -- and keeps the SAME \b(...)\s*#?\s*(\d+)\b
//     numeric-suffix shape and word-boundary discipline the "PR" form
//     already used, so it carries no new false-positive risk: "pull
//     request" is not a substring any ordinary English sentence produces
//     immediately followed by a bare number the way "PR" alone could.
//   - work_item_ticket_key: "CHAOS-3896" -> work_item, Value is the WHOLE
//     matched ticket key ("CHAOS-3896") -- pinned as the exact source-form
//     equivalent of embed_fields.go's ticketKeyAlias inversion; see
//     devhealthsource's chaos3899_census_registry.go and its cross-test
//     against ticketKeyAlias directly. CHAOS-4012 investigated widening
//     this prefix past the literal "CHAOS-" (embed_fields.go's own
//     ticketKeyAlias doc comment cites "jira:ABC-1" as an equally valid
//     stored shape, and "the rule never assumes the prefix set") but
//     DEFERRED it: a real org's own configured ticket-key prefix is
//     org-specific data this static, compile-time registry has no way to
//     read, and relaxing the prefix to a generic uppercase-letter class
//     (e.g. "[A-Z]{2,10}-\d+") would match ordinary capitalized-hyphenated
//     English no org ever intended as a ticket key (COVID-19, PG-13) --
//     exactly the false-discriminator class the ci_run_id "running" fix
//     below exists to prevent. Widening this entry needs org-scoped
//     registry data, not a broader static regex; left as a follow-up.
//   - ci_run_id: "run 18234567", "CI run #18234567", "pipeline 18234567",
//     "CI pipeline #18234567", "pipeline run 18234567" -> ci_pipeline_run,
//     Value is the bound digits. The "pipeline"/"CI pipeline"/"pipeline
//     run" alternatives (CHAOS-4012 widening) are grounded in the base
//     table's own name (ci_pipeline_runs, chaos3899_census_registry.go) --
//     GitLab's own product terminology for this concept ("pipeline") is a
//     confirmed live provider on this table (providerQualifiedNamePattern,
//     chaos3899_source_native_grammar.go, matches gitlab.com repo slugs),
//     not a guessed synonym. Keyword-anchored AND digit-shaped: a bare
//     word after any of these keywords (as in "running", "run break",
//     "run analysis", "pipelining", "pipeline break") must NOT bind -- an
//     earlier version of the ORIGINAL "run" pattern accepted any
//     alphanumeric token here and, lacking a word boundary between the
//     keyword and the token, matched ordinary English ("who is running
//     the deploy pipeline?" bound the literal substring "ning" as a handle
//     VALUE, minting a false census predicate and, with no other keyed
//     discriminator surviving, a structurally false would_no_match --
//     exactly the class D0/§1.2/§3 forbid). The fix is two-part, extended
//     to every keyword alternative here: \b immediately after each keyword
//     (so "running"'s/"pipelining"'s own keyword prefix can never satisfy
//     it -- the very next character is a word character, not a boundary)
//     and \d{4,} instead of an alphanumeric class (so "run break"/"run
//     CHAOS-3896"/"run 532" all fail to bind here -- a ticket key or PR
//     number can never collide with this pattern, restoring the
//     disjointness the other two entries already have by construction).
var handleGrammarRegistry = []handleGrammarEntry{
	{name: "pull_request_number", kind: contextfabric.SubjectPullRequest, pattern: regexp.MustCompile(`(?i)\b(?:PR|pull\s+request)\s*#?\s*(\d+)\b`), valueGroup: 1, valuePattern: regexp.MustCompile(`^\d+$`), sourceColumn: "git_pull_requests.number"},
	{name: "work_item_ticket_key", kind: contextfabric.SubjectWorkItem, pattern: regexp.MustCompile(`\bCHAOS-\d+\b`), valueGroup: 0, valuePattern: regexp.MustCompile(`^CHAOS-\d+$`), sourceColumn: "work_items.work_item_id"},
	{name: "ci_run_id", kind: contractsv1.ContextFabricSubjectCIRun, pattern: regexp.MustCompile(`(?i)\b(?:CI\s+pipeline|CI\s+run|pipeline\s+run|pipeline|run)\b\s*#?\s*(\d{4,})\b`), valueGroup: 1, valuePattern: regexp.MustCompile(`^\d{4,}$`), sourceColumn: "ci_pipeline_runs.run_id"},
}

// ValidateHandleGrammar (CHAOS-3900 P1.E) re-validates a STORED handle
// VALUE against its own named pattern -- redemption-time re-verification's
// counterpart to BindHandles' derive-side extraction (design brief §2.1:
// "redemption re-validates the value against the registry grammar"). Never
// widens BindHandles' own signature (team-lead ruling): a parallel
// function, beside it, in the same file, sharing only the registry data.
//
// Matches against valuePattern, NOT pattern: pattern is anchored to the
// value's ORIGINAL QUESTION-TEXT CONTEXT (a literal keyword + word
// boundaries), which a bare stored value was never meant to satisfy alone
// -- e.g. pull_request_number's pattern requires a "PR" literal prefix,
// but its OWN captured value is just digits ("532"). kind and patternID
// together select ONE registry entry (kind alone is not enough: a future
// entry could share the disjoint-token-shape kind with another, though the
// current 3-entry registry does not); an id/kind pair matching no entry is
// invalid, not silently permissive.
func ValidateHandleGrammar(kind CensusKind, patternID string, value string) bool {
	for _, entry := range handleGrammarRegistry {
		if entry.kind == kind && entry.name == patternID {
			return entry.valuePattern.MatchString(value)
		}
	}
	return false
}

// HandleSourceColumn (CHAOS-3900 P1.C') returns the closed registry's
// "<table>.<column>" a grammar-bound handle would check, for the SAME
// (kind, patternID) pair ValidateHandleGrammar keys on -- see
// handleGrammarEntry.sourceColumn's own doc comment for the mirror/parity
// discipline this backs. ok=false for an unrecognized pair, matching
// ValidateHandleGrammar's own "not silently permissive" rule.
func HandleSourceColumn(kind CensusKind, patternID string) (string, bool) {
	for _, entry := range handleGrammarRegistry {
		if entry.kind == kind && entry.name == patternID {
			return entry.sourceColumn, true
		}
	}
	return "", false
}

// BindHandles applies the closed handle grammar to question (verbatim
// request.Question -- brief §1.2) and returns every bound handle, each
// carrying its exact source span. Overlap between two DIFFERENT registry
// patterns is not deduplicated (the registry's three patterns target
// disjoint token shapes by construction: a PR-number match requires a "PR"
// literal, a ticket key requires "CHAOS-", a run id requires "run" --
// none can occur inside another), and a single pattern's own matches
// within one question are already necessarily non-overlapping (Go's
// regexp.FindAllStringSubmatchIndex never returns overlapping matches for
// one pattern).
//
// RESIDUAL (Slice A's minimal shadow-side interpretation, noted per the
// brief's own "build the minimal shadow-side version and note it"
// discipline): this function only ever reports handles the closed registry
// recognizes; it does not attempt to detect a "handle-shaped but
// unregistered" term (e.g. a "TICKET-123" shape that looks like it wants to
// be a ticket key but isn't the CHAOS- prefix) and tag it
// ReasonHandleGrammarUnbound. That distinction needs a second, broader
// "looks like a handle" heuristic the brief does not pin a grammar for;
// building one is reach, not required for this slice's shadow measurement,
// and a broader heuristic risks false positives worse than simply reporting
// "zero handles bound" (which already correctly refuses handle-based
// decisive outcomes). ReasonHandleGrammarUnbound stays in the closed
// vocabulary for future use.
func BindHandles(question string) []BoundHandle {
	var bound []BoundHandle
	for _, entry := range handleGrammarRegistry {
		locations := entry.pattern.FindAllStringSubmatchIndex(question, -1)
		for _, loc := range locations {
			start, end := loc[0], loc[1]
			if entry.valueGroup > 0 {
				groupIndex := entry.valueGroup * 2
				if groupIndex+1 >= len(loc) || loc[groupIndex] < 0 {
					continue
				}
				start, end = loc[groupIndex], loc[groupIndex+1]
			}
			bound = append(bound, BoundHandle{
				Kind: entry.kind, Grammar: entry.name, Value: question[start:end],
				SpanStart: loc[0], SpanEnd: loc[1],
			})
		}
	}
	return bound
}

// IsMultiHandle reports whether bound names a multi-subject shape (brief
// §1.2's adopted multi-handle rule, v5 §11 addendum): TWO OR MORE
// grammar-bound handles in one question -- same kind or different --
// refuses decisive outcomes (ReasonMultiHandle), never silently picks one.
func IsMultiHandle(bound []BoundHandle) bool {
	return len(bound) >= 2
}
