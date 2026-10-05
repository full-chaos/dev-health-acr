package v1

import (
	"regexp"
	"strconv"
	"strings"
)

// The retrieval-degradation limitation lives here, in the contract, rather
// than in the engine that writes it (CHAOS-3746, option (a)).
//
// It is written by internal/contextfabric and read by
// internal/contextfabric/answerprojection, and the projection may not
// import the engine: answerprojection is import-pure by constraint --
// standard library and this package, nothing else -- so that both the
// hosted API and the MCP sidecar can call it. TestPackageImportsStayPure
// enforces that, which leaves exactly two options for a string both sides
// must recognise: restate it on the read side, or move it to the contract
// they already share. Restating it is the anchor-drift class this codebase
// keeps closing; the string is part of what an answer MEANS, not an
// implementation detail of how one gets composed.

// ContextFabricRetrievalDegradedLimitation is the fixed, non-interpolated
// limitation an investigation carries when a retrieval mechanism was
// unavailable while the answer was produced.
//
// It names no mechanism, no provider, no model, and no error text. A
// limitation is answer-facing prose, and every cause -- an embed timeout,
// an unreachable embedder, a server that served the wrong model, a
// fenced-off stale index -- has the same consequence for a reader:
// retrieval saw less than it should have. Operator-facing detail belongs in
// telemetry, which already receives it.
//
// The phrasing describes the ANSWER'S PROVENANCE ("when this answer was
// produced"), not the current request, and that is load-bearing rather than
// stylistic: a REUSED answer carries this limitation forward verbatim from
// the run that produced it, and the earlier wording pointed ambiguously at
// the current request in exactly that case.
const ContextFabricRetrievalDegradedLimitation = "One retrieval mechanism was unavailable when this answer was produced, so fewer candidate subjects may have been considered than usual."

// ContextFabricRetrievalDegradedLimitationLegacy is the wording used before
// the phrasing above replaced it.
//
// BOTH STRINGS EXIST IN THE WILD, permanently. A
// ContextFabricInvestigationResult is immutable and CHAOS-3782's answer
// reuse keys on its stored bytes, so results written before the change keep
// this spelling verbatim -- nothing rewrites a stored row, and nothing may
// treat one as malformed.
const ContextFabricRetrievalDegradedLimitationLegacy = "One retrieval mechanism was unavailable for this investigation, so fewer candidate subjects may have been considered than usual."

// IsContextFabricRetrievalDegradedLimitation reports whether a limitation
// string is either spelling.
//
// It exists so no caller compares against ONE constant and silently stops
// recognising answers written by the other.
func IsContextFabricRetrievalDegradedLimitation(limitation string) bool {
	return limitation == ContextFabricRetrievalDegradedLimitation ||
		limitation == ContextFabricRetrievalDegradedLimitationLegacy
}

// HasContextFabricRetrievalDegradedLimitation reports whether any entry is either
// spelling. Declared here beside the strings it scans for, so the contract
// can enforce LimitationsDisplaced's coherence rule without depending on
// the engine that writes it.
func HasContextFabricRetrievalDegradedLimitation(limitations []string) bool {
	for _, limitation := range limitations {
		if IsContextFabricRetrievalDegradedLimitation(limitation) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// The rest of the service-authored disclosure vocabulary (CHAOS-4098)
// ---------------------------------------------------------------------------
//
// These three moved here for the SAME reason the retrieval-degradation
// string above did, applied to a second reader: LimitationsDisplaced's
// coherence rule (validate_context_fabric_result.go) has to know which
// entries in a limitation list are service-authored, and the contract may
// not import the engine that writes them.
//
// WHY THIS MATTERS RATHER THAN BEING TIDINESS. That rule was written when
// retrieval degradation was the ONLY thing that could displace a model
// caveat, so it asks for that one disclosure by name. Three more displacers
// shipped afterwards -- CHAOS-3781's standing historical disclosures,
// CHAOS-4085's commit retraction, and CHAOS-4098's clarification override --
// and none of them updated the rule. Every one of them therefore produces a
// result the validator REJECTS whenever the model returned a full
// limitation list: the displacement is recorded, the disclosure that caused
// it is not the one the rule looks for, and the whole investigation fails
// with ErrInvalidResult. That is the exact defect class CHAOS-3746's
// displacement mechanism was built to prevent, reintroduced by a rule that
// enumerates instead of deriving.
//
// So the rule now asks whether ANY service-authored disclosure is present,
// and this list is the single place that answers it. A fifth disclosure is
// covered by being declared here, not by someone remembering to revisit a
// validator.

// ContextFabricTemporalProjectionLimitation is CHAOS-3781's standing
// historical disclosure: the graph holds only the current projection, so a
// subject deleted at source since the requested time is simply gone.
const ContextFabricTemporalProjectionLimitation = "Subjects deleted at source since the requested time are not recoverable from the projected graph."

// ContextFabricObservedTimeLimitation is CHAOS-3781's observed-time
// substitution disclosure: the caller asked what was KNOWN at a past
// instant and the graph answered from what was TRUE then.
const ContextFabricObservedTimeLimitation = "Observed-time questions cannot be answered on their own terms: no canonical source retains observation history, so this answer reflects what was TRUE at the requested time, not what was KNOWN then."

// ContextFabricCommitRetractionLimitation is CHAOS-4085's disclosure that
// the commit gate identified a candidate subject and declined to stand
// behind it.
const ContextFabricCommitRetractionLimitation = "A candidate subject was identified but not committed: the evidence assembled for it does not support naming it as the answer to this question."

// ContextFabricClientSynthesisAnswer is the answer sentence of a turn on
// which the caller asked to write the answer: the service wrote none.
const ContextFabricClientSynthesisAnswer = "The facts and evidence for this question were read. The service wrote no answer on this turn: the caller asked to write it from the synthesis input."

// ContextFabricClientSynthesisCommitNotAffirmedLimitation discloses that the
// commit gate retracted a candidate subject because no service answer
// affirmed it on a client synthesis turn.
const ContextFabricClientSynthesisCommitNotAffirmedLimitation = "Client synthesis: commit not affirmed. A candidate subject was identified but not committed, because the service wrote no answer on this turn and so nothing affirmed the subject. Confirm the candidate with its receipt on the next turn."

// ContextFabricSynthesisClarificationUnavailableLimitation is CHAOS-4098's
// disclosure that the synthesis step declined to conclude on a path with no
// clarification to offer.
const ContextFabricSynthesisClarificationUnavailableLimitation = "This question could not be answered from the evidence assembled, and no clarification could be offered to narrow it further."

// ContextFabricSynthesisNarrativeWithheldLimitation discloses that the model
// returned no_match for a question whose subject was committed and for which
// the service itself held a cohort or fact outcome. The status served is the
// service's, and the model's narrative was not served.
const ContextFabricSynthesisNarrativeWithheldLimitation = "The written narrative for this question was withheld: the service established a subject and retrieval outcome that the narrative did not reflect, so the status, coverage and members shown are the service's own."

// ContextFabricFactScopeUnexpandedLimitation is CHAOS-4099's disclosure that
// a requested fact family could not be reached from the subject this answer
// is about.
//
// WHY THIS STRING EXISTS AT ALL. Before it, that situation was reported to
// the reader as nothing whatsoever: the fact planner pruned the capability,
// SourcePruned is documented as a PROOF that nothing is missing, and the
// answer went out saying it had found no match -- with no indication that
// the evidence had never been looked for. A project-scoped question about
// pull requests, reviews or metrics is the live case (see
// internal/contextfabric/fact_scope.go's header), and the reader could not
// tell it apart from a project that genuinely has none.
//
// FIXED AND NON-INTERPOLATED, the same discipline every disclosure above
// holds. It names no fact family, no subject kind, no policy and no
// traversal: those are operator-facing detail, and telemetry
// (RecordFactScopeExpansion) receives all of them. What a READER needs is
// the one thing that changes how they should read the answer -- that its
// silence on some evidence is a gap in reach, not a finding of absence.
const ContextFabricFactScopeUnexpandedLimitation = "Some requested evidence could not be reached from the subject of this question, so this answer's silence on it is a limit of what was retrievable rather than a finding that none exists."

// ContextFabricFactScopeActivityProxyLimitation is CHAOS-4099's disclosure
// that some evidence in this answer was gathered by ACTIVITY association
// rather than by ownership.
//
// WHY A SECOND STRING RATHER THAN REUSING THE ONE ABOVE. They say opposite
// things. The unexpanded disclosure says "we could not reach some evidence";
// this one says "we DID reach evidence, by a route that is weaker than it
// looks". A reader who is told only the first would take everything present
// in the answer at face value, which is exactly the misreading this exists
// to prevent.
//
// THE MISREADING IT PREVENTS, CONCRETELY. A "project" here is a
// work-tracking project (Linear-shaped). There is no project-to-repository
// ownership edge in the projected graph. What the traversal establishes is
// that a repository has at least one work item linked to the project -- good
// enough to scope "how is this project's code doing", and NOT a statement
// that the project owns the repository or that its repositories are only
// these. Without this sentence a reader takes a proxy for a roster.
//
// FIXED and non-interpolated like every disclosure here: it names no policy,
// no subject kind and no traversal. Operators get all of that on the
// RecordFactScopeExpansion telemetry stream.
const ContextFabricFactScopeActivityProxyLimitation = "Some evidence in this answer was gathered by association rather than by ownership, so it reflects where related activity was found and may not be the complete or authoritative set."

// ContextFabricFactScopeAttributedPrimaryTeamLimitation is CHAOS-4101's
// disclosure that some evidence in this answer was reached via a team's
// PRIMARY WORK-ITEM ATTRIBUTION -- a computed assignment, not a fact any
// provider asserted.
//
// A THIRD, INDEPENDENT DISCLOSURE, alongside (never instead of) the
// unexpanded and activity-proxy limitations above. All three can be true of
// one answer at once: a team-scoped question can reach some evidence
// directly, some through the activity-proxy chain on solid team footing, and
// some through this weaker footing, while a DIFFERENT requirement on the
// same subject hits a policy that has never been activated at all.
//
// WHY A SEPARATE STRING FROM THE ACTIVITY-PROXY ONE. That disclosure says
// "we reached this by association, not ownership" -- true of every
// team-origin target, since the chain is a work_item BELONGS_TO_REPOSITORY
// hop exactly like the project chain's. This one says something further:
// which TEAM the evidence is associated with is itself Ops' own computed
// guess for some of that evidence, not a claim any provider made. A reader
// told only the first would still read "this team's repositories" as a
// settled fact about team ownership of the association; this sentence is
// what corrects that for the subset of evidence where it applies.
//
// FIXED and non-interpolated, the same discipline every disclosure in this
// file holds: it names no source enum value, no confidence level, no team
// and no policy. RecordFactScopeExpansion's AttributionSourceCounts carries
// the closed-vocabulary source breakdown for an operator; this sentence
// carries only the one thing a reader needs -- that some of what is here was
// attributed by inference, not asserted.
const ContextFabricFactScopeAttributedPrimaryTeamLimitation = "Some evidence in this answer was associated with its team by a computed attribution rather than one directly asserted by a data source, so that association may be imprecise."

// contextFabricGroupingRefusalLimitationPrefix, -Middle and -Suffix are the
// three FIXED segments of the grouping-refusal disclosure. It is the one
// disclosure in this file that is INTERPOLATED, and that is chris's own
// ruling rather than an oversight: a reader told only "the answer is
// presented ungrouped" cannot tell whether the axis they asked for was
// missing or merely different, so the sentence names both kinds. Both are
// closed-vocabulary subject kinds, so it still carries no model text and no
// corpus content -- which is the property the "fixed and non-interpolated"
// discipline elsewhere in this file exists to guarantee, reached by a
// different route.
//
// THE PREFIX IS SHARED with the unplaceable disclosure below, deliberately:
// both are answers to "you asked for a breakdown and did not get one", and a
// reader meeting either should recognise the same opening. What must NOT be
// shared is recognition -- see contextFabricGroupingUnplaceableLimitationSuffix
// for why the two families are kept disjoint by their tails rather than their
// heads, and grouping_limitation_families_test.go for the assertion that they
// never accept each other's sentence.
const (
	contextFabricGroupingRefusalLimitationPrefix = "This question asked for a breakdown by "
	contextFabricGroupingRefusalLimitationMiddle = ", but the available facts group by "
	contextFabricGroupingRefusalLimitationSuffix = ", so the answer is presented ungrouped."
)

// contextFabricGroupingUnplaceableLimitationSuffix is the whole tail of the
// SECOND grouping disclosure: the plan declared a group axis and not one
// member of the answer could be placed on it, because no member's own facts
// named a group at all.
//
// A DIFFERENT DEFECT FROM THE MISMATCH ABOVE, and the reader is owed the
// difference. A mismatch means the source disagreed -- it grouped by
// something, just not the asked-for thing, and naming the two kinds tells the
// reader where to look instead. This one means the source was SILENT: there
// is no other axis to point at, so a sentence naming a second kind would have
// to invent one. Hence ONE interpolated value, not two.
//
// ONE INTERPOLATION, NOT TWO, AND NO COUNT. The number of unplaced members
// rides the telemetry line and CohortGroupingOutcome instead. Interpolating it
// here would force the recogniser below to accept an arbitrary digit run in
// the middle of a service-authored sentence, which widens what a model-authored
// caveat can impersonate -- and undisplaceability is exactly what the registry
// grants. A closed-vocabulary subject kind is a set the model never writes
// into; an integer is not.
//
// WHY THE TAIL AND NOT THE HEAD DISCRIMINATES. This family shares its opening
// with the mismatch family, and both end in the same six words, so neither
// segment alone separates them. The whole tail from ", but" onward is one
// constant precisely so that CutSuffix is a decision: the mismatch parse cuts
// its own shorter suffix and then fails to find its Middle, and this parse
// fails CutSuffix outright on a mismatch sentence. Both directions are pinned.
const contextFabricGroupingUnplaceableLimitationSuffix = ", but none of the items in this answer could be placed under one, so the answer is presented ungrouped."

// ContextFabricGroupingRefusalLimitation composes CHAOS-4636's disclosure
// that a grouped question was answered ungrouped because the planned group
// kind and the fact source's own kind disagree.
//
// THE SOLE COMPOSER, and that matters more here than for a constant. Because
// this string is interpolated it cannot be recognised by the equality check
// every other disclosure uses, so recognition is a PARSE
// (IsContextFabricGroupingRefusalLimitation) over exactly the segments this
// function writes. A second, hand-rolled Sprintf at a call site would produce
// a string the parser might not accept -- and an unrecognised service
// disclosure is silently displaceable, which is the round-3 finding.
func ContextFabricGroupingRefusalLimitation(plannedKind, sourceKind ContextFabricSubjectKind) string {
	return contextFabricGroupingRefusalLimitationPrefix + string(plannedKind) +
		contextFabricGroupingRefusalLimitationMiddle + string(sourceKind) +
		contextFabricGroupingRefusalLimitationSuffix
}

// IsContextFabricGroupingRefusalLimitation reports whether a limitation is
// one ContextFabricGroupingRefusalLimitation could have composed.
//
// WHY A PARSE AND NOT A PREFIX MATCH. Everything that consults the
// service-authored registry is deciding whether a string may be DISPLACED to
// make room for another disclosure. A loose match would let a model-authored
// caveat that merely opens with this wording become undisplaceable and take a
// real caveat's place; a prefix match would do exactly that. So both
// interpolated segments must be members of the CLOSED subject-kind registry,
// which is a set the model never writes into. The empty kind is not a member,
// so a half-composed sentence is not recognised either.
func IsContextFabricGroupingRefusalLimitation(limitation string) bool {
	body, ok := strings.CutPrefix(limitation, contextFabricGroupingRefusalLimitationPrefix)
	if !ok {
		return false
	}
	body, ok = strings.CutSuffix(body, contextFabricGroupingRefusalLimitationSuffix)
	if !ok {
		return false
	}
	plannedKind, sourceKind, ok := strings.Cut(body, contextFabricGroupingRefusalLimitationMiddle)
	if !ok {
		return false
	}
	// Exactly one separator. A second occurrence would mean the kinds are
	// not what Cut returned, and guessing which split was intended is
	// precisely the ambiguity a closed vocabulary lets us refuse instead.
	if strings.Contains(sourceKind, contextFabricGroupingRefusalLimitationMiddle) {
		return false
	}
	return ValidContextFabricSubjectKind(ContextFabricSubjectKind(plannedKind)) &&
		ValidContextFabricSubjectKind(ContextFabricSubjectKind(sourceKind))
}

// ContextFabricGroupingUnplaceableLimitation composes the disclosure that a
// grouped question was answered ungrouped because NOT ONE member could be
// placed on the planned axis.
//
// THE SOLE COMPOSER, for the reason its sibling above states at length: an
// interpolated disclosure is recognised by a PARSE, so a second hand-rolled
// Sprintf at a call site produces a string the parser may reject, and an
// unrecognised service disclosure is silently displaceable by the next
// composer. That is not a hypothetical here -- it is the shipped defect this
// file's own registry comment records.
func ContextFabricGroupingUnplaceableLimitation(plannedKind ContextFabricSubjectKind) string {
	return contextFabricGroupingRefusalLimitationPrefix + string(plannedKind) +
		contextFabricGroupingUnplaceableLimitationSuffix
}

// IsContextFabricGroupingUnplaceableLimitation reports whether a limitation
// is one ContextFabricGroupingUnplaceableLimitation could have composed.
//
// A PARSE, not a prefix match, and for the sharper of the two reasons its
// sibling gives. The loose-match hazard is the same -- a model caveat opening
// with this wording would become undisplaceable and take a real caveat's place
// -- but there is a second hazard here that the mismatch family did not have:
// the two grouping families SHARE their opening words and their closing six.
// A prefix match would therefore not merely admit model text, it would make
// the two service families indistinguishable from each other, so a mismatch
// sentence would read as an unplaceable one and vice versa. Requiring the
// whole tail is what keeps them disjoint.
//
// The empty kind is not a vocabulary member, so a half-composed sentence is
// not recognised either.
func IsContextFabricGroupingUnplaceableLimitation(limitation string) bool {
	body, ok := strings.CutPrefix(limitation, contextFabricGroupingRefusalLimitationPrefix)
	if !ok {
		return false
	}
	plannedKind, ok := strings.CutSuffix(body, contextFabricGroupingUnplaceableLimitationSuffix)
	if !ok {
		return false
	}
	return ValidContextFabricSubjectKind(ContextFabricSubjectKind(plannedKind))
}

// contextFabricGroupReadUnreadLimitationPrefix/-Suffix and
// contextFabricGroupListOverBoundLimitationPrefix/-Suffix are the fixed
// segments of the two group-read disclosures: a grouped answer some of whose
// groups could not be read, and a grouped question whose group list was larger
// than an answer can carry.
//
// KIND-ONLY AND COUNT-FREE, like the grouping disclosures above: the one
// interpolated value is a member of the closed subject-kind vocabulary the
// model never writes into, so recognition is a parse that cannot admit model
// text. Neither sentence shares an opening with the grouping families, so
// no service family can be read as another.
const (
	contextFabricGroupReadUnreadLimitationPrefix    = "Not every "
	contextFabricGroupReadUnreadLimitationSuffix    = " group in this answer could be read, so what it says about those groups rests on their members' evidence alone."
	contextFabricGroupListOverBoundLimitationPrefix = "This question asked for more "
	contextFabricGroupListOverBoundLimitationSuffix = " groups than an answer can carry, so the answer is presented ungrouped."
)

// ContextFabricGroupReadUnreadLimitation composes the disclosure that at least
// one group of a grouped answer was not read: denied by authorization, read
// with nothing returned for it, or not read at all because the group read
// failed, could not be authorized, or could not be composed with the member
// read. THE SOLE COMPOSER: recognition is a parse over exactly these segments.
func ContextFabricGroupReadUnreadLimitation(groupKind ContextFabricSubjectKind) string {
	return contextFabricGroupReadUnreadLimitationPrefix + string(groupKind) + contextFabricGroupReadUnreadLimitationSuffix
}

// IsContextFabricGroupReadUnreadLimitation reports whether a limitation is one
// ContextFabricGroupReadUnreadLimitation could have composed. A parse, not a
// prefix match, and the kind must be a vocabulary member, so neither a model
// caveat opening with these words nor a half-composed sentence is recognised.
func IsContextFabricGroupReadUnreadLimitation(limitation string) bool {
	body, ok := strings.CutPrefix(limitation, contextFabricGroupReadUnreadLimitationPrefix)
	if !ok {
		return false
	}
	kind, ok := strings.CutSuffix(body, contextFabricGroupReadUnreadLimitationSuffix)
	if !ok {
		return false
	}
	return ValidContextFabricSubjectKind(ContextFabricSubjectKind(kind))
}

// ContextFabricGroupListOverBoundLimitation composes the disclosure that a
// grouped question proposed more groups than the contract's group bound, so
// its group axis was refused before any group was read and the answer is
// presented ungrouped. THE SOLE COMPOSER, for the same reason.
func ContextFabricGroupListOverBoundLimitation(groupKind ContextFabricSubjectKind) string {
	return contextFabricGroupListOverBoundLimitationPrefix + string(groupKind) + contextFabricGroupListOverBoundLimitationSuffix
}

// IsContextFabricGroupListOverBoundLimitation reports whether a limitation is
// one ContextFabricGroupListOverBoundLimitation could have composed.
func IsContextFabricGroupListOverBoundLimitation(limitation string) bool {
	body, ok := strings.CutPrefix(limitation, contextFabricGroupListOverBoundLimitationPrefix)
	if !ok {
		return false
	}
	kind, ok := strings.CutSuffix(body, contextFabricGroupListOverBoundLimitationSuffix)
	if !ok {
		return false
	}
	return ValidContextFabricSubjectKind(ContextFabricSubjectKind(kind))
}

// contextFabricRefusalBasisLimitationPrefix/-Middle/-Suffix are the three
// FIXED segments of CHAOS-5442's frame-refusal disclosure, and
// contextFabricFrameInvariantRefusalLimitation is the whole sentence for the
// refusal that has no kind to name.
//
// INTERPOLATED, under the same ruling the grouping-refusal disclosure above
// records: both interpolated values are members of CLOSED vocabularies the
// model never writes into (a subject kind and a refusal basis), so the
// sentence still carries no model text and no corpus content -- the property
// the "fixed and non-interpolated" discipline exists to guarantee, reached by
// the same different route.
//
// WHY IT NAMES THE BASIS TOKEN and not only the kind. The reader of a
// refusal has to be able to join the sentence to the machine field beside it
// (Completeness.RefusalBasis) and to the Info line the server logged, and the
// only thing all three can share is the token. A sentence that described the
// refusal in prose alone would leave the operator correlating an English
// phrase against a snake_case field, which is exactly the translation step
// that makes a disclosure go unread.
//
// WHY THE KIND IS IN THE SENTENCE AND NOT IN THE FIELD. The declared kind is
// what makes the refusal ACTIONABLE -- it is the one thing the asker can
// change about their question. Putting it in the closed field would have
// meant either a cross-product vocabulary (one member per basis-and-kind
// pair) or a second field, and the sentence is the honest home for a value
// that varies per question rather than per decision.
const (
	contextFabricRefusalBasisLimitationPrefix = "This question asked about a population of "
	contextFabricRefusalBasisLimitationMiddle = ", which this service has no way to enumerate, so it was not searched and no canonical facts were read. The server refused this question's frame on the basis "
	contextFabricRefusalBasisLimitationSuffix = "."
)

// ContextFabricFrameInvariantRefusalLimitation is the fixed disclosure for a
// frame refused because it VIOLATED AN INVARIANT rather than because it named
// an unservable population.
//
// FIXED, not interpolated, because there is nothing safe to interpolate: the
// only value that would distinguish one instance from another is the failed
// invariant's name, and that vocabulary is a server-internal validation
// detail -- see ContextFabricRefusalBasisFrameInvariantViolated for why it is
// deliberately not promoted to the wire. The operator reads which invariant
// failed on the frame-validation log line.
const ContextFabricFrameInvariantRefusalLimitation = "The server could not act on this question as stated, because the way it combines its subject and its grouping is not answerable as asked, so no canonical facts were read. Rephrasing the question so it asks for one thing at a time may answer it."

// ContextFabricRefusalBasisLimitation composes the disclosure that a
// question was refused because its declared member kind has no discovery arm.
//
// THE SOLE COMPOSER, for the reason its grouping-refusal sibling states at
// length: an interpolated string cannot be recognised by the equality check
// the fixed disclosures use, so recognition is a PARSE over exactly the
// segments this function writes, and a second hand-rolled Sprintf at a call
// site would produce a string the parser might not accept. An unrecognised
// service disclosure is silently displaceable.
func ContextFabricRefusalBasisLimitation(declaredKind ContextFabricSubjectKind, basis ContextFabricRefusalBasis) string {
	return contextFabricRefusalBasisLimitationPrefix + string(declaredKind) +
		contextFabricRefusalBasisLimitationMiddle + string(basis) +
		contextFabricRefusalBasisLimitationSuffix
}

// IsContextFabricRefusalBasisLimitation reports whether a limitation is one
// ContextFabricRefusalBasisLimitation could have composed.
//
// A PARSE, AND BOTH INTERPOLATED SEGMENTS ARE CHECKED FOR MEMBERSHIP, for the
// reason the grouping recogniser states: everything that consults the
// service-authored registry is deciding whether a string may be DISPLACED,
// and a loose or prefix match would let a model-authored caveat that merely
// opens with this wording become undisplaceable and take a real caveat's
// place. The empty kind and the empty basis are not members, so a
// half-composed sentence is not recognised either.
func IsContextFabricRefusalBasisLimitation(limitation string) bool {
	body, ok := strings.CutPrefix(limitation, contextFabricRefusalBasisLimitationPrefix)
	if !ok {
		return false
	}
	body, ok = strings.CutSuffix(body, contextFabricRefusalBasisLimitationSuffix)
	if !ok {
		return false
	}
	declaredKind, basis, ok := strings.Cut(body, contextFabricRefusalBasisLimitationMiddle)
	if !ok {
		return false
	}
	// Exactly one separator, the same refusal the grouping parse makes:
	// a second occurrence means the two values are not what Cut returned,
	// and guessing which split was intended is precisely the ambiguity a
	// closed vocabulary lets us decline instead.
	if strings.Contains(basis, contextFabricRefusalBasisLimitationMiddle) {
		return false
	}
	return ValidContextFabricSubjectKind(ContextFabricSubjectKind(declaredKind)) &&
		ValidContextFabricFrameRefusalBasis(ContextFabricRefusalBasis(basis))
}

// ContextFabricContinuationContextUnverifiableLimitation is the fixed
// disclosure for a window-only continuation refused because the prior turn's
// semantic context could not be verified
// (ContextFabricRefusalBasisContinuationContextUnverifiable).
//
// FIXED, not interpolated. The only values that would tell one instance from
// another are the prior result's id and the internal reason the carrier failed
// admission, and neither belongs in prose: the id is the caller's own
// reference, and the reason vocabulary is server-internal. Both are on the
// Info decision line an operator reads (referenced_result_id and
// decision_reason, with carrier_read separating an unreadable carrier from one
// read and proved invalid).
//
// IT NAMES THE BASIS TOKEN for the reason the member-kind sentence does: a
// reader joins the sentence to the machine field and to the log line through
// the one value all three share.
const ContextFabricContinuationContextUnverifiableLimitation = "This request continued an earlier answer by confirming only its evidence window, and the server could not verify the earlier answer's reading of the question, so it did not answer under a different reading and no canonical facts were read. Ask the question again without the earlier answer's window offer to start a fresh investigation. The server refused this continuation on the basis continuation_context_unverifiable."

// ContextFabricDeclaredKindUnmatchedLimitation is the sentence a turn carries
// when retrieval offered options and none of them advances a role of the
// accepted reading that a caller's choice decides.
//
// It is true of every state that basis covers: a named subject whose offers
// are all of another kind, and a question whose offers match a declared kind
// only on a role no choice decides (a grouped question's member and group, a
// scoped question's members). So it names no kind, no role and no subject:
// naming the declared kind would claim it was absent when it was offered, and
// naming a candidate would hand back the guess the decision withholds.
//
// It carries no basis token. A person reads this sentence, and a token is not
// readable; the basis is on the wire as refusal_basis and on the log line.
const ContextFabricDeclaredKindUnmatchedLimitation = "No option found for this question could be chosen to answer it, so no subject was confirmed and no canonical facts were read. Rewording the question may let it be answered."

// ContextFabricOrganizationScopeUnsupportedLimitation is the sentence an
// organization-scope question that counts nothing carries when it is refused
// (ContextFabricRefusalBasisOrganizationScopeUnsupported).
//
// It says what is not supported AND what is: organization-wide analysis of
// status, health or drivers is not a capability, and organization-wide counts
// of one kind of subject are. The second half is the one the asker can act on.
// It names no subject, because the subject is the caller's own organization.
//
// It carries no basis token. A person reads this sentence, and a token is not
// readable; the basis is on the wire as refusal_basis and on the log line.
const ContextFabricOrganizationScopeUnsupportedLimitation = "This question was read as being about the organization as a whole. Organization-wide analysis of status, health or drivers is not supported; organization-wide counts of one kind of subject, such as how many repositories or teams there are, are supported. No canonical facts were read."

// ContextFabricSubjectIdentityUnconfirmedLimitation is the sentence a
// follow-up carries when the subject-substitution guard fired and this
// caller declined (or cannot accept) a clarification
// (ContextFabricRefusalBasisSubjectIdentityUnconfirmed). Chris-worded and
// ACCEPTED verbatim (CHAOS-5926, dictations 1998/1999) -- not composed here.
//
// IT NAMES THE BASIS TOKEN, the same choice
// ContextFabricContinuationContextUnverifiableLimitation makes and for the
// same reason: a reader joins the sentence to the machine field and to the
// log line through the one value all three share.
const ContextFabricSubjectIdentityUnconfirmedLimitation = "This follow-up appears to be about a different subject than the earlier answer it continues, so the server did not guess which one was meant and no canonical facts were read. Name the subject directly, or answer the earlier offer naming it, to continue. The server refused this follow-up on the basis subject_identity_unconfirmed."

// ContextFabricSynthesisInputBoundedLimitation is the sentence an answer
// carries when the facts read for it did not fit the model input and only
// part of them was given to answer synthesis. It states the consequence for
// a reader and no number: the counts are on the telemetry line.
const ContextFabricSynthesisInputBoundedLimitation = "Only part of the facts read for this question was given to answer synthesis, because the full set is larger than the model input limit. The answer may not reflect every fact that was read."

// ContextFabricTerminalNotSavedLimitation is the sentence a terminal answer
// (clarification, refusal, no match) carries when the server could not store
// its reading. The answer itself stands; what is lost is the stored copy.
const ContextFabricTerminalNotSavedLimitation = "This answer could not be saved, so it cannot be fetched again by its result id and a follow-up cannot build on it. Ask the question again to continue."

// ContextFabricSingleSubjectCountLimitation is the sentence an answer carries
// when a count was asked about one named subject: the subject has no members
// of its own kind, so no member count is stated, and the values given are the
// subject's own stored values, with a total of the additive daily values
// stated separately where there is one.
const ContextFabricSingleSubjectCountLimitation = "This question asks for a count about one named subject, which has no set of members to count, so no member count was stated. The values given are the stored daily or period values for that subject. A total of those daily values over the stated period is stated in the answer, with the days it rests on, only where the stored values can be added; where none is stated, none was computed. A day with no stored row is either a day with no activity or a day that was not computed, and the server cannot tell which, so such a day is named and never counted as zero."

// ContextFabricServiceAuthoredLimitations returns every disclosure this
// service composes for itself, in no significant order.
//
// A NEW FIXED DISCLOSURE BELONGS IN THIS LIST. Everything that reasons
// about "did the service write this, or did the model?" asks
// IsContextFabricServiceAuthoredLimitation -- the engine's displacement
// rule (which never displaces a service disclosure) and the validator's
// coherence rule (which accepts a positive displaced count only when one is
// present) -- and that predicate is this list PLUS the interpolated
// grouping-refusal family, which no list of constants can hold.
//
// A NEW INTERPOLATED DISCLOSURE therefore needs its own composer, its own
// parse-based recogniser, and a line in that predicate. It is more work than
// adding a constant on purpose: the round-3 finding was an interpolated
// disclosure that nobody could recognise, so it was displaced by the next
// composer and the served answer carried no statement that the question had
// been answered on a different axis than it asked for.
func ContextFabricServiceAuthoredLimitations() []string {
	return []string{
		ContextFabricRetrievalDegradedLimitation,
		ContextFabricRetrievalDegradedLimitationLegacy,
		ContextFabricTemporalProjectionLimitation,
		ContextFabricObservedTimeLimitation,
		ContextFabricCommitRetractionLimitation,
		ContextFabricClientSynthesisCommitNotAffirmedLimitation,
		ContextFabricSynthesisClarificationUnavailableLimitation,
		ContextFabricSynthesisNarrativeWithheldLimitation,
		ContextFabricFactScopeUnexpandedLimitation,
		ContextFabricFactScopeActivityProxyLimitation,
		ContextFabricFactScopeAttributedPrimaryTeamLimitation,
		ContextFabricFrameInvariantRefusalLimitation,
		ContextFabricContinuationContextUnverifiableLimitation,
		ContextFabricDeclaredKindUnmatchedLimitation,
		ContextFabricOrganizationScopeUnsupportedLimitation,
		ContextFabricSubjectIdentityUnconfirmedLimitation,
		ContextFabricSynthesisInputBoundedLimitation,
		ContextFabricTerminalNotSavedLimitation,
		ContextFabricBudgetTrimClaimedFactsLimitation,
		ContextFabricSingleSubjectCountLimitation,
	}
}

// IsContextFabricServiceAuthoredLimitation reports whether one limitation
// is a disclosure this service composes rather than a model caveat.
func IsContextFabricServiceAuthoredLimitation(limitation string) bool {
	for _, disclosure := range ContextFabricServiceAuthoredLimitations() {
		if limitation == disclosure {
			return true
		}
	}
	// The two grouping disclosures are interpolated, so they can only be
	// recognised by parsing them -- see
	// IsContextFabricGroupingRefusalLimitation and
	// IsContextFabricGroupingUnplaceableLimitation. This predicate, NOT the
	// fixed list above, is the authority every consumer asks; the list
	// remains what it says it is (the fixed disclosures) and
	// ContextFabricServiceAuthoredLimitations' own callers use it only to
	// enumerate those.
	//
	// EVERY MEMBER OF THE GROUPING VOCABULARY THAT DISCLOSES NEEDS A LINE
	// HERE. Omitting one does not fail loudly: the sentence is composed, is
	// classified as a model caveat, and is displaced by the composer that runs
	// after it, so the served answer states nothing about having been answered
	// on a different axis than the one asked for. That is the shipped round-3
	// defect, and it is invisible to any test that drives the composer.
	return IsContextFabricGroupingRefusalLimitation(limitation) ||
		IsContextFabricGroupingUnplaceableLimitation(limitation) ||
		IsContextFabricRefusalBasisLimitation(limitation) ||
		IsContextFabricGroupReadUnreadLimitation(limitation) ||
		IsContextFabricGroupListOverBoundLimitation(limitation) ||
		IsContextFabricCohortNarrowingLimitation(limitation) ||
		IsContextFabricFactRowTruncationLimitation(limitation) ||
		IsContextFabricClaimDepthLimitation(limitation) ||
		IsContextFabricBudgetTrimLimitation(limitation) ||
		IsContextFabricPathDropLimitation(limitation) ||
		IsContextFabricWorkItemMemberFilterLimitation(limitation) ||
		IsContextFabricWorkItemRepositoryLimitation(limitation) ||
		IsContextFabricWorkItemListedLimitation(limitation) ||
		IsContextFabricWorkItemCensusRepositoryScopeLimitation(limitation) ||
		IsContextFabricStatedRangeConflictLimitation(limitation) ||
		IsContextFabricComparisonPeriodUnreadLimitation(limitation)
}

// ContextFabricStatedRangeConflictLimitation is served when a turn ran on the
// period its question states while the interpretation carried a range that
// differs from it. Dates are YYYY-MM-DD.
func ContextFabricStatedRangeConflictLimitation(interpretedStart, interpretedEnd, statedStart, statedEnd string) string {
	return "The interpretation sent with this question read its period as " + interpretedStart + " to " + interpretedEnd + ", which is not the period the question states; this answer uses the stated period, " + statedStart + " to " + statedEnd + "."
}

var statedRangeConflictLimitationPattern = regexp.MustCompile(`^The interpretation sent with this question read its period as \d{4}-\d{2}-\d{2} to \d{4}-\d{2}-\d{2}, which is not the period the question states; this answer uses the stated period, \d{4}-\d{2}-\d{2} to \d{4}-\d{2}-\d{2}\.$`)

// IsContextFabricStatedRangeConflictLimitation reports whether one limitation
// is that disclosure; it matches the whole sentence.
func IsContextFabricStatedRangeConflictLimitation(limitation string) bool {
	return statedRangeConflictLimitationPattern.MatchString(limitation)
}

// ContextFabricComparisonPeriodUnreadLimitation is served when a question
// compares two periods and the turn read only the period it states. Bounds are
// RFC 3339 in UTC, as an evidence window states them.
//
// The service does not compare two periods, and a second call that repeats the
// comparison wording is refused the same way, so the sentence says what does
// work: one period per call, asked in its single-period form, with the
// comparison period as the evidence window, and the comparison made by the
// client from the two answers.
func ContextFabricComparisonPeriodUnreadLimitation(statedStart, statedEnd, comparisonStart, comparisonEnd string) string {
	return "This question compares two periods; this answer read only the stated period, " + statedStart + " to " + statedEnd + ". The period it is compared with, " + comparisonStart + " to " + comparisonEnd + ", was not read. This service reads one period per call and does not compare two: ask about one period only, with the question in its single-period form without the comparison wording, and send evidence_window start " + comparisonStart + " and end " + comparisonEnd + "; then compare the two answers yourself."
}

const comparisonPeriodInstant = `\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z`

var comparisonPeriodUnreadLimitationPattern = regexp.MustCompile(`^This question compares two periods; this answer read only the stated period, ` + comparisonPeriodInstant + ` to ` + comparisonPeriodInstant + `\. The period it is compared with, (` + comparisonPeriodInstant + `) to (` + comparisonPeriodInstant + `), was not read\. This service reads one period per call and does not compare two: ask about one period only, with the question in its single-period form without the comparison wording, and send evidence_window start (` + comparisonPeriodInstant + `) and end (` + comparisonPeriodInstant + `); then compare the two answers yourself\.$`)

// IsContextFabricComparisonPeriodUnreadLimitation reports whether one
// limitation is that disclosure; it matches the whole sentence.
func IsContextFabricComparisonPeriodUnreadLimitation(limitation string) bool {
	match := comparisonPeriodUnreadLimitationPattern.FindStringSubmatch(limitation)
	return match != nil && match[1] == match[3] && match[2] == match[4]
}

// ContextFabricWorkItemCensusRepositoryScopeLimitation is served, in the same
// words every time, when the caller's repository scope was applied to the
// work item census: a work item with no repository of its own is outside a
// named repository, so the census did not search it.
const ContextFabricWorkItemCensusRepositoryScopeLimitation = "A repository scope was given: work items that have no repository of their own (for example tracker issues linked only through pull requests) were not searched."

// IsContextFabricWorkItemCensusRepositoryScopeLimitation reports whether one
// limitation is that disclosure; it matches the whole sentence.
func IsContextFabricWorkItemCensusRepositoryScopeLimitation(limitation string) bool {
	return limitation == ContextFabricWorkItemCensusRepositoryScopeLimitation
}

// The work-item member-filter disclosures. A filtered member answer states the
// filter it was read under, names an empty match set, and states one fixed
// exclusion for the items the caller may not read. Each is composed from these
// parts by internal/contextfabric and recognised here by them, so a composer
// and the recogniser cannot drift apart, and none is displaced at the cap.
const (
	ContextFabricWorkItemMemberFilterLimitationPrefix = "Members are the work items "
	ContextFabricWorkItemNoMatchLimitationPrefix      = "No work item in this project within the authorized scope "
	ContextFabricWorkItemNoMatchLimitationSuffix      = "; that is a count of matches, not a statement about the project's health."
	// ContextFabricWorkItemRepositoryMembershipRationale is the inclusion
	// reason of a work item read on a repository, without its link tier: an
	// issue linked to a pull request of that repository. It is the cohort's
	// own rationale; each member's reason adds the tier
	// (ContextFabricWorkItemRepositoryMembershipReason).
	ContextFabricWorkItemRepositoryMembershipRationale = "Issue linked to a pull request of the named repository."
	// The no-match sentence for members read through a repository's pull
	// requests. It states the relation: the members are issues LINKED to a
	// pull request of the repository.
	ContextFabricWorkItemRepositoryNoMatchLimitationPrefix = "No work item of this repository within the authorized scope "
	ContextFabricWorkItemRepositoryNoMatchLimitationSuffix = "; that is a count of matches, not a statement about the repository's health."
	// ContextFabricWorkItemDeniedScopeExclusionLimitation is the same words
	// for every outcome, so it cannot tell a caller how many denied items hold
	// a status or fall in a period.
	ContextFabricWorkItemDeniedScopeExclusionLimitation = "Work items outside this principal's authorized scope are neither counted nor described here."
	// ContextFabricWorkItemWindowNotAppliedLimitation is served beside current
	// members when the request supplied a period the membership did not use.
	ContextFabricWorkItemWindowNotAppliedLimitation     = "Members are the work items as of now; the period supplied with this request was not applied to the membership. Ask for work items created, completed or updated in that period to filter by one of those."
	contextFabricWorkItemMemberFilterLimitationMaxRunes = 400
)

var (
	workItemStatusFilterLimitationPattern = regexp.MustCompile(`^Members are the work items whose current status is [a-z_]{1,32}; status is read as of now, over no period, and is not completion or readiness\.$`)
	workItemWindowFilterLimitationPattern = regexp.MustCompile(`^Members are the work items (?:created|completed|last updated) from \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z to \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z \(the (?:created|completed|updated)_at field\); their status and every other fact is as of now, not as of the period\.$`)
	workItemNoMatchLimitationPattern      = regexp.MustCompile(`^(?:No work item in this project within the authorized scope (?:currently has status [a-z_]{1,32}|was (?:created|completed|last updated) in that period(?: and a current status of [a-z_]{1,32})?); that is a count of matches, not a statement about the project's health|No work item of this repository within the authorized scope (?:currently has status [a-z_]{1,32}|was (?:created|completed|last updated) in that period(?: and a current status of [a-z_]{1,32})?); that is a count of matches, not a statement about the repository's health)\.$`)
)

// IsContextFabricWorkItemMemberFilterLimitation reports whether one limitation
// is a work-item member-filter disclosure. It matches each composed sentence
// whole, so a model caveat that only starts with the same words is not one.
func IsContextFabricWorkItemMemberFilterLimitation(limitation string) bool {
	if limitation == ContextFabricWorkItemDeniedScopeExclusionLimitation || limitation == ContextFabricWorkItemWindowNotAppliedLimitation {
		return true
	}
	if len(limitation) > contextFabricWorkItemMemberFilterLimitationMaxRunes*4 {
		return false
	}
	return workItemStatusFilterLimitationPattern.MatchString(limitation) ||
		workItemWindowFilterLimitationPattern.MatchString(limitation) ||
		workItemNoMatchLimitationPattern.MatchString(limitation)
}

// HasContextFabricServiceAuthoredLimitation reports whether any entry is
// one. This is LimitationsDisplaced's coherence oracle: a displacement only
// ever happens to force a service disclosure into a list that was already
// full, so a positive count requires one to be present.
func HasContextFabricServiceAuthoredLimitation(limitations []string) bool {
	for _, limitation := range limitations {
		if IsContextFabricServiceAuthoredLimitation(limitation) {
			return true
		}
	}
	return false
}

// The work items of a named repository are the issues linked to its pull
// requests (the link of record, tiers native, explicit_text, heuristic). The
// tier that linked a member is named in the member's inclusion reason, and the
// sentences below disclose what the link source cannot promise. Each is fixed
// (or fixed but for a count) so a recogniser can match it whole and no
// composer displaces it.
const (
	ContextFabricWorkItemRepositoryTierNative       = "native"
	ContextFabricWorkItemRepositoryTierExplicitText = "explicit_text"
	ContextFabricWorkItemRepositoryTierHeuristic    = "heuristic"

	// ContextFabricWorkItemRepositoryFreshnessLimitation is served on every
	// repository work-item answer.
	ContextFabricWorkItemRepositoryFreshnessLimitation = "The issue to pull request links come from the last link build and can lag behind the source."
	// ContextFabricWorkItemRepositoryNoPullRequestsLimitation: no member, and
	// the repository has no pull request in the graph.
	ContextFabricWorkItemRepositoryNoPullRequestsLimitation = "No pull request of this repository is known, so no issue is linked to it; that is a count of linked issues, not a statement about the repository's health."
	// ContextFabricWorkItemRepositoryUnlinkedLimitation: the repository has
	// pull requests and none links an issue.
	ContextFabricWorkItemRepositoryUnlinkedLimitation = "No pull request of this repository links an issue, so it has no linked work items; that is a count of links, not a statement about the repository's health."
	// ContextFabricWorkItemRepositoryPartialLimitation: the read did not cover
	// every issue linked to the repository (a member whose status or
	// completion could not be read, or a walk cut at its read bound).
	ContextFabricWorkItemRepositoryPartialLimitation = "Not every work item linked to this repository could be read, so this list can miss members."
	// ContextFabricWorkItemRepositoryStrongestFirstLimitation: more members
	// than the answer lists; the list keeps the strongest links.
	ContextFabricWorkItemRepositoryStrongestFirstLimitation = "Not every member is listed: members are kept by the strength of their link (native, then stated in text, then heuristic), so members of the lower link tiers were cut first."
	// ContextFabricWorkItemRepositoryPeriodRoleRefusalLimitation refuses a
	// created or updated period over the work items of a repository.
	ContextFabricWorkItemRepositoryPeriodRoleRefusalLimitation = "No canonical fact carries the created or last updated time of the work items linked to a repository, so a period on those times cannot be applied and no members are listed. Ask for work items completed in that period, or ask without a period."

	contextFabricWorkItemRepositoryHeuristicPrefix = " of these members are linked only by a heuristic match "
	contextFabricWorkItemRepositoryHeuristicSuffix = "(a pull request opened near the issue's last update in the issue's own repository)."
)

const (
	contextFabricWorkItemListedPrefix  = "Not every member is listed: "
	contextFabricWorkItemListedOf      = " of "
	contextFabricWorkItemListedAtLeast = "at least "
	contextFabricWorkItemListedSuffix  = " members are listed, because the server limits how many items one answer carries."
)

var workItemListedLimitationPattern = regexp.MustCompile(`^Not every member is listed: ([1-9]\d{0,8}) of (at least )?([1-9]\d{0,8}) members are listed, because the server limits how many items one answer carries\.$`)

// ContextFabricWorkItemListedLimitation states how many of a work-item
// cohort's members the answer lists, when the list is shorter than the
// population. It returns false when the counts do not describe a cut list.
func ContextFabricWorkItemListedLimitation(listed, population int, lowerBound bool) (string, bool) {
	if listed < 1 || population <= listed || population > 999999999 {
		return "", false
	}
	bound := ""
	if lowerBound {
		bound = contextFabricWorkItemListedAtLeast
	}
	return contextFabricWorkItemListedPrefix + strconv.Itoa(listed) + contextFabricWorkItemListedOf + bound + strconv.Itoa(population) + contextFabricWorkItemListedSuffix, true
}

// IsContextFabricWorkItemListedLimitation reports whether a limitation is one
// ContextFabricWorkItemListedLimitation could have composed.
func IsContextFabricWorkItemListedLimitation(limitation string) bool {
	match := workItemListedLimitationPattern.FindStringSubmatch(limitation)
	if match == nil {
		return false
	}
	listed, _ := strconv.Atoi(match[1])
	population, _ := strconv.Atoi(match[3])
	return population > listed
}

var workItemRepositoryHeuristicLimitationPattern = regexp.MustCompile(`^[1-9]\d{0,5} of these members are linked only by a heuristic match \(a pull request opened near the issue's last update in the issue's own repository\)\.$`)

// ContextFabricWorkItemRepositoryMembershipReason is one member's inclusion
// reason on a repository anchor: the tier of the strongest link that reached
// it, in plain words. A tier outside the closed set is the heuristic reading
// (the weakest), never the native one.
func ContextFabricWorkItemRepositoryMembershipReason(tier string) string {
	switch tier {
	case ContextFabricWorkItemRepositoryTierNative:
		return "Issue linked to a pull request of the named repository (native link)"
	case ContextFabricWorkItemRepositoryTierExplicitText:
		return "Issue linked to a pull request of the named repository (link stated in text)"
	default:
		return "Issue linked to a pull request of the named repository (heuristic match)"
	}
}

// ContextFabricWorkItemRepositoryHeuristicLimitation states how many of the
// listed members are linked only by the heuristic tier.
func ContextFabricWorkItemRepositoryHeuristicLimitation(count int) string {
	return strconv.Itoa(count) + contextFabricWorkItemRepositoryHeuristicPrefix + contextFabricWorkItemRepositoryHeuristicSuffix
}

// IsContextFabricWorkItemRepositoryLimitation reports whether one limitation
// is a repository work-item disclosure. It matches each sentence whole.
func IsContextFabricWorkItemRepositoryLimitation(limitation string) bool {
	switch limitation {
	case ContextFabricWorkItemRepositoryFreshnessLimitation,
		ContextFabricWorkItemRepositoryNoPullRequestsLimitation,
		ContextFabricWorkItemRepositoryUnlinkedLimitation,
		ContextFabricWorkItemRepositoryPartialLimitation,
		ContextFabricWorkItemRepositoryStrongestFirstLimitation,
		ContextFabricWorkItemRepositoryPeriodRoleRefusalLimitation:
		return true
	}
	return len(limitation) <= contextFabricWorkItemMemberFilterLimitationMaxRunes*4 && workItemRepositoryHeuristicLimitationPattern.MatchString(limitation)
}
